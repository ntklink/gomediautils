package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg is the client here, so gomedia's rtsp *server* has to satisfy an
// implementation that shares no code with it: the sdp it writes, the
// transport it agrees to and the rtp it sends all have to be right by the
// rfc. Both transports are covered, because they take completely different
// paths out of the server, interleaved over the rtsp connection on one side
// and a pair of udp sockets on the other.
func TestServeRTSP(t *testing.T) {
	tools := mediatest.Require(t)

	dir := t.TempDir()
	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 6, GOP: 25,
	})
	copyFile(t, src, filepath.Join(dir, "movie.flv"))

	server := &Server{Dir: dir, FirstUDPPort: mediatest.FreeUDPPortPair(t)}
	bound, err := server.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	url := fmt.Sprintf("rtsp://%s/movie", bound)

	for _, transport := range []string{"tcp", "udp"} {
		t.Run(transport, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "played.ts")
			const playSeconds = 3

			// the server paces the stream, so ffmpeg gets a live feed and
			// -t bounds how much of it is taken
			tools.Start(t, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
				"-rtsp_transport", transport,
				"-i", url, "-t", fmt.Sprint(playSeconds),
				"-c", "copy", "-f", "mpegts", dst).Wait(t)

			out := tools.Probe(t, dst)
			video, ok := out.Video()
			if !ok {
				t.Fatalf("the server sent no video over %s (format %q)", transport, out.Format.FormatName)
			}
			if video.CodecName != "h264" {
				t.Errorf("video codec %q, want h264", video.CodecName)
			}
			if video.Width != 320 || video.Height != 240 {
				t.Errorf("resolution %dx%d, want 320x240", video.Width, video.Height)
			}
			if _, ok := out.Audio(); !ok {
				t.Error("the server sent no audio")
			}

			want := playSeconds * 25
			if video.Frames() < want*3/4 {
				t.Errorf("%d frames arrived over %s in %d seconds, want at least %d",
					video.Frames(), transport, playSeconds, want*3/4)
			}
			tools.AssertDecodable(t, dst)
			mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "served video")
		})
	}
}

// A stream that does not exist has to be refused at DESCRIBE rather than
// answered with an empty session.
func TestServeRTSPRejectsAMissingStream(t *testing.T) {
	tools := mediatest.Require(t)

	server := &Server{Dir: t.TempDir(), FirstUDPPort: mediatest.FreeUDPPortPair(t)}
	bound, err := server.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })

	url := fmt.Sprintf("rtsp://%s/nothing", bound)
	if _, _, err := tools.RunFFprobe(url); err == nil {
		t.Error("ffprobe opened a stream the server does not have")
	}
}

// The file a request names has to stay inside the served directory.
func TestServeRTSPStaysInsideItsDirectory(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(nil, dir, 20000)

	for _, uri := range []string{
		"rtsp://127.0.0.1:8554/../../etc/passwd",
		"rtsp://127.0.0.1:8554/live/../../secret",
	} {
		got := sess.sourcePath(uri)
		if want := filepath.Dir(got); want != dir {
			t.Errorf("%q resolved to %q, outside %q", uri, got, dir)
		}
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o666); err != nil {
		t.Fatal(err)
	}
}
