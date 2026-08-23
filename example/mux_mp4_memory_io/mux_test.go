package main

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/yapingcat/gomedia/example/internal/mediatest"
)

// An mp4 built in memory has to be the same file as one built on disk, which
// means ffmpeg has to be able to decode it and get the pictures ffmpeg put
// into the transport stream in the first place.
func TestMuxTSToMP4InMemory(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mpegts", Video: "libx264", Audio: "aac", Seconds: 2, BFrames: 2,
	})

	data, err := MuxTSToMP4InMemory(src)
	if err != nil {
		t.Fatalf("mux: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("the muxer produced nothing")
	}

	dst := filepath.Join(t.TempDir(), "out.mp4")
	if err := os.WriteFile(dst, data, 0o666); err != nil {
		t.Fatal(err)
	}

	out := tools.MustProbe(t, dst, 2)
	if out.Format.FormatName != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Fatalf("ffprobe read the output as %q, want mp4", out.Format.FormatName)
	}
	tools.AssertDecodable(t, dst)

	srcVideo, _ := tools.Probe(t, src).Video()
	dstVideo, _ := out.Video()
	if dstVideo.Frames() != srcVideo.Frames() {
		t.Errorf("%d video frames in the mp4, want %d", dstVideo.Frames(), srcVideo.Frames())
	}
	tools.AssertSameDecoded(t, src, dst, "v:0")
	tools.AssertSameDecoded(t, src, dst, "a:0")
	mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "mp4 video")
}

// The writer is what somebody will lift out of this example, and the muxer
// leans on the seeking hard: it writes placeholder box sizes and comes back
// for them. A writer that grows wrong, or that refuses a seek past the end,
// corrupts the file in ways that only show up later as an unreadable moov.
func TestCacheWriterSeeker(t *testing.T) {
	rnd := rand.New(rand.NewSource(2))

	t.Run("matches a file", func(t *testing.T) {
		ws := newCacheWriterSeeker(16)
		f, err := os.CreateTemp(t.TempDir(), "ref")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		// the access pattern a muxer produces: append, jump back, patch,
		// jump forward again
		for i := 0; i < 200; i++ {
			switch rnd.Intn(3) {
			case 0:
				chunk := make([]byte, 1+rnd.Intn(300))
				rnd.Read(chunk)
				if _, err := ws.Write(chunk); err != nil {
					t.Fatalf("Write: %v", err)
				}
				if _, err := f.Write(chunk); err != nil {
					t.Fatal(err)
				}
			case 1:
				size, _ := f.Seek(0, io.SeekEnd)
				if size == 0 {
					continue
				}
				at := rnd.Int63n(size)
				if _, err := ws.Seek(at, io.SeekStart); err != nil {
					t.Fatalf("Seek: %v", err)
				}
				if _, err := f.Seek(at, io.SeekStart); err != nil {
					t.Fatal(err)
				}
			case 2:
				if _, err := ws.Seek(0, io.SeekEnd); err != nil {
					t.Fatalf("Seek to end: %v", err)
				}
				if _, err := f.Seek(0, io.SeekEnd); err != nil {
					t.Fatal(err)
				}
			}
		}

		want, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ws.Bytes(), want) {
			t.Errorf("the buffer differs from the file: %d bytes vs %d", len(ws.Bytes()), len(want))
		}
	})

	t.Run("overwrites in place without truncating", func(t *testing.T) {
		ws := newCacheWriterSeeker(4)
		ws.Write([]byte("0123456789"))
		if _, err := ws.Seek(2, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		ws.Write([]byte("ab"))
		if got := string(ws.Bytes()); got != "01ab456789" {
			t.Errorf("got %q, want %q", got, "01ab456789")
		}
	})

	t.Run("rejects a seek before the start", func(t *testing.T) {
		ws := newCacheWriterSeeker(4)
		if _, err := ws.Seek(-1, io.SeekStart); err == nil {
			t.Error("Seek(-1, SeekStart) was accepted")
		}
	})
}
