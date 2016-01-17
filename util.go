package main

import (
	"fmt"
	"io"
)

// ChunkError displays the decode operation as well as the error.
type ChunkError struct {
	Op  string
	Err error
}

func (e *ChunkError) Error() string {
	return fmt.Sprintf("%v: %v", e.Op, e.Err)
}

func errIsEOF(err error) bool {
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return true
	}
	if err, ok := err.(*ChunkError); ok {
		return errIsEOF(err.Err)
	}
	return false
}

func min(a uint32, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
