package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yapingcat/gomedia/example/internal/mediatest"
)

// ffmpeg is a real HLS client: pointing it at the generated playlist makes it
// fetch the init segment, parse the moov out of it and then decode every
// media segment against it. A wrong track id, a bad tfhd base offset or a
// trun the moov does not match all show up as a stream ffmpeg cannot play,
// while gomedia's own demuxer would happily read its own output.
func TestGenerateHLS(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 6, GOP: 25,
	})
	outDir := t.TempDir()

	playlist, err := GenerateHLS(src, outDir)
	if err != nil {
		t.Fatalf("generate hls: %v", err)
	}

	body, err := os.ReadFile(playlist)
	if err != nil {
		t.Fatal(err)
	}
	m3u8 := string(body)
	for _, want := range []string{"#EXTM3U", "#EXT-X-MAP:URI=", "#EXTINF:", "#EXT-X-ENDLIST"} {
		if !strings.Contains(m3u8, want) {
			t.Errorf("playlist is missing %q:\n%s", want, m3u8)
		}
	}

	segments := 0
	for _, line := range strings.Split(m3u8, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		segments++
		if _, err := os.Stat(filepath.Join(outDir, line)); err != nil {
			t.Errorf("playlist lists %q but %v", line, err)
		}
	}
	if segments < 2 {
		t.Fatalf("the playlist has %d segments, expected the clip to be split into several", segments)
	}
	if _, err := os.Stat(filepath.Join(outDir, "init.mp4")); err != nil {
		t.Fatalf("no init segment: %v", err)
	}

	// ffmpeg plays the presentation the way a player would
	tools.AssertDecodable(t, playlist)
	out := tools.Probe(t, playlist)
	video, ok := out.Video()
	if !ok {
		t.Fatal("ffmpeg found no video in the hls presentation")
	}
	if video.CodecName != "h264" {
		t.Errorf("codec %q, want h264", video.CodecName)
	}

	in := tools.Probe(t, src)
	srcVideo, _ := in.Video()
	if video.Frames() != srcVideo.Frames() {
		t.Errorf("%d frames playable over hls, want %d", video.Frames(), srcVideo.Frames())
	}
	tools.AssertSameDecoded(t, src, playlist, "v:0")
}
