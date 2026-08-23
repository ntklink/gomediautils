package main

import (
	"errors"
	"fmt"
	"io"
)

// cacheReaderSeeker serves an mp4 out of a byte slice.
//
// bytes.Reader does all of this and does it better; the point of writing it
// out is to show what the demuxer actually asks of its input, so that a real
// implementation backed by a cache, an object store or a network range
// request has something to copy. The demuxer seeks freely, so the whole
// stream has to stay addressable.
type cacheReaderSeeker struct {
	buf    []byte
	offset int
}

func newCacheReaderSeeker(buf []byte) *cacheReaderSeeker {
	return &cacheReaderSeeker{buf: buf}
}

func (rs *cacheReaderSeeker) Read(p []byte) (int, error) {
	if rs.offset >= len(rs.buf) {
		return 0, io.EOF
	}
	// a short read is not an error: Read may return fewer bytes than asked
	// for, and returning 0 with a nil error instead is what breaks callers
	n := copy(p, rs.buf[rs.offset:])
	rs.offset += n
	return n, nil
}

func (rs *cacheReaderSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = int64(rs.offset) + offset
	case io.SeekEnd:
		abs = int64(len(rs.buf)) + offset
	default:
		return 0, fmt.Errorf("cacheReaderSeeker: unknown whence %d", whence)
	}
	if abs < 0 {
		return 0, errors.New("cacheReaderSeeker: seek before the start of the buffer")
	}
	// seeking past the end is legal, reading there is not
	rs.offset = int(abs)
	return abs, nil
}
