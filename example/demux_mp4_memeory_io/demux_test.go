package main

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// Demuxing out of memory has to give the same result as demuxing off disk,
// which is the whole claim the example makes.
func TestDemuxMP4FromMemory(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 2, BFrames: 2,
	})
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	files, err := DemuxMP4FromMemory(data, t.TempDir())
	if err != nil {
		t.Fatalf("demux: %v", err)
	}

	videoPath, ok := files["h264"]
	if !ok {
		t.Fatalf("no h264 stream came out, got %v", files)
	}
	audioPath, ok := files["aac"]
	if !ok {
		t.Fatalf("no aac stream came out, got %v", files)
	}

	tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:v:0", "h264"), videoPath, "v:0")
	tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:a:0", "aac"), audioPath, "a:0")
}

// A file whose moov sits at the end is the case that needs seeking at all.
// The default ffmpeg layout already puts it there; faststart moves it to the
// front. Both have to work, or the custom reader is only accidentally right.
func TestDemuxMP4FromMemoryMoovAtEitherEnd(t *testing.T) {
	tools := mediatest.Require(t)

	for _, movflags := range []string{"", "+faststart"} {
		name := "moov last"
		clip := mediatest.Clip{Container: "mp4", Video: "libx264", Seconds: 2, BFrames: 2}
		if movflags != "" {
			name = "moov first"
			clip.Extra = []string{"-movflags", movflags}
		}
		t.Run(name, func(t *testing.T) {
			src := tools.MakeClip(t, clip)
			data, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			files, err := DemuxMP4FromMemory(data, t.TempDir())
			if err != nil {
				t.Fatalf("demux: %v", err)
			}
			videoPath, ok := files["h264"]
			if !ok {
				t.Fatalf("no h264 stream came out, got %v", files)
			}
			tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:v:0", "h264"), videoPath, "v:0")
		})
	}
}

// The reader is the part of the example somebody will copy, so it gets
// checked against the io contracts directly. A Read that returns (0, nil)
// when data is left, or a Seek that rejects a legal offset, breaks callers
// in ways that are hard to trace back from a demuxer error.
func TestCacheReaderSeeker(t *testing.T) {
	data := make([]byte, 8192)
	rnd := rand.New(rand.NewSource(1))
	rnd.Read(data)

	t.Run("reads everything in short chunks", func(t *testing.T) {
		rs := newCacheReaderSeeker(data)
		var got bytes.Buffer
		buf := make([]byte, 100)
		for {
			n, err := rs.Read(buf)
			if n == 0 && err == nil {
				t.Fatal("Read returned no bytes and no error, which loops forever")
			}
			got.Write(buf[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
		}
		if !bytes.Equal(got.Bytes(), data) {
			t.Errorf("read back %d bytes, want %d", got.Len(), len(data))
		}
	})

	t.Run("matches bytes.Reader", func(t *testing.T) {
		mine := newCacheReaderSeeker(data)
		want := bytes.NewReader(data)
		seeks := []struct {
			offset int64
			whence int
		}{
			{0, io.SeekStart}, {4096, io.SeekStart}, {-1024, io.SeekEnd},
			{0, io.SeekEnd}, {100, io.SeekCurrent}, {-50, io.SeekCurrent},
			{int64(len(data)), io.SeekStart},
		}
		for _, s := range seeks {
			gotPos, gotErr := mine.Seek(s.offset, s.whence)
			wantPos, wantErr := want.Seek(s.offset, s.whence)
			if (gotErr == nil) != (wantErr == nil) || gotPos != wantPos {
				t.Fatalf("Seek(%d, %d) = %d, %v; bytes.Reader gives %d, %v",
					s.offset, s.whence, gotPos, gotErr, wantPos, wantErr)
			}
			mineBuf := make([]byte, 64)
			wantBuf := make([]byte, 64)
			gotN, gotErr := mine.Read(mineBuf)
			wantN, wantErr := want.Read(wantBuf)
			if gotN != wantN || (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("after Seek(%d, %d), Read = %d, %v; bytes.Reader gives %d, %v",
					s.offset, s.whence, gotN, gotErr, wantN, wantErr)
			}
			if !bytes.Equal(mineBuf[:gotN], wantBuf[:wantN]) {
				t.Fatalf("after Seek(%d, %d) the bytes differ", s.offset, s.whence)
			}
		}
	})

	t.Run("rejects a seek before the start", func(t *testing.T) {
		rs := newCacheReaderSeeker(data)
		if _, err := rs.Seek(-1, io.SeekStart); err == nil {
			t.Error("Seek(-1, SeekStart) was accepted")
		}
	})
}
