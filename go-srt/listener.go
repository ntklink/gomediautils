package srt

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// ConnRequest describes a caller asking to connect, for the Authorize
// callback of a listener.
type ConnRequest struct {
	StreamID   string
	RemoteAddr net.Addr
	// Passphrase starts out as the listener's; Authorize may set another
	// one per stream, or clear it.
	Passphrase string
}

// ListenConfig configures a Listener.
type ListenConfig struct {
	Config
	// Authorize decides whether to take a connection, typically by its
	// stream id. Returning an error rejects it; a *RejectError chooses the
	// reason the caller is given.
	Authorize func(req *ConnRequest) error
	// Backlog is how many accepted connections may wait for Accept, 16 by
	// default.
	Backlog int
}

// Listener accepts SRT connections on one UDP port.
type Listener struct {
	udp    *net.UDPConn
	cfg    ListenConfig
	secret []byte
	start  time.Time

	mu       sync.Mutex
	conns    map[uint32]*Conn
	byCaller map[string]*Conn
	accept   chan *Conn
	closed   bool
	done     chan struct{}
}

// Listen opens a listener on address, e.g. ":9000".
func Listen(address string, cfg ListenConfig) (*Listener, error) {
	var err error
	if cfg.Config, err = cfg.Config.withDefaults(); err != nil {
		return nil, err
	}
	if cfg.Backlog <= 0 {
		cfg.Backlog = 16
	}
	laddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	udp, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, err
	}
	l := &Listener{
		udp:      udp,
		cfg:      cfg,
		secret:   make([]byte, 32),
		start:    time.Now(),
		conns:    make(map[uint32]*Conn),
		byCaller: make(map[string]*Conn),
		accept:   make(chan *Conn, cfg.Backlog),
		done:     make(chan struct{}),
	}
	rand.Read(l.secret)
	go l.readLoop()
	return l, nil
}

// Addr is the local address the listener is bound to.
func (l *Listener) Addr() net.Addr {
	return l.udp.LocalAddr()
}

