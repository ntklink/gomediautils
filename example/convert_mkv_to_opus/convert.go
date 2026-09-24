package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-mkv"
	"github.com/ntklink/gomediautils/go-ogg"
)

// ConvertMKVToOpus pulls the first Opus track out of a WebM or Matroska file
// into an Ogg Opus (.opus) file. The OpusHead in the track's CodecPrivate
// becomes the first Ogg page, and the discard padding of the last packet
// becomes the end trimming of the final page, so the audio decodes to the
// same samples.
func ConvertMKVToOpus(mkvPath, opusPath string) error {
	src, err := os.Open(mkvPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(opusPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer dst.Close()

	demuxer := mkv.NewDemuxer(src)
	infos, err := demuxer.ReadHead()
	if err != nil {
		return err
	}
	var track uint64
	var head []byte
	for _, info := range infos {
		if info.Cid == codec.CODECID_AUDIO_OPUS {
			track, head = info.TrackNumber, info.ExtraData
			break
		}
	}
	if track == 0 {
		return errors.New("no opus track")
	}

	muxer := ogg.NewMuxer(dst)
	serial, err := muxer.AddOpusStream(head)
	if err != nil {
		return err
	}
	for {
		pkt, err := demuxer.ReadPacket()
		if err == io.EOF {
			return muxer.WriteTrailer()
		}
		if err != nil {
			return err
		}
		if pkt.TrackNumber != track {
			continue
		}
		if err := muxer.WritePadded(serial, pkt.Data, pkt.Pts, pkt.Dts, pkt.DiscardPadding); err != nil {
			return err
		}
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.webm|.mkv> <output.opus>\n", os.Args[0])
		os.Exit(2)
	}
	if err := ConvertMKVToOpus(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
