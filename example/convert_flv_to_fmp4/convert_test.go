package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yapingcat/gomedia/example/internal/mediatest"
)

// A fragmented mp4 exercises a completely different set of boxes than a plain
// one: moof/traf/trun for every fragment and mfra/tfra for the index. ffmpeg
// has to be able to play the result end to end.
func TestConvertFLVToFMP4(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name string
		clip mediatest.Clip
	}{
		{"h264 and aac", mediatest.Clip{Container: "flv", Video: "libx264", Audio: "aac"}},
		{"h264 with b frames", mediatest.Clip{Container: "flv", Video: "libx264", Audio: "aac", BFrames: 2}},
		{"several fragments", mediatest.Clip{Container: "flv", Video: "libx264", Audio: "aac", Seconds: 4, GOP: 12}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			dst := filepath.Join(t.TempDir(), "out.mp4")

			if err := ConvertFLVToFMP4(src, dst); err != nil {
				t.Fatalf("convert: %v", err)
			}

			out := tools.MustProbe(t, dst, 2)
			tools.AssertDecodable(t, dst)

			in := tools.Probe(t, src)
			srcVideo, _ := in.Video()
			dstVideo, ok := out.Video()
			if !ok {
				t.Fatal("no video stream in the fragmented mp4")
			}
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertSameDecoded(t, src, dst, "v:0")
			mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "fmp4 video")

			assertFragmented(t, dst)
		})
	}
}

// assertFragmented fails when the file is not actually fragmented, which
// would let a broken fragment path pass every other check by falling back to
// a plain mp4.
func assertFragmented(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"moof", "traf", "trun", "mvex", "mfra"} {
		if !containsBoxType(data, want) {
			t.Errorf("%s box missing, the output is not a fragmented mp4", want)
		}
	}
}

func containsBoxType(data []byte, boxType string) bool {
	for i := 0; i+4 <= len(data); i++ {
		if string(data[i:i+4]) == boxType {
			return true
		}
	}
	return false
}
