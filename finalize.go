package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/golang/glog"
)

// Collate interesting commands from RTMP messages within a single TCP stream.

// MessageFinalizer receives all decoded data for all messages within a single TCP stream.
type MessageFinalizer struct {
	sync.RWMutex
	r chan string

	complete bool
	connect  map[string]interface{} // options to "connect"
	play     string                 // URL given to play command
	// TODO: retain and display optional args
}

// input:  'some/long/path?optional&args'
// output: 'path'
func (c *MessageFinalizer) makeFilename() string {
	f := filepath.Base(filepath.Clean(c.play))
	return strings.SplitN(f, "?", 2)[0]
}

func addFlag(s string, v string) string {
	return fmt.Sprintf(" -%v '%v'", s, v)
}

// RTMPDumpCommand combines parsed message data into an "rtmpdump" command.
func (c *MessageFinalizer) RTMPDumpCommand() {
	if !c.complete {
		return
	}

	cmd := "rtmpdump"
	if tc := c.connect["tcUrl"]; tc != nil {
		cmd += fmt.Sprintf(" -r '%v'", tc)
	}
	cmd += addFlag("y", c.play)

	var keys []string
	for k := range c.connect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := fmt.Sprintf("%v", c.connect[k])
		if k == "app" {
			cmd += addFlag("a", s)
		} else if k == "tcUrl" {
			cmd += addFlag("t", s)
		} else if k == "pageUrl" {
			cmd += addFlag("p", s)
		} else if k == "flashVer" {
			cmd += addFlag("f", s)
		}
	}
	cmd += fmt.Sprintf(" -R -o '%s'", c.makeFilename())
	c.r <- cmd
}

func (c *MessageFinalizer) add(d []interface{}) {
	if len(d) == 0 {
		return
	}
	glog.V(2).Infof("ADD %v", d)

	// Multiple goroutines may be sending us processed messages.
	c.Lock()
	defer c.Unlock()

	if cmd, ok := d[0].(string); ok {
		if len(d) >= 2 {
			if cmd == "connect" {
				c.connect = d[2].(map[string]interface{})
			} else if cmd == "play" {
				c.play = d[2].(string)
			}
		}
	}
	if !c.complete && c.connect != nil && len(c.play) > 0 {
		// Show command as soon as we have enough information.
		c.complete = true
		c.RTMPDumpCommand()
	}
}

func (c *MessageFinalizer) exit() {
	// This is called as the TCP stream goes away.
}
