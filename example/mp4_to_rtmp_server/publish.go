package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-mp4"
	"github.com/yapingcat/gomedia/go-rtmp"
)

// PublishMP4 pushes the tracks of an mp4 file to an rtmp server.
//
// The mp4 demuxer returns each track's samples in that track's own decode
// order, not interleaved with the others, so the packets are collected and
// sorted before they go out. rtmp carries one timeline: a server handed all
// the video and then all the audio has no way to play them together, and
// most will drop the audio outright for arriving an entire file late.
func PublishMP4(rtmpURL, mp4Path string, paced bool) error {
	packets, err := readPackets(mp4Path)
	if err != nil {
		return err
	}
	if len(packets) == 0 {
		return errors.New("the mp4 holds no streams rtmp can carry")
	}

	addr, err := rtmpAddr(rtmpURL)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp4", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer conn.Close()

	cli := rtmp.NewRtmpClient(rtmp.WithComplexHandshake(), rtmp.WithEnablePublish())
	cli.SetOutput(func(data []byte) error {
		_, err := conn.Write(data)
		return err
	})

	ready := make(chan struct{})
	var readyOnce bool
	cli.OnStateChange(func(newState rtmp.RtmpState) {
		if newState == rtmp.STATE_RTMP_PUBLISH_START && !readyOnce {
			readyOnce = true
			close(ready)
		}
	})

	pushDone := make(chan error, 1)
	go func() {
		select {
		case <-ready:
		case <-time.After(15 * time.Second):
			pushDone <- errors.New("server never accepted the publish")
			return
		}
		pushDone <- push(cli, packets, paced)
		// let the last chunks reach the server before the connection goes
		time.Sleep(300 * time.Millisecond)
		conn.Close()
	}()

	cli.Start(rtmpURL)

	buf := make([]byte, 64*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if err := cli.Input(buf[:n]); err != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return <-pushDone
}

// packet is one sample, ready to send.
type packet struct {
	cid codec.CodecID
	pts uint32
	dts uint32
	// order keeps the demuxer's ordering within a track stable when two
	// samples of different tracks share a decode time
	order int
	data  []byte
}

// readPackets loads the whole file and puts it into one interleaved
// timeline.
func readPackets(mp4Path string) ([]packet, error) {
	f, err := os.Open(mp4Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	demuxer := mp4.CreateMp4Demuxer(f)
	if _, err := demuxer.ReadHead(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	var packets []packet
	for {
		pkg, err := demuxer.ReadPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		cid, ok := rtmpCodec(pkg.Cid)
		if !ok {
			continue
		}
		packets = append(packets, packet{
			cid:   cid,
			pts:   uint32(pkg.Pts),
			dts:   uint32(pkg.Dts),
			order: len(packets),
			data:  pkg.Data,
		})
	}

	sort.SliceStable(packets, func(i, j int) bool {
		if packets[i].dts != packets[j].dts {
			return packets[i].dts < packets[j].dts
		}
		return packets[i].order < packets[j].order
	})
	return packets, nil
}

func rtmpCodec(cid mp4.MP4_CODEC_TYPE) (codec.CodecID, bool) {
	switch cid {
	case mp4.MP4_CODEC_H264:
		return codec.CODECID_VIDEO_H264, true
	case mp4.MP4_CODEC_H265:
		return codec.CODECID_VIDEO_H265, true
	case mp4.MP4_CODEC_AAC:
		return codec.CODECID_AUDIO_AAC, true
	case mp4.MP4_CODEC_MP3:
		return codec.CODECID_AUDIO_MP3, true
	default:
		return 0, false
	}
}

func push(cli *rtmp.RtmpClient, packets []packet, paced bool) error {
	start := time.Now()
	for _, p := range packets {
		if paced {
			// hold each frame back until its own timestamp is due
			if wait := time.Duration(p.dts)*time.Millisecond - time.Since(start); wait > 0 {
				time.Sleep(wait)
			}
		}
		if err := cli.WriteFrame(p.cid, p.data, p.pts, p.dts); err != nil {
			return err
		}
	}
	return nil
}

// rtmpAddr turns an rtmp url into the host:port to dial.
func rtmpAddr(rtmpURL string) (string, error) {
	u, err := url.Parse(rtmpURL)
	if err != nil {
		return "", err
	}
	if u.Port() == "" {
		return u.Host + ":1935", nil
	}
	return u.Host, nil
}

var (
	rtmpURL = flag.String("url", "rtmp://127.0.0.1/live/test", "rtmp url to publish to")
	mp4File = flag.String("mp4", "test.mp4", "mp4 file to publish")
	paced   = flag.Bool("paced", true, "pace the push at wall clock speed")
)

func main() {
	flag.Parse()
	if err := PublishMP4(*rtmpURL, *mp4File, *paced); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("published", *mp4File, "to", *rtmpURL)
}
