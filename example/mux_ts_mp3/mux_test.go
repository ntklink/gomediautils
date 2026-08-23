package main

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// mp3 in ts uses stream type 0x03, a different pes path from aac, and the
// frame durations come from the frame headers rather than a constant. Both
// a constant and a variable bitrate stream are muxed so the header parsing
// is exercised, not just the first frame's.
func TestMuxMP3ToTS(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name  string
		extra []string
	}{
		{"constant bitrate", []string{"-b:a", "128k"}},
		{"variable bitrate", []string{"-q:a", "5"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clip := mediatest.Clip{Audio: "libmp3lame", Seconds: 2, Extra: tc.extra}
			src := tools.MakeElementaryStream(t, clip, "mp3")
			dst := filepath.Join(t.TempDir(), "out.ts")

			if err := MuxMP3ToTS(src, dst); err != nil {
				t.Fatalf("mux: %v", err)
			}

			out := tools.MustProbe(t, dst, 1)
			if out.Format.FormatName != "mpegts" {
				t.Fatalf("ffprobe read the output as %q, want mpegts", out.Format.FormatName)
			}
			tools.AssertDecodable(t, dst)

			srcAudio, _ := tools.Probe(t, src).Audio()
			dstAudio, ok := out.Audio()
			if !ok {
				t.Fatal("no audio stream in the ts")
			}
			if dstAudio.CodecName != "mp3" {
				t.Errorf("codec %q, want mp3", dstAudio.CodecName)
			}
			if dstAudio.Packets() != srcAudio.Packets() {
				t.Errorf("%d frames in the ts, want %d", dstAudio.Packets(), srcAudio.Packets())
			}
			tools.AssertSameDecoded(t, src, dst, "a:0")

			if got := out.Format.Seconds(); math.Abs(got-2) > 0.15 {
				t.Errorf("the ts says it is %.3fs long, want about 2s", got)
			}
			mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "a:0"), "ts audio")
		})
	}
}
