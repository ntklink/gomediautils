package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-flv"
	"github.com/ntklink/gomediautils/go-rtmp"
)

var rtmpUrl = flag.String("url", "rtmp://127.0.0.1/live/test", "publish rtmp url")
var flvFile = flag.String("flv", "test.flv", "push flv file to server")
var paced = flag.Bool("paced", true, "pace the push at wall clock speed")

// PublishFLV pushes a flv file to an rtmp server and returns once the whole
// file has been sent and the server has acknowledged the connection close.
//
// With paced set the frames go out at the speed their timestamps describe,
// the way a live encoder would; without it the file is pushed as fast as the
// link allows, which is what a test wants.
func PublishFLV(rtmpURL, flvPath string, paced bool) error {
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
		pushDone <- pushFLV(cli, flvPath, paced)
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

// pushFLV feeds every frame of the file into the client.
func pushFLV(cli *rtmp.RtmpClient, flvPath string, paced bool) error {
	f, err := os.Open(flvPath)
	if err != nil {
		return err
	}
	defer f.Close()

	start := time.Now()
	var writeErr error
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if writeErr != nil {
			return
		}
		if paced {
			// hold each frame back until its own timestamp is due
			if wait := time.Duration(dts)*time.Millisecond - time.Since(start); wait > 0 {
				time.Sleep(wait)
			}
		}
		writeErr = cli.WriteFrame(cid, frame, pts, dts)
	}

	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if err := reader.Input(buf[:n]); err != nil {
				return err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return writeErr
			}
			return err
		}
		if writeErr != nil {
			return writeErr
		}
	}
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

func main() {
	flag.Parse()
	if err := PublishFLV(*rtmpUrl, *flvFile, *paced); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("published", *flvFile, "to", *rtmpUrl)
}
