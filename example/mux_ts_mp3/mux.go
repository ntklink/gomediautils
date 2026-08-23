package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-mpeg2"
)

// MuxMP3ToTS wraps an mp3 elementary stream in an mpeg-ts file.
//
// Each frame's duration comes out of its own header rather than being assumed
// constant, because an mp3 stream may switch bitrate, and a variable bitrate
// file muxed at a fixed frame duration drifts out of sync with itself.
//
// Timestamps are accumulated in samples and converted once, so the rounding
// error of a frame that is not a whole number of milliseconds long, which is
// every one of them, does not build up over the file.
func MuxMP3ToTS(mp3Path, tsPath string) error {
	mp3, err := os.ReadFile(mp3Path)
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
	pid := muxer.AddStream(mpeg2.TS_STREAM_AUDIO_MPEG1)

	samples := uint64(0)
	frames := 0
	codec.SplitMp3Frames(mp3, func(head *codec.MP3FrameHead, frame []byte) {
		if writeErr != nil {
			return
		}
		rate := head.GetSampleRate()
		if rate == 0 {
			writeErr = errors.New("mp3 frame header has no sample rate")
			return
		}
		ts := samples * 1000 / uint64(rate)
		samples += uint64(head.SampleSize)
		frames++
		writeErr = muxer.Write(pid, frame, ts, ts)
	})
	if writeErr != nil {
		return writeErr
	}
	if frames == 0 {
		return errors.New("no mp3 frames found; is the input really an mp3 elementary stream?")
	}
	return nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.mp3> <output.ts>\n", os.Args[0])
		os.Exit(2)
	}
	if err := MuxMP3ToTS(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
