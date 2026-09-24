package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg writes a Matroska file, GoMediaUtils remuxes it into an mp4, and
// ffmpeg has to decode the same pictures and samples out of that. The mp4
// needs decode times Matroska does not store, so b frames are the case that
// matters most.
func TestConvertMKVToMP4(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name string
		clip mediatest.Clip
	}{
		{"h264 and aac with b frames", mediatest.Clip{Container: "matroska", Video: "libx264", Audio: "aac", BFrames: 2}},
		{"h265 and aac", mediatest.Clip{Container: "matroska", Video: "libx265", Audio: "aac", BFrames: 2}},
		{"h264 and opus, deep b frames", mediatest.Clip{Container: "matroska", Video: "libx264", Audio: "libopus", BFrames: 3}},
		{"h264 and mp3", mediatest.Clip{Container: "matroska", Video: "libx264", Audio: "libmp3lame"}},
		{"live stream, unknown sizes", mediatest.Clip{Container: "matroska", Video: "libx264", Audio: "aac", BFrames: 2,
			Extra: []string{"-live", "1"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			checkRemux(t, tools, src, tc.clip.Audio != "")
		})
	}
}

// mkvmerge writes Matroska differently from ffmpeg: by default it laces
// eight aac frames into each block, and on request it compresses frames with
// zlib and writes BlockGroups instead of SimpleBlocks. The demuxer has to
// undo all of it.
func TestConvertMkvmergeFileToMP4(t *testing.T) {
	tools := mediatest.Require(t)
	mkvmerge, err := exec.LookPath("mkvmerge")
	if err != nil {
		t.Skip("mkvmerge not found")
	}
	src := tools.MakeClip(t, mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "aac", BFrames: 2})
	for name, args := range map[string][]string{
		"laced audio": nil,
		"zlib and block groups": {"--engage", "no_simpleblocks",
			"--compression", "0:zlib", "--compression", "1:zlib"},
	} {
		t.Run(name, func(t *testing.T) {
			mkv := filepath.Join(t.TempDir(), "mkvmerge.mkv")
			cmd := exec.Command(mkvmerge, append(append([]string{"-q", "-o", mkv}, args...), src)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("mkvmerge: %v\n%s", err, out)
			}
			checkRemux(t, tools, mkv, true)
		})
	}
}

func checkRemux(t *testing.T, tools mediatest.Tools, src string, withAudio bool) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "out.mp4")
	if err := ConvertMKVToMP4(src, dst); err != nil {
		t.Fatalf("convert: %v", err)
	}

	wantStreams := 1
	if withAudio {
		wantStreams = 2
	}
	out := tools.MustProbe(t, dst, wantStreams)
	tools.AssertDecodable(t, dst)

	in := tools.Probe(t, src)
	srcVideo, _ := in.Video()
	dstVideo, ok := out.Video()
	if !ok {
		t.Fatal("no video stream in the mp4")
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
	mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "mp4 video")
	// the mp4 presents its first frame at zero wherever the source started
	mediatest.AssertSameTimestamps(t, mediatest.FromZero(tools.Packets(t, src, "v:0")),
		mediatest.FromZero(tools.Packets(t, dst, "v:0")), 0.002, "mp4 video")

	if withAudio {
		srcAudio, _ := in.Audio()
		dstAudio, ok := out.Audio()
		if !ok {
			t.Fatal("no audio stream in the mp4")
		}
		if dstAudio.CodecName != srcAudio.CodecName || dstAudio.Channels != srcAudio.Channels {
			t.Errorf("audio %s %d ch, want %s %d ch",
				dstAudio.CodecName, dstAudio.Channels, srcAudio.CodecName, srcAudio.Channels)
		}
		if dstAudio.Packets() != srcAudio.Packets() {
			t.Errorf("%d audio packets, want %d", dstAudio.Packets(), srcAudio.Packets())
		}
		mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "a:0"), "mp4 audio")
	}
}
