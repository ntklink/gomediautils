package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
	"github.com/yapingcat/gomedia/go-mp4"
)

// ConvertFLVToFMP4 remuxes a flv file into a fragmented mp4.
//
// Unlike a plain mp4 the tracks have to be declared before the first sample,
// because the moov box goes out ahead of the first fragment. The flv is
// therefore scanned once to find out what it carries.
func ConvertFLVToFMP4(flvPath, mp4Path string) error {
	codecs, err := scanFLVCodecs(flvPath)
	if err != nil {
		return err
	}
	if len(codecs) == 0 {
		return errors.New("flv carries no stream this example can convert")
	}

	mp4File, err := os.OpenFile(mp4Path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer mp4File.Close()

	muxer, err := mp4.CreateMp4Muxer(mp4File, mp4.WithMp4Flag(mp4.MP4_FLAG_FRAGMENT))
	if err != nil {
		return err
	}

	tracks := make(map[codec.CodecID]uint32, len(codecs))
	for _, cid := range codecs {
		tid, err := addTrack(muxer, cid)
		if err != nil {
			return err
		}
		if tid != 0 {
			tracks[cid] = tid
		}
	}

	var writeErr error
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if writeErr != nil {
			return
		}
		if tid, ok := tracks[cid]; ok {
			writeErr = muxer.Write(tid, frame, uint64(pts), uint64(dts))
		}
	}
	if err := feedFLV(flvPath, reader); err != nil {
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	return muxer.WriteTrailer()
}

// scanFLVCodecs reads the file once and reports the codecs it carries, in the
// order they first appear.
func scanFLVCodecs(flvPath string) ([]codec.CodecID, error) {
	var order []codec.CodecID
	seen := make(map[codec.CodecID]bool)
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if !seen[cid] {
			seen[cid] = true
			order = append(order, cid)
		}
	}
	if err := feedFLV(flvPath, reader); err != nil {
		return nil, err
	}
	return order, nil
}

func feedFLV(path string, reader *flv.FlvReader) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if err := reader.Input(buf[:n]); err != nil {
				return err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func addTrack(muxer *mp4.Movmuxer, cid codec.CodecID) (uint32, error) {
	switch cid {
	case codec.CODECID_VIDEO_H264:
		return muxer.AddVideoTrack(mp4.MP4_CODEC_H264)
	case codec.CODECID_VIDEO_H265:
		return muxer.AddVideoTrack(mp4.MP4_CODEC_H265)
	case codec.CODECID_AUDIO_AAC:
		return muxer.AddAudioTrack(mp4.MP4_CODEC_AAC)
	case codec.CODECID_AUDIO_MP3:
		return muxer.AddAudioTrack(mp4.MP4_CODEC_MP3)
	case codec.CODECID_AUDIO_G711A:
		return muxer.AddAudioTrack(mp4.MP4_CODEC_G711A)
	case codec.CODECID_AUDIO_G711U:
		return muxer.AddAudioTrack(mp4.MP4_CODEC_G711U)
	default:
		return 0, nil
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.flv> <output.mp4>\n", os.Args[0])
		os.Exit(2)
	}
	if err := ConvertFLVToFMP4(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
