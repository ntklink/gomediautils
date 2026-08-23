package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-mpeg2"
)

// frameRate is the rate a bare elementary stream is assumed to run at: it
// carries no timing of its own.
const frameRate = 25

// MuxElementaryStreamToPS wraps an Annex-B h264 or h265 elementary stream in
// an mpeg program stream, the container gb28181 devices send.
//
// Whole access units are handed to the muxer rather than single nalus. A
// parameter set written as its own pes packet with its own timestamp is legal
// but wasteful, and more to the point it separates the sps and pps from the
// picture that needs them, which some decoders will not recover from.
//
// The timestamps are invented from the frame index in decode order, so this
// is only right for a stream without frame reordering. A real pipeline takes
// them from wherever the stream came from.
func MuxElementaryStreamToPS(esPath, psPath string) error {
	es, err := os.ReadFile(esPath)
	if err != nil {
		return err
	}

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

	hevc := isHEVC(esPath)
	streamType := mpeg2.PS_STREAM_H264
	if hevc {
		streamType = mpeg2.PS_STREAM_H265
	}
	sid := muxer.AddStream(streamType)

	var au []byte
	frames := uint64(0)
	flush := func() error {
		if len(au) == 0 {
			return nil
		}
		ts := frames * 1000 / frameRate
		frames++
		err := muxer.Write(sid, au, ts, ts)
		au = au[:0]
		return err
	}

	codec.SplitFrameWithStartCode(es, func(nalu []byte) bool {
		if isVCL(nalu, hevc) && hasVCL(au, hevc) {
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
	if frames == 0 {
		return fmt.Errorf("no access units found in %s; is it an Annex-B elementary stream?", esPath)
	}
	return writeErr
}

// isHEVC guesses the codec from the file name, the only clue a bare
// elementary stream gives without parsing it.
func isHEVC(path string) bool {
	name := strings.ToUpper(filepath.Base(path))
	for _, suffix := range []string{"H265", "265", "HEVC"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func isVCL(nalu []byte, hevc bool) bool {
	if hevc {
		return codec.IsH265VCLNaluType(codec.H265NaluType(nalu))
	}
	return codec.IsH264VCLNaluType(codec.H264NaluType(nalu))
}

// hasVCL reports whether the buffered access unit already holds a slice, so
// leading parameter sets stay attached to the picture that follows them.
func hasVCL(au []byte, hevc bool) bool {
	found := false
	codec.SplitFrameWithStartCode(au, func(nalu []byte) bool {
		if isVCL(nalu, hevc) {
			found = true
			return false
		}
		return true
	})
	return found
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.h264|input.h265> [output.ps]\n", os.Args[0])
		os.Exit(2)
	}
	psPath := os.Args[1] + ".ps"
	if len(os.Args) > 2 {
		psPath = os.Args[2]
	}
	if err := MuxElementaryStreamToPS(os.Args[1], psPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", psPath)
}
