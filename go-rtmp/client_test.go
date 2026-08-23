package rtmp

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-flv"
)

// netTestAddr returns the rtmp server used by the network tests, they are skipped unless
// GOMEDIA_NET_TEST=1 is set (GOMEDIA_RTMP_ADDR overrides the default address)
func netTestAddr(t *testing.T) string {
	t.Helper()
	if os.Getenv("GOMEDIA_NET_TEST") != "1" {
		t.Skip("network test, set GOMEDIA_NET_TEST=1 to run")
	}
	if addr := os.Getenv("GOMEDIA_RTMP_ADDR"); addr != "" {
		return addr
	}
	return "49.235.110.177:1935"
}

func TestRtmpClient_Play(t *testing.T) {
	addr := netTestAddr(t)
	outDir := t.TempDir()
	t.Run("play", func(t *testing.T) {
		c, err := net.Dial("tcp4", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()

		cli := NewRtmpClient(WithComplexHandshake(),
			WithComplexHandshakeSchema(HANDSHAKE_COMPLEX_SCHEMA1))

		cli.OnError(func(code, describe string) {
			fmt.Printf("rtmp error code:%s,describe:%s\n", code, describe)
		})

		cli.OnStatus(func(code, level, describe string) {
			fmt.Printf("rtmp onstatus code:%s,level:%s describe:%s\n", code, level, describe)
		})

		firstVideo := true
		firstAudio := true
		var fd *os.File = nil
		var fd2 *os.File = nil
		defer func() {
			if fd != nil {
				fd.Close()
			}
			if fd2 != nil {
				fd2.Close()
			}
		}()

		cli.OnFrame(func(cid codec.CodecID, pts, dts uint32, frame []byte) {
			if cid == codec.CODECID_VIDEO_H264 {
				if firstVideo {
					fd, _ = os.OpenFile(filepath.Join(outDir, "v.h264"), os.O_CREATE|os.O_RDWR, 0666)
					firstVideo = false
				}
				fmt.Printf("recv frame id:%d, pts:%d , dts:%d\n", cid, pts, dts)
				fd.Write(frame)
			} else {
				if firstAudio {
					fd2, _ = os.OpenFile(filepath.Join(outDir, "a.aac"), os.O_CREATE|os.O_RDWR, 0666)
					firstAudio = false
				}
				fd2.Write(frame)
			}
		})

		cli.SetOutput(func(data []byte) error {
			_, err := c.Write(data)
			return err
		})

		if err := cli.Start("rtmp://" + addr + "/live/test"); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4096)
		n := 0
		for err == nil {
			n, err = c.Read(buf)
			if err != nil {
				continue
			}
			cli.Input(buf[:n])
		}
		fmt.Println(err)
	})
}

func TestRtmpClient_Pub(t *testing.T) {

	addr := netTestAddr(t)
	t.Run("pub", func(t *testing.T) {
		c, err := net.Dial("tcp4", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()

		cli := NewRtmpClient(WithComplexHandshake(),
			WithComplexHandshakeSchema(HANDSHAKE_COMPLEX_SCHEMA1),
			WithEnablePublish())

		cli.OnError(func(code, describe string) {
			fmt.Printf("rtmp code:%s , describe:%s\n", code, describe)
		})

		isReady := make(chan struct{})
		cli.OnStatus(func(code, level, describe string) {
			fmt.Printf("rtmp onstatus code:%s,level:%s describe:%s\n", code, level, describe)
		})

		cli.OnStateChange(func(newState RtmpState) {
			if newState == STATE_RTMP_PUBLISH_START {
				fmt.Println("ready for publish")
				close(isReady)
			}
		})

		go func() {
			<-isReady
			fmt.Println("start to read flv")
			f := flv.CreateFlvReader()
			f.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
				if cid == codec.CODECID_VIDEO_H264 {
					fmt.Println("write video frame", pts, dts)
					cli.WriteVideo(cid, frame, pts, dts)
					time.Sleep(time.Millisecond * 33)
				} else if cid == codec.CODECID_AUDIO_AAC {
					cli.WriteAudio(cid, frame, pts, dts)
				}
			}
			fd, _ := os.Open("source.200kbps.768x320.flv")
			defer fd.Close()
			cache := make([]byte, 4096)
			for {
				n, err := fd.Read(cache)
				if err != nil {
					fmt.Println(err)
					break
				}
				f.Input(cache[0:n])
			}
		}()

		cli.SetOutput(func(data []byte) error {
			_, err := c.Write(data)
			return err
		})

		if err := cli.Start("rtmp://" + addr + "/live/test"); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4096)
		n := 0
		for err == nil {
			n, err = c.Read(buf)
			if err != nil {
				continue
			}
			cli.Input(buf[:n])
		}
		fmt.Println(err)
	})
}
