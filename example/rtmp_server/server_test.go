package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-flv"
	"github.com/ntklink/gomediautils/go-rtmp"
)

// TestFFmpegPublishToRtmpServer pushes a real clip into the rtmp server with
// ffmpeg and checks that what comes out of the server is the same media.
//
// Nothing else in the suite covers this path: the handshake, chunk assembly,
// the AMF0 connect/createStream/publish exchange and the flv tag demuxer all
// have to agree with a real client, and a mistake in any of them shows up as
// a connection ffmpeg drops or as frames that no longer decode.
func TestFFmpegPublishToRtmpServer(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 2,
	})

	recv := newRecorder()
	listen := startCollectingServer(t, recv)
	url := fmt.Sprintf("rtmp://%s/live/publishtest", listen.Addr().String())

	tools.PublishRTMP(t, src, url, mediatest.PublishOpts{}).Wait(t)

	// the publisher exits as soon as it has written the last chunk; give the
	// server the moment it needs to parse what is still in flight
	mediatest.WaitFor(t, "the server to stop receiving frames", 5*time.Second, recv.idleFor(300*time.Millisecond))

	got := recv.snapshot()
	if len(got) == 0 {
		t.Fatal("the server received no frames at all")
	}

	// write what the server received back out as flv and let ffmpeg judge it
	dst := filepath.Join(t.TempDir(), "received.flv")
	writeFLV(t, dst, got)

	out := tools.MustProbe(t, dst, 2)
	tools.AssertDecodable(t, dst)

	in := tools.Probe(t, src)
	srcVideo, _ := in.Video()
	dstVideo, ok := out.Video()
	if !ok {
		t.Fatal("no video stream came through rtmp")
	}
	if dstVideo.CodecName != srcVideo.CodecName {
		t.Errorf("video codec %q, want %q", dstVideo.CodecName, srcVideo.CodecName)
	}
	if dstVideo.Width != srcVideo.Width || dstVideo.Height != srcVideo.Height {
		t.Errorf("resolution %dx%d, want %dx%d",
			dstVideo.Width, dstVideo.Height, srcVideo.Width, srcVideo.Height)
	}
	if dstVideo.Frames() != srcVideo.Frames() {
		t.Errorf("%d video frames arrived, want %d", dstVideo.Frames(), srcVideo.Frames())
	}
	tools.AssertSameDecoded(t, src, dst, "v:0")

	if _, ok := out.Audio(); !ok {
		t.Error("no audio stream came through rtmp")
	}
}

// TestFFmpegPlayFromRtmpServer has ffmpeg publish and then ffmpeg play the
// same stream back through the relay, which is the only test that drives the
// chunk *writer* with a real client on the other end.
func TestFFmpegPlayFromRtmpServer(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "flv", Video: "libx264", Audio: "aac", Seconds: 6, GOP: 12,
	})

	listen := startRelay(t)
	addr := listen.Addr().String()
	url := fmt.Sprintf("rtmp://%s/live/playtest", addr)

	// -re so the stream is still running when the player joins
	publisher := tools.PublishRTMP(t, src, url, mediatest.PublishOpts{Realtime: true})
	defer publisher.Stop()

	mediatest.WaitFor(t, "the stream to be registered", 10*time.Second, func() bool {
		return center.find("playtest") != nil
	})

	dst := filepath.Join(t.TempDir(), "played.flv")
	tools.Start(t, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", url, "-t", "2", "-c", "copy", "-f", "flv", dst).Wait(t)

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
	// a player joins at a key frame, so the count is not exact, but a relay
	// that corrupts its chunks delivers almost nothing
	if video.Frames() < 25 {
		t.Errorf("only %d frames decoded out of a 2 second play, the stream is broken", video.Frames())
	}
	tools.AssertDecodable(t, dst)
}

// startRelay runs the example's relay server on an ephemeral port.
func startRelay(t *testing.T) net.Listener {
	t.Helper()
	listen, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start rtmp server: %v", err)
	}
	t.Cleanup(func() { listen.Close() })
	mediatest.WaitPort(t, listen.Addr().String(), 3*time.Second)
	return listen
}

// startCollectingServer runs the smallest rtmp server that accepts a publish
// and hands every frame to rec. Keeping it separate from the relay keeps the
// assertion about what arrived independent of the relay's own buffering and
// key frame logic.
func startCollectingServer(t *testing.T, rec *recorder) net.Listener {
	t.Helper()
	listen, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listen.Close() })

	go func() {
		for {
			conn, err := listen.Accept()
			if err != nil {
				return
			}
			go serveCollector(conn, rec)
		}
	}()
	mediatest.WaitPort(t, listen.Addr().String(), 3*time.Second)
	return listen
}

func serveCollector(conn net.Conn, rec *recorder) {
	defer conn.Close()
	handle := rtmp.NewRtmpServerHandle()
	handle.OnPublish(func(app, streamName string) rtmp.StatusCode {
		return rtmp.NETSTREAM_PUBLISH_START
	})
	handle.SetOutput(func(b []byte) error {
		_, err := conn.Write(b)
		return err
	})
	handle.OnFrame(func(cid codec.CodecID, pts, dts uint32, frame []byte) {
		rec.add(cid, pts, dts, frame)
	})

	buf := make([]byte, 64*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if err := handle.Input(buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// frame is one media frame as the server handed it over.
type frame struct {
	cid      codec.CodecID
	data     []byte
	pts, dts uint32
}

// recorder collects the frames a publishing session delivered.
type recorder struct {
	mu     sync.Mutex
	frames []frame
	last   time.Time
}

func newRecorder() *recorder { return &recorder{last: time.Now()} }

func (r *recorder) add(cid codec.CodecID, pts, dts uint32, data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, frame{cid: cid, data: cp, pts: pts, dts: dts})
	r.last = time.Now()
}

func (r *recorder) snapshot() []frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]frame(nil), r.frames...)
}

// idleFor reports whether nothing arrived for the given period, which is how
// the test knows the publisher is really finished.
func (r *recorder) idleFor(d time.Duration) func() bool {
	return func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.frames) > 0 && time.Since(r.last) > d
	}
}

func writeFLV(t *testing.T, path string, frames []frame) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := flv.CreateFlvWriter(f)
	if err := w.WriteFlvHeader(); err != nil {
		t.Fatal(err)
	}
	for _, fr := range frames {
		var err error
		switch fr.cid {
		case codec.CODECID_VIDEO_H264:
			err = w.WriteH264(fr.data, fr.pts, fr.dts)
		case codec.CODECID_VIDEO_H265:
			err = w.WriteH265(fr.data, fr.pts, fr.dts)
		case codec.CODECID_AUDIO_AAC:
			err = w.WriteAAC(fr.data, fr.pts, fr.dts)
		case codec.CODECID_AUDIO_MP3:
			err = w.WriteMp3(fr.data, fr.pts, fr.dts)
		}
		if err != nil {
			t.Fatalf("write flv: %v", err)
		}
	}
}
