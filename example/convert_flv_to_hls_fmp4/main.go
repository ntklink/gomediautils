package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
	"github.com/yapingcat/gomedia/go-mp4"
)

type hlsSegment struct {
	duration float32
	uri      string
}

type hlsMuxer struct {
	initUri  string
	segments []hlsSegment
}

func (muxer *hlsMuxer) makeM3u8() string {
	buf := make([]byte, 0, 4096)
	m3u := bytes.NewBuffer(buf)
	maxDuration := 0
	for _, seg := range muxer.segments {
		if maxDuration < int(math.Ceil(float64(seg.duration))) {
			maxDuration = int(math.Ceil(float64(seg.duration)))
		}
	}

	m3u.WriteString("#EXTM3U\n")
	m3u.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", maxDuration))
	m3u.WriteString("#EXT-X-VERSION:7\n")
	m3u.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	m3u.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	m3u.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=\"%s\"\n", muxer.initUri))

	for _, seg := range muxer.segments {
		m3u.WriteString(fmt.Sprintf("#EXTINF:%.3f,%s\n", seg.duration, "no desc"))
		m3u.WriteString(seg.uri + "\n")
	}
	m3u.WriteString("#EXT-X-ENDLIST\n")
	return m3u.String()
}

// GenerateHLS turns a flv file into a fragmented mp4 HLS presentation in
// outDir: an init segment, one media segment per fragment and the playlist
// that ties them together. It reports the path of the playlist.
func GenerateHLS(flvPath, outDir string) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}

	hls := &hlsMuxer{}
	segIndex := 0
	segName := func(i int) string { return fmt.Sprintf("stream-%d.mp4", i) }

	segFile, err := os.OpenFile(filepath.Join(outDir, segName(0)), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return "", err
	}
	defer func() { segFile.Close() }()

	muxer, err := mp4.CreateMp4Muxer(segFile, mp4.WithMp4Flag(mp4.MP4_FLAG_DASH))
	if err != nil {
		return "", err
	}

	var fragErr error
	muxer.OnNewFragment(func(duration uint32, firstPts, firstDts uint64) {
		if fragErr != nil {
			return
		}
		hls.segments = append(hls.segments, hlsSegment{
			uri:      segName(segIndex),
			duration: float32(duration) / 1000,
		})
		segFile.Close()

		if segIndex == 0 {
			// the init segment holds the moov, which every segment refers to
			initFile, err := os.OpenFile(filepath.Join(outDir, "init.mp4"), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
			if err != nil {
				fragErr = err
				return
			}
			if err := muxer.WriteInitSegment(initFile); err != nil {
				initFile.Close()
				fragErr = err
				return
			}
			initFile.Close()
			hls.initUri = "init.mp4"
		}

		segIndex++
		segFile, fragErr = os.OpenFile(filepath.Join(outDir, segName(segIndex)), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
		if fragErr != nil {
			return
		}
		muxer.ReBindWriter(segFile)
	})

	vtid, err := muxer.AddVideoTrack(mp4.MP4_CODEC_H264)
	if err != nil {
		return "", err
	}
	atid, err := muxer.AddAudioTrack(mp4.MP4_CODEC_AAC)
	if err != nil {
		return "", err
	}

	flvFile, err := os.Open(flvPath)
	if err != nil {
		return "", err
	}
	defer flvFile.Close()

	var writeErr error
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if writeErr != nil {
			return
		}
		switch cid {
		case codec.CODECID_AUDIO_AAC:
			writeErr = muxer.Write(atid, frame, uint64(pts), uint64(dts))
		case codec.CODECID_VIDEO_H264:
			writeErr = muxer.Write(vtid, frame, uint64(pts), uint64(dts))
		}
	}

	cache := make([]byte, 64*1024)
	for {
		n, err := flvFile.Read(cache)
		if n > 0 {
			if err := reader.Input(cache[:n]); err != nil {
				return "", err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
	}
	if writeErr != nil {
		return "", writeErr
	}
	if fragErr != nil {
		return "", fragErr
	}
	if err := muxer.FlushFragment(); err != nil {
		return "", err
	}
	if fragErr != nil {
		return "", fragErr
	}
	segFile.Close()

	playlist := filepath.Join(outDir, "test.m3u8")
	if err := os.WriteFile(playlist, []byte(hls.makeM3u8()), 0o666); err != nil {
		return "", err
	}
	return playlist, nil
}

// hlsDir is where the generated presentation lives, served by onHLSVod.
var hlsDir = "."

func onHLSVod(w http.ResponseWriter, r *http.Request) {
	buf := bytes.NewBuffer(make([]byte, 0, 1024*1024))
	if strings.LastIndex(r.URL.Path, "m3u8") != -1 {
		fmt.Println("request m3u8", r.URL.Path)
		m3u8, err := os.Open(filepath.Join(hlsDir, "test.m3u8"))
		if err != nil {
			return
		}
		defer m3u8.Close()
		b, _ := ioutil.ReadAll(m3u8)
		buf.Write(b)
		w.Header().Add("Content-Type", "application/vnd.apple.mpegurl")
	} else {
		fmt.Println("request fmp4", r.URL.Path)
		fmp4File := strings.TrimPrefix(r.URL.Path, "/vod/")
		fmp4, err := os.Open(filepath.Join(hlsDir, filepath.Base(fmp4File)))
		if err != nil {
			return
		}
		defer fmp4.Close()
		b, _ := ioutil.ReadAll(fmp4)
		buf.Write(b)
		w.Header().Set("Content-Type", "video/mp4")
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Write(buf.Bytes())
}

// http://127.0.0.1:19999/vod/test.m3u8
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.flv> [outdir]\n", os.Args[0])
		os.Exit(2)
	}
	outDir := "."
	if len(os.Args) > 2 {
		outDir = os.Args[2]
	}
	if _, err := GenerateHLS(os.Args[1], outDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	hlsDir = outDir

	mux := http.NewServeMux()
	mux.HandleFunc("/vod/", onHLSVod)
	server := http.Server{
		Addr:         ":19999",
		Handler:      mux,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}
	fmt.Println("server.listen")
	fmt.Println(server.ListenAndServe())

}
