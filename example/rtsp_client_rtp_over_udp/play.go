package main

import (
	"errors"
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

// udpPair is the two sockets one rtsp track needs when the media does not
// travel over the rtsp connection: rtp on an even port and rtcp on the odd
// one right after it, as rfc3550 requires.
type udpPair struct {
	rtp  *net.UDPConn
	rtcp *net.UDPConn
}

func (p *udpPair) close() {
	if p.rtp != nil {
		p.rtp.Close()
	}
	if p.rtcp != nil {
		p.rtcp.Close()
	}
}

// bindUDPPair binds a consecutive even/odd port pair on localPort and
// connects both sockets to the ports the server named.
func bindUDPPair(localRtp, localRtcp uint16, remoteHost string, remoteRtp, remoteRtcp uint16) (*udpPair, error) {
	ip := net.ParseIP(remoteHost)
	if ip == nil {
		addrs, err := net.LookupIP(remoteHost)
		if err != nil || len(addrs) == 0 {
			return nil, fmt.Errorf("resolve %s: %w", remoteHost, err)
		}
		ip = addrs[0]
	}

	pair := &udpPair{}
	var err error
	pair.rtp, err = net.DialUDP("udp4",
		&net.UDPAddr{IP: net.IPv4zero, Port: int(localRtp)},
		&net.UDPAddr{IP: ip, Port: int(remoteRtp)})
	if err != nil {
		return nil, fmt.Errorf("bind rtp socket: %w", err)
	}
	pair.rtcp, err = net.DialUDP("udp4",
		&net.UDPAddr{IP: net.IPv4zero, Port: int(localRtcp)},
		&net.UDPAddr{IP: ip, Port: int(remoteRtcp)})
	if err != nil {
		pair.close()
		return nil, fmt.Errorf("bind rtcp socket: %w", err)
	}
	// a burst arrives faster than a depacketizer drains it, and udp drops
	// whatever does not fit
	pair.rtp.SetReadBuffer(2 << 20)
	return pair, nil
}

// RtspUdpPlaySession plays an rtsp url with the media on its own udp sockets.
type RtspUdpPlaySession struct {
	outDir  string
	udpPort uint16

	c         net.Conn
	timeout   int
	once      sync.Once
	die       chan struct{}
	lastError error

	mtx          sync.Mutex
	pairs        map[string]*udpPair
	files        map[string]*os.File
	samples      map[string]int
	videoStarted bool
}

func NewRtspUdpPlaySession(c net.Conn, outDir string, firstUDPPort uint16) *RtspUdpPlaySession {
	return &RtspUdpPlaySession{
		outDir:  outDir,
		udpPort: firstUDPPort,
		c:       c,
		die:     make(chan struct{}),
		pairs:   make(map[string]*udpPair),
		files:   make(map[string]*os.File),
		samples: make(map[string]int),
	}
}

// Samples reports how many samples of each kind the session received.
func (cli *RtspUdpPlaySession) Samples() map[string]int {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	out := make(map[string]int, len(cli.samples))
	for k, v := range cli.samples {
		out[k] = v
	}
	return out
}

func (cli *RtspUdpPlaySession) write(kind, name string, data []byte) {
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

func (cli *RtspUdpPlaySession) Destory() {
	cli.once.Do(func() {
		close(cli.die)
		cli.mtx.Lock()
		for _, p := range cli.pairs {
			p.close()
		}
		for _, f := range cli.files {
			f.Close()
		}
		cli.mtx.Unlock()
		cli.c.Close()
	})
}

func (cli *RtspUdpPlaySession) HandleOption(client *rtsp.RtspClient, res rtsp.RtspResponse, public []string) error {
	return nil
}

func (cli *RtspUdpPlaySession) HandleDescribe(client *rtsp.RtspClient, res rtsp.RtspResponse, sdpCtx *sdp.Sdp, tracks map[string]*rtsp.RtspTrack) error {
	if res.StatusCode != 200 {
		return fmt.Errorf("describe failed: %d %s", res.StatusCode, res.Reason)
	}
	for _, t := range tracks {
		if t == nil {
			continue
		}
		// each track gets its own consecutive even/odd pair
		transport := rtsp.NewRtspTransport(
			rtsp.WithEnableUdp(),
			rtsp.WithClientUdpPort(cli.udpPort, cli.udpPort+1),
			rtsp.WithMode(rtsp.MODE_PLAY))
		t.SetTransport(transport)
		t.OpenTrack()
		cli.udpPort += 2

		switch t.Codec.Cid {
		case rtsp.RTSP_CODEC_H264:
			t.OnSample(func(sample rtsp.RtspSample) {
				// A player joins a live stream mid gop. The frames before
				// the next key frame reference pictures that were never
				// received, so writing them produces a file that starts
				// with decode errors.
				if !cli.videoStarted {
					if isTrailingSlice(sample.Sample) {
						return
					}
					cli.videoStarted = true
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

func (cli *RtspUdpPlaySession) HandleSetup(client *rtsp.RtspClient, res rtsp.RtspResponse, track *rtsp.RtspTrack, tracks map[string]*rtsp.RtspTrack, sessionId string, timeout int) error {
	if res.StatusCode == rtsp.Unsupported_Transport {
		return errors.New("the server does not offer rtp over udp")
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("setup failed: %d %s", res.StatusCode, res.Reason)
	}
	cli.timeout = timeout

	host, _, err := net.SplitHostPort(cli.c.RemoteAddr().String())
	if err != nil {
		return err
	}
	transport := track.GetTransport()
	pair, err := bindUDPPair(
		transport.Client_ports[0], transport.Client_ports[1],
		host, transport.Server_ports[0], transport.Server_ports[1])
	if err != nil {
		return err
	}
	cli.mtx.Lock()
	cli.pairs[track.TrackName] = pair
	cli.mtx.Unlock()

	track.OnPacket(func(b []byte, isRtcp bool) (err error) {
		if isRtcp {
			_, err = pair.rtcp.Write(b)
		}
		return
	})

	// Many servers only learn where to send when the client speaks first:
	// a nat between the two rewrites the source port, and the ports named
	// in the transport header are then wrong. A datagram from each socket
	// punches the hole and tells the server the real address.
	pair.rtp.Write(nil)
	pair.rtcp.Write(nil)

	go cli.readLoop(track, pair.rtp, false)
	go cli.readLoop(track, pair.rtcp, true)
	return nil
}

func (cli *RtspUdpPlaySession) readLoop(track *rtsp.RtspTrack, conn *net.UDPConn, isRtcp bool) {
	buf := make([]byte, 2048)
	for {
		select {
		case <-cli.die:
			return
		default:
		}
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		if err := track.Input(buf[:n], isRtcp); err != nil {
			return
		}
	}
}

func (cli *RtspUdpPlaySession) HandleAnnounce(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspUdpPlaySession) HandlePlay(client *rtsp.RtspClient, res rtsp.RtspResponse, timeRange *rtsp.RangeTime, info *rtsp.RtpInfo) error {
	if res.StatusCode != 200 {
		return fmt.Errorf("play failed: %d %s", res.StatusCode, res.Reason)
	}
	// the media is on its own sockets now, so the rtsp connection goes
	// quiet; without keepalives the server times the session out
	interval := cli.timeout / 2
	if interval <= 0 {
		interval = 30
	}
	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				client.KeepAlive(rtsp.OPTIONS)
			case <-cli.die:
				return
			}
		}
	}()
	return nil
}

func (cli *RtspUdpPlaySession) HandlePause(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspUdpPlaySession) HandleTeardown(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspUdpPlaySession) HandleGetParameter(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspUdpPlaySession) HandleSetParameter(client *rtsp.RtspClient, res rtsp.RtspResponse) error {
	return nil
}

func (cli *RtspUdpPlaySession) HandleRedirect(client *rtsp.RtspClient, req rtsp.RtspRequest, location string, timeRange *rtsp.RangeTime) error {
	return nil
}

func (cli *RtspUdpPlaySession) HandleRecord(client *rtsp.RtspClient, res rtsp.RtspResponse, timeRange *rtsp.RangeTime, info *rtsp.RtpInfo) error {
	return nil
}

func (cli *RtspUdpPlaySession) HandleRequest(client *rtsp.RtspClient, req rtsp.RtspRequest) error {
	return nil
}

func (cli *RtspUdpPlaySession) sendInLoop(sendChan chan []byte) {
	for {
		select {
		case b := <-sendChan:
			if _, err := cli.c.Write(b); err != nil {
				cli.lastError = err
				cli.Destory()
				return
			}
		case <-cli.die:
			return
		}
	}
}

// isTrailingSlice reports whether the sample is a non key frame slice, the
// kind that can only be decoded with pictures a late joiner never saw.
func isTrailingSlice(sample []byte) bool {
	naluType := codec.H264NaluType(sample)
	return naluType >= codec.H264_NAL_P_SLICE && naluType <= codec.H264_NAL_SLICE_C
}

// PlayRTSPOverUDP plays an rtsp url with the media on its own udp sockets and
// writes the elementary streams into outDir. It stops after limit, or when
// the server closes the rtsp connection.
//
// firstUDPPort is the first local port to bind; each track takes the next
// even/odd pair after it.
func PlayRTSPOverUDP(rtspURL, outDir string, firstUDPPort uint16, limit time.Duration) (*RtspUdpPlaySession, error) {
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

	sess := NewRtspUdpPlaySession(c, outDir, firstUDPPort)
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
		select {
		case sendChan <- b:
			return nil
		case <-sess.die:
			return errors.New("session closed")
		}
	})

	if limit > 0 {
		// the read loop blocks on the socket, so the limit is enforced by
		// closing everything under it
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
	sess, err := PlayRTSPOverUDP(os.Args[1], outDir, 30000, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("received samples:", sess.Samples())
}
