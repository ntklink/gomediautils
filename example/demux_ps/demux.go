package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-mpeg2"
)

// DemuxPS pulls the elementary streams out of an mpeg program stream, the
// container gb28181 devices send, and writes each into outDir. It returns the
// files it created, keyed by the extension chosen for the stream.
//
// The ps demuxer reports one nalu per callback rather than a whole access
// unit, so access unit delimiters are dropped here: they carry no picture
// data and confuse decoders that see them without the frame they belong to.
func DemuxPS(psPath, outDir string) (map[string]string, error) {
	psFile, err := os.Open(psPath)
	if err != nil {
		return nil, err
	}
	defer psFile.Close()

	files := newStreamFiles(outDir)
	defer files.close()

	demuxer := mpeg2.NewPSDemuxer()
	demuxer.OnFrame = func(frame []byte, cid mpeg2.PS_STREAM_TYPE, pts, dts uint64) {
		ext, ok := extensionFor(cid)
		if !ok {
			return
		}
		if cid == mpeg2.PS_STREAM_H264 && codec.H264NaluType(frame) == codec.H264_NAL_AUD {
			return
		}
		files.write(ext, frame)
	}

	buf, err := io.ReadAll(psFile)
	if err != nil {
		return nil, err
	}
	// Input wants whole packets; errNeedMore just means the tail of the
	// buffer is a partial packet, which Flush then completes
	if err := demuxer.Input(buf); err != nil && !errors.Is(err, mpeg2.ErrNeedMore) {
		return nil, err
	}
	demuxer.Flush()

	if files.err != nil {
		return nil, files.err
	}
	return files.paths(), nil
}

func extensionFor(cid mpeg2.PS_STREAM_TYPE) (string, bool) {
	switch cid {
	case mpeg2.PS_STREAM_H264:
		return "h264", true
	case mpeg2.PS_STREAM_H265:
		return "h265", true
	case mpeg2.PS_STREAM_AAC:
		return "aac", true
	case mpeg2.PS_STREAM_G711A:
		return "alaw", true
	case mpeg2.PS_STREAM_G711U:
		return "mulaw", true
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
		fmt.Fprintf(os.Stderr, "usage: %s <input.ps> [outdir]\n", os.Args[0])
		os.Exit(2)
	}
	outDir := "."
	if len(os.Args) > 2 {
		outDir = os.Args[2]
	}
	files, err := DemuxPS(os.Args[1], outDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for ext, path := range files {
		fmt.Printf("%s -> %s\n", ext, path)
	}
}
