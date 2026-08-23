package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// The frame walk has to agree with ffprobe on how many frames there are,
// where each one starts and how big it is. ffprobe reports exactly that, so
// the two can be compared frame by frame rather than only in aggregate.
func TestReadMP3Frames(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name  string
		extra []string
	}{
		{"constant bitrate 128k", []string{"-b:a", "128k", "-ar", "44100"}},
		{"variable bitrate", []string{"-q:a", "5", "-ar", "44100"}},
		{"mpeg2 low sample rate", []string{"-b:a", "64k", "-ar", "22050"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeElementaryStream(t,
				mediatest.Clip{Audio: "libmp3lame", Seconds: 2, Extra: tc.extra}, "mp3")

			frames, err := ReadMP3Frames(src)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(frames) == 0 {
				t.Fatal("no frames found")
			}

			packets := tools.Packets(t, src, "a:0")
			if len(frames) != len(packets) {
				t.Fatalf("%d frames found, ffprobe sees %d", len(frames), len(packets))
			}
			for i, p := range packets {
				if frames[i].Size != p.Size() {
					t.Fatalf("frame %d is %d bytes, ffprobe says %d",
						i, frames[i].Size, p.Size())
				}
				if frames[i].Offset != p.Pos() {
					t.Fatalf("frame %d starts at %d, ffprobe says %d",
						i, frames[i].Offset, p.Pos())
				}
			}

			// and the headers have to describe the stream ffmpeg encoded
			stream, _ := tools.Probe(t, src).Audio()
			if got := frames[0].SampleRate; got != atoi(t, stream.SampleRate) {
				t.Errorf("sample rate %d, want %s", got, stream.SampleRate)
			}
			if got := frames[0].ChannelCount; got != stream.Channels {
				t.Errorf("%d channels, want %d", got, stream.Channels)
			}
			if got := TotalDuration(frames) / 1000; math.Abs(got-2) > 0.1 {
				t.Errorf("the frames add up to %.3fs, want about 2s", got)
			}
		})
	}
}

// An id3v2 tag sits in front of the audio and is not a frame. Skipping it is
// the difference between parsing an mp3 and finding garbage in it.
func TestReadMP3SkipsID3Tag(t *testing.T) {
	tools := mediatest.Require(t)

	bare := tools.MakeElementaryStream(t,
		mediatest.Clip{Audio: "libmp3lame", Seconds: 2, Extra: []string{"-b:a", "128k"}}, "mp3")

	tagged := filepath.Join(t.TempDir(), "tagged.mp3")
	tools.Run(t, tools.FFmpeg, mediatest.FFmpegArgs(
		"-i", bare, "-c", "copy",
		"-id3v2_version", "3", "-write_xing", "0",
		"-metadata", "title=a title long enough to matter",
		"-metadata", "artist=gomedia",
		"-f", "mp3", tagged)...)

	if st, err := os.Stat(tagged); err != nil || st.Size() <= fileSize(t, bare) {
		t.Skip("ffmpeg did not actually add a tag")
	}

	want, err := ReadMP3Frames(bare)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadMP3Frames(tagged)
	if err != nil {
		t.Fatalf("read the tagged file: %v", err)
	}
	if len(got) != len(want) {
		t.Errorf("%d frames in the tagged file, want %d; the id3 tag was not skipped",
			len(got), len(want))
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}
