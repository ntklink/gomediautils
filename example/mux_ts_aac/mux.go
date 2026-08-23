package main

import (
	"fmt"
	"os"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-mpeg2"
)

// aacSamplesPerFrame is fixed for aac-lc: every adts frame decodes to
// exactly this many samples per channel, which is what makes the timestamps
// below derivable from nothing but the frame index.
const aacSamplesPerFrame = 1024

// MuxAACToTS wraps an adts aac elementary stream in an mpeg-ts file.
//
// A bare adts stream carries no timestamps, so they are computed from the
// sample rate in the adts header: frame n starts at n*1024 samples. Deriving
// them rather than counting a fixed number of milliseconds per frame matters,
// because 1024 samples is not a whole number of milliseconds at any of the
// usual rates and the drift accumulates over a long file.
func MuxAACToTS(aacPath, tsPath string) error {
	aac, err := os.ReadFile(aacPath)
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
	pid := muxer.AddStream(mpeg2.TS_STREAM_AAC)

	sampleRate := 0
	frames := uint64(0)
	codec.SplitAACFrame(aac, func(frame []byte) {
		if writeErr != nil {
			return
		}
		if sampleRate == 0 {
			hdr := codec.NewAdtsFrameHeader()
			if err := hdr.Decode(frame); err != nil {
				writeErr = err
				return
			}
			sampleRate = codec.AACSampleIdxToSample(int(hdr.Fix_Header.Sampling_frequency_index))
			if sampleRate == 0 {
				writeErr = fmt.Errorf("adts header names sampling frequency index %d, which has no rate",
					hdr.Fix_Header.Sampling_frequency_index)
				return
			}
		}
		ts := frames * aacSamplesPerFrame * 1000 / uint64(sampleRate)
		frames++
		writeErr = muxer.Write(pid, frame, ts, ts)
	})
	return writeErr
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.aac> <output.ts>\n", os.Args[0])
		os.Exit(2)
	}
	if err := MuxAACToTS(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
