package main

import (
	"errors"
	"fmt"
	"io"
)

// cacheWriterSeeker collects an mp4 in memory.
//
// The mp4 muxer needs a WriteSeeker, not a Writer: it reserves space for box
// sizes and the moov and goes back to fill them in once it knows what they
// are. That is why a plain io.Writer cannot be used, and why anything that
// buffers an mp4 for upload, or serves it out of a cache, has to look like
// this.
type cacheWriterSeeker struct {
	buf    []byte
	offset int
}

func newCacheWriterSeeker(capacity int) *cacheWriterSeeker {
	return &cacheWriterSeeker{buf: make([]byte, 0, capacity)}
}

// Bytes returns the file written so far.
func (ws *cacheWriterSeeker) Bytes() []byte { return ws.buf }

func (ws *cacheWriterSeeker) Write(p []byte) (int, error) {
	if end := ws.offset + len(p); end > len(ws.buf) {
		if end > cap(ws.buf) {
			grown := make([]byte, end, 2*end)
			copy(grown, ws.buf)
			ws.buf = grown
		} else {
			ws.buf = ws.buf[:end]
		}
	}
	copy(ws.buf[ws.offset:], p)
	ws.offset += len(p)
	return len(p), nil
}

func (ws *cacheWriterSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = int64(ws.offset) + offset
	case io.SeekEnd:
		abs = int64(len(ws.buf)) + offset
	default:
		return 0, fmt.Errorf("cacheWriterSeeker: unknown whence %d", whence)
	}
	if abs < 0 {
		return 0, errors.New("cacheWriterSeeker: seek before the start of the buffer")
	}
	// seeking past the end is legal; the gap fills with zeroes on the next
	// write, the same as a file
	ws.offset = int(abs)
	return abs, nil
}
