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

// ConvertFLVToMP4 remuxes a flv file into an mp4 file. Tracks are added on
// the first frame of each kind, so a flv with only audio or only video works
// without any extra handling.
func ConvertFLVToMP4(flvPath, mp4Path string) error {
	flvFile, err := os.Open(flvPath)
	if err != nil {
		return err
	}
	defer flvFile.Close()

	mp4File, err := os.OpenFile(mp4Path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer mp4File.Close()

	muxer, err := mp4.CreateMp4Muxer(mp4File)
	if err != nil {
		return err
	}

	tracks := make(map[codec.CodecID]uint32)
	var writeErr error

	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if writeErr != nil {
			return
		}
		tid, ok := tracks[cid]
		if !ok {
			tid, writeErr = addTrack(muxer, cid)
			if writeErr != nil || tid == 0 {
				return
			}
			tracks[cid] = tid
		}
		writeErr = muxer.Write(tid, frame, uint64(pts), uint64(dts))
	}

	buf := make([]byte, 64*1024)
	for {
		n, err := flvFile.Read(buf)
		if n > 0 {
			if err := reader.Input(buf[:n]); err != nil {
				return err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if writeErr != nil {
		return writeErr
	}
	return muxer.WriteTrailer()
}

// addTrack maps a codec id onto an mp4 track, reporting (0, nil) for a codec
// this example does not carry over.
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
	if err := ConvertFLVToMP4(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
