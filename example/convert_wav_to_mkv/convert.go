package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-es"
	"github.com/ntklink/gomediautils/go-mkv"
	"github.com/ntklink/gomediautils/go-wav"
)

// ConvertWAVToMKV puts the G.711 audio of a WAV file into a Matroska file.
// A wav.Reader is an es.Reader, so it feeds the muxer the same way a bare
// elementary stream does: 20 ms frames with their timestamps.
func ConvertWAVToMKV(wavPath, mkvPath string) error {
	src, err := os.Open(wavPath)
	if err != nil {
		return err
	}
	defer src.Close()
	reader, err := wav.NewReader(src)
	if err != nil {
		return err
	}
	format := reader.Format()
	if reader.Codec() == codec.CODECID_UNRECOGNIZED {
		return fmt.Errorf("wav format 0x%04X is not G.711", format.AudioFormat)
	}

	dst, err := os.OpenFile(mkvPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer dst.Close()
	muxer, err := mkv.NewMuxer(dst)
	if err != nil {
		return err
	}
	track, err := muxer.AddAudioTrack(reader.Codec(),
		mkv.WithAudioSampleRate(format.SampleRate), mkv.WithAudioChannelCount(uint8(format.Channels)))
	if err != nil {
		return err
	}
	return copyFrames(reader, muxer, track)
}

func copyFrames(r es.Reader, muxer *mkv.Muxer, track uint64) error {
	for {
		f, err := r.ReadFrame()
		if errors.Is(err, io.EOF) {
			return muxer.WriteTrailer()
		}
		if err != nil {
			return err
		}
		if err := muxer.Write(track, f.Data, f.Pts, f.Dts); err != nil {
			return err
		}
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.wav> <output.mkv>\n", os.Args[0])
		os.Exit(2)
	}
	if err := ConvertWAVToMKV(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
