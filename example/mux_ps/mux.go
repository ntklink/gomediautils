package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/yapingcat/gomedia/go-mpeg2"
)

// MuxTSToPS reads an mpeg-ts file and writes the same streams as an mpeg
// program stream, the container gb28181 devices speak.
func MuxTSToPS(tsPath, psPath string) error {
	tsFile, err := os.Open(tsPath)
	if err != nil {
		return err
	}
	defer tsFile.Close()

	psFile, err := os.OpenFile(psPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer psFile.Close()

	var writeErr error
	muxer := mpeg2.NewPsMuxer()
	muxer.OnPacket = func(pkg []byte) {
		if writeErr == nil {
			_, writeErr = psFile.Write(pkg)
		}
	}

	streams := make(map[mpeg2.TS_STREAM_TYPE]uint8)
	demuxer := mpeg2.NewTSDemuxer()
	demuxer.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts, dts uint64) {
		if writeErr != nil {
			return
		}
		sid, ok := streams[cid]
		if !ok {
			psType, supported := psStreamType(cid)
			if !supported {
				return
			}
			sid = muxer.AddStream(psType)
			streams[cid] = sid
		}
		writeErr = muxer.Write(sid, frame, pts, dts)
	}

	if err := demuxer.Input(tsFile); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return writeErr
}

// psStreamType maps a ts stream type onto the program stream equivalent.
func psStreamType(cid mpeg2.TS_STREAM_TYPE) (mpeg2.PS_STREAM_TYPE, bool) {
	switch cid {
	case mpeg2.TS_STREAM_H264:
		return mpeg2.PS_STREAM_H264, true
	case mpeg2.TS_STREAM_H265:
		return mpeg2.PS_STREAM_H265, true
	case mpeg2.TS_STREAM_AAC:
		return mpeg2.PS_STREAM_AAC, true
	default:
		return 0, false
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.ts> <output.ps>\n", os.Args[0])
		os.Exit(2)
	}
	if err := MuxTSToPS(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
