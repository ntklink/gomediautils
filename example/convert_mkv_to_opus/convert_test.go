package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes WebM and Matroska files with Opus audio, GoMediaUtils moves
// the audio into an Ogg Opus file, and ffmpeg has to decode exactly the same
// samples out of it: the pre-skip, every packet and the trimmed end.
func TestConvertMKVToOpus(t *testing.T) {
	tools := mediatest.Require(t)
	for _, clip := range []mediatest.Clip{
		{Container: "webm", Video: "libvpx", Audio: "libopus", Seconds: 3},
		{Container: "matroska", Video: "libx264", Audio: "libopus", BFrames: 3},
		{Container: "webm", Audio: "libopus", Seconds: 70},
	} {
		t.Run(clip.Container+"_"+clip.Video, func(t *testing.T) {
			src := tools.MakeClip(t, clip)
			dst := filepath.Join(t.TempDir(), "out.opus")
			if err := ConvertMKVToOpus(src, dst); err != nil {
				t.Fatalf("convert: %v", err)
			}
			out := tools.MustProbe(t, dst, 1)
			if out.Format.FormatName != "ogg" {
				t.Errorf("format %q, want ogg", out.Format.FormatName)
			}
			tools.AssertDecodable(t, dst)
			got, _ := out.Audio()
			want, _ := tools.Probe(t, src).Audio()
			if got.CodecName != "opus" || got.Channels != want.Channels {
				t.Errorf("audio %s %d ch, want opus %d ch", got.CodecName, got.Channels, want.Channels)
			}
			if got.Packets() != want.Packets() {
				t.Errorf("%d packets, want %d", got.Packets(), want.Packets())
			}
			tools.AssertSameDecoded(t, src, dst, "a:0")
		})
	}
}
