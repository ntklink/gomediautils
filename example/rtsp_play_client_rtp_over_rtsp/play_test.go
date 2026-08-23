package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yapingcat/gomedia/example/internal/mediatest"
)

// TestPlayRTSPFromServer has ffmpeg publish into a third party rtsp server and
// gomedia's rtsp client play it back with rtp interleaved over the rtsp
// connection.
//
// The server side of this exchange is gortsplib, which shares no code with
// gomedia, so the sdp gomedia parses, the transport it negotiates and the
// rtp depacketising it does all have to be right by the rfc rather than by
// agreement with itself.
func TestPlayRTSPFromServer(t *testing.T) {
	tools := mediatest.Require(t)
	remote := mediatest.RequireStreamingServer(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 6, GOP: 25,
	})
	path := mediatest.UniquePath(t, "rtsp-play")
	url := remote.RTSPURL(path)

	publisher := tools.PublishRTSP(t, src, url, mediatest.PublishOpts{Realtime: true, Loop: true})
	defer publisher.Stop()

	remote.WaitStreamReady(t, tools, url, 20*time.Second)

	outDir := t.TempDir()
	const playSeconds = 4
	sess, err := PlayRTSP(url, outDir, playSeconds*time.Second)
	if err != nil {
		t.Fatalf("play: %v\nffmpeg publisher said:\n%s", err, publisher.Stderr())
	}

	got := sess.Samples()
	if got["video"] == 0 {
		t.Fatalf("no video samples arrived (got %v)", got)
	}
	if got["audio"] == 0 {
		t.Errorf("no audio samples arrived (got %v)", got)
	}

	// the elementary stream the client wrote has to be one ffmpeg can decode
	videoPath := filepath.Join(outDir, "video.h264")
	if st, err := os.Stat(videoPath); err != nil || st.Size() == 0 {
		t.Fatalf("no video written: %v", err)
	}
	tools.AssertDecodable(t, videoPath)

	probe := tools.Probe(t, videoPath)
	video, ok := probe.Video()
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
		floor = 25
	}
	if video.Frames() < floor {
		t.Errorf("%d frames decodable from a %d second play, want at least %d",
			video.Frames(), playSeconds, floor)
	}
}
