package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// The ts muxer is fed a bare Annex-B stream that ffmpeg produced and the
// result has to be a transport stream ffmpeg can demux back into the same
// pictures. This is the direction the unit tests cannot check on their own:
// a muxer that agrees with gomedia's own demuxer can still write a pat, pmt
// or pes header no other implementation accepts.
func TestMuxH264ToTS(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name string
		clip mediatest.Clip
	}{
		{"short gop", mediatest.Clip{Video: "libx264", GOP: 12, Seconds: 2}},
		{"long gop", mediatest.Clip{Video: "libx264", GOP: 250, Seconds: 2}},
		// no b frames: the example synthesises timestamps in decode order,
		// which cannot express a reordered presentation. See MuxH264ToTS.
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeElementaryStream(t, tc.clip, "h264")
			dst := filepath.Join(t.TempDir(), "out.ts")

			if err := MuxH264ToTS(src, dst); err != nil {
				t.Fatalf("mux: %v", err)
			}

			out := tools.MustProbe(t, dst, 1)
			if out.Format.FormatName != "mpegts" {
				t.Fatalf("ffprobe read the output as %q, want mpegts", out.Format.FormatName)
			}
			tools.AssertDecodable(t, dst)

			in := tools.Probe(t, src)
			srcVideo, _ := in.Video()
			dstVideo, _ := out.Video()
			if dstVideo.CodecName != "h264" {
				t.Errorf("codec %q, want h264", dstVideo.CodecName)
			}
			if dstVideo.Width != srcVideo.Width || dstVideo.Height != srcVideo.Height {
				t.Errorf("resolution %dx%d, want %dx%d",
					dstVideo.Width, dstVideo.Height, srcVideo.Width, srcVideo.Height)
			}
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d frames in the ts, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertSameDecoded(t, src, dst, "v:0")
		})
	}
}
