package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/golang/glog"
	"github.com/google/gopacket"
	"github.com/google/gopacket/tcpassembly"
	"github.com/google/gopacket/tcpassembly/tcpreader"
)

type rtmpStream struct {
	sync.WaitGroup                         // chunk decoders
	track          map[uint32]*RTMPMessage // chunkStreamID to message
	finalizer      *MessageFinalizer       // decoded messages are sent here
	chunkSize      uint32
}

type rtmpStreamWrapper struct {
	outer  *sync.WaitGroup // tcp streams
	output chan string
}

func (w *rtmpStreamWrapper) New(net, tcp gopacket.Flow) tcpassembly.Stream {
	// TODO all glog messages in the rtmpStream should include this prefix:
	//	prefix := fmt.Sprintf("%s ", tcp)  FIXME
	s := &rtmpStream{
		track:     make(map[uint32]*RTMPMessage),
		finalizer: &MessageFinalizer{r: w.output},
		chunkSize: 128,
	}

	w.outer.Add(1)
	r := tcpreader.NewReaderStream()
	go s.parseStream(w.outer, &r)
	return &r
}

// RTMPMessage is a single message and its decoder.
type RTMPMessage struct {
	header
	headerOk        bool
	streamID        uint32
	remainingLength uint32
	writer          *io.PipeWriter
}

type uint24 [3]uint8

type header struct {
	Timestamp     uint24
	MessageLength uint24
	TypeID        uint8
}

func (s *rtmpStream) parseStream(wg *sync.WaitGroup, r io.Reader) {
	defer func() {
		// Clean up all the chunkstream processors.
		for _, v := range s.track {
			if v != nil && v.writer != nil {
				v.writer.CloseWithError(fmt.Errorf("stream exiting"))
			}
		}
		s.Wait()                       // chunk processors
		s.finalizer.exit()             // show any results we found
		tcpreader.DiscardBytesToEOF(r) // read rest of tcp stream
		wg.Done()                      // this tcp stream is done
	}()

	buf := bufio.NewReader(r)

	// C0
	if d, err := buf.ReadByte(); err == io.EOF {
		return
	} else if err != nil {
		glog.V(2).Infof("read: %v", err)
		return
	} else if d != 0x03 {
		glog.V(1).Info("not RTMP")
		return
	}

	glog.V(2).Info("MAYBE RTMP")
	glog.V(2).Info("...should read more")

	data1 := make([]byte, 0x600)
	// C1
	if _, err := io.ReadFull(buf, data1); err != nil {
		glog.V(2).Infof("read: %v", err)
		return
	}

	// C2
	if _, err := io.ReadFull(buf, data1); err != nil {
		glog.V(2).Infof("read: %v", err)
		return
	}

	glog.V(1).Info("RTMP?")

	for {
		if err := s.processChunk(buf); errIsEOF(err) {
			glog.V(1).Info(err)
			break
		} else if err != nil {
			glog.Error(err)
			break
		}
	}
}

