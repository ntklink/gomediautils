package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ntklink/gomediautils/go-mp4"
)

// DemuxFragments reads a fragmented mp4 that arrives the way a player gets
// one: an init segment holding the moov, followed by media segments each
// holding a moof and its mdat.
//
// The segments are joined before demuxing because that is what the pieces
// are, a single fragmented file cut at fragment boundaries. Joining them
// gives the demuxer the moov it needs to make sense of every trun that
// follows, which is also why a media segment on its own is not playable.
func DemuxFragments(initPath string, segmentPaths []string, outDir string) (map[string]string, error) {
	joined := bytes.NewBuffer(nil)
	for _, path := range append([]string{initPath}, segmentPaths...) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		joined.Write(b)
	}
	return DemuxFragmentedMP4(bytes.NewReader(joined.Bytes()), outDir)
}

// DemuxFragmentedMP4 writes the elementary streams of a fragmented mp4 into
// outDir, keyed by the extension chosen for each stream.
func DemuxFragmentedMP4(r io.ReadSeeker, outDir string) (map[string]string, error) {
	demuxer := mp4.CreateMp4Demuxer(r)
	if _, err := demuxer.ReadHead(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	files := newStreamFiles(outDir)
	defer files.close()

	for {
		pkg, err := demuxer.ReadPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		ext, ok := extensionFor(pkg.Cid)
		if !ok {
			continue
		}
		files.write(ext, pkg.Data)
	}

	if files.err != nil {
		return nil, files.err
	}
	return files.paths(), nil
}

func extensionFor(cid mp4.MP4_CODEC_TYPE) (string, bool) {
	switch cid {
	case mp4.MP4_CODEC_H264:
		return "h264", true
	case mp4.MP4_CODEC_H265:
		return "h265", true
	case mp4.MP4_CODEC_AAC:
		return "aac", true
	case mp4.MP4_CODEC_MP3:
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <init.mp4> [segment.m4s ...]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "   or: %s <whole-fragmented.mp4>\n", os.Args[0])
		os.Exit(2)
	}
	files, err := DemuxFragments(os.Args[1], os.Args[2:], ".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for ext, path := range files {
		fmt.Printf("%s -> %s\n", ext, path)
	}
}
