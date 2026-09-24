package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes an mp4, GoMediaUtils remuxes it into Matroska, and ffmpeg
// has to decode exactly the same pictures and samples out of the result.
// b frames matter here: Matroska keeps presentation times only, so a muxer
// that stored decode times would reorder the pictures.
func TestConvertMP4ToMKV(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name string
		clip mediatest.Clip
		// the elementary stream the audio is compared with, "" to compare
		// with the mp4 itself
		audioExt string
	}{
		{"h264 and aac with b frames", mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "aac", BFrames: 2}, "aac"},
		{"h265 and aac", mediatest.Clip{Container: "mp4", Video: "libx265", Audio: "aac", BFrames: 2}, "aac"},
		{"h264 and opus", mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "libopus", BFrames: 3}, ""},
		{"h264 and mp3", mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "libmp3lame"}, "mp3"},
		{"h264 and g711 a-law in mov", mediatest.Clip{Container: "mov", Video: "libx264", Audio: "pcm_alaw"}, ""},
		{"h264 and g711 mu-law in mov", mediatest.Clip{Container: "mov", Video: "libx264", Audio: "pcm_mulaw"}, ""},
		{"h264 only, long gop", mediatest.Clip{Container: "mp4", Video: "libx264", GOP: 250, BFrames: 3}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			dst := filepath.Join(t.TempDir(), "out.mkv")
			if err := ConvertMP4ToMKV(src, dst); err != nil {
				t.Fatalf("convert: %v", err)
			}

			wantStreams := 1
			if tc.clip.Audio != "" {
				wantStreams = 2
			}
			out := tools.MustProbe(t, dst, wantStreams)
			if out.Format.FormatName != "matroska,webm" {
				t.Errorf("format %q, want matroska", out.Format.FormatName)
			}
			if out.Format.Seconds() < tc.clip.Seconds*0.9 {
				t.Errorf("duration %gs, want about %gs", out.Format.Seconds(), tc.clip.Seconds)
			}
			// ffmpeg guesses the layout of every A_MS/ACM track, its own too
			tools.AssertDecodable(t, dst, "Guessed Channel Layout")

			in := tools.Probe(t, src)
			srcVideo, _ := in.Video()
			dstVideo, ok := out.Video()
			if !ok {
				t.Fatal("no video stream in the mkv")
			}
			if dstVideo.CodecName != srcVideo.CodecName ||
				dstVideo.Width != srcVideo.Width || dstVideo.Height != srcVideo.Height {
				t.Errorf("video %s %dx%d, want %s %dx%d", dstVideo.CodecName, dstVideo.Width, dstVideo.Height,
					srcVideo.CodecName, srcVideo.Width, srcVideo.Height)
			}
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertSameDecoded(t, src, dst, "v:0")
			// the mp4 edit list moves the whole source timeline, which the
			// mp4 demuxer does not apply; the spacing has to survive
			mediatest.AssertSameTimestamps(t, mediatest.FromZero(tools.Packets(t, src, "v:0")),
				mediatest.FromZero(tools.Packets(t, dst, "v:0")), 0.002, "mkv video")

			if tc.clip.Audio != "" {
				srcAudio, _ := in.Audio()
				dstAudio, ok := out.Audio()
				if !ok {
					t.Fatal("no audio stream in the mkv")
				}
				if dstAudio.CodecName != srcAudio.CodecName || dstAudio.Channels != srcAudio.Channels ||
					dstAudio.SampleRate != srcAudio.SampleRate {
					t.Errorf("audio %s %d ch %s Hz, want %s %d ch %s Hz",
						dstAudio.CodecName, dstAudio.Channels, dstAudio.SampleRate,
						srcAudio.CodecName, srcAudio.Channels, srcAudio.SampleRate)
				}
				// the mp4 edit list hides the aac and mp3 priming samples,
				// which Matroska keeps like the bare stream does; the opus
				// pre-skip maps onto CodecDelay, so opus matches the mp4
				want := src
				if tc.audioExt != "" {
					want = tools.ExtractStream(t, src, "0:a:0", tc.audioExt)
				}
				tools.AssertSameDecoded(t, want, dst, "a:0")
			}
		})
	}
}
