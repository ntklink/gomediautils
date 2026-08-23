package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// TestPublishToRemoteServer pushes a real clip with gomedia's rtmp client
// into a third party server and pulls it back with ffmpeg.
//
// The ffmpeg tests in this repository put a real client in front of gomedia's
// servers. This is the other half: gomedia's *client* has to satisfy a server
// that shares no code with it, which is where a handshake shortcut or a chunk
// header that only gomedia's own parser accepts shows up.
func TestPublishToRemoteServer(t *testing.T) {
	tools := mediatest.Require(t)
	remote := mediatest.RequireStreamingServer(t)

	// long enough that the stream is still live once the puller has
	// connected: a server drops a live path the moment its publisher leaves
	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 12, GOP: 25,
	})
	const pullSeconds = 4
	path := mediatest.UniquePath(t, "rtmp-pub")
	url := remote.RTMPURL(path)

	// pull with ffmpeg while gomedia publishes: the server only holds a live
	// stream for as long as a publisher is connected
	dst := filepath.Join(t.TempDir(), "pulled.flv")

	published := make(chan error, 1)
	go func() { published <- PublishFLV(url, src, true) }()

	remote.WaitStreamReady(t, tools, url, 20*time.Second)

	tools.Start(t, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", url, "-t", fmt.Sprint(pullSeconds), "-c", "copy", "-f", "flv", dst).Wait(t)

	select {
	case err := <-published:
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the publisher never finished")
	}

	out := tools.Probe(t, dst)
	video, ok := out.Video()
	if !ok {
		t.Fatalf("the server gave back no video (format %q)", out.Format.FormatName)
	}
	if video.CodecName != "h264" {
		t.Errorf("video codec %q, want h264", video.CodecName)
	}
	if video.Width != 320 || video.Height != 240 {
		t.Errorf("resolution %dx%d, want 320x240", video.Width, video.Height)
	}
	// on a server the test started, delivery is not rate limited by anything
	// outside the test, so almost every frame of the window has to be there
	want := pullSeconds * 25
	floor := want * 3 / 4
	if !remote.Local {
		// somebody else's link, only check the stream is really flowing
		floor = 25
	}
	if video.Frames() < floor {
		t.Errorf("%d frames came back from a %d second pull, want at least %d",
			video.Frames(), pullSeconds, floor)
	}
	if _, ok := out.Audio(); !ok {
		t.Error("the server gave back no audio")
	}
	tools.AssertDecodable(t, dst)
}
