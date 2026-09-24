package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-es"
	"github.com/ntklink/gomediautils/go-mkv"
)

// MuxElementaryStreams writes bare elementary streams into one Matroska
// file: a .h264/.h265 video stream and optionally an .aac, .mp3, .alaw or
// .ulaw audio stream. The es readers give every frame its timestamps, b
// frames included, and the two streams are interleaved by decode time.
func MuxElementaryStreams(videoPath, audioPath, mkvPath string) error {
	var readers []es.Reader
	for _, path := range []string{videoPath, audioPath} {
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		r, err := openReader(f, path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		readers = append(readers, r)
	}

	out, err := os.OpenFile(mkvPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer out.Close()
	muxer, err := mkv.NewMuxer(out)
	if err != nil {
		return err
	}

	tracks := make([]uint64, len(readers))
	next := make([]*es.Frame, len(readers))
	for i, r := range readers {
		if r.Codec() == codec.CODECID_VIDEO_H264 || r.Codec() == codec.CODECID_VIDEO_H265 {
			tracks[i], err = muxer.AddVideoTrack(r.Codec())
		} else {
			tracks[i], err = muxer.AddAudioTrack(r.Codec())
		}
		if err != nil {
			return err
		}
		if next[i], err = r.ReadFrame(); err != nil && err != io.EOF {
			return err
		}
	}

	for {
		// the stream whose next frame decodes first goes next
		pick := -1
		for i, f := range next {
			if f != nil && (pick < 0 || f.Dts < next[pick].Dts) {
				pick = i
			}
		}
		if pick < 0 {
			break
		}
		f := next[pick]
		if err := muxer.Write(tracks[pick], f.Data, f.Pts, f.Dts); err != nil {
			return err
		}
		if next[pick], err = readers[pick].ReadFrame(); err == io.EOF {
			next[pick] = nil
		} else if err != nil {
			return err
		}
	}
	return muxer.WriteTrailer()
}

// openReader picks the reader by extension for G.711, which has no header
// to recognise, and by content for everything else.
func openReader(r io.Reader, path string) (es.Reader, error) {
	switch {
	case strings.HasSuffix(path, ".alaw"):
		return es.NewG711Reader(r, codec.CODECID_AUDIO_G711A), nil
	case strings.HasSuffix(path, ".ulaw"):
		return es.NewG711Reader(r, codec.CODECID_AUDIO_G711U), nil
	}
	return es.NewReader(r)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <video.h264|.h265> [audio.aac|.mp3|.alaw|.ulaw] <output.mkv>\n", os.Args[0])
		os.Exit(2)
	}
	video, audio, dst := os.Args[1], "", os.Args[len(os.Args)-1]
	if len(os.Args) > 3 {
		audio = os.Args[2]
	}
	if err := MuxElementaryStreams(video, audio, dst); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", dst)
}
