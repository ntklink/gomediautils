package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg pulls an http-flv stream out of the server and has to end up with
// the same pictures the source file holds. http-flv is a remux on every
// frame, so a muxer that writes a tag header no other reader accepts, or
// loses the sequence header, shows up immediately.
func TestServeHTTPFLV(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 2,
	})

	// serve from a directory holding just this clip
	dir := t.TempDir()
	name := "clip.flv"
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o666); err != nil {
		t.Fatal(err)
	}

	// not realtime: the test wants the whole file, not a live pace
	listen, err := Listen("127.0.0.1:0", &FLVServer{Dir: dir, Realtime: false})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer listen.Close()
	mediatest.WaitPort(t, listen.Addr().String(), 3*time.Second)

	url := fmt.Sprintf("http://%s/live/%s", listen.Addr().String(), name)
	dst := filepath.Join(t.TempDir(), "pulled.flv")
	tools.Start(t, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", url, "-c", "copy", "-f", "flv", dst).Wait(t)

	out := tools.MustProbe(t, dst, 2)
	tools.AssertDecodable(t, dst)

	in := tools.Probe(t, src)
	srcVideo, _ := in.Video()
	dstVideo, ok := out.Video()
	if !ok {
		t.Fatal("no video came over http-flv")
	}
	if dstVideo.Frames() != srcVideo.Frames() {
		t.Errorf("%d video frames arrived, want %d", dstVideo.Frames(), srcVideo.Frames())
	}
	tools.AssertSameDecoded(t, src, dst, "v:0")
	mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "http-flv video")
}

// A request must not be able to reach outside the directory the server was
// pointed at.
func TestServeHTTPFLVRejectsPathTraversal(t *testing.T) {
	tools := mediatest.Require(t)

	dir := t.TempDir()
	secret := filepath.Join(filepath.Dir(dir), "secret.flv")
	if err := os.WriteFile(secret, []byte("not yours"), 0o666); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(secret)

	listen, err := Listen("127.0.0.1:0", &FLVServer{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer listen.Close()
	mediatest.WaitPort(t, listen.Addr().String(), 3*time.Second)

	url := fmt.Sprintf("http://%s/live/../secret.flv", listen.Addr().String())
	if _, _, err := tools.RunFFprobe(url); err == nil {
		t.Error("the server served a file from outside its directory")
	}
}
