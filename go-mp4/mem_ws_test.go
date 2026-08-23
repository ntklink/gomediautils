package mp4

import (
	"errors"
	"io"
)

type memWriteSeeker struct {
	buf []byte
	pos int
}

func newMemWriteSeeker() *memWriteSeeker { return &memWriteSeeker{} }

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	if m.pos+len(p) > len(m.buf) {
		nb := make([]byte, m.pos+len(p))
		copy(nb, m.buf)
		m.buf = nb
	}
	copy(m.buf[m.pos:], p)
	m.pos += len(p)
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = int64(m.pos) + offset
	case io.SeekEnd:
		np = int64(len(m.buf)) + offset
	}
	if np < 0 {
		return 0, errors.New("negative seek")
	}
	m.pos = int(np)
	return np, nil
}