func (s *rtmpStream) processChunk(buf io.Reader) error {
	// first find out headerFormat and chunkStreamID (1-3 bytes)
	var headerFormat uint8
	err := binary.Read(buf, binary.BigEndian, &headerFormat)
	if err != nil {
		return &ChunkError{"read headertype", err}
	}

	chunkStreamID := uint32(headerFormat) & 0x3F
	headerFormat = headerFormat >> 6

	if chunkStreamID == 0 {
		var b2 uint8
		err = binary.Read(buf, binary.BigEndian, &b2)
		if err != nil {
			return &ChunkError{
				"read chunkStreamID=0 next byte", err}
		}
		chunkStreamID = uint32(b2) + 64
	} else if chunkStreamID == 1 {
		var b23 uint16
		err = binary.Read(buf, binary.BigEndian, &b23)
		if err != nil {
			return &ChunkError{
				"read chunkStreamID=1 next bytes", err}
		}
		chunkStreamID = uint32(b23) + 64
	}

	glog.V(2).Infof("chunkStreamID now %v", chunkStreamID)

	// Consume rest of header, format is 0-3, 3 has no additional bytes.

	glog.V(2).Infof("headerFormat %v", headerFormat)

	var msg *RTMPMessage
	if msg = s.track[chunkStreamID]; msg == nil {
		msg = &RTMPMessage{}
		s.track[chunkStreamID] = msg
	}

	if headerFormat == 0 || headerFormat == 1 {
		err = binary.Read(buf, binary.BigEndian, &msg.header)
		if err != nil {
			return &ChunkError{"read message header", err}
		}
		msg.headerOk = true
	}

	if headerFormat == 0 {
		err = binary.Read(buf, binary.LittleEndian, &msg.streamID)
		if err != nil {
			return &ChunkError{"read streamID", err}
		}
		glog.V(2).Infof("have streamID %v", msg.streamID)
	}

	if !msg.headerOk {
		return fmt.Errorf("dropping stream, we missed the header of chunkStreamID %v", chunkStreamID)
	}

	if msg.writer == nil {
		reader, writer := io.Pipe()
		msg.writer = writer
		s.Add(1)
		go processNewMessage(&s.WaitGroup, s.finalizer, reader, msg.header.TypeID)
	}

	if headerFormat == 2 {
		err = binary.Read(buf, binary.BigEndian, &msg.header.Timestamp)
		if err != nil {
			return &ChunkError{"read timestampDelta", err}
		}
		glog.V(3).Infof("  timestampDelta %v", msg.header.Timestamp)
	}

	if msg.header.Timestamp == [3]byte{255, 255, 255} {
		var extendedTimestamp uint32
		err = binary.Read(buf, binary.BigEndian, &extendedTimestamp)
		if err != nil {
			return &ChunkError{"read extendedTimestamp", err}
		}
		glog.V(3).Infof("  extendedTimestamp %v", extendedTimestamp)
	}

	// If not a continuation of an earlier message, assume another message as per the header.
	// TODO: this could use a cleanup, and perhaps also put explicitly when reading the header
	if msg.remainingLength == 0 {
		olen := msg.header.MessageLength
		msg.remainingLength = uint32(olen[0])<<16 + uint32(olen[1])<<8 + uint32(olen[2])
	}

	glog.V(2).Infof("chunkStreamID %v TypeID %v REMAINING %v",
		chunkStreamID,
		msg.header.TypeID,
		msg.remainingLength)
	glog.V(3).Infof("END1 %+v", msg.header)
	glog.V(3).Infof("END2 %+v", msg)

	// Special case for "set chunk size", it affects the entire TCP stream.
	if msg.header.TypeID == 1 && msg.remainingLength >= 4 {
		err = binary.Read(buf, binary.BigEndian, &s.chunkSize)
		if err != nil {
			return &ChunkError{"read chunkSize", err}
		}
		msg.remainingLength -= 4
		glog.V(1).Infof("NEW CHUNKSIZE %v", s.chunkSize)
	}

	if err := s.sendMessagePayload(buf, chunkStreamID, msg); err != nil {
		return err
	}

	if msg.remainingLength == 0 {
		glog.V(2).Infof("CLOSING OUT chunkStreamID %v", chunkStreamID)
		msg.writer.Close()
		msg.writer = nil
	}
	return nil
}

func (s *rtmpStream) sendMessagePayload(buf io.Reader, chunkStreamID uint32, msg *RTMPMessage) error {
	rl := min(s.chunkSize, msg.remainingLength)
	msg.remainingLength -= rl

	glog.V(2).Infof("POST StreamID %v TypeID %v READ %v REMAINING %v", chunkStreamID, msg.header.TypeID, rl, msg.remainingLength)

	if rl > 0 {
		bx := make([]byte, rl)
		if _, err := io.ReadFull(buf, bx); err != nil {
			return &ChunkError{fmt.Sprintf("read streamID %v: %v bytes", chunkStreamID, rl), err}
		}
		glog.V(200).Infof("SENDING %v bytes to chunkStreamID %v (%q)", rl, chunkStreamID, string(bx)) // raw data
		if _, err := msg.writer.Write(bx); err != nil {
			return err
		}
	}

	return nil
}
