package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-flv"
	"github.com/ntklink/gomediautils/go-wav"
)

// ConvertFLVToWAV pulls the G.711 audio out of an flv file, the way cameras
// publish it over RTMP, and writes it into a WAV file any player opens.
// G.711 in flv is 8 kHz mono by definition of the format.
func ConvertFLVToWAV(flvPath, wavPath string) error {
	src, err := os.Open(flvPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(wavPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer dst.Close()

	var writer *wav.Writer
	var writeErr error
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if writeErr != nil || (cid != codec.CODECID_AUDIO_G711A && cid != codec.CODECID_AUDIO_G711U) {
			return
		}
		if writer == nil {
			var format wav.Format
			if format, writeErr = wav.FormatOf(cid, 8000, 1); writeErr != nil {
				return
			}
			if writer, writeErr = wav.NewWriter(dst, format); writeErr != nil {
				return
			}
		}
		_, writeErr = writer.Write(frame)
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
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if writer == nil {
		return errors.New("no g711 audio in the flv file")
	}
	return writer.Close()
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.flv> <output.wav>\n", os.Args[0])
		os.Exit(2)
	}
	if err := ConvertFLVToWAV(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
