package es

import (
	"bytes"
	"io"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

func TestNalScannerLargeAndBoundary(t *testing.T) {
	// Place a 4-byte start code straddling the 64KB read boundary.
	var stream bytes.Buffer
	payload1 := bytes.Repeat([]byte{0x41}, nalReadChunkSize-6)
	payload2 := bytes.Repeat([]byte{0x42}, 70000)
	stream.Write([]byte{0x00, 0x00, 0x00, 0x01})
	stream.Write(payload1)
	stream.Write([]byte{0x00, 0x00, 0x00, 0x01})
	stream.Write(payload2)

	sc := newNalScanner(bytes.NewReader(stream.Bytes()))
	n1, err := sc.next()
	if err != nil {
		t.Fatalf("first nalu: %v", err)
	}
	if !bytes.Equal(n1, payload1) {
		t.Fatalf("first nalu len=%d, want %d", len(n1), len(payload1))
	}
	n2, err := sc.next()
	if err != nil {
		t.Fatalf("second nalu: %v", err)
	}
	if !bytes.Equal(n2, payload2) {
		t.Fatalf("second nalu len=%d, want %d", len(n2), len(payload2))
	}
	if _, err := sc.next(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestG711ShortFrameDuration(t *testing.T) {
	r := NewG711Reader(bytes.NewReader([]byte{0x55, 0xAA}), codec.CODECID_AUDIO_G711A, WithSampleRate(500), WithFrameDuration(1))
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(f.Data) != 1 || f.Data[0] != 0x55 {
		t.Fatalf("got %x, want 55", f.Data)
	}
}
