package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
	"github.com/yapingcat/gomedia/go-mpeg2"
	"github.com/yapingcat/gomedia/go-rtsp"
)

// maxRtpPayload keeps an rtp packet inside a typical 1500 byte mtu once the
// ip, udp and rtp headers are counted. An mpeg-ts payload is a whole number
// of 188 byte packets, so 7 of them, 1316 bytes, is the usual choice.
const maxRtpPayload = 7 * 188

// udpSender is the pair of sockets one track needs when the client asked for
// the media on its own ports.
type udpSender struct {
	rtp  *net.UDPConn
	rtcp *net.UDPConn
}

func (s *udpSender) close() {
	if s.rtp != nil {
		s.rtp.Close()
	}
	if s.rtcp != nil {
		s.rtcp.Close()
	}
}

// Session serves one rtsp client.
//
// The media is mpeg-ts carried in rtp, payload type 33 as rfc1890 assigns it.
// Wrapping a transport stream rather than packetising the elementary streams
// keeps the server simple, because the ts already carries the timing, and it
// is what most ip cameras and gb28181 devices do.
type Session struct {
	dir string
	c   net.Conn

	die       sync.Once
	completed chan struct{}

	mtx     sync.Mutex
	track   *rtsp.RtspTrack
	sender  *udpSender
	udpPort int
	sent    int
	lastErr error
}

func NewSession(conn net.Conn, dir string, firstUDPPort int) *Session {
	return &Session{
		dir:       dir,
		c:         conn,
		completed: make(chan struct{}),
		udpPort:   firstUDPPort,
	}
}

// Sent reports how many rtp payloads the session has written.
func (s *Session) Sent() int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.sent
}

