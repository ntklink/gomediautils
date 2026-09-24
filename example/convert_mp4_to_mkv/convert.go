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

// ConvertMP4ToMKV remuxes an mp4 file into a Matroska file. The mp4 demuxer
// hands out Annex-B video and ADTS aac, the same shapes the Matroska muxer
// takes, so frames go across untouched. Opus is the exception: its OpusHead
// only exists in the mp4 header, so it is passed on as the track's extra
// data.
func ConvertMP4ToMKV(mp4Path, mkvPath string) error {
	mp4File, err := os.Open(mp4Path)
	if err != nil {
		return err
	}
	defer mp4File.Close()

	mkvFile, err := os.OpenFile(mkvPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer mkvFile.Close()

	demuxer := mp4.CreateMp4Demuxer(mp4File)
	infos, err := demuxer.ReadHead()
	if err != nil {
		return err
	}
	muxer, err := mkv.NewMuxer(mkvFile)
	if err != nil {
		return err
	}

	tracks := make(map[int]uint64)
	for _, info := range infos {
		var tn uint64
		switch info.Cid {
		case mp4.MP4_CODEC_H264:
			tn, err = muxer.AddVideoTrack(codec.CODECID_VIDEO_H264)
		case mp4.MP4_CODEC_H265:
			tn, err = muxer.AddVideoTrack(codec.CODECID_VIDEO_H265)
		case mp4.MP4_CODEC_AAC:
			tn, err = muxer.AddAudioTrack(codec.CODECID_AUDIO_AAC)
		case mp4.MP4_CODEC_MP3:
			tn, err = muxer.AddAudioTrack(codec.CODECID_AUDIO_MP3)
		case mp4.MP4_CODEC_G711A:
			tn, err = muxer.AddAudioTrack(codec.CODECID_AUDIO_G711A,
				mkv.WithAudioSampleRate(info.SampleRate), mkv.WithAudioChannelCount(info.ChannelCount))
		case mp4.MP4_CODEC_G711U:
			tn, err = muxer.AddAudioTrack(codec.CODECID_AUDIO_G711U,
				mkv.WithAudioSampleRate(info.SampleRate), mkv.WithAudioChannelCount(info.ChannelCount))
		case mp4.MP4_CODEC_OPUS:
			var head []byte
			if head, err = demuxer.GetExtraData(uint32(info.TrackId)); err != nil {
				return err
			}
			tn, err = muxer.AddAudioTrack(codec.CODECID_AUDIO_OPUS, mkv.WithExtraData(head))
		default:
			// a codec this example does not carry over
			continue
		}
		if err != nil {
			return err
		}
		tracks[info.TrackId] = tn
	}
	if len(tracks) == 0 {
		return errors.New("no track this example can carry into matroska")
	}

	for {
		pkt, err := demuxer.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		tn, ok := tracks[pkt.TrackId]
		if !ok {
			continue
		}
		if err := muxer.Write(tn, pkt.Data, pkt.Pts, pkt.Dts); err != nil {
			return err
		}
	}
	return muxer.WriteTrailer()
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.mp4> <output.mkv>\n", os.Args[0])
		os.Exit(2)
	}
	if err := ConvertMP4ToMKV(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("done:", os.Args[1], "->", os.Args[2])
}
