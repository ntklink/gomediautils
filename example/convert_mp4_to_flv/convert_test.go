package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// The mp4 demuxer and the flv muxer are exercised together against real
// files: ffmpeg makes the mp4, gomedia converts, ffmpeg decodes the flv.
func TestConvertMP4ToFLV(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name string
		clip mediatest.Clip
	}{
		{"h264 and aac", mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "aac"}},
		{"h264 with b frames", mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "aac", BFrames: 2}},
		{"h265 and aac", mediatest.Clip{Container: "mp4", Video: "libx265", Audio: "aac"}},
		{"video only", mediatest.Clip{Container: "mp4", Video: "libx264"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			dst := filepath.Join(t.TempDir(), "out.flv")

			if err := ConvertMP4ToFLV(src, dst); err != nil {
				t.Fatalf("convert: %v", err)
			}

			out := tools.Probe(t, dst)
			if out.Format.FormatName != "flv" {
				t.Fatalf("ffprobe read the output as %q, want flv", out.Format.FormatName)
			}
			tools.AssertDecodable(t, dst)

			in := tools.Probe(t, src)
			srcVideo, _ := in.Video()
			dstVideo, ok := out.Video()
			if !ok {
				t.Fatal("no video stream in the flv")
			}
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertSameDecoded(t, src, dst, "v:0")
			mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "flv video")

			if tc.clip.Audio != "" {
				if _, ok := out.Audio(); !ok {
					t.Fatal("no audio stream in the flv")
				}
				mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "a:0"), "flv audio")
			}
		})
	}
}
