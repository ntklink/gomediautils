package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
	"github.com/yapingcat/gomedia/go-rtmp"
)

var rtmpUrl = flag.String("url", "rtmp://127.0.0.1/live/test", "play rtmp url")
var outFile = flag.String("out", "out.flv", "write the played stream to this flv file")
var duration = flag.Duration("t", 0, "stop after this long (0 plays until the server closes)")

// PlayToFLV plays an rtmp stream and writes what arrives to a flv file, which
// is a form any player can check. It returns when the server closes the
// stream or, with a non zero limit, after that long.
func PlayToFLV(rtmpURL, flvPath string, limit time.Duration) error {
	addr, err := rtmpAddr(rtmpURL)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp4", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer conn.Close()

	out, err := os.OpenFile(flvPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer out.Close()

	writer := flv.CreateFlvWriter(out)
	if err := writer.WriteFlvHeader(); err != nil {
		return err
	}

	var writeErr error
	frames := 0
	cli := rtmp.NewRtmpClient(rtmp.WithComplexHandshake())
	cli.SetOutput(func(b []byte) error {
		_, err := conn.Write(b)
		return err
	})
	cli.OnFrame(func(cid codec.CodecID, pts, dts uint32, frame []byte) {
		if writeErr != nil {
			return
		}
		frames++
		switch cid {
		case codec.CODECID_VIDEO_H264:
			writeErr = writer.WriteH264(frame, pts, dts)
		case codec.CODECID_VIDEO_H265:
			writeErr = writer.WriteH265(frame, pts, dts)
		case codec.CODECID_AUDIO_AAC:
			writeErr = writer.WriteAAC(frame, pts, dts)
		case codec.CODECID_AUDIO_MP3:
			writeErr = writer.WriteMp3(frame, pts, dts)
		}
	})

	if limit > 0 {
		// the read loop below blocks on the socket, so the limit is enforced
		// by closing the connection under it
		timer := time.AfterFunc(limit, func() { conn.Close() })
		defer timer.Stop()
	}

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
	if writeErr != nil {
		return writeErr
	}
	if frames == 0 {
		return errors.New("the server sent no frames")
	}
	return nil
}

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

func main() {
	flag.Parse()
	if err := PlayToFLV(*rtmpUrl, *outFile, *duration); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("played", *rtmpUrl, "into", *outFile)
}
