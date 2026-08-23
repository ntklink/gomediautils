package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yapingcat/gomedia/go-mpeg2"
)

// DemuxTS pulls the elementary streams out of an mpeg-ts file and writes each
// one next to the others in outDir. It returns the files it created, keyed by
// the extension it chose for the stream ("h264", "aac", ...).
//
// Only streams that actually carry data get a file: a ts file with no audio
// should not leave an empty audio.aac behind.
func DemuxTS(tsPath, outDir string) (map[string]string, error) {
	tsFile, err := os.Open(tsPath)
	if err != nil {
		return nil, err
	}
	defer tsFile.Close()

	files := newStreamFiles(outDir)
	defer files.close()

	demuxer := mpeg2.NewTSDemuxer()
	demuxer.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts, dts uint64) {
		ext, ok := extensionFor(cid)
		if !ok {
			return
		}
		files.write(ext, frame)
	}

	// a ts file is read sequentially and can be large, so buffer rather than
	// slurping it into memory
	if err := demuxer.Input(bufio.NewReaderSize(tsFile, 64*1024)); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if files.err != nil {
		return nil, files.err
	}
	return files.paths(), nil
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.ts> [outdir]\n", os.Args[0])
		os.Exit(2)
	}
	outDir := "."
	if len(os.Args) > 2 {
		outDir = os.Args[2]
	}
	files, err := DemuxTS(os.Args[1], outDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for ext, path := range files {
		fmt.Printf("%s -> %s\n", ext, path)
	}
}
