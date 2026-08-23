package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yapingcat/gomedia/example/internal/mediatest"
)

// ffmpeg plays the generated presentation the way a player would: fetch the
// init segment, read the hvcC out of its moov, then decode every media
// segment against it. hevc is the case worth its own test because the codec
// configuration record is a different shape from avcC, with the vps, sps and
// pps in three separate arrays.
func TestGenerateH265HLS(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx265", Audio: "aac", Seconds: 6, GOP: 25,
	})
	outDir := t.TempDir()

	playlist, err := GenerateH265HLS(src, outDir)
	if err != nil {
		t.Fatalf("generate hls: %v", err)
	}

	m3u8 := readFile(t, playlist)
	for _, want := range []string{"#EXTM3U", "#EXT-X-VERSION:7", "#EXT-X-MAP:URI=", "#EXTINF:", "#EXT-X-ENDLIST"} {
		if !strings.Contains(m3u8, want) {
			t.Errorf("playlist is missing %q:\n%s", want, m3u8)
		}
	}

	segments := listedSegments(t, m3u8, outDir)
	if len(segments) < 2 {
		t.Fatalf("the playlist has %d segments, expected the clip to be split into several", len(segments))
	}
	if _, err := os.Stat(filepath.Join(outDir, "init.mp4")); err != nil {
		t.Fatalf("no init segment: %v", err)
	}

	tools.AssertDecodable(t, playlist)
	out := tools.Probe(t, playlist)
	video, ok := out.Video()
	if !ok {
		t.Fatal("ffmpeg found no video in the hls presentation")
	}
	if video.CodecName != "hevc" {
		t.Errorf("codec %q, want hevc", video.CodecName)
	}

	srcVideo, _ := tools.Probe(t, src).Video()
	if video.Frames() != srcVideo.Frames() {
		t.Errorf("%d frames playable over hls, want %d", video.Frames(), srcVideo.Frames())
	}
	tools.AssertSameDecoded(t, src, playlist, "v:0")

	// The audio is compared against ffmpeg's own fragmented mp4 repackage
	// rather than against the source mp4. An aac encoder emits a frame of
	// priming samples that the source's edit list trims away; hls carries no
	// edit list, so that frame is audible in any fmp4 presentation. ffmpeg
	// does exactly the same thing, so its output is the honest reference for
	// what a correct repackage sounds like.
	tools.AssertSameDecoded(t, referenceHLS(t, tools, src), playlist, "a:0")
}

// referenceHLS is the presentation ffmpeg produces from the same source with
// the same segment layout.
func referenceHLS(t *testing.T, tools mediatest.Tools, src string) string {
	t.Helper()
	dir := t.TempDir()
	index := filepath.Join(dir, "index.m3u8")
	tools.Run(t, tools.FFmpeg, mediatest.FFmpegArgs(
		"-i", src, "-c", "copy", "-f", "hls",
		"-hls_segment_type", "fmp4", "-hls_time", "1", "-hls_list_size", "0",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(dir, "seg%03d.m4s"),
		index)...)
	return index
}

// The presentation has to survive being fetched over http, which is how a
// player will actually get it. This also covers the handler, including the
// part that keeps a request from escaping the directory.
func TestServeH265HLS(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx265", Audio: "aac", Seconds: 4, GOP: 25,
	})
	outDir := t.TempDir()
	if _, err := GenerateH265HLS(src, outDir); err != nil {
		t.Fatalf("generate hls: %v", err)
	}

	hlsDir = outDir
	mux := http.NewServeMux()
	mux.HandleFunc("/vod/", onHLSVod)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })

	base := fmt.Sprintf("http://%s/vod/", ln.Addr().String())
	tools.AssertDecodable(t, base+"test.m3u8")
	tools.AssertSameDecoded(t, src, base+"test.m3u8", "v:0")

	t.Run("a request cannot escape the directory", func(t *testing.T) {
		secret := filepath.Join(filepath.Dir(outDir), "secret.txt")
		if err := os.WriteFile(secret, []byte("not yours"), 0o666); err != nil {
			t.Fatal(err)
		}
		resp, err := http.Get(base + "../secret.txt")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(body), "not yours") {
			t.Error("the handler served a file from outside the presentation directory")
		}
	})
}

// The example is the hevc one, so an h264 input is a mistake worth naming
// rather than a file that quietly comes out empty.
func TestGenerateH265HLSRejectsH264(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Seconds: 2,
	})
	if _, err := GenerateH265HLS(src, t.TempDir()); err == nil {
		t.Error("an h264 input was accepted by the hevc example")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func listedSegments(t *testing.T, m3u8, dir string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(m3u8, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, line)); err != nil {
			t.Errorf("playlist lists %q but %v", line, err)
			continue
		}
		out = append(out, line)
	}
	return out
}
