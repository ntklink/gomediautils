package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-mkv"
	"github.com/ntklink/gomediautils/go-mp4"
)

// ConvertMKVToMP4 remuxes a Matroska or WebM file into an mp4 file. The
// Matroska demuxer hands out Annex-B video and ADTS aac with decode times
// derived for b frames, which is what the mp4 muxer takes.
func ConvertMKVToMP4(mkvPath, mp4Path string) error {
	mkvFile, err := os.Open(mkvPath)
	if err != nil {
		return err
	}
	defer mkvFile.Close()

	mp4File, err := os.OpenFile(mp4Path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer mp4File.Close()

	demuxer := mkv.NewDemuxer(mkvFile)
	infos, err := demuxer.ReadHead()
	if err != nil {
		return err
	}
	muxer, err := mp4.CreateMp4Muxer(mp4File)
	if err != nil {
		return err
	}

	tracks := make(map[uint64]uint32)
	for _, info := range infos {
		var tid uint32
		switch info.Cid {
		case codec.CODECID_VIDEO_H264:
			tid, err = muxer.AddVideoTrack(mp4.MP4_CODEC_H264)
		case codec.CODECID_VIDEO_H265:
			tid, err = muxer.AddVideoTrack(mp4.MP4_CODEC_H265)
		case codec.CODECID_AUDIO_AAC:
			tid, err = muxer.AddAudioTrack(mp4.MP4_CODEC_AAC)
		case codec.CODECID_AUDIO_MP3:
			tid, err = muxer.AddAudioTrack(mp4.MP4_CODEC_MP3)
		case codec.CODECID_AUDIO_OPUS:
			// the OpusHead in CodecPrivate becomes the dOps box
			tid, err = muxer.AddAudioTrack(mp4.MP4_CODEC_OPUS,
				mp4.WithExtraData(info.ExtraData),
				mp4.WithAudioChannelCount(info.ChannelCount),
				mp4.WithAudioSampleRate(info.SampleRate))
		default:
			// a codec this example does not carry over
			continue
		}
		if err != nil {
			return err
		}
		tracks[info.TrackNumber] = tid
	}
	if len(tracks) == 0 {
		return errors.New("no track this example can carry into mp4")
	}

	for {
		pkt, err := demuxer.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		tid, ok := tracks[pkt.TrackNumber]
		if !ok {
			continue
		}
		if err := muxer.Write(tid, pkt.Data, pkt.Pts, pkt.Dts); err != nil {
			return err
		}
	}
	return muxer.WriteTrailer()
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.mkv> <output.mp4>\n", os.Args[0])
		os.Exit(2)
	}
	if err := ConvertMKVToMP4(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
