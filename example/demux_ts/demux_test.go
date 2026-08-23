package main

import (
	"os"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// Demuxing has to give back exactly what the muxer put in. ffmpeg encodes a
// clip into ts, the example takes it apart, and the elementary streams are
// decoded and compared against the same streams extracted by ffmpeg itself.
// Comparing decoded samples rather than byte counts is what makes this catch
// a demuxer that drops a nalu or splices two access units together.
func TestDemuxTS(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name     string
		clip     mediatest.Clip
		video    string
		audio    string
		videoExt string
		audioExt string
	}{
		{
			name: "h264 and aac",
			clip: mediatest.Clip{Container: "mpegts", Video: "libx264", Audio: "aac",
				Seconds: 2, BFrames: 2},
			video: "h264", audio: "aac", videoExt: "h264", audioExt: "aac",
		},
		{
			name: "h265 and mp3",
			clip: mediatest.Clip{Container: "mpegts", Video: "libx265", Audio: "libmp3lame",
				Seconds: 2},
			video: "hevc", audio: "mp3", videoExt: "h265", audioExt: "mp3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			outDir := t.TempDir()

			files, err := DemuxTS(src, outDir)
			if err != nil {
				t.Fatalf("demux: %v", err)
			}

			videoPath, ok := files[tc.videoExt]
			if !ok {
				t.Fatalf("no %s stream came out, got %v", tc.videoExt, keys(files))
			}
			audioPath, ok := files[tc.audioExt]
			if !ok {
				t.Fatalf("no %s stream came out, got %v", tc.audioExt, keys(files))
			}
			for _, p := range []string{videoPath, audioPath} {
				if st, err := os.Stat(p); err != nil || st.Size() == 0 {
					t.Fatalf("%s is empty", p)
				}
			}

			// the same streams, extracted by ffmpeg, are the reference
			wantVideo := tools.ExtractStream(t, src, "0:v:0", tc.videoExt)
			wantAudio := tools.ExtractStream(t, src, "0:a:0", tc.audioExt)

			tools.AssertSameDecoded(t, wantVideo, videoPath, "v:0")
			tools.AssertSameDecoded(t, wantAudio, audioPath, "a:0")

			// and the count has to line up too, so a stream that decodes to
			// the same pictures but lost a frame at the tail still fails
			in := tools.Probe(t, src)
			srcVideo, _ := in.Video()
			gotVideo, _ := tools.Probe(t, videoPath).Video()
			if gotVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames demuxed, want %d", gotVideo.Frames(), srcVideo.Frames())
			}
		})
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
