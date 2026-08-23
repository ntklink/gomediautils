package main

import (
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// TestFFmpegPushAndPullRTSP drives the rtsp server with ffmpeg on both ends:
// one process announces and pushes a real stream, another describes and plays
// it back. That covers the parts of the rtsp implementation no unit test can
// reach, the request parsing, the sdp exchange, the interleaved rtp framing
// and the h264 packetiser, against a client that follows the rfc rather than
// gomedia's own idea of it.
func TestFFmpegPushAndPullRTSP(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 8, GOP: 12,
	})

	listen := startTestRTSPServer(t)
	// the example asks for digest authentication, so the real client has to
	// go through it too, which is the only coverage that exchange gets
	url := fmt.Sprintf("rtsp://test:test123@%s/live/test", listen.Addr().String())

	// push over tcp: udp would need a second pair of ports and makes the
	// test flaky on a loaded machine
	publisher := tools.Start(t,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-re",
		"-i", src, "-c", "copy",
		"-f", "rtsp", "-rtsp_transport", "tcp", url)
	defer publisher.Stop()

	mediatest.WaitFor(t, "the stream to be announced", 15*time.Second, func() bool {
		_, found := g_manager.getSource("test")
		return found
	})

	dst := filepath.Join(t.TempDir(), "played.mp4")
	tools.Start(t,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-rtsp_transport", "tcp", "-i", url,
		"-t", "2", "-c", "copy", dst).Wait(t)

	out := tools.Probe(t, dst)
	video, ok := out.Video()
	if !ok {
		t.Fatalf("ffmpeg played the stream but found no video (format %q)", out.Format.FormatName)
	}
	if video.CodecName != "h264" {
		t.Errorf("video codec %q, want h264", video.CodecName)
	}
	if video.Width != 320 || video.Height != 240 {
		t.Errorf("resolution %dx%d, want 320x240", video.Width, video.Height)
	}
	// a player joins mid stream, so the exact count depends on timing, but a
	// broken packetiser delivers almost nothing decodable
	if video.Frames() < 25 {
		t.Errorf("only %d frames decoded from a 2 second play, the stream is broken", video.Frames())
	}
	tools.AssertDecodable(t, dst)
}

func startTestRTSPServer(t *testing.T) net.Listener {
	t.Helper()
	listen, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start rtsp server: %v", err)
	}
	t.Cleanup(func() { listen.Close() })
	mediatest.WaitPort(t, listen.Addr().String(), 3*time.Second)
	return listen
}
