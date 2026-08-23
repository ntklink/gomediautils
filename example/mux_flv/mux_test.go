package main

import (
	"path/filepath"
	"testing"

	"github.com/yapingcat/gomedia/example/internal/mediatest"
)

// The remux goes flv -> elementary streams -> flv, so both halves of the flv
// code are on the hook: whatever the reader loses or the writer mangles ends
// up in the decoded checksum.
func TestRemuxFLV(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name  string
		clip  mediatest.Clip
		audio bool
	}{
		{
			name: "h264 and aac",
			clip: mediatest.Clip{Container: "flv", Video: "libx264", Audio: "aac",
				Seconds: 2, BFrames: 2},
			audio: true,
		},
		{
			name: "h264 and mp3",
			clip: mediatest.Clip{Container: "flv", Video: "libx264", Audio: "libmp3lame",
				Seconds: 2},
			audio: true,
		},
		{
			name:  "video only",
			clip:  mediatest.Clip{Container: "flv", Video: "libx264", Seconds: 2, BFrames: 2},
			audio: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			dst := filepath.Join(t.TempDir(), "out.flv")

			if err := RemuxFLV(src, dst); err != nil {
				t.Fatalf("remux: %v", err)
			}

			wantStreams := 1
			if tc.audio {
				wantStreams = 2
			}
			out := tools.MustProbe(t, dst, wantStreams)
			if out.Format.FormatName != "flv" {
				t.Fatalf("ffprobe read the output as %q, want flv", out.Format.FormatName)
			}
			tools.AssertDecodable(t, dst)

			srcVideo, _ := tools.Probe(t, src).Video()
			dstVideo, _ := out.Video()
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames after the remux, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertSameDecoded(t, src, dst, "v:0")
			if tc.audio {
				tools.AssertSameDecoded(t, src, dst, "a:0")
			}

			// timestamps survive too: flv stores dts and a 24 bit signed
			// composition offset, which is where a reordered stream breaks
			mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "flv video")
			mediatest.AssertSameTimestamps(t,
				tools.Packets(t, src, "v:0"), tools.Packets(t, dst, "v:0"), 0.002, "flv video")
		})
	}
}
