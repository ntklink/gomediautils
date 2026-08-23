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
	"strconv"
	"strings"
	"time"

	"github.com/yapingcat/gomedia/go-mp4"
	"github.com/yapingcat/gomedia/go-mpeg2"
)

// segment is one entry of the playlist: a half open interval [start, end) of
// the mp4's decode timeline, in milliseconds.
type segment struct {
	start uint64
	end   uint64
}

func (s segment) duration() float32 { return float32(s.end-s.start) / 1000 }

// Segments cuts a presentation into pieces of about targetSeconds each.
//
// The cuts land on sync samples, never between them: a segment that does not
// start with a keyframe cannot be decoded on its own, which is the one thing
// an hls segment has to be able to do. That is also why the segments come out
// uneven, and why the playlist has to advertise the longest one rather than
// the target.
func Segments(syncTable []mp4.SyncSample, endTimestamp uint64, targetSeconds int) []segment {
	if len(syncTable) == 0 {
		return nil
	}
	target := uint64(targetSeconds) * 1000

	var segments []segment
	start := syncTable[0].Dts
	for _, sync := range syncTable[1:] {
		if sync.Dts-start < target {
			continue
		}
		segments = append(segments, segment{start: start, end: sync.Dts})
		start = sync.Dts
	}
	if start < endTimestamp {
		segments = append(segments, segment{start: start, end: endTimestamp})
	}
	return segments
}

// Playlist renders the m3u8 for a stream cut into the given segments.
func Playlist(streamName string, segments []segment) string {
	m3u := bytes.NewBuffer(make([]byte, 0, 4096))
	maxDuration := 0
	for _, seg := range segments {
		if d := int(math.Ceil(float64(seg.duration()))); d > maxDuration {
			maxDuration = d
		}
	}

	m3u.WriteString("#EXTM3U\n")
	m3u.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", maxDuration))
	m3u.WriteString("#EXT-X-VERSION:3\n")
	m3u.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	m3u.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	for i, seg := range segments {
		m3u.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", seg.duration()))
		m3u.WriteString(fmt.Sprintf("%s/sequence-%d.ts?start=%d&end=%d\n",
			streamName, i, seg.start, seg.end))
	}
	m3u.WriteString("#EXT-X-ENDLIST\n")
	return m3u.String()
}

// SegmentTS transcodes one interval of an mp4 into a transport stream, which
// is what the playlist points at.
//
// Nothing is stored: the segment is built when it is asked for, by seeking
// the mp4 to the requested time and remuxing until the interval ends. That is
// what makes serving an mp4 over hls possible without writing a second copy
// of it to disk.
func SegmentTS(mp4Path string, start, end uint64) ([]byte, error) {
	f, err := os.Open(mp4Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	demuxer := mp4.CreateMp4Demuxer(f)
	if _, err := demuxer.ReadHead(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if err := demuxer.SeekTime(start); err != nil {
		return nil, err
	}

	buf := bytes.NewBuffer(make([]byte, 0, 1<<20))
	muxer := mpeg2.NewTSMuxer()
	muxer.OnPacket = func(pkg []byte) { buf.Write(pkg) }

	tracks := make(map[mp4.MP4_CODEC_TYPE]uint16)
	for {
		pkg, err := demuxer.ReadPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if pkg.Dts >= end {
			break
		}
		tsType, ok := tsStreamType(pkg.Cid)
		if !ok {
			continue
		}
		pid, ok := tracks[pkg.Cid]
		if !ok {
			pid = muxer.AddStream(tsType)
			tracks[pkg.Cid] = pid
		}
		if err := muxer.Write(pid, pkg.Data, pkg.Pts, pkg.Dts); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func tsStreamType(cid mp4.MP4_CODEC_TYPE) (mpeg2.TS_STREAM_TYPE, bool) {
	switch cid {
	case mp4.MP4_CODEC_H264:
		return mpeg2.TS_STREAM_H264, true
	case mp4.MP4_CODEC_H265:
		return mpeg2.TS_STREAM_H265, true
	case mp4.MP4_CODEC_AAC:
		return mpeg2.TS_STREAM_AAC, true
	case mp4.MP4_CODEC_MP3:
		return mpeg2.TS_STREAM_AUDIO_MPEG1, true
	default:
		return 0, false
	}
}

// PlaylistFor reads an mp4 and renders the playlist that describes it.
func PlaylistFor(mp4Path, streamName string, targetSeconds int) (string, error) {
	f, err := os.Open(mp4Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	demuxer := mp4.CreateMp4Demuxer(f)
	infos, err := demuxer.ReadHead()
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}

	var videoTrack uint32
	var endTs uint64
	found := false
	for _, info := range infos {
		if info.Cid == mp4.MP4_CODEC_H264 || info.Cid == mp4.MP4_CODEC_H265 {
			videoTrack = uint32(info.TrackId)
			endTs = info.EndDts
			found = true
			break
		}
	}
	if !found {
		return "", errors.New("no video track to cut the presentation on")
	}

	table, err := demuxer.GetSyncTable(videoTrack)
	if err != nil {
		return "", err
	}
	return Playlist(streamName, Segments(table, endTs, targetSeconds)), nil
}

// Server serves the mp4 files in Dir as hls presentations.
type Server struct {
	Dir string
	// SegmentSeconds is how long a segment should be, before it is rounded
	// up to the next sync sample.
	SegmentSeconds int
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/vod/")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if strings.HasSuffix(path, ".m3u8") {
		s.servePlaylist(w, r, strings.TrimSuffix(path, ".m3u8"))
		return
	}
	// <stream>/sequence-N.ts?start=..&end=..
	stream, _, ok := strings.Cut(path, "/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.serveSegment(w, r, stream)
}

func (s *Server) servePlaylist(w http.ResponseWriter, r *http.Request, stream string) {
	body, err := PlaylistFor(s.mp4Path(stream), stream, s.segmentSeconds())
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	io.WriteString(w, body)
}

func (s *Server) serveSegment(w http.ResponseWriter, r *http.Request, stream string) {
	start, err1 := strconv.ParseUint(r.URL.Query().Get("start"), 10, 64)
	end, err2 := strconv.ParseUint(r.URL.Query().Get("end"), 10, 64)
	if err1 != nil || err2 != nil || end <= start {
		http.Error(w, "a segment needs a start and an end", http.StatusBadRequest)
		return
	}
	body, err := SegmentTS(s.mp4Path(stream), start, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

// mp4Path keeps a request for ../../etc/passwd inside Dir.
func (s *Server) mp4Path(stream string) string {
	return filepath.Join(s.Dir, filepath.Base(stream)+".mp4")
}

func (s *Server) segmentSeconds() int {
	if s.SegmentSeconds <= 0 {
		return 10
	}
	return s.SegmentSeconds
}

// Listen serves the mp4 files in dir. The playlist for foo.mp4 is at
// http://<addr>/vod/foo.m3u8
func Listen(addr, dir string, segmentSeconds int) error {
	mux := http.NewServeMux()
	mux.Handle("/vod/", &Server{Dir: dir, SegmentSeconds: segmentSeconds})
	server := http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return server.ListenAndServe()
}

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	fmt.Println("serving", dir, "at http://127.0.0.1:19999/vod/<name>.m3u8")
	fmt.Fprintln(os.Stderr, Listen(":19999", dir, 10))
}
