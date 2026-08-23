package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ntklink/gomediautils/go-mp4"
)

type hlsSegment struct {
	duration float32
	uri      string
}

type hlsPlaylist struct {
	initUri  string
	segments []hlsSegment
}

func (p *hlsPlaylist) makeM3u8() string {
	m3u := bytes.NewBuffer(make([]byte, 0, 4096))
	maxDuration := 0
	for _, seg := range p.segments {
		if d := int(math.Ceil(float64(seg.duration))); d > maxDuration {
			maxDuration = d
		}
	}

	m3u.WriteString("#EXTM3U\n")
	m3u.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", maxDuration))
	// version 7 is the first that allows fragmented mp4 segments
	m3u.WriteString("#EXT-X-VERSION:7\n")
	m3u.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	m3u.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	m3u.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=%q\n", p.initUri))
	for _, seg := range p.segments {
		m3u.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", seg.duration))
		m3u.WriteString(seg.uri + "\n")
	}
	m3u.WriteString("#EXT-X-ENDLIST\n")
	return m3u.String()
}

// GenerateH265HLS repackages an h265 mp4 into a fragmented mp4 HLS
// presentation in outDir and returns the path of the playlist.
//
// h265 is the reason this exists next to the h264 version: the init segment
// has to carry an hvcC rather than an avcC, and the parameter sets go into
// it in three arrays instead of two. A player that gets a segment whose
// codec configuration does not match sees a stream it cannot start.
func GenerateH265HLS(mp4Path, outDir string) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}

	src, err := os.Open(mp4Path)
	if err != nil {
		return "", err
	}
	defer src.Close()

	demuxer := mp4.CreateMp4Demuxer(src)
	infos, err := demuxer.ReadHead()
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if !hasCodec(infos, mp4.MP4_CODEC_H265) {
		return "", fmt.Errorf("%s carries no h265 track; this example is the hevc one", mp4Path)
	}

	playlist := &hlsPlaylist{}
	segIndex := 0
	segName := func(i int) string { return fmt.Sprintf("hevcstream-%d.mp4", i) }

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
		playlist.segments = append(playlist.segments, hlsSegment{
			uri:      segName(segIndex),
			duration: float32(duration) / 1000,
		})
		segFile.Close()

		if segIndex == 0 {
			// the init segment holds the moov every media segment refers to
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
			playlist.initUri = "init.mp4"
		}

		segIndex++
		segFile, fragErr = os.OpenFile(filepath.Join(outDir, segName(segIndex)), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
		if fragErr != nil {
			return
		}
		muxer.ReBindWriter(segFile)
	})

	vtid, err := muxer.AddVideoTrack(mp4.MP4_CODEC_H265)
	if err != nil {
		return "", err
	}
	atid := uint32(0)
	hasAudio := hasCodec(infos, mp4.MP4_CODEC_AAC)
	if hasAudio {
		if atid, err = muxer.AddAudioTrack(mp4.MP4_CODEC_AAC); err != nil {
			return "", err
		}
	}

	for {
		pkg, err := demuxer.ReadPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		switch {
		case pkg.Cid == mp4.MP4_CODEC_H265:
			err = muxer.Write(vtid, pkg.Data, pkg.Pts, pkg.Dts)
		case pkg.Cid == mp4.MP4_CODEC_AAC && hasAudio:
			err = muxer.Write(atid, pkg.Data, pkg.Pts, pkg.Dts)
		}
		if err != nil {
			return "", err
		}
	}

	if err := muxer.FlushFragment(); err != nil {
		return "", err
	}
	if fragErr != nil {
		return "", fragErr
	}
	segFile.Close()

	playlistPath := filepath.Join(outDir, "test.m3u8")
	if err := os.WriteFile(playlistPath, []byte(playlist.makeM3u8()), 0o666); err != nil {
		return "", err
	}
	return playlistPath, nil
}

func hasCodec(infos []mp4.TrackInfo, cid mp4.MP4_CODEC_TYPE) bool {
	for _, info := range infos {
		if info.Cid == cid {
			return true
		}
	}
	return false
}

// hlsDir is where the generated presentation lives, served by onHLSVod.
var hlsDir = "."

func onHLSVod(w http.ResponseWriter, r *http.Request) {
	// filepath.Base keeps a request for ../../etc/passwd inside hlsDir
	name := filepath.Base(strings.TrimPrefix(r.URL.Path, "/vod/"))
	if strings.HasSuffix(name, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	} else {
		w.Header().Set("Content-Type", "video/mp4")
	}
	body, err := os.ReadFile(filepath.Join(hlsDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(body)
}

// Listen serves the generated presentation. The playlist is at
// http://<addr>/vod/test.m3u8
func Listen(addr, dir string) error {
	hlsDir = dir
	mux := http.NewServeMux()
	mux.HandleFunc("/vod/", onHLSVod)
	server := http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return server.ListenAndServe()
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <input-h265.mp4> [outdir]\n", os.Args[0])
		os.Exit(2)
	}
	outDir := "."
	if len(os.Args) > 2 {
		outDir = os.Args[2]
	}
	playlist, err := GenerateH265HLS(os.Args[1], outDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("playlist:", playlist)
	fmt.Println("serving http://127.0.0.1:19999/vod/test.m3u8")
	fmt.Fprintln(os.Stderr, Listen(":19999", outDir))
}