// Accept waits for the next connection.
func (l *Listener) Accept() (*Conn, error) {
	select {
	case c := <-l.accept:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close stops listening and closes every connection it accepted.
func (l *Listener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	close(l.done)
	conns := make([]*Conn, 0, len(l.conns))
	for _, c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
	return l.udp.Close()
}

func (l *Listener) readLoop() {
	buf := make([]byte, 2048)
	for {
		n, addr, err := l.udp.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		p, err := parsePacket(append([]byte(nil), buf[:n]...))
		if err != nil {
			continue
		}
		if p.dstSocket == 0 {
			if p.control && p.ctrlType == ctrlHandshake {
				l.handshake(p, addr)
			}
			continue
		}
		l.mu.Lock()
		c := l.conns[p.dstSocket]
		l.mu.Unlock()
		if c != nil && sameAddr(c.remote, addr) {
			c.handlePacket(p)
		}
	}
}

func sameAddr(a net.Addr, b *net.UDPAddr) bool {
	ua, ok := a.(*net.UDPAddr)
	return ok && ua.Port == b.Port && ua.IP.Equal(b.IP)
}

// cookie is a stateless SYN cookie: a caller has to echo it, proving it
// receives at the address it claims, before the listener keeps any state.
func (l *Listener) cookie(addr *net.UDPAddr, minute int64) uint32 {
	mac := hmac.New(sha256.New, l.secret)
	fmt.Fprintf(mac, "%s|%d", addr, minute)
	return binary.BigEndian.Uint32(mac.Sum(nil))
}

func (l *Listener) ts() uint32 {
	return uint32(time.Since(l.start) / time.Microsecond)
}

func (l *Listener) reply(addr *net.UDPAddr, dst uint32, ts uint32, hs *handshake) []byte {
	p := packet{control: true, ctrlType: ctrlHandshake, timestamp: ts, dstSocket: dst, payload: hs.marshal()}
	raw := p.marshal()
	l.udp.WriteToUDP(raw, addr)
	return raw
}

func (l *Listener) reject(addr *net.UDPAddr, req *handshake, reason int) {
	hs := *req
	hs.hsReq, hs.kmReq, hs.streamID = nil, nil, ""
	hs.hsType = uint32(hsRejectBase + reason)
	l.reply(addr, req.socketID, l.ts(), &hs)
}

func (l *Listener) handshake(p *packet, addr *net.UDPAddr) {
	req, err := parseHandshake(p.payload)
	if err != nil {
		return
	}
	minute := time.Now().Unix() / 60
	switch req.hsType {
	case hsInduction:
		resp := &handshake{
			version:   5,
			extension: hsMagic,
			isn:       req.isn,
			mtu:       req.mtu,
			window:    req.window,
			hsType:    hsInduction,
			socketID:  newSocketID(),
			cookie:    l.cookie(addr, minute),
			peerIP:    peerIPField(addr),
		}
		// no key length is advertised: the listener takes whatever length
		// the caller's key material comes with, and libsrt callers warn
		// about an advertised one that differs from theirs
		l.reply(addr, req.socketID, l.ts(), resp)
	case hsConclusion:
		if req.cookie != l.cookie(addr, minute) && req.cookie != l.cookie(addr, minute-1) {
			return
		}
		l.conclusion(p, req, addr)
	}
}

func (l *Listener) conclusion(p *packet, req *handshake, addr *net.UDPAddr) {
	key := fmt.Sprintf("%s/%d", addr, req.socketID)
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	if c := l.byCaller[key]; c != nil {
		// the caller did not get our answer; send the same one again
		l.mu.Unlock()
		l.udp.WriteToUDP(c.response, addr)
		return
	}
	l.mu.Unlock()

	if req.version != 5 || req.hsReq == nil {
		l.reject(addr, req, RejectVersion)
		return
	}
	if req.hsReq.flags&flagStream != 0 {
		// file (stream) mode is not implemented, only live mode
		l.reject(addr, req, RejectMessageAPI)
		return
	}
	cr := &ConnRequest{StreamID: req.streamID, RemoteAddr: addr, Passphrase: l.cfg.Passphrase}
	if l.cfg.Authorize != nil {
		if err := l.cfg.Authorize(cr); err != nil {
			reason := RejectPeer
			var rej *RejectError
			if errors.As(err, &rej) {
				reason = rej.Reason
			}
			l.reject(addr, req, reason)
			return
		}
	}
	var crypto *cryptoCtx
	switch {
	case cr.Passphrase != "" && req.kmReq == nil, cr.Passphrase == "" && req.kmReq != nil:
		l.reject(addr, req, RejectUnsecure)
		return
	case cr.Passphrase != "":
		crypto = &cryptoCtx{passphrase: cr.Passphrase}
		if err := crypto.applyKM(req.kmReq); err != nil {
			l.reject(addr, req, RejectBadSecret)
			return
		}
	}

	latency := uint16(l.cfg.Latency / time.Millisecond)
	rcvLatency := max(latency, req.hsReq.sendDelay)
	sndLatency := max(latency, req.hsReq.recvDelay)
	flags := uint32(flagTSBPDSnd | flagTSBPDRcv | flagTLPktDrop | flagPeriodicNAK | flagRexmit)
	ext := uint16(1)
	if crypto != nil {
		flags |= flagCrypt
		ext |= 2
	}
	resp := &handshake{
		version:   5,
		extension: ext,
		isn:       req.isn,
		mtu:       req.mtu,
		window:    req.window,
		hsType:    hsConclusion,
		socketID:  newSocketID(),
		peerIP:    peerIPField(addr),
		hsRsp:     &hsReq{version: srtVersion, flags: flags, recvDelay: rcvLatency, sendDelay: sndLatency},
		kmRsp:     req.kmReq,
	}
	if crypto != nil {
		resp.encryption = uint16(crypto.keyLen / 8)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	if len(l.accept) == cap(l.accept) {
		// nobody is accepting fast enough
		l.reject(addr, req, RejectResource)
		return
	}
	for l.conns[resp.socketID] != nil {
		resp.socketID = newSocketID()
	}
	start := time.Now()
	params := connParams{
		socketID:      resp.socketID,
		peerSocketID:  req.socketID,
		isn:           req.isn,
		peerTimestamp: p.timestamp,
		rcvLatency:    time.Duration(rcvLatency) * time.Millisecond,
		sndLatency:    time.Duration(sndLatency) * time.Millisecond,
		crypto:        crypto,
		streamID:      req.streamID,
		local:         l.udp.LocalAddr(),
		remote:        addr,
	}
	send := func(b []byte) error {
		_, err := l.udp.WriteToUDP(b, addr)
		return err
	}
	var c *Conn
	c = newConn(l.cfg.Config, params, send, func() {
		l.mu.Lock()
		delete(l.conns, c.socketID)
		delete(l.byCaller, key)
		l.mu.Unlock()
	}, start)
	// registered and answered before the application sees it, so the
	// caller's first packets find the connection and its first writes go
	// to a caller that knows it is connected; the read loop is the only
	// sender on l.accept, so the room checked above is still there
	l.conns[c.socketID] = c
	l.byCaller[key] = c
	c.response = l.reply(addr, req.socketID, 0, resp)
	l.accept <- c
}
