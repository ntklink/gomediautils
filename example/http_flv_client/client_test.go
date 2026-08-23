package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
	"github.com/ntklink/gomediautils/go-codec"
)

// The client is pointed at an ffmpeg-fed http-flv stream, so what it parses
// came from a real muxer rather than from GoMediaUtils' own writer.
func TestPullFLVFromFFmpeg(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 4, GOP: 25, BFrames: 2,
	})
	url := serveFile(t, src)

	out := filepath.Join(t.TempDir(), "pulled.flv")
	frames, err := PullFLV(context.Background(), url, out, PullOptions{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("the pull produced no frames")
	}

	var video, audio int
	for _, f := range frames {
		switch f.Codec {
		case codec.CODECID_VIDEO_H264:
			video++
		case codec.CODECID_AUDIO_AAC:
			audio++
		}
	}
	srcProbe := tools.Probe(t, src)
	srcVideo, _ := srcProbe.Video()
	srcAudio, _ := srcProbe.Audio()
	if video != srcVideo.Packets() {
		t.Errorf("%d video frames parsed, want %d", video, srcVideo.Packets())
	}
	if audio != srcAudio.Packets() {
		t.Errorf("%d audio frames parsed, want %d", audio, srcAudio.Packets())
	}

	// the saved file is the stream verbatim, so it has to be the same media
	tools.AssertDecodable(t, out)
	tools.AssertSameDecoded(t, src, out, "v:0")
	tools.AssertSameDecoded(t, src, out, "a:0")

	// decode timestamps are what a player paces on
	for i := 1; i < len(frames); i++ {
		if frames[i].Codec != frames[i-1].Codec {
			continue
		}
		if frames[i].Dts < frames[i-1].Dts {
			t.Fatalf("dts goes backwards at frame %d: %d after %d",
				i, frames[i].Dts, frames[i-1].Dts)
		}
	}
}

// MaxFrames is what makes the client usable against a live stream, which
// never ends on its own.
func TestPullFLVStopsAtMaxFrames(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 4, GOP: 25,
	})
	url := serveFile(t, src)

	out := filepath.Join(t.TempDir(), "partial.flv")
	frames, err := PullFLV(context.Background(), url, out, PullOptions{
		MaxFrames: 20, Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(frames) < 20 {
		t.Errorf("stopped after %d frames, want at least the 20 asked for", len(frames))
	}
	if len(frames) > 200 {
		t.Errorf("read %d frames, MaxFrames was 20; the limit is not being honoured", len(frames))
	}
}

// A url that is not there has to come back as an error, not as an empty file
// the caller mistakes for an empty stream.
func TestPullFLVReportsHTTPErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.NotFoundHandler()}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })

	url := fmt.Sprintf("http://%s/live/missing.flv", ln.Addr())
	if _, err := PullFLV(context.Background(), url, filepath.Join(t.TempDir(), "x.flv"), PullOptions{
		Timeout: 5 * time.Second,
	}); err == nil {
		t.Error("a 404 was reported as a successful pull")
	}
}

// serveFile publishes one file over http and returns its url.
func serveFile(t *testing.T, path string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/live/", func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "video/x-flv")
		http.ServeContent(w, r, "stream.flv", time.Time{}, f)
	})
	server := &http.Server{Handler: mux}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	return fmt.Sprintf("http://%s/live/stream.flv", ln.Addr())
}
