package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yapingcat/gomedia/go-mp4"
)

// DemuxMP4FromMemory demuxes an mp4 held entirely in memory.
//
// mp4 is not a streamable container: the sample tables live in the moov box,
// which a muxer is free to put after every sample it describes, so the
// demuxer needs a ReadSeeker rather than a Reader. Anything that can seek
// works, and this shows the shape a custom one has to have.
func DemuxMP4FromMemory(data []byte, outDir string) (map[string]string, error) {
	demuxer := mp4.CreateMp4Demuxer(newCacheReaderSeeker(data))
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

var (
	mp4FileName = flag.String("mp4file", "test.mp4", "mp4 file to demux")
	outDir      = flag.String("outdir", ".", "directory to write the elementary streams into")
)

func main() {
	flag.Parse()
	data, err := os.ReadFile(*mp4FileName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	files, err := DemuxMP4FromMemory(data, *outDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for ext, path := range files {
		fmt.Printf("%s -> %s\n", ext, path)
	}
}