// Serve reads the connection until the client goes away.
func (s *Session) Serve() {
	svr, err := rtsp.NewRtspServer(s)
	if err != nil {
		return
	}
	svr.SetOutput(func(b []byte) error {
		_, err := s.c.Write(b)
		return err
	})
	defer s.Stop()

	buf := make([]byte, 65535)
	for {
		n, err := s.c.Read(buf)
		if n > 0 {
			if perr := svr.Input(buf[:n]); perr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) Stop() {
	s.die.Do(func() {
		close(s.completed)
		s.mtx.Lock()
		if s.sender != nil {
			s.sender.close()
		}
		s.mtx.Unlock()
		s.c.Close()
	})
}

func (s *Session) HandleOption(svr *rtsp.RtspServer, req rtsp.RtspRequest, res *rtsp.RtspResponse) {
}

func (s *Session) HandleDescribe(svr *rtsp.RtspServer, req rtsp.RtspRequest, res *rtsp.RtspResponse) {
	if _, err := os.Stat(s.sourcePath(req.Uri)); err != nil {
		res.StatusCode = rtsp.Not_Found
		return
	}
	// rfc1890 assigns payload type 33 to mpeg-ts, and its clock is 90kHz
	track := rtsp.NewVideoTrack(rtsp.RtspCodec{
		Cid: rtsp.RTSP_CODEC_TS, PayloadType: 33, SampleRate: 90000,
	})
	if err := svr.AddTrack(track); err != nil {
		res.StatusCode = rtsp.Internal_Server_Error
		return
	}
	s.mtx.Lock()
	s.track = track
	s.mtx.Unlock()
}

func (s *Session) HandleSetup(svr *rtsp.RtspServer, req rtsp.RtspRequest, res *rtsp.RtspResponse, transport *rtsp.RtspTransport, track *rtsp.RtspTrack) {
	track.OpenTrack()
	if transport.Proto == rtsp.TCP {
		// the library interleaves the rtp over the rtsp connection itself
		return
	}

	// udp: bind our own pair and send to the ports the client named
	host, _, err := net.SplitHostPort(s.c.RemoteAddr().String())
	if err != nil {
		res.StatusCode = rtsp.Internal_Server_Error
		return
	}
	sender, err := bindSender(s.udpPort, host, transport.Client_ports[0], transport.Client_ports[1])
	if err != nil {
		res.StatusCode = rtsp.Internal_Server_Error
		return
	}
	transport.SetServerUdpPort(uint16(s.udpPort), uint16(s.udpPort+1))
	s.udpPort += 2

	s.mtx.Lock()
	s.sender = sender
	s.mtx.Unlock()

	track.OnPacket(func(b []byte, isRtcp bool) (err error) {
		if isRtcp {
			_, err = sender.rtcp.Write(b)
		} else {
			_, err = sender.rtp.Write(b)
		}
		return
	})
	go s.readRtcp(track, sender.rtcp)
}

func bindSender(localPort int, remoteHost string, remoteRtp, remoteRtcp uint16) (*udpSender, error) {
	ip := net.ParseIP(remoteHost)
	if ip == nil {
		return nil, fmt.Errorf("cannot send to %q", remoteHost)
	}
	sender := &udpSender{}
	var err error
	sender.rtp, err = net.DialUDP("udp4",
		&net.UDPAddr{IP: net.IPv4zero, Port: localPort},
		&net.UDPAddr{IP: ip, Port: int(remoteRtp)})
	if err != nil {
		return nil, err
	}
	sender.rtcp, err = net.DialUDP("udp4",
		&net.UDPAddr{IP: net.IPv4zero, Port: localPort + 1},
		&net.UDPAddr{IP: ip, Port: int(remoteRtcp)})
	if err != nil {
		sender.close()
		return nil, err
	}
	return sender, nil
}

func (s *Session) readRtcp(track *rtsp.RtspTrack, conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		select {
		case <-s.completed:
			return
		default:
		}
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		if n > 0 {
			track.Input(buf[:n], true)
		}
	}
}

func (s *Session) HandlePlay(svr *rtsp.RtspServer, req rtsp.RtspRequest, res *rtsp.RtspResponse, timeRange *rtsp.RangeTime, info []*rtsp.RtpInfo) {
	s.mtx.Lock()
	track := s.track
	s.mtx.Unlock()
	if track == nil {
		res.StatusCode = rtsp.Internal_Server_Error
		return
	}

	go func() {
		if err := s.stream(track, s.sourcePath(req.Uri)); err != nil {
			s.mtx.Lock()
			s.lastErr = err
			s.mtx.Unlock()
		}
		s.Stop()
	}()

	// rtcp sender reports let the client line the stream up against a wall
	// clock, and prove to it that the session is still alive
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := track.SendReport(); err != nil {
					return
				}
			case <-s.completed:
				return
			}
		}
	}()
}

