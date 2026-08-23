package main

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// Audio-only transport streams are their own case: there is no video pid to
// carry the pcr, so the muxer has to put it on the audio pid instead. A file
// with no pcr at all decodes but nothing can seek in it, which is why the
// test asserts on the duration ffprobe reports rather than only on frames.
func TestMuxAACToTS(t *testing.T) {
	tools := mediatest.Require(t)

	for _, rate := range []string{"48000", "44100"} {
		t.Run(rate+"Hz", func(t *testing.T) {
			clip := mediatest.Clip{Audio: "aac", Seconds: 2, Extra: []string{"-ar", rate}}
			src := tools.MakeElementaryStream(t, clip, "adts")
			dst := filepath.Join(t.TempDir(), "out.ts")

			if err := MuxAACToTS(src, dst); err != nil {
				t.Fatalf("mux: %v", err)
			}

			out := tools.MustProbe(t, dst, 1)
			if out.Format.FormatName != "mpegts" {
				t.Fatalf("ffprobe read the output as %q, want mpegts", out.Format.FormatName)
			}
			tools.AssertDecodable(t, dst)

			in := tools.Probe(t, src)
			srcAudio, _ := in.Audio()
			dstAudio, ok := out.Audio()
			if !ok {
				t.Fatal("no audio stream in the ts")
			}
			if dstAudio.CodecName != "aac" {
				t.Errorf("codec %q, want aac", dstAudio.CodecName)
			}
			if dstAudio.SampleRate != srcAudio.SampleRate {
				t.Errorf("sample rate %q, want %q", dstAudio.SampleRate, srcAudio.SampleRate)
			}
			if dstAudio.Packets() != srcAudio.Packets() {
				t.Errorf("%d frames in the ts, want %d", dstAudio.Packets(), srcAudio.Packets())
			}
			tools.AssertSameDecoded(t, src, dst, "a:0")

			// the timestamps have to describe a two second clip, not a
			// stream that plays at the wrong speed
			if got := out.Format.Seconds(); math.Abs(got-2) > 0.15 {
				t.Errorf("the ts says it is %.3fs long, want about 2s", got)
			}
			mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "a:0"), "ts audio")
		})
	}
}
