package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-flv"
)

var addr = flag.String("addr", ":19999", "listen address")
var dir = flag.String("dir", ".", "directory the flv files are served from")
var realtime = flag.Bool("realtime", true, "pace the stream at wall clock speed")

// FLVServer serves flv files under Dir over http, remuxing them through the
// flv reader and writer on the way, which is what a live http-flv edge does.
type FLVServer struct {
	Dir string
	// Realtime holds each frame back until its timestamp is due, the way a
	// live stream arrives. Turning it off sends the file as fast as the
	// client reads it.
	Realtime bool
}

// ServeHTTP streams the requested file as http-flv.
func (s *FLVServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/live/")
	// only a plain file name: never let a request walk out of the directory
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) {
		http.Error(w, "no stream named", http.StatusBadRequest)
		return
	}

	src, err := os.Open(filepath.Join(s.Dir, name))
	if err != nil {
		http.Error(w, "no such stream", http.StatusNotFound)
		return
	}
	defer src.Close()

	w.Header().Set("Content-Type", "video/x-flv")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	writer := flv.CreateFlvWriter(w)
	if err := writer.WriteFlvHeader(); err != nil {
		return
	}

	start := time.Now()
	var writeErr error
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if writeErr != nil {
			return
		}
		if s.Realtime {
			if wait := time.Duration(dts)*time.Millisecond - time.Since(start); wait > 0 {
				time.Sleep(wait)
			}
		}
		switch cid {
		case codec.CODECID_VIDEO_H264:
			writeErr = writer.WriteH264(frame, pts, dts)
		case codec.CODECID_VIDEO_H265:
			writeErr = writer.WriteH265(frame, pts, dts)
		case codec.CODECID_AUDIO_AAC:
			writeErr = writer.WriteAAC(frame, pts, dts)
		case codec.CODECID_AUDIO_MP3:
			writeErr = writer.WriteMp3(frame, pts, dts)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	buf := make([]byte, 64*1024)
	for writeErr == nil {
		n, err := src.Read(buf)
		if n > 0 {
			if err := reader.Input(buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintln(os.Stderr, err)
			}
			return
		}
	}
}

// Listen starts the server on addr and returns the listener, so a caller can
// ask for an ephemeral port and shut it down again.
func Listen(addr string, s *FLVServer) (net.Listener, error) {
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/live/", s)
	go http.Serve(l, mux)
	return l, nil
}

func main() {
	flag.Parse()
	l, err := Listen(*addr, &FLVServer{Dir: *dir, Realtime: *realtime})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer l.Close()
	fmt.Println("serving http-flv on", l.Addr(), "from", *dir)
	select {}
}
