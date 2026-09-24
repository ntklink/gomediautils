package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ntklink/gomediautils/go-mp4"
	"github.com/ntklink/gomediautils/go-mpeg2"
	"github.com/ntklink/gomediautils/go-srt"
)

// tsPerMessage is how many 188 byte TS packets go into one SRT message:
// seven fill the 1316 byte payload every SRT implementation expects.
const tsPerMessage = 7

// PushMP4OverSRT remuxes an mp4 file into MPEG-TS and sends it to an SRT
// listener as a caller, the way an encoder publishes to a streaming
// server. With realtime set the frames go out at the pace of their
// timestamps, as a live source would send them. It reports the statistics
// of the connection: packets sent, lost and sent again.
func PushMP4OverSRT(mp4Path, address string, cfg srt.Config, realtime bool) (stats srt.Stats, err error) {
	f, err := os.Open(mp4Path)
	if err != nil {
		return stats, err
	}
	defer f.Close()
	demuxer := mp4.CreateMp4Demuxer(f)
	infos, err := demuxer.ReadHead()
	if err != nil {
		return stats, err
	}

	conn, err := srt.Dial(address, cfg)
	if err != nil {
		return stats, err
	}
	defer func() {
		// Close waits for the last packets to be acknowledged and played
		conn.Close()
		stats = conn.Stats()
	}()

	// TS packets are gathered into full SRT messages
	msg := make([]byte, 0, tsPerMessage*188)
	var sendErr error
	muxer := mpeg2.NewTSMuxer()
	muxer.OnPacket = func(pkg []byte) {
		if sendErr != nil {
			return
		}
		msg = append(msg, pkg...)
		if len(msg) == cap(msg) {
			_, sendErr = conn.Write(msg)
			msg = msg[:0]
		}
	}

	pids := make(map[int]uint16)
	for _, info := range infos {
		switch info.Cid {
		case mp4.MP4_CODEC_H264:
			pids[info.TrackId] = muxer.AddStream(mpeg2.TS_STREAM_H264)
		case mp4.MP4_CODEC_H265:
			pids[info.TrackId] = muxer.AddStream(mpeg2.TS_STREAM_H265)
		case mp4.MP4_CODEC_AAC:
			pids[info.TrackId] = muxer.AddStream(mpeg2.TS_STREAM_AAC)
		case mp4.MP4_CODEC_MP3:
			pids[info.TrackId] = muxer.AddStream(mpeg2.TS_STREAM_AUDIO_MPEG1)
		}
	}
	if len(pids) == 0 {
		return stats, errors.New("no track mpeg-ts can carry")
	}

	start := time.Now()
	var firstDts uint64
	first := true
	for {
		pkt, err := demuxer.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stats, err
		}
		pid, ok := pids[pkt.TrackId]
		if !ok {
			continue
		}
		if first {
			firstDts, first = pkt.Dts, false
		}
		if realtime && pkt.Dts > firstDts {
			time.Sleep(time.Until(start.Add(time.Duration(pkt.Dts-firstDts) * time.Millisecond)))
		}
		if err := muxer.Write(pid, pkt.Data, pkt.Pts, pkt.Dts); err != nil {
			return stats, err
		}
		if sendErr != nil {
			return stats, sendErr
		}
	}
	if len(msg) > 0 {
		if _, err := conn.Write(msg); err != nil {
			return stats, err
		}
	}
	return stats, sendErr
}

func main() {
	streamID := flag.String("streamid", "", "stream id, e.g. #!::r=live/cam1,m=publish")
	passphrase := flag.String("passphrase", "", "encryption passphrase")
	latency := flag.Duration("latency", 120*time.Millisecond, "latency")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] <input.mp4> <host:port>\n", os.Args[0])
		os.Exit(2)
	}
	cfg := srt.Config{StreamID: *streamID, Passphrase: *passphrase, Latency: *latency}
	stats, err := PushMP4OverSRT(flag.Arg(0), flag.Arg(1), cfg, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("done: %s -> %s, %d packets sent, %d sent again\n",
		flag.Arg(0), flag.Arg(1), stats.PacketsSent, stats.PacketsRetransmitted)
}
