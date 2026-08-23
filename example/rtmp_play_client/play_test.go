package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// TestPlayFromRemoteServer has ffmpeg publish into a third party server and
// GoMediaUtils' rtmp client play it back. Whatever the client hands over is
// written to a flv and checked with ffprobe, so a client that loses the
// parameter sets or mangles a chunk fails here even though its own demuxer
// would accept the result.
func TestPlayFromRemoteServer(t *testing.T) {
	tools := mediatest.Require(t)
	remote := mediatest.RequireStreamingServer(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 6, GOP: 25,
	})
	path := mediatest.UniquePath(t, "rtmp-play")
	url := remote.RTMPURL(path)

	publisher := tools.PublishRTMP(t, src, url, mediatest.PublishOpts{Realtime: true, Loop: true})
	defer publisher.Stop()

	remote.WaitStreamReady(t, tools, url, 20*time.Second)

	// a generous window: the handshake and the play negotiation happen inside
	// it, and a remote link adds real latency on top
	const playSeconds = 4
	dst := filepath.Join(t.TempDir(), "played.flv")
	if err := PlayToFLV(url, dst, playSeconds*time.Second); err != nil {
		t.Fatalf("play: %v\nffmpeg publisher said:\n%s", err, publisher.Stderr())
	}

	out := tools.Probe(t, dst)
	video, ok := out.Video()
	if !ok {
		t.Fatalf("GoMediaUtils played the stream but produced no video (format %q)", out.Format.FormatName)
	}
	if video.CodecName != "h264" {
		t.Errorf("video codec %q, want h264", video.CodecName)
	}
	if video.Width != 320 || video.Height != 240 {
		t.Errorf("resolution %dx%d, want 320x240", video.Width, video.Height)
	}
	// the client joins mid stream, so the first frames before a key frame are
	// not decodable; on a local server everything after that has to arrive
	want := playSeconds * 25
	floor := want * 3 / 4
	if !remote.Local {
		floor = 25
	}
	if video.Frames() < floor {
		t.Errorf("%d frames decoded from a %d second play, want at least %d",
			video.Frames(), playSeconds, floor)
	}
	if _, ok := out.Audio(); !ok {
		t.Error("no audio came through")
	}
	tools.AssertDecodable(t, dst)
}
