package wav

import (
	"bytes"
	"io"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

func FuzzReader(f *testing.F) {
	ws := &memWriteSeeker{}
	format, _ := FormatOf(codec.CODECID_AUDIO_G711A, 8000, 1)
	w, _ := NewWriter(ws, format)
	w.Write(make([]byte, 333))
	w.Close()
	f.Add(ws.buf)
	f.Add([]byte("RIFF\x00\x00\x00\x00WAVE"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		r.Duration()
		for i := 0; i < 1000; i++ {
			fr, err := r.ReadFrame()
			if err == io.EOF {
				return
			}
			if err != nil {
				return
			}
			if len(fr.Data)%r.Format().BlockAlign() != 0 {
				t.Fatalf("frame of %d bytes splits a sample of %d", len(fr.Data), r.Format().BlockAlign())
			}
		}
	})
}
