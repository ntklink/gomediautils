package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// This is the direction the play client cannot cover: GoMediaUtils writes the sdp,
// packetises h264 into rtp itself and interleaves it over the rtsp
// connection, and a server that shares no code with it has to accept all
// three. A wrong sdp fmtp line, a payload type the announce never declared or
// an interleaved frame header off by a byte all end here rather than in a
// round trip against GoMediaUtils' own parser.
func TestPushFLVToRemoteServer(t *testing.T) {
	tools := mediatest.Require(t)
	remote := mediatest.RequireStreamingServer(t)

	// long enough that the stream is still live once the puller has
	// connected: a server drops a live path the moment its publisher leaves
	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 12, GOP: 25,
	})
	const pullSeconds = 4
	url := remote.RTSPURL(mediatest.UniquePath(t, "rtsp-push"))

	pushed := make(chan *RtspRecordSession, 1)
	errc := make(chan error, 1)
	go func() {
		sess, err := PushFLV(url, src, true)
		if err != nil {
			errc <- err
			return
		}
		pushed <- sess
	}()

	remote.WaitStreamReady(t, tools, url, 20*time.Second)

	dst := filepath.Join(t.TempDir(), "pulled.h264")
	tools.Start(t, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-rtsp_transport", "tcp", "-i", url,
		"-t", fmt.Sprint(pullSeconds), "-c", "copy", "-f", "h264", dst).Wait(t)

	var sess *RtspRecordSession
	select {
	case sess = <-pushed:
	case err := <-errc:
		t.Fatalf("push: %v", err)
	case <-time.After(40 * time.Second):
		t.Fatal("the publisher never finished")
	}
	if sess.Sent() == 0 {
		t.Fatal("the publisher sent no frames")
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
		t.Errorf("resolution %dx%d, want 320x240; the sps did not survive the sdp", video.Width, video.Height)
	}

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
	tools.AssertDecodable(t, dst)
}

// A url nothing is listening on has to come back as an error rather than
// hanging until the test times out.
func TestPushFLVReportsADeadServer(t *testing.T) {
	tools := mediatest.Require(t)
	src := tools.MakeClip(t, mediatest.Clip{Container: "flv", Video: "libx264", Seconds: 2})

	port := mediatest.FreePort(t)
	url := fmt.Sprintf("rtsp://127.0.0.1:%d/live/nothing", port)

	done := make(chan error, 1)
	go func() {
		_, err := PushFLV(url, src, false)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("publishing to a closed port was reported as a success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("publishing to a closed port never returned")
	}
}
