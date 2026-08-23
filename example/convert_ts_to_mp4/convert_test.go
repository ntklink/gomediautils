package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes a real transport stream, gomedia turns it into an mp4, and
// ffmpeg has to be able to read that mp4 back and decode exactly the same
// pictures and samples. A remux that loses a frame, drops the parameter sets
// or mangles the composition offsets fails here even though it would still
// "round trip" through gomedia's own demuxer.
func TestConvertTSToMP4(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name  string
		clip  mediatest.Clip
		audio string // ffmpeg stream selector, "" when the clip has no audio
	}{
		{
			name: "h264 and aac with b frames",
			clip: mediatest.Clip{Container: "mpegts", Video: "libx264", Audio: "aac", BFrames: 2},
		},
		{
			name: "h264 only, no b frames",
			clip: mediatest.Clip{Container: "mpegts", Video: "libx264"},
		},
		{
			name: "h265 and aac",
			clip: mediatest.Clip{Container: "mpegts", Video: "libx265", Audio: "aac", BFrames: 2},
		},
		{
			name: "h264 and mp3",
			clip: mediatest.Clip{Container: "mpegts", Video: "libx264", Audio: "libmp3lame"},
		},
		{
			name: "long gop, one key frame for the whole clip",
			clip: mediatest.Clip{Container: "mpegts", Video: "libx264", Audio: "aac", GOP: 250, BFrames: 3},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			dst := filepath.Join(t.TempDir(), "out.mp4")

			if err := ConvertTSToMP4(src, dst); err != nil {
				t.Fatalf("convert: %v", err)
			}

			wantStreams := 1
			if tc.clip.Audio != "" {
				wantStreams = 2
			}
			out := tools.MustProbe(t, dst, wantStreams)
			if out.Format.FormatName == "" {
				t.Fatal("ffprobe did not recognise the output as any container")
			}
			tools.AssertDecodable(t, dst)

			in := tools.Probe(t, src)
			srcVideo, _ := in.Video()
			dstVideo, ok := out.Video()
			if !ok {
				t.Fatal("no video stream in the mp4")
			}
			if dstVideo.Width != srcVideo.Width || dstVideo.Height != srcVideo.Height {
				t.Errorf("resolution %dx%d, want %dx%d",
					dstVideo.Width, dstVideo.Height, srcVideo.Width, srcVideo.Height)
			}
			if dstVideo.CodecName != srcVideo.CodecName {
				t.Errorf("video codec %q, want %q", dstVideo.CodecName, srcVideo.CodecName)
			}
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames, want %d", dstVideo.Frames(), srcVideo.Frames())
			}

			// the strongest check: the pictures themselves survived
			tools.AssertSameDecoded(t, src, dst, "v:0")

			mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "mp4 video")

			if tc.clip.Audio != "" {
				srcAudio, _ := in.Audio()
				dstAudio, ok := out.Audio()
				if !ok {
					t.Fatal("no audio stream in the mp4")
				}
				if dstAudio.CodecName != srcAudio.CodecName {
					t.Errorf("audio codec %q, want %q", dstAudio.CodecName, srcAudio.CodecName)
				}
				mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "a:0"), "mp4 audio")
			}
		})
	}
}
