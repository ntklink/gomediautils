package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-rtsp"
	"github.com/yapingcat/gomedia/go-rtsp/sdp"
)

const (
	Init      = 0
	HandShake = 1
	Playing   = 2
	Teardown  = 3
)

type RtspPlaySession struct {
	outDir    string
	timeout   int
	once      sync.Once
	die       chan struct{}
	c         net.Conn
	lastError error

	// The rtsp connection is read on one goroutine while the play limit
	// expires on another, so everything both of them touch is guarded.
	mtx          sync.Mutex
	videoStarted bool
	files        map[string]*os.File
	samples      map[string]int // samples received per kind, for the caller
}

func NewRtspPlaySession(c net.Conn, outDir string) *RtspPlaySession {
	return &RtspPlaySession{
		die:     make(chan struct{}),
		c:       c,
		outDir:  outDir,
		files:   make(map[string]*os.File),
		samples: make(map[string]int),
	}
}

// write appends a sample to the file for its kind, opening it on first use.
func (cli *RtspPlaySession) write(kind, name string, data []byte) {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	f, ok := cli.files[kind]
	if !ok {
		var err error
		f, err = os.OpenFile(filepath.Join(cli.outDir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
		if err != nil {
			cli.lastError = err
			return
		}
		cli.files[kind] = f
	}
	if _, err := f.Write(data); err != nil {
		cli.lastError = err
		return
	}
	cli.samples[kind]++
}

// startVideo reports whether this sample may begin the recording, and
// remembers that it did.
func (cli *RtspPlaySession) startVideo(sample []byte) bool {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	if cli.videoStarted {
		return true
	}
	if isTrailingSlice(sample) {
		return false
	}
	cli.videoStarted = true
	return true
}

// Samples reports how many samples of each kind the session received.
func (cli *RtspPlaySession) Samples() map[string]int {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	out := make(map[string]int, len(cli.samples))
	for k, v := range cli.samples {
		out[k] = v
	}
	return out
}

func (cli *RtspPlaySession) Destory() {
	cli.once.Do(func() {
		cli.mtx.Lock()
		for _, f := range cli.files {
			f.Close()
		}
		cli.mtx.Unlock()
		cli.c.Close()
		close(cli.die)
	})
}

func (cli *RtspPlaySession) HandleOption(client *rtsp.RtspClient, res rtsp.RtspResponse, public []string) error {
	fmt.Println("rtsp server public ", public)
	return nil
}

func (cli *RtspPlaySession) HandleDescribe(client *rtsp.RtspClient, res rtsp.RtspResponse, sdp *sdp.Sdp, tracks map[string]*rtsp.RtspTrack) error {
	fmt.Println("handle describe ", res.StatusCode, res.Reason)
	for k, t := range tracks {
		if t == nil {
			continue
		}
		fmt.Println("Got ", k, " track")
		switch t.Codec.Cid {
		case rtsp.RTSP_CODEC_H264:
			t.OnSample(func(sample rtsp.RtspSample) {
				// A player joins a live stream in the middle of a gop. The
				// frames before the next key frame reference pictures that
				// were never received, so writing them produces a file that
				// starts with decode errors: skip until the stream is
				// decodable from the beginning.
				if !cli.startVideo(sample.Sample) {
					return
				}
				cli.write("video", "video.h264", sample.Sample)
			})
		case rtsp.RTSP_CODEC_AAC:
			t.OnSample(func(sample rtsp.RtspSample) {
				cli.write("audio", "audio.aac", sample.Sample)
			})
		case rtsp.RTSP_CODEC_TS:
			t.OnSample(func(sample rtsp.RtspSample) {
				cli.write("ts", "mp2t.ts", sample.Sample)
			})
		}
	}
	return nil
}

func (cli *RtspPlaySession) HandleSetup(client *rtsp.RtspClient, res rtsp.RtspResponse, track *rtsp.RtspTrack, tracks map[string]*rtsp.RtspTrack, sessionId string, timeout int) error {
	fmt.Println("HandleSetup sessionid:", sessionId, " timeout:", timeout)
	cli.timeout = timeout
	return nil
}

func (cli *RtspPlaySession) HandleAnnounce(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspPlaySession) HandlePlay(client *rtsp.RtspClient, res rtsp.RtspResponse, timeRange *rtsp.RangeTime, info *rtsp.RtpInfo) error {
	if res.StatusCode != 200 {
		fmt.Println("play failed ", res.StatusCode, res.Reason)
		return nil
	}
	go func() {
		//rtsp keepalive
		to := time.NewTicker(time.Duration(cli.timeout/2) * time.Second)
		defer to.Stop()
		for {
			select {
			case <-to.C:
				client.KeepAlive(rtsp.OPTIONS)
			case <-cli.die:
				return
			}
		}
	}()
	return nil
}

func (cli *RtspPlaySession) HandlePause(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspPlaySession) HandleTeardown(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspPlaySession) HandleGetParameter(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspPlaySession) HandleSetParameter(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspPlaySession) HandleRedirect(client *rtsp.RtspClient, req rtsp.RtspRequest, location string, timeRange *rtsp.RangeTime) error {
	return nil
}

func (cli *RtspPlaySession) HandleRecord(client *rtsp.RtspClient, res rtsp.RtspResponse, timeRange *rtsp.RangeTime, info *rtsp.RtpInfo) error {
	return nil
}

func (cli *RtspPlaySession) HandleRequest(client *rtsp.RtspClient, req rtsp.RtspRequest) error {
	return nil
}

func (cli *RtspPlaySession) sendInLoop(sendChan chan []byte) {
	for {
		select {
		case b := <-sendChan:
			_, err := cli.c.Write(b)
			if err != nil {
				cli.Destory()
				cli.lastError = err
				fmt.Println("quit send in loop")
				return
			}

		case <-cli.die:
			fmt.Println("quit send in loop")
			return
		}
	}
}

// isTrailingSlice reports whether the sample is a non key frame slice, the
// kind that can only be decoded with pictures a late joiner never saw.
// Anything else (a parameter set, an sei, an idr) can start the recording.
func isTrailingSlice(sample []byte) bool {
	naluType := codec.H264NaluType(sample)
	return naluType >= codec.H264_NAL_P_SLICE && naluType <= codec.H264_NAL_SLICE_C
}

// PlayRTSP plays an rtsp url with rtp interleaved over the rtsp connection
// and writes the elementary streams into outDir as video.h264 / audio.aac.
// It stops after limit, or when the server closes the connection.
func PlayRTSP(rtspURL, outDir string, limit time.Duration) (*RtspPlaySession, error) {
	u, err := url.Parse(rtspURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		host += ":554"
	}
	c, err := net.DialTimeout("tcp4", host, 10*time.Second)
	if err != nil {
		return nil, err
	}

	sess := NewRtspPlaySession(c, outDir)
	defer sess.Destory()

	sendChan := make(chan []byte, 100)
	go sess.sendInLoop(sendChan)

	client, err := rtsp.NewRtspClient(rtspURL, sess)
	if err != nil {
		return sess, err
	}
	client.SetOutput(func(b []byte) error {
		if sess.lastError != nil {
			return sess.lastError
		}
		sendChan <- b
		return nil
	})

	if limit > 0 {
		// the read loop blocks on the socket, so the limit is enforced by
		// closing the connection under it
		timer := time.AfterFunc(limit, func() { sess.Destory() })
		defer timer.Stop()
	}

	if err := client.Start(); err != nil {
		return sess, err
	}
	buf := make([]byte, 64*1024)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if perr := client.Input(buf[:n]); perr != nil {
				// a parse error is the interesting one: report it rather
				// than returning as though the stream simply ended
				return sess, perr
			}
		}
		if err != nil {
			break
		}
	}
	return sess, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <rtsp url> [outdir]\n", os.Args[0])
		os.Exit(2)
	}
	outDir := "."
	if len(os.Args) > 2 {
		outDir = os.Args[2]
	}
	sess, err := PlayRTSP(os.Args[1], outDir, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("received samples:", sess.Samples())
}
