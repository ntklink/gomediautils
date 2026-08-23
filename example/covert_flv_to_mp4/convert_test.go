package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes a real flv, GoMediaUtils turns it into an mp4, and ffmpeg has to
// decode the same pictures out of the result.
func TestConvertFLVToMP4(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name string
		clip mediatest.Clip
	}{
		{"h264 and aac", mediatest.Clip{Container: "flv", Video: "libx264", Audio: "aac"}},
		{"h264 with b frames", mediatest.Clip{Container: "flv", Video: "libx264", Audio: "aac", BFrames: 2}},
		{"h264 and mp3", mediatest.Clip{Container: "flv", Video: "libx264", Audio: "libmp3lame"}},
		{"video only", mediatest.Clip{Container: "flv", Video: "libx264"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			dst := filepath.Join(t.TempDir(), "out.mp4")

			if err := ConvertFLVToMP4(src, dst); err != nil {
				t.Fatalf("convert: %v", err)
			}

			wantStreams := 1
			if tc.clip.Audio != "" {
				wantStreams = 2
			}
			out := tools.MustProbe(t, dst, wantStreams)
			tools.AssertDecodable(t, dst)

			in := tools.Probe(t, src)
			srcVideo, _ := in.Video()
			dstVideo, ok := out.Video()
			if !ok {
				t.Fatal("no video stream in the mp4")
			}
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertSameDecoded(t, src, dst, "v:0")
			mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "mp4 video")

			if tc.clip.Audio != "" {
				if _, ok := out.Audio(); !ok {
					t.Fatal("no audio stream in the mp4")
				}
				mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "a:0"), "mp4 audio")
			}
		})
	}
}
