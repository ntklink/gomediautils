package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
	"github.com/yapingcat/gomedia/go-rtsp"
	"github.com/yapingcat/gomedia/go-rtsp/sdp"
)

// RtspRecordSession publishes a flv file to an rtsp server with ANNOUNCE and
// RECORD, carrying the rtp over the rtsp connection itself.
//
// Interleaving over tcp rather than opening udp sockets is what makes a
// publisher work from behind nat and through firewalls, at the cost of
// head of line blocking when the link is congested.
type RtspRecordSession struct {
	flvPath string
	// Paced sends each frame when its timestamp says, the way a live
	// encoder would. Turning it off pushes as fast as the link allows.
	paced bool

	c          net.Conn
	once       sync.Once
	die        chan struct{}
	sendChanel chan []byte
	waitSend   sync.WaitGroup

	mtx       sync.Mutex
	sent      int
	sendError error
	pushErr   error
	finished  chan struct{}
}

func NewRtspRecordSession(c net.Conn, flvPath string, paced bool) *RtspRecordSession {
	return &RtspRecordSession{
		c:          c,
		flvPath:    flvPath,
		paced:      paced,
		die:        make(chan struct{}),
		sendChanel: make(chan []byte, 100),
		finished:   make(chan struct{}),
	}
}

// Sent reports how many video frames have been handed to the track.
func (cli *RtspRecordSession) Sent() int {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	return cli.sent
}

// Err reports why the push stopped, if it stopped badly.
func (cli *RtspRecordSession) Err() error {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	return cli.pushErr
}

func (cli *RtspRecordSession) Destory() {
	cli.once.Do(func() {
		close(cli.die)
		cli.waitSend.Wait()
		// drain whatever the client queued before it noticed we were going
		for {
			select {
			case b := <-cli.sendChanel:
				if _, err := cli.c.Write(b); err != nil {
					cli.c.Close()
					return
				}
			default:
				cli.c.Close()
				return
			}
		}
	})
}

func (cli *RtspRecordSession) HandleOption(client *rtsp.RtspClient, res rtsp.RtspResponse, public []string) error {
	return nil
}

func (cli *RtspRecordSession) HandleDescribe(client *rtsp.RtspClient, res rtsp.RtspResponse, sdp *sdp.Sdp, tracks map[string]*rtsp.RtspTrack) error {
	return nil
}

func (cli *RtspRecordSession) HandleSetup(client *rtsp.RtspClient, res rtsp.RtspResponse, track *rtsp.RtspTrack, tracks map[string]*rtsp.RtspTrack, sessionId string, timeout int) error {
	return nil
}

func (cli *RtspRecordSession) HandleAnnounce(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	if res.StatusCode != 200 {
		return fmt.Errorf("announce refused: %d %s", res.StatusCode, res.Reason)
	}
	return nil
}

func (cli *RtspRecordSession) HandlePlay(client *rtsp.RtspClient, res rtsp.RtspResponse, timeRange *rtsp.RangeTime, info *rtsp.RtpInfo) error {
	return nil
}

func (cli *RtspRecordSession) HandlePause(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspRecordSession) HandleTeardown(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspRecordSession) HandleGetParameter(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspRecordSession) HandleSetParameter(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspRecordSession) HandleRedirect(client *rtsp.RtspClient, req rtsp.RtspRequest, location string, timeRange *rtsp.RangeTime) error {
	return nil
}

