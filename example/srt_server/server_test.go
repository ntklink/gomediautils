package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
	"github.com/ntklink/gomediautils/go-srt"
)

// ffmpeg publishes MPEG-TS over SRT with libsrt, the way OBS or a hardware
// encoder does, and the GoMediaUtils server records it into an mp4 that has
// to decode to the same pictures. Through a lossy link, the server's
// receiver has to get every lost packet sent again within the latency.
func TestRecordFromFFmpeg(t *testing.T) {
	tools := mediatest.Require(t)
	tools.RequireProtocol(t, "srt")
	src := tools.MakeClip(t, mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "aac", BFrames: 2})

	cases := []struct {
		name       string
		passphrase string
		loss       float64
	}{
		{"clear", "", 0},
		{"encrypted", "correct horse battery", 0},
		{"5% loss each way", "", 0.05},
		{"encrypted with 5% loss", "correct horse battery", 0.05},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l, err := srt.Listen("127.0.0.1:0", srt.ListenConfig{
				Config:    srt.Config{Passphrase: tc.passphrase, Latency: 400 * time.Millisecond},
				Authorize: Authorize,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := make(chan string, 1)
			go Serve(l, dir, done)

			addr := l.Addr().String()
			if tc.loss > 0 {
				addr = mediatest.LossyUDPProxy(t, addr, tc.loss)
			}
			// linger lets libsrt deliver the end of the stream before it
			// hangs up, instead of dropping what it has not sent yet
			url := fmt.Sprintf("srt://%s?streamid=#!::r=live/cam1,m=publish&latency=400000&linger=2", addr)
			if tc.passphrase != "" {
				url += "&passphrase=" + tc.passphrase
			}
			tools.Start(t, mediatest.FFmpegArgs("-re", "-i", src, "-c", "copy", "-f", "mpegts", url)...).Wait(t)

			var path string
			select {
			case path = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("recording did not finish")
			}
			if filepath.Base(path) != "live_cam1.mp4" {
				t.Errorf("recorded into %s", path)
			}
			out := tools.MustProbe(t, path, 2)
			srcVideo, _ := tools.Probe(t, src).Video()
			dstVideo, _ := out.Video()
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames recorded, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertDecodable(t, path)
			tools.AssertSameDecoded(t, src, path, "v:0")
		})
	}
}

// A caller that asks to play instead of publish is turned away with a
// rejection libsrt reports, before the server keeps any state for it.
func TestRejectsPlayers(t *testing.T) {
	tools := mediatest.Require(t)
	tools.RequireProtocol(t, "srt")
	l, err := srt.Listen("127.0.0.1:0", srt.ListenConfig{Authorize: Authorize})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	url := fmt.Sprintf("srt://%s?streamid=#!::r=live/cam1,m=request&connect_timeout=2000", l.Addr())
	_, stderr, err := tools.RunFFmpeg(mediatest.FFmpegArgs("-i", url, "-f", "null", "-")...)
	if err == nil {
		t.Fatal("a player was let in")
	}
	t.Logf("ffmpeg: %s", stderr)
}
