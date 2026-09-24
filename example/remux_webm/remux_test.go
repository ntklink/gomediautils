package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes a VP8 and Opus WebM file, GoMediaUtils reads it and writes
// a new one, and ffmpeg has to decode the same pictures and samples out of
// it: once as a seekable file with cues and once as a live stream.
func TestRemuxWebM(t *testing.T) {
	tools := mediatest.Require(t)
	src := tools.MakeClip(t, mediatest.Clip{Container: "webm", Video: "libvpx", Audio: "libopus", Seconds: 3})

	for _, live := range []bool{false, true} {
		name := "file"
		if live {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "out.webm")
			if err := RemuxWebM(src, dst, live); err != nil {
				t.Fatalf("remux: %v", err)
			}
			data, err := os.ReadFile(dst)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data[:64], []byte("webm")) {
				t.Error("the EBML header does not declare webm")
			}

			out := tools.MustProbe(t, dst, 2)
			tools.AssertDecodable(t, dst)
			// a live stream declares no duration, ffprobe estimates one
			if !live && out.Format.Seconds() < 2.9 {
				t.Errorf("duration %gs, want about 3s", out.Format.Seconds())
			}

			in := tools.Probe(t, src)
			srcVideo, _ := in.Video()
			dstVideo, _ := out.Video()
			if dstVideo.CodecName != "vp8" || dstVideo.Width != srcVideo.Width || dstVideo.Height != srcVideo.Height {
				t.Errorf("video %s %dx%d", dstVideo.CodecName, dstVideo.Width, dstVideo.Height)
			}
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertSameDecoded(t, src, dst, "v:0")
			tools.AssertSameDecoded(t, src, dst, "a:0")
			mediatest.AssertSameTimestamps(t, tools.Packets(t, src, "v:0"), tools.Packets(t, dst, "v:0"), 0.002, "webm video")
			mediatest.AssertSameTimestamps(t, tools.Packets(t, src, "a:0"), tools.Packets(t, dst, "a:0"), 0.002, "webm audio")
		})
	}
}
