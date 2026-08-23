package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/yapingcat/gomedia/go-flv"
	"github.com/yapingcat/gomedia/go-mp4"
)

// ConvertMP4ToFLV remuxes an mp4 file into a flv file.
func ConvertMP4ToFLV(mp4Path, flvPath string) error {
	mp4File, err := os.Open(mp4Path)
	if err != nil {
		return err
	}
	defer mp4File.Close()

	demuxer := mp4.CreateMp4Demuxer(mp4File)
	if _, err := demuxer.ReadHead(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	flvFile, err := os.OpenFile(flvPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer flvFile.Close()

	writer := flv.CreateFlvWriter(flvFile)
	if err := writer.WriteFlvHeader(); err != nil {
		return err
	}

	for {
		pkg, err := demuxer.ReadPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := writePacket(writer, pkg); err != nil {
			return err
		}
	}
}

func writePacket(writer *flv.FlvWriter, pkg *mp4.AVPacket) error {
	pts, dts := uint32(pkg.Pts), uint32(pkg.Dts)
	switch pkg.Cid {
	case mp4.MP4_CODEC_H264:
		return writer.WriteH264(pkg.Data, pts, dts)
	case mp4.MP4_CODEC_H265:
		return writer.WriteH265(pkg.Data, pts, dts)
	case mp4.MP4_CODEC_AAC:
		return writer.WriteAAC(pkg.Data, pts, dts)
	case mp4.MP4_CODEC_MP3:
		return writer.WriteMp3(pkg.Data, pts, dts)
	case mp4.MP4_CODEC_G711A:
		return writer.WriteG711A(pkg.Data, pts, dts)
	case mp4.MP4_CODEC_G711U:
		return writer.WriteG711U(pkg.Data, pts, dts)
	default:
		// a codec flv cannot carry: skip it rather than write a broken tag
		return nil
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.mp4> <output.flv>\n", os.Args[0])
		os.Exit(2)
	}
	if err := ConvertMP4ToFLV(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
