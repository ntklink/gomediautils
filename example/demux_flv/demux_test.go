package main

import (
	"os"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes the flv, GoMediaUtils takes it apart, and the elementary streams
// have to decode to what ffmpeg itself would have extracted. The aac case
// also covers the sequence header path: flv carries the audio specific
// config in a tag of its own, and a demuxer that forgets to turn it back
// into adts headers produces a file no decoder can open.
func TestDemuxFLV(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name     string
		clip     mediatest.Clip
		videoExt string
		audioExt string
	}{
		{
			name: "h264 and aac",
			clip: mediatest.Clip{Container: "flv", Video: "libx264", Audio: "aac",
				Seconds: 2, BFrames: 2},
			videoExt: "h264", audioExt: "aac",
		},
		{
			name: "h264 and mp3",
			clip: mediatest.Clip{Container: "flv", Video: "libx264", Audio: "libmp3lame",
				Seconds: 2},
			videoExt: "h264", audioExt: "mp3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			outDir := t.TempDir()

			files, err := DemuxFLV(src, outDir)
			if err != nil {
				t.Fatalf("demux: %v", err)
			}

			videoPath, ok := files[tc.videoExt]
			if !ok {
				t.Fatalf("no %s stream came out, got %v", tc.videoExt, files)
			}
			audioPath, ok := files[tc.audioExt]
			if !ok {
				t.Fatalf("no %s stream came out, got %v", tc.audioExt, files)
			}
			for _, p := range []string{videoPath, audioPath} {
				if st, err := os.Stat(p); err != nil || st.Size() == 0 {
					t.Fatalf("%s is empty", p)
				}
			}

			tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:v:0", tc.videoExt), videoPath, "v:0")
			tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:a:0", tc.audioExt), audioPath, "a:0")

			srcVideo, _ := tools.Probe(t, src).Video()
			gotVideo, _ := tools.Probe(t, videoPath).Video()
			if gotVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames demuxed, want %d", gotVideo.Frames(), srcVideo.Frames())
			}
		})
	}
}
