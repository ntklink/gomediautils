package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-mpeg2"
)

// frameRate is the rate the elementary stream is assumed to run at: a bare
// Annex-B stream carries no timing of its own.
const frameRate = 25

// MuxH264ToTS wraps an Annex-B h264 elementary stream in an mpeg-ts file,
// giving each access unit a timestamp derived from frameRate.
//
// A bare elementary stream carries no timing, so the timestamps are invented
// from the frame index in decode order. That is only right for a stream
// without frame reordering: recovering the presentation order of a stream
// with b frames would mean decoding picture order counts, and a real pipeline
// takes the timestamps from the container the stream came out of instead.
func MuxH264ToTS(h264Path, tsPath string) error {
	h264, err := os.ReadFile(h264Path)
	if err != nil {
		return err
	}

	tsFile, err := os.OpenFile(tsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer tsFile.Close()

	var writeErr error
	muxer := mpeg2.NewTSMuxer()
	muxer.OnPacket = func(pkg []byte) {
		if writeErr == nil {
			_, writeErr = tsFile.Write(pkg)
		}
	}
	pid := muxer.AddStream(mpeg2.TS_STREAM_H264)

	// gather the nalus of one access unit before handing it over: the muxer
	// wants a whole frame so it can decide on the pes packet boundaries
	var au []byte
	frames := 0
	flush := func() error {
		if len(au) == 0 {
			return nil
		}
		ts := uint64(frames) * 1000 / frameRate
		frames++
		err := muxer.Write(pid, au, ts, ts)
		au = au[:0]
		return err
	}

	codec.SplitFrameWithStartCode(h264, func(nalu []byte) bool {
		naluType := codec.H264NaluType(nalu)
		if codec.IsH264VCLNaluType(naluType) && len(au) > 0 && hasVCL(au) {
			if writeErr = flush(); writeErr != nil {
				return false
			}
		}
		au = append(au, nalu...)
		return true
	})
	if writeErr != nil {
		return writeErr
	}
	if err := flush(); err != nil {
		return err
	}
	return writeErr
}

// hasVCL reports whether the buffered access unit already holds a slice, so
// that leading parameter sets stay attached to the frame that follows them.
func hasVCL(au []byte) bool {
	found := false
	codec.SplitFrameWithStartCode(au, func(nalu []byte) bool {
		if codec.IsH264VCLNaluType(codec.H264NaluType(nalu)) {
			found = true
			return false
		}
		return true
	})
	return found
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.h264> <output.ts>\n", os.Args[0])
		os.Exit(2)
	}
	if err := MuxH264ToTS(os.Args[1], os.Args[2]); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