// stream reads the flv, remuxes it into mpeg-ts and pushes the ts out as rtp.
func (s *Session) stream(track *rtsp.RtspTrack, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()

	// rtp payloads are filled to just under the mtu, and a new frame always
	// starts a new payload so a lost packet costs one picture rather than
	// smearing across two
	payload := make([]byte, 0, maxRtpPayload)
	var timestamp uint32
	var sendErr error

	flush := func() {
		if len(payload) == 0 || sendErr != nil {
			return
		}
		sendErr = track.WriteSample(rtsp.RtspSample{Sample: payload, Timestamp: timestamp * 90})
		if sendErr == nil {
			s.mtx.Lock()
			s.sent++
			s.mtx.Unlock()
		}
		payload = payload[:0]
	}

	muxer := mpeg2.NewTSMuxer()
	muxer.OnPacket = func(pkg []byte) {
		if len(payload)+len(pkg) > maxRtpPayload {
			flush()
		}
		payload = append(payload, pkg...)
	}

	streams := make(map[codec.CodecID]uint16)
	start := time.Now()
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if sendErr != nil {
			return
		}
		tsType, ok := tsStreamType(cid)
		if !ok {
			return
		}
		// pace the stream: this is a live server, and a client given six
		// seconds of media in one burst has nowhere to put it
		if wait := time.Duration(dts)*time.Millisecond - time.Since(start); wait > 0 {
			select {
			case <-time.After(wait):
			case <-s.completed:
				return
			}
		}
		pid, known := streams[cid]
		if !known {
			pid = muxer.AddStream(tsType)
			streams[cid] = pid
		}
		// a payload never spans two frames
		flush()
		timestamp = dts
		if err := muxer.Write(pid, frame, uint64(pts), uint64(dts)); err != nil {
			sendErr = err
		}
	}

	buf := make([]byte, 64*1024)
	for {
		select {
		case <-s.completed:
			return nil
		default:
		}
		n, err := src.Read(buf)
		if n > 0 {
			if perr := reader.Input(buf[:n]); perr != nil {
				return perr
			}
		}
		if sendErr != nil {
			return sendErr
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	flush()
	return sendErr
}

func tsStreamType(cid codec.CodecID) (mpeg2.TS_STREAM_TYPE, bool) {
	switch cid {
	case codec.CODECID_VIDEO_H264:
		return mpeg2.TS_STREAM_H264, true
	case codec.CODECID_VIDEO_H265:
		return mpeg2.TS_STREAM_H265, true
	case codec.CODECID_AUDIO_AAC:
		return mpeg2.TS_STREAM_AAC, true
	case codec.CODECID_AUDIO_MP3:
		return mpeg2.TS_STREAM_AUDIO_MPEG1, true
	default:
		return 0, false
	}
}

// sourcePath maps a request uri onto a file, keeping the request inside dir.
func (s *Session) sourcePath(uri string) string {
	name := uri[strings.LastIndex(uri, "/")+1:]
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	return filepath.Join(s.dir, filepath.Base(name)+".flv")
}

func (s *Session) HandleAnnounce(svr *rtsp.RtspServer, req rtsp.RtspRequest, tracks map[string]*rtsp.RtspTrack) {
}
func (s *Session) HandlePause(svr *rtsp.RtspServer, req rtsp.RtspRequest, res *rtsp.RtspResponse) {}
func (s *Session) HandleTeardown(svr *rtsp.RtspServer, req rtsp.RtspRequest, res *rtsp.RtspResponse) {
}
func (s *Session) HandleGetParameter(svr *rtsp.RtspServer, req rtsp.RtspRequest, res *rtsp.RtspResponse) {
}
func (s *Session) HandleSetParameter(svr *rtsp.RtspServer, req rtsp.RtspRequest, res *rtsp.RtspResponse) {
}
func (s *Session) HandleRecord(svr *rtsp.RtspServer, req rtsp.RtspRequest, res *rtsp.RtspResponse, timeRange *rtsp.RangeTime, info []*rtsp.RtpInfo) {
}
func (s *Session) HandleResponse(svr *rtsp.RtspServer, res rtsp.RtspResponse) {}

// Server accepts rtsp clients and serves the flv files in Dir.
type Server struct {
	Dir string
	// FirstUDPPort is the first local port a udp session binds. Each track
	// takes the next even/odd pair after it.
	FirstUDPPort int

	ln net.Listener
}

// Listen starts accepting on addr and returns the address it bound.
func (s *Server) Listen(addr string) (string, error) {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return "", err
	}
	s.ln = ln
	go s.serve()
	return ln.Addr().String(), nil
}

func (s *Server) serve() {
	port := s.FirstUDPPort
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		sess := NewSession(conn, s.Dir, port)
		// each session gets its own block of ports so two clients cannot
		// try to bind the same one
		port += 16
		go sess.Serve()
	}
}

func (s *Server) Close() error {
	if s.ln == nil {
		return nil
	}
	return s.ln.Close()
}

var (
	addr    = flag.String("addr", ":8554", "listen address")
	dir     = flag.String("dir", ".", "directory the flv files are served from")
	udpPort = flag.Int("udp-port", 20000, "first local udp port to bind for rtp")
)

// rtsp://127.0.0.1:8554/<name> serves <name>.flv from -dir
func main() {
	flag.Parse()
	server := &Server{Dir: *dir, FirstUDPPort: *udpPort}
	bound, err := server.Listen(*addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("rtsp server listening on", bound)
	select {}
}
