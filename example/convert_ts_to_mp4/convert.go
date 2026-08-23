package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ntklink/gomediautils/go-mp4"
	"github.com/ntklink/gomediautils/go-mpeg2"
)

// ConvertTSToMP4 remuxes an mpeg-ts file into an mp4 file. Tracks are added
// the first time a stream of that kind shows up, so the caller does not have
// to know what the ts carries in advance.
func ConvertTSToMP4(tsPath, mp4Path string) error {
	tsFile, err := os.Open(tsPath)
	if err != nil {
		return err
	}
	defer tsFile.Close()

	mp4File, err := os.OpenFile(mp4Path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer mp4File.Close()

	muxer, err := mp4.CreateMp4Muxer(mp4File)
	if err != nil {
		return err
	}

	tracks := make(map[mpeg2.TS_STREAM_TYPE]uint32)
	var writeErr error

	demuxer := mpeg2.NewTSDemuxer()
	demuxer.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts uint64, dts uint64) {
		if writeErr != nil {
			return
		}
		tid, ok := tracks[cid]
		if !ok {
			tid, writeErr = addTrack(muxer, cid)
			if writeErr != nil {
				return
			}
			if tid == 0 {
				// a stream type this example does not carry over
				return
			}
			tracks[cid] = tid
		}
		writeErr = muxer.Write(tid, frame, pts, dts)
	}

	if err := demuxer.Input(tsFile); err != nil && err != io.EOF {
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	return muxer.WriteTrailer()
}

// addTrack maps a ts stream type onto an mp4 track. It reports (0, nil) for a
// stream this example does not handle.
func addTrack(muxer *mp4.Movmuxer, cid mpeg2.TS_STREAM_TYPE) (uint32, error) {
	switch cid {
	case mpeg2.TS_STREAM_H264:
		return muxer.AddVideoTrack(mp4.MP4_CODEC_H264)
	case mpeg2.TS_STREAM_H265:
		return muxer.AddVideoTrack(mp4.MP4_CODEC_H265)
	case mpeg2.TS_STREAM_AAC:
		return muxer.AddAudioTrack(mp4.MP4_CODEC_AAC)
	case mpeg2.TS_STREAM_AUDIO_MPEG1, mpeg2.TS_STREAM_AUDIO_MPEG2:
		return muxer.AddAudioTrack(mp4.MP4_CODEC_MP3)
	default:
		return 0, nil
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.ts> <output.mp4>\n", os.Args[0])
		os.Exit(2)
	}
	if err := ConvertTSToMP4(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
