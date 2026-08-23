package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
)

// ffmpeg's rtp_mpegts muxer is the sender, so the packets on the wire are
// what a real encoder emits: rfc2250 framing, seven ts packets to a
// datagram, and a stream that just stops when the file runs out.
//
// The reference is not the source clip but the bytes that actually arrived,
// recorded as they came off the socket. ffmpeg's rtp sender only emits full
// datagrams, so it silently drops the last partial one and the tail of the
// clip never leaves the machine; measuring GoMediaUtils against the file would
// be measuring that instead. Recording the wire and demuxing it with ffmpeg
// gives the exact answer GoMediaUtils has to match.
//
// The clip is sent with -re, at the rate a real encoder would. udp has no
// flow control, so blasting a file at loopback as fast as it can be read
// overruns the receive buffer and the test starts failing on dropped
// packets rather than on anything GoMediaUtils did.
func TestReceiveTSOverRTP(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mpegts", Video: "libx264", Audio: "aac", Seconds: 3, GOP: 25, BFrames: 2,
	})

	// bind first so the socket exists before ffmpeg starts sending: udp
	// gives no connection refused a sender would retry on
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(*net.UDPConn).SetReadBuffer(4 << 20)
	port := conn.LocalAddr().(*net.UDPAddr).Port

	recording := filepath.Join(t.TempDir(), "wire.ts")
	tee, err := recordWire(conn, recording)
	if err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	done := make(chan map[string]string, 1)
	errc := make(chan error, 1)
	go func() {
		files, err := receive(tee, outDir, 3*time.Second)
		if err != nil {
			errc <- err
			return
		}
		done <- files
	}()

	sender := tools.Start(t, mediatest.FFmpegArgs(
		"-re", "-i", src, "-c", "copy",
		"-f", "rtp_mpegts", fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1316", port))...)
	sender.Wait(t)

	var files map[string]string
	select {
	case files = <-done:
	case err := <-errc:
		t.Fatalf("receive: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("the receiver never finished")
	}
	if err := tee.Close(); err != nil {
		t.Fatal(err)
	}

	videoPath, ok := files["h264"]
	if !ok {
		t.Fatalf("no h264 stream was received, got %v", files)
	}
	audioPath, ok := files["aac"]
	if !ok {
		t.Fatalf("no aac stream was received, got %v", files)
	}

	// what GoMediaUtils pulled out of the live stream has to match what ffmpeg
	// pulls out of the same bytes
	tools.AssertSameDecoded(t, tools.ExtractStream(t, recording, "0:v:0", "h264"), videoPath, "v:0")
	tools.AssertSameDecoded(t, tools.ExtractStream(t, recording, "0:a:0", "aac"), audioPath, "a:0")

	wireVideo, _ := tools.Probe(t, recording).Video()
	gotVideo, _ := tools.Probe(t, videoPath).Video()
	if gotVideo.Frames() != wireVideo.Frames() {
		t.Errorf("%d video frames demuxed, %d arrived", gotVideo.Frames(), wireVideo.Frames())
	}

	// and the stream that arrived has to be substantially the clip that was
	// sent, so a transport that lost most of it still fails here
	srcVideo, _ := tools.Probe(t, src).Video()
	if wireVideo.Frames() < srcVideo.Frames()-2 {
		t.Errorf("only %d of %d frames made it over rtp", wireVideo.Frames(), srcVideo.Frames())
	}
}

// The receiver has to give up on a stream that stops rather than block for
// good, which is the only way it can be used from a program that does
// anything else.
func TestReceiveTSOverRTPTimesOut(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	start := time.Now()
	files, err := receive(conn, t.TempDir(), 300*time.Millisecond)
	if err != nil {
		t.Fatalf("an idle receive should end quietly, got %v", err)
	}
	if len(files) != 0 {
		t.Errorf("nothing was sent but %v came out", files)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the receiver waited %v for a 300ms timeout", elapsed)
	}
}

// wireRecorder writes a copy of the reassembled transport stream, so the
// test can hold the receiver to what was actually delivered rather than to
// what the sender was asked to deliver.
type wireRecorder struct {
	net.PacketConn
	file *os.File
}

func recordWire(conn net.PacketConn, path string) (*wireRecorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &wireRecorder{PacketConn: conn, file: f}, nil
}

func (w *wireRecorder) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := w.PacketConn.ReadFrom(p)
	if n > rtpFixedHeaderLen {
		if _, werr := w.file.Write(p[rtpFixedHeaderLen:n]); werr != nil {
			return n, addr, werr
		}
	}
	return n, addr, err
}

// Close closes the recording, not the connection the test still owns.
func (w *wireRecorder) Close() error { return w.file.Close() }

var _ io.Closer = (*wireRecorder)(nil)
