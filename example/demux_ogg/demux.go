package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-mp4"
	"github.com/yapingcat/gomedia/go-ogg"
)

// ConvertOggToMP4 pulls the opus stream out of an ogg file and writes it into
// an mp4. The ogg demuxer reports frames with the timestamps it derives from
// the granule positions, which is exactly what the mp4 muxer needs.
func ConvertOggToMP4(oggPath, mp4Path string) error {
	oggFile, err := os.Open(oggPath)
	if err != nil {
		return err
	}
	defer oggFile.Close()

	mp4File, err := os.OpenFile(mp4Path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer mp4File.Close()

	muxer, err := mp4.CreateMp4Muxer(mp4File)
	if err != nil {
		return err
	}

	tracks := make(map[uint32]uint32)
	var writeErr error

	demuxer := ogg.NewDemuxer()
	demuxer.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts, dts uint64, lost int) {
		if writeErr != nil {
			return
		}
		tid, ok := tracks[streamId]
		if !ok {
			if cid != codec.CODECID_AUDIO_OPUS {
				// this example only carries opus over into mp4
				return
			}
			// mp4 describes an opus track with a dOps box built from the
			// OpusHead the ogg file opens with: channel count, pre skip and
			// the channel mapping all come from there, so the track cannot be
			// declared before the demuxer has read it
			param := demuxer.GetAudioParam()
			if param == nil || len(param.ExtraData) == 0 {
				writeErr = errors.New("ogg: opus stream without an OpusHead")
				return
			}
			tid, writeErr = muxer.AddAudioTrack(mp4.MP4_CODEC_OPUS,
				mp4.WithExtraData(param.ExtraData),
				mp4.WithAudioChannelCount(uint8(param.ChannelCount)),
				mp4.WithAudioSampleRate(param.SampleRate))
			if writeErr != nil {
				return
			}
			tracks[streamId] = tid
		}
		writeErr = muxer.Write(tid, frame, pts, dts)
	}

	buf := make([]byte, 64*1024)
	for {
		n, err := oggFile.Read(buf)
		if n > 0 {
			if err := demuxer.Input(buf[:n]); err != nil {
				return err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if writeErr != nil {
		return writeErr
	}
	if len(tracks) == 0 {
		return errors.New("the ogg file carries no opus stream")
	}
	return muxer.WriteTrailer()
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.ogg> <output.mp4>\n", os.Args[0])
		os.Exit(2)
	}
	if err := ConvertOggToMP4(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
