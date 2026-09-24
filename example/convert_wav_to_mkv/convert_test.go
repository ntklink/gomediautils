package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes a G.711 WAV file, GoMediaUtils reads it and muxes the audio
// into Matroska, and ffmpeg has to decode the same samples out of that.
func TestConvertWAVToMKV(t *testing.T) {
	tools := mediatest.Require(t)
	for _, audio := range []string{"pcm_alaw", "pcm_mulaw"} {
		t.Run(audio, func(t *testing.T) {
			src := tools.MakeClip(t, mediatest.Clip{Container: "wav", Audio: audio, Seconds: 3})
			dst := filepath.Join(t.TempDir(), "out.mkv")
			if err := ConvertWAVToMKV(src, dst); err != nil {
				t.Fatalf("convert: %v", err)
			}
			out := tools.MustProbe(t, dst, 1)
			tools.AssertDecodable(t, dst, "Guessed Channel Layout")
			got, _ := out.Audio()
			if got.CodecName != audio || got.SampleRate != "8000" {
				t.Errorf("audio %s %s Hz, want %s 8000 Hz", got.CodecName, got.SampleRate, audio)
			}
			if s := out.Format.Seconds(); s < 2.95 || s > 3.05 {
				t.Errorf("duration %gs, want 3s", s)
			}
			tools.AssertSameDecoded(t, src, dst, "a:0")
		})
	}
}
