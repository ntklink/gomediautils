package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes an flv with G.711 audio, the shape a camera publishes over
// RTMP; GoMediaUtils writes that audio into a WAV file, and ffmpeg has to
// read it as the same codec and decode the same samples.
func TestConvertFLVToWAV(t *testing.T) {
	tools := mediatest.Require(t)
	for _, audio := range []string{"pcm_alaw", "pcm_mulaw"} {
		t.Run(audio, func(t *testing.T) {
			src := tools.MakeClip(t, mediatest.Clip{Container: "flv", Video: "libx264", Audio: audio})
			dst := filepath.Join(t.TempDir(), "out.wav")
			if err := ConvertFLVToWAV(src, dst); err != nil {
				t.Fatalf("convert: %v", err)
			}
			out := tools.MustProbe(t, dst, 1)
			if out.Format.FormatName != "wav" {
				t.Errorf("format %q, want wav", out.Format.FormatName)
			}
			tools.AssertDecodable(t, dst, "Guessed Channel Layout")
			got, _ := out.Audio()
			if got.CodecName != audio || got.SampleRate != "8000" || got.Channels != 1 {
				t.Errorf("audio %s %s Hz %d ch, want %s 8000 Hz mono", got.CodecName, got.SampleRate, got.Channels, audio)
			}
			if out.Format.Seconds() < 1.9 {
				t.Errorf("duration %gs, want 2s", out.Format.Seconds())
			}
			tools.AssertSameDecoded(t, src, dst, "a:0")
		})
	}
}
