package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/golang/glog"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
	"github.com/google/gopacket/tcpassembly"
)

var (
	pcapFile      = flag.String("f", "", "pcap file from tcpdump")
	pcapInterface = flag.String("i", "all", "interface to read packets from (\"en4\", \"eth0\", ..)")
	snapLen       = flag.Int("s", 65535, "interface snap length")
	promiscOff    = flag.Bool("p", false, "disable promiscuous mode")
)

const usageMessage = `
Usage: rtmp_debug [-i <interface>] [<bpf expression>]

Display decoded RTMP commands from pcap wire data.

Options:
`

func usage() {
	fmt.Fprintf(os.Stderr, usageMessage)
	flag.PrintDefaults()
}

func printResults(done chan struct{}, results chan string) {
	for x := range results {
		if len(x) > 0 {
			fmt.Printf("%v\n", x)
		}
	}
	close(done)
}

func openPcap() (h *pcap.Handle, err error) {
	if len(*pcapFile) > 0 {
		h, err = pcap.OpenOffline(*pcapFile)
		if err != nil {
			glog.Errorf("unable to open \"%s\"", *pcapFile)
		}
		return
	}

	h, err = pcap.OpenLive(*pcapInterface,
		int32(*snapLen),
		!*promiscOff,
		pcap.BlockForever)
	if err != nil {
		glog.Errorf("unable to open interface \"%s\"", *pcapInterface)
	}
	return
}

func main() {
	flag.Usage = usage
	flag.Parse()

	pcapfile, err := openPcap()
	if err != nil {
		glog.Errorf("%v", err)
		os.Exit(1)
	}

	bpf := strings.Join(flag.Args(), " ")
	if err = pcapfile.SetBPFFilter(bpf); err != nil {
		glog.Errorf("unable to set BPF: %v", err)
		os.Exit(1)
	}

	// "Pass this stream factory to an tcpassembly.StreamPool ,
	// start up an tcpassembly.Assembler, and you're good to go!"

	done := make(chan struct{})
	results := make(chan string)
	go printResults(done, results)

	wg := &sync.WaitGroup{}
	rtmp := &rtmpStreamWrapper{wg, results}
	pool := tcpassembly.NewStreamPool(rtmp)
	asm := tcpassembly.NewAssembler(pool)
	asm.MaxBufferedPagesTotal = 4096 // limit gopacket memory allocation

	source := gopacket.NewPacketSource(pcapfile, pcapfile.LinkType())

	var pkt gopacket.Packet
	for {
		pkt, err = source.NextPacket()
		if pkt == nil || err != nil {
			break
		}

		if tcp := pkt.Layer(layers.LayerTypeTCP); tcp != nil {
			asm.AssembleWithTimestamp(
				pkt.TransportLayer().TransportFlow(),
				tcp.(*layers.TCP),
				pkt.Metadata().Timestamp)
		}
	}

	if err != nil && !errIsEOF(err) {
		glog.Errorf("packet: %v", err)
		if err = pcapfile.Error(); err != nil {
			glog.Errorf("pcap: %v", err)
		}
	}

	asm.FlushAll() // abort any in progress tcp connections
	wg.Wait()      // tcp streams have finished processing
	close(results) // no more results will be generated by tcp streams
	<-done         // printResults has finished
}
