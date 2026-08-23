package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// The program stream muxer is what gb28181 pipelines rely on, and a device
// that produces a stream only gomedia can read is no use. ffmpeg writes the
// input, gomedia converts it and ffmpeg has to demux the result back into the
// same pictures.
func TestMuxTSToPS(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name string
		clip mediatest.Clip
	}{
		{"h264 and aac", mediatest.Clip{Container: "mpegts", Video: "libx264", Audio: "aac"}},
		{"h264 only", mediatest.Clip{Container: "mpegts", Video: "libx264"}},
		{"h265 and aac", mediatest.Clip{Container: "mpegts", Video: "libx265", Audio: "aac"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			dst := filepath.Join(t.TempDir(), "out.ps")

			if err := MuxTSToPS(src, dst); err != nil {
				t.Fatalf("mux: %v", err)
			}

			out := tools.Probe(t, dst)
			video, ok := out.Video()
			if !ok {
				t.Fatalf("ffprobe found no video in the program stream (format %q)", out.Format.FormatName)
			}

			in := tools.Probe(t, src)
			srcVideo, _ := in.Video()
			if video.CodecName != srcVideo.CodecName {
				t.Errorf("video codec %q, want %q", video.CodecName, srcVideo.CodecName)
			}
			if video.Width != srcVideo.Width || video.Height != srcVideo.Height {
				t.Errorf("resolution %dx%d, want %dx%d",
					video.Width, video.Height, srcVideo.Width, srcVideo.Height)
			}
			if video.Frames() != srcVideo.Frames() {
				t.Errorf("%d frames in the program stream, want %d", video.Frames(), srcVideo.Frames())
			}
			tools.AssertSameDecoded(t, src, dst, "v:0")
		})
	}
}
