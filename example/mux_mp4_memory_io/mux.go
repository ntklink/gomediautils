package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ntklink/gomediautils/go-mp4"
	"github.com/ntklink/gomediautils/go-mpeg2"
)

// MuxTSToMP4InMemory reads an mpeg-ts file and returns the mp4 it produces,
// without ever putting a partial file on disk.
//
// Buffering the whole thing is what lets the muxer seek back and patch the
// box sizes. If the output has to go somewhere that cannot seek, a socket or
// a pipe, the answer is not a cleverer writer but a fragmented mp4: with
// MP4_FLAG_FRAGMENT every fragment is final once written.
func MuxTSToMP4InMemory(tsPath string) ([]byte, error) {
	buf, err := os.ReadFile(tsPath)
	if err != nil {
		return nil, err
	}

	out := newCacheWriterSeeker(1 << 20)
	muxer, err := mp4.CreateMp4Muxer(out)
	if err != nil {
		return nil, err
	}

	tracks := make(map[mpeg2.TS_STREAM_TYPE]uint32)
	var writeErr error
	demuxer := mpeg2.NewTSDemuxer()
	demuxer.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts, dts uint64) {
		if writeErr != nil {
			return
		}
		tid, ok := tracks[cid]
		if !ok {
			var supported bool
			tid, supported, writeErr = addTrack(muxer, cid)
			if writeErr != nil || !supported {
				return
			}
			tracks[cid] = tid
		}
		writeErr = muxer.Write(tid, frame, pts, dts)
	}

	if err := demuxer.Input(bytes.NewReader(buf)); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if writeErr != nil {
		return nil, writeErr
	}
	if len(tracks) == 0 {
		return nil, errors.New("no supported streams found in the transport stream")
	}
	if err := muxer.WriteTrailer(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func addTrack(muxer *mp4.Movmuxer, cid mpeg2.TS_STREAM_TYPE) (tid uint32, supported bool, err error) {
	switch cid {
	case mpeg2.TS_STREAM_H264:
		tid, err = muxer.AddVideoTrack(mp4.MP4_CODEC_H264)
	case mpeg2.TS_STREAM_H265:
		tid, err = muxer.AddVideoTrack(mp4.MP4_CODEC_H265)
	case mpeg2.TS_STREAM_AAC:
		tid, err = muxer.AddAudioTrack(mp4.MP4_CODEC_AAC)
	case mpeg2.TS_STREAM_AUDIO_MPEG1, mpeg2.TS_STREAM_AUDIO_MPEG2:
		tid, err = muxer.AddAudioTrack(mp4.MP4_CODEC_MP3)
	default:
		return 0, false, nil
	}
	return tid, err == nil, err
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.ts> <output.mp4>\n", os.Args[0])
		os.Exit(2)
	}
	data, err := MuxTSToMP4InMemory(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], data, 0666); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("done: %s -> %s (%d bytes)\n", os.Args[1], os.Args[2], len(data))
}
