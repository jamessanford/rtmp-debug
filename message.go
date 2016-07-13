package main

// This parses a dechunked RTMP message as it is received over the wire.

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"

	"github.com/golang/glog"
)

const (
	msgAMF    = 0x14
	msgAMF3   = 0x11
	amfFloat  = 0x00
	amfBool   = 0x01
	amfString = 0x02
	amfMap    = 0x03
	amfNull   = 0x05
	amfArray  = 0x08
	amfEnd    = 0x09
)

func readString(reader *io.PipeReader) (string, error) {
	var lenstr uint16
	if err := binary.Read(reader, binary.BigEndian, &lenstr); err != nil {
		return "", fmt.Errorf("read length failed: %v", err)
	}
	foundStr := make([]byte, lenstr)
	if _, err := io.ReadFull(reader, foundStr); err != nil {
		return "", fmt.Errorf("read foundStr failed: %v", err)
	}
	return string(foundStr), nil
}

func readMap(reader *io.PipeReader) (map[string]interface{}, error) {
	v := make(map[string]interface{})
	for {
		key, err := readString(reader)
		if err != nil {
			return nil, err
		}
		value, err := nextObject(reader)
		if err != nil {
			return nil, err
		}
		if value == nil {
			break
		}
		glog.V(2).Infof("MESSAGE OBJECT KEY=%s VALUE=%s", key, value)
		v[key] = value
	}
	return v, nil
}

// TODO: This is probably not implemented correctly.
func readArray(reader *io.PipeReader) ([]interface{}, error) {
	var arrayLen uint32
	if err := binary.Read(reader, binary.BigEndian, &arrayLen); err != nil {
		return nil, fmt.Errorf("read length failed: %v", err)
	}
	array := make([]interface{}, arrayLen)
	for count := uint32(0); count < arrayLen; count++ {
		item, err := nextObject(reader)
		if err != nil {
			return nil, err
		}
		array = append(array, item)
	}
	return array, nil
}

func nextObject(reader *io.PipeReader) (value interface{}, err error) {
	b := make([]byte, 1)
	_, err = io.ReadFull(reader, b)
	if err != nil {
		return nil, err
	}
	glog.V(2).Infof("NEXT TYPE IS %v", b[0])
	if b[0] == amfFloat {
		var num float64
		if err = binary.Read(reader, binary.BigEndian, &num); err != nil {
			return nil, err
		}
		return num, nil
	} else if b[0] == amfBool {
		boolval := make([]uint8, 1)
		if _, err = io.ReadFull(reader, boolval); err != nil {
			return nil, err
		} else if boolval[0] == 0 {
			return false, nil
		}
		return true, nil
	} else if b[0] == amfString {
		return readString(reader)
	} else if b[0] == amfMap {
		return readMap(reader)
	} else if b[0] == amfNull {
		return nil, nil // NULL
	} else if b[0] == amfArray {
		return readArray(reader)
	} else if b[0] == amfEnd {
		return nil, nil // END OF OBJECT MARKER
	}
	return nil, fmt.Errorf("unknown message object type %v", b[0])
}

func processNewMessage(reader *io.PipeReader, finalizer *MessageFinalizer, msgTypeID uint8) error {
	defer ioutil.ReadAll(reader)

	var vals []interface{}

	if msgTypeID == msgAMF || msgTypeID == msgAMF3 {
		if msgTypeID == msgAMF3 {
			b := make([]byte, 1)
			_, err := io.ReadFull(reader, b)
			if err != nil {
				return err
			}
		}
		for {
			value, err := nextObject(reader)
			if err == io.ErrUnexpectedEOF || err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if value != nil {
				glog.V(2).Infof("message value %s", value)
				vals = append(vals, value)
			}
		}
	}

	if len(vals) > 0 {
		glog.V(1).Infof("Commands: %#v", vals)
		finalizer.add(vals)
	}
	return nil
}
