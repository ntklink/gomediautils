package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes an ogg file, GoMediaUtils pulls the opus stream out of it and
// writes an mp4, and ffmpeg has to decode the same audio out of that.
//
// This is the only coverage the ogg demuxer gets against a real file: page
// segmentation, packets that span pages and the granule positions the
// timestamps come from all have to match what a real encoder produced.
func TestConvertOggToMP4(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "ogg", Audio: "libopus", Seconds: 3,
	})
	dst := filepath.Join(t.TempDir(), "out.mp4")

	if err := ConvertOggToMP4(src, dst); err != nil {
		t.Fatalf("convert: %v", err)
	}

	out := tools.MustProbe(t, dst, 1)
	tools.AssertDecodable(t, dst)

	audio, ok := out.Audio()
	if !ok {
		t.Fatalf("no audio in the mp4 (format %q)", out.Format.FormatName)
	}
	if audio.CodecName != "opus" {
		t.Errorf("codec %q, want opus", audio.CodecName)
	}

	in := tools.Probe(t, src)
	srcAudio, _ := in.Audio()
	if audio.Channels != srcAudio.Channels {
		t.Errorf("%d channels, want %d", audio.Channels, srcAudio.Channels)
	}
	if got, want := audio.Frames(), srcAudio.Frames(); got != want {
		t.Errorf("%d opus frames, want %d", got, want)
	}
}
