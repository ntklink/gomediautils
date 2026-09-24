package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-mkv"
)

// RemuxWebM reads a WebM or Matroska file and writes its VP8 and Opus tracks
// into a new WebM file. With live set the output goes through a plain
// io.Writer, the way it would go to a socket or an HTTP response: the
// muxer then writes an unknown size segment without cues, which is what a
// live WebM stream looks like.
func RemuxWebM(srcPath, dstPath string, live bool) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer dst.Close()

	var w io.Writer = dst
	if live {
		w = struct{ io.Writer }{dst} // hide Seek
	}

	demuxer := mkv.NewDemuxer(src)
	infos, err := demuxer.ReadHead()
	if err != nil {
		return err
	}
	muxer, err := mkv.NewMuxer(w, mkv.WithDocType(mkv.DocTypeWebM))
	if err != nil {
		return err
	}

	tracks := make(map[uint64]uint64)
	for _, info := range infos {
		var tn uint64
		switch info.Cid {
		case codec.CODECID_VIDEO_VP8:
			tn, err = muxer.AddVideoTrack(info.Cid, mkv.WithVideoSize(info.Width, info.Height))
		case codec.CODECID_AUDIO_OPUS:
			tn, err = muxer.AddAudioTrack(info.Cid, mkv.WithExtraData(info.ExtraData))
		default:
			// webm carries nothing else this package writes
			continue
		}
		if err != nil {
			return err
		}
		tracks[info.TrackNumber] = tn
	}
	if len(tracks) == 0 {
		return errors.New("no vp8 or opus track to remux")
	}

	for {
		pkt, err := demuxer.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if tn, ok := tracks[pkt.TrackNumber]; ok {
			// the padding trims the encoder's padding off the last opus frame
			if err := muxer.WritePadded(tn, pkt.Data, pkt.Pts, pkt.Dts, pkt.DiscardPadding); err != nil {
				return err
			}
		}
	}
	return muxer.WriteTrailer()
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.webm> <output.webm> [live]\n", os.Args[0])
		os.Exit(2)
	}
	if err := RemuxWebM(os.Args[1], os.Args[2], len(os.Args) > 3 && os.Args[3] == "live"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
