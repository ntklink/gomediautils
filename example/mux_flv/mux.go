package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
)

// RemuxFLV reads an flv file and writes an equivalent one, going all the way
// down to elementary stream frames in between.
//
// It is the shortest end to end check there is on the flv code: the reader
// has to hand over exactly the frames the writer needs, and the writer has to
// rebuild the sequence headers the reader consumed. Anything the pair gets
// wrong about avcc lengths, composition times or sound formats shows up as a
// file that no longer plays.
func RemuxFLV(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer dst.Close()

	writer := flv.CreateFlvWriter(dst)
	if err := writer.WriteFlvHeader(); err != nil {
		return err
	}

	var writeErr error
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if writeErr != nil {
			return
		}
		switch cid {
		case codec.CODECID_AUDIO_AAC:
			writeErr = writer.WriteAAC(frame, pts, dts)
		case codec.CODECID_AUDIO_MP3:
			writeErr = writer.WriteMp3(frame, pts, dts)
		case codec.CODECID_VIDEO_H264:
			writeErr = writer.WriteH264(frame, pts, dts)
		case codec.CODECID_VIDEO_H265:
			writeErr = writer.WriteH265(frame, pts, dts)
		}
	}

	buf := make([]byte, 64*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if err := reader.Input(buf[:n]); err != nil {
				return err
			}
		}
		if writeErr != nil {
			return writeErr
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	return writeErr
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.flv> <output.flv>\n", os.Args[0])
		os.Exit(2)
	}
	if err := RemuxFLV(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