// HandleRecord runs once the server has accepted the publish. Everything up
// to here was negotiation; from here the session owns the connection and
// pushes until the file runs out.
func (cli *RtspRecordSession) HandleRecord(client *rtsp.RtspClient, res rtsp.RtspResponse, timeRange *rtsp.RangeTime, info *rtsp.RtpInfo) error {
	if res.StatusCode != 200 {
		return fmt.Errorf("record refused: %d %s", res.StatusCode, res.Reason)
	}
	videoTrack, found := client.GetTrack("video")
	if !found {
		return errors.New("the client has no video track to record")
	}

	go func() {
		defer close(cli.finished)
		if err := cli.pushFLV(videoTrack); err != nil {
			cli.mtx.Lock()
			cli.pushErr = err
			cli.mtx.Unlock()
		}
		// let the last interleaved packets reach the server
		time.Sleep(300 * time.Millisecond)
		cli.Destory()
	}()

	// rtcp sender reports let the server line the stream up against a wall
	// clock. Not every server needs them, but one that does will drop a
	// publisher that never sends any.
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				videoTrack.SendReport()
			case <-cli.die:
				return
			}
		}
	}()
	return nil
}

func (cli *RtspRecordSession) HandleRequest(client *rtsp.RtspClient, req rtsp.RtspRequest) error {
	return nil
}

// pushFLV reads the file and hands every h264 frame to the track.
func (cli *RtspRecordSession) pushFLV(track *rtsp.RtspTrack) error {
	f, err := os.Open(cli.flvPath)
	if err != nil {
		return err
	}
	defer f.Close()

	start := time.Now()
	var writeErr error
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if writeErr != nil || cid != codec.CODECID_VIDEO_H264 {
			return
		}
		if cli.paced {
			if wait := time.Duration(dts)*time.Millisecond - time.Since(start); wait > 0 {
				time.Sleep(wait)
			}
		}
		// the rtsp track counts in its own clock, 90kHz for h264
		writeErr = track.WriteSample(rtsp.RtspSample{Sample: frame, Timestamp: pts * 90})
		if writeErr == nil {
			cli.mtx.Lock()
			cli.sent++
			cli.mtx.Unlock()
		}
	}

	buf := make([]byte, 64*1024)
	for {
		select {
		case <-cli.die:
			return nil
		default:
		}
		n, err := f.Read(buf)
		if n > 0 {
			if perr := reader.Input(buf[:n]); perr != nil {
				return perr
			}
		}
		if writeErr != nil {
			return writeErr
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (cli *RtspRecordSession) loopSend() {
	cli.waitSend.Add(1)
	defer cli.waitSend.Done()
	for {
		select {
		case <-cli.die:
			return
		case b := <-cli.sendChanel:
			if _, err := cli.c.Write(b); err != nil {
				cli.mtx.Lock()
				cli.sendError = err
				cli.mtx.Unlock()
				return
			}
		}
	}
}

func (cli *RtspRecordSession) writeError() error {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	return cli.sendError
}

// PushFLV publishes the h264 track of a flv file to an rtsp server and
// returns once the whole file has been sent.
func PushFLV(rtspURL, flvPath string, paced bool) (*RtspRecordSession, error) {
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

	sess := NewRtspRecordSession(c, flvPath, paced)
	defer sess.Destory()

	client, err := rtsp.NewRtspClient(rtspURL, sess, rtsp.WithEnableRecord())
	if err != nil {
		return sess, err
	}
	// the track has to exist before ANNOUNCE: it is what the sdp describes
	client.AddTrack(rtsp.NewVideoTrack(rtsp.RtspCodec{
		Cid: rtsp.RTSP_CODEC_H264, PayloadType: 96, SampleRate: 90000,
	}))
	client.SetOutput(func(b []byte) error {
		if err := sess.writeError(); err != nil {
			return err
		}
		select {
		case sess.sendChanel <- b:
			return nil
		case <-sess.die:
			return errors.New("session closed")
		}
	})

	go sess.loopSend()
	if err := client.Start(); err != nil {
		return sess, err
	}

	buf := make([]byte, 64*1024)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if perr := client.Input(buf[:n]); perr != nil {
				return sess, perr
			}
		}
		if err != nil {
			break
		}
	}
	<-sess.finished
	return sess, sess.Err()
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <rtsp url> <input.flv>\n", os.Args[0])
		os.Exit(2)
	}
	sess, err := PushFLV(os.Args[1], os.Args[2], true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("published %d frames to %s\n", sess.Sent(), os.Args[1])
}
