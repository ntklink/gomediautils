package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// This is the transport the interleaved client cannot cover. Over udp the
// client has to bind its own even/odd port pair, tell the server about it in
// the transport header, understand the ports the server names back and then
// survive packets arriving on a socket with no ordering guarantee at all.
// gortsplib is the server, so every one of those has to be right by the rfc
// rather than by agreement with GoMediaUtils' own server.
func TestPlayRTSPOverUDP(t *testing.T) {
	tools := mediatest.Require(t)
	remote := mediatest.RequireStreamingServer(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 6, GOP: 25,
	})
	url := remote.RTSPURL(mediatest.UniquePath(t, "rtsp-udp"))

	publisher := tools.PublishRTSP(t, src, url, mediatest.PublishOpts{Realtime: true, Loop: true})
	defer publisher.Stop()

	remote.WaitStreamReady(t, tools, url, 20*time.Second)

	outDir := t.TempDir()
	const playSeconds = 4
	// an even port: rfc3550 wants rtp on the even one of the pair
	firstPort := uint16(mediatest.FreeUDPPortPair(t))
	sess, err := PlayRTSPOverUDP(url, outDir, firstPort, playSeconds*time.Second)
	if err != nil {
		t.Fatalf("play: %v\nffmpeg publisher said:\n%s", err, publisher.Stderr())
	}

	got := sess.Samples()
	if got["video"] == 0 {
		t.Fatalf("no video samples arrived over udp (got %v)", got)
	}
	if got["audio"] == 0 {
		t.Errorf("no audio samples arrived over udp (got %v)", got)
	}

	videoPath := filepath.Join(outDir, "video.h264")
	if st, err := os.Stat(videoPath); err != nil || st.Size() == 0 {
		t.Fatalf("no video written: %v", err)
	}
	tools.AssertDecodable(t, videoPath)

	video, ok := tools.Probe(t, videoPath).Video()
	if !ok {
		t.Fatal("ffprobe found no video in what the client wrote")
	}
	if video.CodecName != "h264" {
		t.Errorf("codec %q, want h264", video.CodecName)
	}
	if video.Width != 320 || video.Height != 240 {
		t.Errorf("resolution %dx%d, want 320x240", video.Width, video.Height)
	}

	want := playSeconds * 25
	floor := want * 3 / 4
	if !remote.Local {
		// somebody else's link, and udp does not retransmit
		floor = 25
	}
	if video.Frames() < floor {
		t.Errorf("%d frames decodable from a %d second play, want at least %d",
			video.Frames(), playSeconds, floor)
	}
}

// A url nothing is listening on has to come back as an error rather than
// hanging.
func TestPlayRTSPOverUDPReportsADeadServer(t *testing.T) {
	port := mediatest.FreePort(t)
	url := "rtsp://127.0.0.1:" + itoa(port) + "/live/nothing"

	done := make(chan error, 1)
	go func() {
		_, err := PlayRTSPOverUDP(url, t.TempDir(), uint16(mediatest.FreeUDPPortPair(t)), 5*time.Second)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("playing from a closed port was reported as a success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("playing from a closed port never returned")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
