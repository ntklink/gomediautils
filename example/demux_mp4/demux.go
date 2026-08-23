package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ntklink/gomediautils/go-mp4"
)

// DemuxMP4 pulls the elementary streams out of an mp4 file and writes each
// into outDir. It returns the files it created, keyed by the extension chosen
// for the stream, along with the track descriptions read from the header.
//
// The same code reads a fragmented mp4: ReadHead follows the moov, and
// ReadPacket walks whatever sample tables it found, whether they came from an
// stbl or from the trun of every moof.
func DemuxMP4(r io.ReadSeeker, outDir string) (map[string]string, []mp4.TrackInfo, error) {
	demuxer := mp4.CreateMp4Demuxer(r)
	infos, err := demuxer.ReadHead()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}

	files := newStreamFiles(outDir)
	defer files.close()

	for {
		pkg, err := demuxer.ReadPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, infos, err
		}
		ext, ok := extensionFor(pkg.Cid)
		if !ok {
			continue
		}
		files.write(ext, pkg.Data)
	}

	if files.err != nil {
		return nil, infos, files.err
	}
	return files.paths(), infos, nil
}

// DemuxMP4File is DemuxMP4 over a file on disk.
func DemuxMP4File(mp4Path, outDir string) (map[string]string, []mp4.TrackInfo, error) {
	f, err := os.Open(mp4Path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return DemuxMP4(f, outDir)
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
	files, infos, err := DemuxMP4File(*mp4FileName, *outDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, info := range infos {
		fmt.Printf("track %d: %+v\n", info.TrackId, info)
	}
	for ext, path := range files {
		fmt.Printf("%s -> %s\n", ext, path)
	}
}
