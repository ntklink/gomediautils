package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
)

// DemuxFLV pulls the elementary streams out of an flv file and writes each
// into outDir. It returns the files it created, keyed by the extension chosen
// for the stream ("h264", "aac", ...).
func DemuxFLV(flvPath, outDir string) (map[string]string, error) {
	flvFile, err := os.Open(flvPath)
	if err != nil {
		return nil, err
	}
	defer flvFile.Close()

	files := newStreamFiles(outDir)
	defer files.close()

	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		ext, ok := extensionFor(cid)
		if !ok {
			return
		}
		files.write(ext, frame)
	}

	buf := make([]byte, 64*1024)
	for {
		n, err := flvFile.Read(buf)
		if n > 0 {
			if err := reader.Input(buf[:n]); err != nil {
				return nil, err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}

	if files.err != nil {
		return nil, files.err
	}
	return files.paths(), nil
}

func extensionFor(cid codec.CodecID) (string, bool) {
	switch cid {
	case codec.CODECID_VIDEO_H264:
		return "h264", true
	case codec.CODECID_VIDEO_H265:
		return "h265", true
	case codec.CODECID_AUDIO_AAC:
		return "aac", true
	case codec.CODECID_AUDIO_MP3:
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
		fmt.Fprintf(os.Stderr, "usage: %s <input.flv> [outdir]\n", os.Args[0])
		os.Exit(2)
	}
	outDir := "."
	if len(os.Args) > 2 {
		outDir = os.Args[2]
	}
	files, err := DemuxFLV(os.Args[1], outDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for ext, path := range files {
		fmt.Printf("%s -> %s\n", ext, path)
	}
}
