package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ntklink/gomediautils/go-mpeg2"
)

// rtpFixedHeaderLen is the size of an rtp header with no csrc list and no
// extension, which is what an mpeg-ts payload arrives with.
const rtpFixedHeaderLen = 12

// rtpReader turns a udp socket carrying rtp into the byte stream the ts
// demuxer reads.
//
// rfc2250 puts whole ts packets in the rtp payload, so stripping the header
// off each datagram and concatenating what is left rebuilds the transport
// stream exactly. Nothing here reorders by sequence number: on a local link
// packets arrive in order, and a receiver that has to survive a real network
// needs a jitter buffer, which is a different piece of code from this one.
type rtpReader struct {
	conn    net.PacketConn
	timeout time.Duration
	buf     []byte
	pending bytes.Reader
}

func newRTPReader(conn net.PacketConn, timeout time.Duration) *rtpReader {
	return &rtpReader{conn: conn, timeout: timeout, buf: make([]byte, 2048)}
}

func (r *rtpReader) Read(p []byte) (int, error) {
	for r.pending.Len() == 0 {
		if r.timeout > 0 {
			if err := r.conn.SetReadDeadline(time.Now().Add(r.timeout)); err != nil {
				return 0, err
			}
		}
		n, _, err := r.conn.ReadFrom(r.buf)
		if err != nil {
			// going quiet is how a live stream ends. Reporting it as io.EOF
			// rather than as a network error matters: the demuxer only
			// flushes the frame it is still holding when its reader ends
			// cleanly, so anything else silently loses the last picture.
			if isTimeout(err) {
				return 0, io.EOF
			}
			return 0, err
		}
		if n <= rtpFixedHeaderLen {
			continue // an rtcp packet, or a datagram with no payload
		}
		r.pending.Reset(append([]byte(nil), r.buf[rtpFixedHeaderLen:n]...))
	}
	return r.pending.Read(p)
}

// ReceiveTSOverRTP listens on addr for an mpeg-ts over rtp stream and writes
// the elementary streams it carries into outDir, keyed by the extension
// chosen for each stream.
//
// It runs until idleTimeout passes with no packet, because a live stream has
// no end of its own. Send one with:
//
//	ffmpeg -re -i input.mp4 -c copy -f rtp_mpegts rtp://127.0.0.1:19999
func ReceiveTSOverRTP(addr string, outDir string, idleTimeout time.Duration) (map[string]string, error) {
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// A udp socket drops whatever arrives while the receiver is busy, and
	// demuxing a burst takes long enough for that to happen. The default
	// buffer holds only a few datagrams; a large one absorbs the burst.
	// This is the difference between a stream that arrives intact and one
	// with holes that look like an encoder fault.
	if udp, ok := conn.(*net.UDPConn); ok {
		udp.SetReadBuffer(4 << 20)
	}
	return receive(conn, outDir, idleTimeout)
}

func receive(conn net.PacketConn, outDir string, idleTimeout time.Duration) (map[string]string, error) {
	files := newStreamFiles(outDir)
	defer files.close()

	demuxer := mpeg2.NewTSDemuxer()
	demuxer.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts, dts uint64) {
		if ext, ok := extensionFor(cid); ok {
			files.write(ext, frame)
		}
	}

	// the stream stopping is how this ends, not a failure
	if err := demuxer.Input(newRTPReader(conn, idleTimeout)); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if files.err != nil {
		return nil, files.err
	}
	return files.paths(), nil
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func extensionFor(cid mpeg2.TS_STREAM_TYPE) (string, bool) {
	switch cid {
	case mpeg2.TS_STREAM_H264:
		return "h264", true
	case mpeg2.TS_STREAM_H265:
		return "h265", true
	case mpeg2.TS_STREAM_AAC:
		return "aac", true
	case mpeg2.TS_STREAM_AUDIO_MPEG1, mpeg2.TS_STREAM_AUDIO_MPEG2:
		return "mp3", true
	default:
		return "", false
	}
}

// streamFiles opens an output file the first time a stream is seen.
type streamFiles struct {
	dir   string
	files map[string]*os.File
	err   error
}

func newStreamFiles(dir string) *streamFiles {
	return &streamFiles{dir: dir, files: make(map[string]*os.File)}
}

func (s *streamFiles) write(ext string, frame []byte) {
	if s.err != nil {
		return
	}
	f, ok := s.files[ext]
	if !ok {
		f, s.err = os.OpenFile(filepath.Join(s.dir, "stream."+ext), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
		if s.err != nil {
			return
		}
		s.files[ext] = f
	}
	_, s.err = f.Write(frame)
}

func (s *streamFiles) paths() map[string]string {
	out := make(map[string]string, len(s.files))
	for ext, f := range s.files {
		out[ext] = f.Name()
	}
	return out
}

func (s *streamFiles) close() {
	for _, f := range s.files {
		if err := f.Close(); err != nil && s.err == nil {
			s.err = err
		}
	}
}

var (
	listenAddr = flag.String("addr", "127.0.0.1:19999", "udp address to receive rtp on")
	outDir     = flag.String("outdir", ".", "directory to write the elementary streams into")
	idle       = flag.Duration("idle", 10*time.Second, "give up after this long with no packet")
)

// ffmpeg -re -i <media file> -c copy -f rtp_mpegts rtp://127.0.0.1:19999
func main() {
	flag.Parse()
	files, err := ReceiveTSOverRTP(*listenAddr, *outDir, *idle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for ext, path := range files {
		fmt.Printf("%s -> %s\n", ext, path)
	}
}
