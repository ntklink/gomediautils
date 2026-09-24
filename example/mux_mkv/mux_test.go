package main

import (
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg encodes a clip into a container and GoMediaUtils is handed the
// bare streams ffmpeg pulls out of it. A bare stream carries no timestamps,
// so the es readers have to recover them: the frame rate from the SPS and,
// with b frames, the presentation order from the picture order counts. The
// Matroska file has to decode to the same pictures and show them at the
// same times as the source container did.
func TestMuxElementaryStreams(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name     string
		clip     mediatest.Clip
		videoExt string
		audioExt string
	}{
		{"h264 with b frames and aac", mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "aac", BFrames: 2}, "h264", "aac"},
		{"h264 b-pyramid and mp3", mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "libmp3lame", BFrames: 3}, "h264", "mp3"},
		{"h265 with b frames", mediatest.Clip{Container: "mp4", Video: "libx265", BFrames: 3, GOP: 12}, "h265", ""},
		{"h264 without b frames and g711", mediatest.Clip{Container: "mov", Video: "libx264", Audio: "pcm_alaw"}, "h264", "alaw"},
		// four slices per picture: an access unit ends at the next first slice
		// of a picture, not at every slice
		{"h264 with four slices per picture", mediatest.Clip{Container: "mp4", Video: "libx264", BFrames: 2,
			Extra: []string{"-x264-params", "slices=4"}}, "h264", ""},
		{"h264 at 30 fps, long gop", mediatest.Clip{Container: "mp4", Video: "libx264", BFrames: 2, FPS: 30, GOP: 250}, "h264", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			video := tools.ExtractStream(t, src, "0:v:0", tc.videoExt)
			audio := ""
			if tc.audioExt != "" {
				audio = tools.ExtractStream(t, src, "0:a:0", tc.audioExt)
			}
			dst := filepath.Join(t.TempDir(), "out.mkv")
			if err := MuxElementaryStreams(video, audio, dst); err != nil {
				t.Fatalf("mux: %v", err)
			}

			wantStreams := 1
			if audio != "" {
				wantStreams = 2
			}
			out := tools.MustProbe(t, dst, wantStreams)
			tools.AssertDecodable(t, dst, "Guessed Channel Layout")

			srcVideo, _ := tools.Probe(t, src).Video()
			dstVideo, _ := out.Video()
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertSameDecoded(t, video, dst, "v:0")
			// the pictures are shown when the source container showed them
			mediatest.AssertSameTimestamps(t, mediatest.FromZero(tools.Packets(t, src, "v:0")),
				mediatest.FromZero(tools.Packets(t, dst, "v:0")), 0.002, "video")
			if audio != "" {
				// ffmpeg cannot open bare g711 without being told its
				// format; the mov it came from decodes to the same samples
				want := audio
				if tc.audioExt == "alaw" {
					want = src
				}
				tools.AssertSameDecoded(t, want, dst, "a:0")
			}
		})
	}
}
