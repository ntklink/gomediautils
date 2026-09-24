package srt

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestPacketRoundTrip(t *testing.T) {
	data := &packet{seq: 0x7FFFFFFE, position: positionSolo, keyFlags: 2, retransmit: true,
		msgNo: 0x03FFFFFF, timestamp: 123456, dstSocket: 0x1234, payload: []byte("payload")}
	got, err := parsePacket(data.marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got.control || got.seq != data.seq || got.position != positionSolo || got.keyFlags != 2 ||
		!got.retransmit || got.msgNo != data.msgNo || got.timestamp != 123456 || got.dstSocket != 0x1234 ||
		string(got.payload) != "payload" {
		t.Fatalf("data packet %+v", got)
	}
	raw := (&packet{seq: 5, position: positionSolo, msgNo: 1}).marshal()
	markRetransmitted(raw)
	if p, _ := parsePacket(raw); !p.retransmit || p.msgNo != 1 || p.seq != 5 {
		t.Fatalf("R flag: %+v", p)
	}

	ctrl := &packet{control: true, ctrlType: ctrlUserDefine, subtype: extKMReq, typeInfo: 99, payload: []byte{1, 2, 3, 4}}
	got, _ = parsePacket(ctrl.marshal())
	if !got.control || got.ctrlType != ctrlUserDefine || got.subtype != extKMReq || got.typeInfo != 99 {
		t.Fatalf("control packet %+v", got)
	}
}

func TestSequenceArithmetic(t *testing.T) {
	if seqNext(seqMask) != 0 || seqAdd(0, -1) != seqMask {
		t.Fatal("wrap")
	}
	if !seqLess(seqMask, 0) || seqLess(0, seqMask) || seqDiff(seqMask-1, 2) != 4 {
		t.Fatal("comparison across the wrap")
	}
}

func TestLossList(t *testing.T) {
	seqs := []uint32{seqMask - 1, seqMask, 0, 1, 5, 9, 10}
	enc := encodeLossList(seqs)
	// a range across the wrap, a single number and a two number range
	if len(enc) != 5*4 {
		t.Fatalf("%x", enc)
	}
	var got []uint32
	decodeLossList(enc, func(s uint32) { got = append(got, s) })
	if fmt.Sprint(got) != fmt.Sprint(seqs) {
		t.Fatalf("%v", got)
	}
}

func TestStreamIDWordOrder(t *testing.T) {
	// libsrt sends each 32 bit word of the stream id byte reversed
	packed := packString("#!::r=abc")
	if hex.EncodeToString(packed) != "3a3a212362613d7200000063" {
		t.Fatalf("%x", packed)
	}
	if unpackString(packed) != "#!::r=abc" {
		t.Fatal(unpackString(packed))
	}
}

func TestHandshakeRoundTrip(t *testing.T) {
	hs := &handshake{version: 5, extension: 5, isn: 42, mtu: 1500, window: 8192, hsType: hsConclusion,
		socketID: 7, cookie: 9, hsReq: &hsReq{version: srtVersion, flags: 0x3f, recvDelay: 200, sendDelay: 120},
		kmReq: bytes.Repeat([]byte{1}, 48), streamID: "live/stream"}
	got, err := parseHandshake(hs.marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got.isn != 42 || got.hsType != hsConclusion || got.cookie != 9 || got.streamID != "live/stream" ||
		*got.hsReq != *hs.hsReq || !bytes.Equal(got.kmReq, hs.kmReq) {
		t.Fatalf("%+v", got)
	}
	if _, err := parseHandshake(append(hs.marshal()[:hsCIFSize], 0, 5, 0, 200)); err == nil {
		t.Fatal("extension longer than the packet accepted")
	}
}

// RFC 3394 section 4.1: 128 bit key data wrapped with a 128 bit KEK.
func TestKeyWrapVector(t *testing.T) {
	kek, _ := hex.DecodeString("000102030405060708090A0B0C0D0E0F")
	key, _ := hex.DecodeString("00112233445566778899AABBCCDDEEFF")
	wrapped, err := keyWrap(kek, key)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(wrapped) != "1fa68b0a8112b447aef34bd8fb5a7b829d3e862371d2cfe5" {
		t.Fatalf("%x", wrapped)
	}
	back, err := keyUnwrap(kek, wrapped)
	if err != nil || !bytes.Equal(back, key) {
		t.Fatal(err)
	}
	wrapped[3] ^= 1
	if _, err := keyUnwrap(kek, wrapped); !errors.Is(err, errBadSecret) {
		t.Fatalf("tampered key unwrapped: %v", err)
	}
}

func TestKeyMaterialExchange(t *testing.T) {
	for _, keyLen := range []int{16, 24, 32} {
		caller, err := newCryptoCtx("correct horse battery", keyLen)
		if err != nil {
			t.Fatal(err)
		}
		km, _ := caller.marshalKM()
		listener := &cryptoCtx{passphrase: "correct horse battery"}
		if err := listener.applyKM(km); err != nil {
			t.Fatal(err)
		}
		msg := []byte("the same payload, encrypted and decrypted")
		enc := append([]byte(nil), msg...)
		caller.xorPayload(1, 77, enc)
		if bytes.Equal(enc, msg) {
			t.Fatal("payload not encrypted")
		}
		listener.xorPayload(1, 77, enc)
		if !bytes.Equal(enc, msg) {
			t.Fatalf("key length %d: decrypted %q", keyLen, enc)
		}
		wrong := &cryptoCtx{passphrase: "wrong passphrase!"}
		if err := wrong.applyKM(km); !errors.Is(err, errBadSecret) {
			t.Fatalf("wrong passphrase: %v", err)
		}
	}
}

// lossyProxy relays UDP between a client and a server, dropping and
// delaying packets in both directions.
type lossyProxy struct {
	conn   *net.UDPConn
	server *net.UDPAddr
	loss   float64
	mu     sync.Mutex
	client *net.UDPAddr
	toSrv  *net.UDPConn
	closed chan struct{}
}

func newLossyProxy(t *testing.T, server string, loss float64) *lossyProxy {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	saddr, _ := net.ResolveUDPAddr("udp", server)
	toSrv, err := net.DialUDP("udp", nil, saddr)
	if err != nil {
		t.Fatal(err)
	}
	p := &lossyProxy{conn: conn, server: saddr, loss: loss, toSrv: toSrv, closed: make(chan struct{})}
	go p.pump(conn, func(b []byte, from *net.UDPAddr) {
		p.mu.Lock()
		p.client = from
		p.mu.Unlock()
		toSrv.Write(b)
	})
	go p.pump(toSrv, func(b []byte, _ *net.UDPAddr) {
		p.mu.Lock()
		client := p.client
		p.mu.Unlock()
		if client != nil {
			conn.WriteToUDP(b, client)
		}
	})
	t.Cleanup(func() { close(p.closed); conn.Close(); toSrv.Close() })
	return p
}

func (p *lossyProxy) Addr() string { return p.conn.LocalAddr().String() }

func (p *lossyProxy) pump(c *net.UDPConn, forward func([]byte, *net.UDPAddr)) {
	buf := make([]byte, 2048)
	for {
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			return
		}
		b := append([]byte(nil), buf[:n]...)
		ctrl := b[0]&0x80 != 0
		// handshakes get through so the test is about recovery, not setup
		if !(ctrl && b[1] == 0 && b[0] == 0x80) && rand.Float64() < p.loss {
			continue
		}
		// some jitter, which also reorders
		delay := time.Duration(rand.IntN(3)) * time.Millisecond
		time.AfterFunc(delay, func() {
			select {
			case <-p.closed:
			default:
				forward(b, from)
			}
		})
	}
}

// pair connects a caller to a listener, optionally through a proxy.
func pair(t *testing.T, lcfg ListenConfig, ccfg Config, loss float64) (caller, accepted *Conn) {
	t.Helper()
	l, err := Listen("127.0.0.1:0", lcfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	addr := l.Addr().String()
	if loss > 0 {
		addr = newLossyProxy(t, addr, loss).Addr()
	}
	type result struct {
		c   *Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := l.Accept()
		ch <- result{c, err}
	}()
	caller, err = Dial(addr, ccfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { caller.Close() })
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}
		accepted = r.c
	case <-time.After(3 * time.Second):
		t.Fatal("nothing accepted")
	}
	return caller, accepted
}

// send writes n numbered messages paced like a live stream and checks the
// other side reads every one, in order.
func sendAndCheck(t *testing.T, from, to *Conn, n int) {
	t.Helper()
	go func() {
		for i := 0; i < n; i++ {
			msg := make([]byte, 1316)
			binaryPut(msg, i)
			from.Write(msg)
			if i%20 == 19 {
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()
	buf := make([]byte, 1500)
	to.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < n; i++ {
		k, err := to.Read(buf)
		if err != nil {
			t.Fatalf("message %d: %v (stats %+v)", i, err, to.Stats())
		}
		if k != 1316 || binaryGet(buf) != i {
			t.Fatalf("message %d: got #%d of %d bytes (stats %+v)", i, binaryGet(buf), k, to.Stats())
		}
	}
}

func binaryPut(b []byte, i int) { copy(b, fmt.Sprintf("%08d", i)) }
func binaryGet(b []byte) int {
	var i int
	fmt.Sscanf(string(b[:8]), "%08d", &i)
	return i
}

func TestLoopback(t *testing.T) {
	var gotStreamID string
	caller, accepted := pair(t, ListenConfig{Authorize: func(r *ConnRequest) error {
		gotStreamID = r.StreamID
		return nil
	}}, Config{StreamID: "#!::r=live/test,m=publish"}, 0)
	if gotStreamID != "#!::r=live/test,m=publish" || accepted.StreamID() != gotStreamID {
		t.Fatalf("stream id %q", gotStreamID)
	}
	sendAndCheck(t, caller, accepted, 2000)
	sendAndCheck(t, accepted, caller, 500)
}

// With 10% of the packets lost each way, retransmission still gets every
// message through inside the latency.
func TestLossRecovery(t *testing.T) {
	caller, accepted := pair(t, ListenConfig{Config: Config{Latency: 400 * time.Millisecond}}, Config{}, 0.10)
	sendAndCheck(t, caller, accepted, 3000)
	s := accepted.Stats()
	if s.PacketsLost == 0 || caller.Stats().PacketsRetransmitted == 0 {
		t.Fatalf("no loss seen through a lossy link: %+v", s)
	}
	if s.PacketsDropped != 0 {
		t.Fatalf("%d packets given up", s.PacketsDropped)
	}
}

func TestEncryption(t *testing.T) {
	for _, keyLen := range []int{16, 32} {
		lc := ListenConfig{Config: Config{Passphrase: "0123456789abcdef"}}
		caller, accepted := pair(t, lc, Config{Passphrase: "0123456789abcdef", PBKeyLen: keyLen}, 0.05)
		sendAndCheck(t, caller, accepted, 1000)
		sendAndCheck(t, accepted, caller, 300)
	}
}

func TestRejections(t *testing.T) {
	cases := map[string]struct {
		lcfg   ListenConfig
		ccfg   Config
		reason int
	}{
		"wrong passphrase": {ListenConfig{Config: Config{Passphrase: "0123456789abcdef"}},
			Config{Passphrase: "fedcba9876543210"}, RejectBadSecret},
		"caller without passphrase": {ListenConfig{Config: Config{Passphrase: "0123456789abcdef"}},
			Config{}, RejectUnsecure},
		"listener without passphrase": {ListenConfig{}, Config{Passphrase: "0123456789abcdef"}, RejectUnsecure},
		"authorize says no": {ListenConfig{Authorize: func(r *ConnRequest) error {
			if r.StreamID != "allowed" {
				return &RejectError{Reason: RejectPeer}
			}
			return nil
		}}, Config{StreamID: "other"}, RejectPeer},
	}
	for name, tc := range cases {
		l, err := Listen("127.0.0.1:0", tc.lcfg)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Dial(l.Addr().String(), tc.ccfg)
		var rej *RejectError
		if !errors.As(err, &rej) || rej.Reason != tc.reason {
			t.Errorf("%s: %v, want reason %d", name, err, tc.reason)
		}
		l.Close()
	}
}

func TestPeerCloseEndsRead(t *testing.T) {
	caller, accepted := pair(t, ListenConfig{}, Config{}, 0)
	caller.Write([]byte("last words"))
	time.Sleep(300 * time.Millisecond)
	caller.Close()
	buf := make([]byte, 1500)
	accepted.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := accepted.Read(buf)
	if err != nil || string(buf[:n]) != "last words" {
		t.Fatalf("%q %v", buf[:n], err)
	}
	if _, err := accepted.Read(buf); err != io.EOF {
		t.Fatalf("after shutdown: %v", err)
	}
}

func TestReadInPieces(t *testing.T) {
	caller, accepted := pair(t, ListenConfig{}, Config{}, 0)
	caller.Write([]byte("0123456789"))
	caller.Write([]byte("abc"))
	accepted.SetReadDeadline(time.Now().Add(2 * time.Second))
	var got []string
	buf := make([]byte, 4)
	for len(got) < 4 {
		n, err := accepted.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(buf[:n]))
	}
	// the first message in pieces, never mixed with the second
	if fmt.Sprint(got) != "[0123 4567 89 abc]" {
		t.Fatalf("%q", got)
	}
}

func TestParseStreamID(t *testing.T) {
	info := ParseStreamID("#!::r=live/cam1,m=publish,u=alice,x=1")
	if info.Resource != "live/cam1" || info.Mode != "publish" || info.User != "alice" || info.Params["x"] != "1" {
		t.Fatalf("%+v", info)
	}
	if info.Type != "stream" {
		t.Fatalf("type %q, want the default stream", info.Type)
	}
	if info := ParseStreamID("live/cam2"); info.Resource != "live/cam2" || info.Mode != "request" || info.Type != "stream" {
		t.Fatalf("%+v", info)
	}
}

// testConn is a connection whose packets go to a slice instead of a socket.
func testConn(t *testing.T, crypto *cryptoCtx) (*Conn, func() [][]byte) {
	t.Helper()
	return testConnWith(t, Config{PeerIdleTimeout: time.Minute}, crypto)
}

// testConnWith is testConn with its own configuration.
func testConnWith(t *testing.T, cfg Config, crypto *cryptoCtx) (*Conn, func() [][]byte) {
	t.Helper()
	cfg, err := cfg.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sent [][]byte
	c := newConn(cfg, connParams{
		socketID: 1, peerSocketID: 2, isn: 100, crypto: crypto,
		rcvLatency: 120 * time.Millisecond, sndLatency: 120 * time.Millisecond,
	}, func(b []byte) error {
		mu.Lock()
		sent = append(sent, append([]byte(nil), b...))
		mu.Unlock()
		return nil
	}, nil, time.Now())
	t.Cleanup(func() {
		c.mu.Lock()
		c.closeLocked(nil)
		c.mu.Unlock()
	})
	return c, func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), sent...)
	}
}

// An encrypted connection delivers only packets encrypted with its keys,
// and a clear one only clear packets: nothing injected in the other form
// reaches the application.
func TestCryptoModeMustMatch(t *testing.T) {
	ctx, _ := newCryptoCtx("0123456789abcdef", 16)
	for name, tc := range map[string]struct {
		crypto   *cryptoCtx
		keyFlags byte
	}{
		"clear packet, encrypted connection": {ctx, 0},
		"encrypted packet, clear connection": {nil, 1},
	} {
		c, _ := testConn(t, tc.crypto)
		p := &packet{seq: 100, position: positionSolo, msgNo: 1, keyFlags: tc.keyFlags, payload: []byte("injected")}
		c.handlePacket(p)
		c.mu.Lock()
		got := len(c.rcvBuf) + len(c.ready)
		c.mu.Unlock()
		if got != 0 {
			t.Errorf("%s: packet accepted", name)
		}
	}
}

// The last packets of a burst are sent again before the sender lets go of
// them, even while the round trip is still the initial 100 ms guess: no
// later packet will reveal their loss to the receiver.
func TestTailResentBeforeDropped(t *testing.T) {
	c, sent := testConn(t, nil)
	c.Write(make([]byte, 3*1316))
	time.Sleep(c.sendHoldLimit() + 50*time.Millisecond)
	resent := 0
	for _, raw := range sent() {
		if p, err := parsePacket(raw); err == nil && !p.control && p.retransmit {
			resent++
		}
	}
	if resent == 0 {
		t.Fatal("unacknowledged tail dropped without being sent again")
	}
}

// A packet the socket refuses (ENOBUFS, a network change) is a lost packet:
// Write carries on, the sequence numbers stay consecutive, and a NAK for
// the refused packet resends the right one. A refused retransmission
// counts in SendErrors too.
func TestFailedSendIsRecoveredAsLoss(t *testing.T) {
	var mu sync.Mutex
	var sent [][]byte
	calls := 0
	refuse := false
	c := newConn(Config{PayloadSize: 1316, PeerIdleTimeout: time.Minute}, connParams{
		socketID: 1, peerSocketID: 2, isn: 100,
		rcvLatency: 120 * time.Millisecond, sndLatency: 120 * time.Millisecond,
	}, func(b []byte) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 2 || refuse {
			return syscall.ENOBUFS
		}
		sent = append(sent, append([]byte(nil), b...))
		return nil
	}, nil, time.Now())
	defer func() {
		c.mu.Lock()
		c.closeLocked(nil)
		c.mu.Unlock()
	}()

	for i := 0; i < 5; i++ {
		if _, err := c.Write([]byte{byte(i)}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	c.mu.Lock()
	var seqs []uint32
	for _, sp := range c.sndBuf {
		seqs = append(seqs, sp.seq)
	}
	c.mu.Unlock()
	if fmt.Sprint(seqs) != "[100 101 102 103 104]" {
		t.Fatalf("send buffer holds %v, want five consecutive numbers", seqs)
	}
	if s := c.Stats(); s.SendErrors != 1 || s.PacketsSent != 4 {
		t.Fatalf("stats %+v", s)
	}

	// the receiver sees 102 after 100 and asks for 101; the socket refuses
	// the first retransmission and takes the second
	nak := &packet{control: true, ctrlType: ctrlNAK, payload: encodeLossList([]uint32{101})}
	mu.Lock()
	refuse = true
	mu.Unlock()
	c.handlePacket(nak)
	mu.Lock()
	refuse = false
	mu.Unlock()
	if s := c.Stats(); s.SendErrors != 2 || s.PacketsRetransmitted != 0 {
		t.Fatalf("after a refused retransmission: %+v", s)
	}
	c.handlePacket(nak)

	// the connection's own ticks may have sent control packets meanwhile,
	// so look for the data packet rather than take the last one
	var resent []*packet
	mu.Lock()
	for _, raw := range sent {
		if p, err := parsePacket(raw); err == nil && !p.control && p.retransmit {
			resent = append(resent, p)
		}
	}
	mu.Unlock()
	if len(resent) != 1 || resent[0].seq != 101 || resent[0].payload[0] != 1 {
		t.Fatalf("resent %+v", resent)
	}
}

// What is sent and not acknowledged shows in Stats, so a sender can see
// its link fall behind before packets are given up.
func TestSendBufferStats(t *testing.T) {
	c, _ := testConn(t, nil)
	c.Write(make([]byte, 3*1316))
	time.Sleep(20 * time.Millisecond)
	s := c.Stats()
	if s.SendBuffered != 3 || s.SendBufferDelay < 20*time.Millisecond {
		t.Fatalf("stats %+v", s)
	}
	ack := binary.BigEndian.AppendUint32(nil, 103)
	c.handlePacket(&packet{control: true, ctrlType: ctrlACK, typeInfo: 1, payload: append(ack, make([]byte, 24)...)})
	if s := c.Stats(); s.SendBuffered != 0 || s.SendBufferDelay != 0 {
		t.Fatalf("after the ACK: %+v", s)
	}
}

// ackUpTo acknowledges every packet before seq.
func ackUpTo(c *Conn, seq uint32) {
	ack := binary.BigEndian.AppendUint32(nil, seq)
	c.handlePacket(&packet{control: true, ctrlType: ctrlACK, typeInfo: 1, payload: append(ack, make([]byte, 24)...)})
}

// A full send buffer holds Write back until an ACK makes room. A write
// deadline bounds the wait, and n counts the whole packets taken.
func TestWriteWaitsForSendBuffer(t *testing.T) {
	c, _ := testConnWith(t, Config{PeerIdleTimeout: time.Minute, SendBufferSize: 2 * 1316}, nil)
	if n, err := c.Write(make([]byte, 1316)); n != 1316 || err != nil {
		t.Fatalf("first write: %d %v", n, err)
	}
	// room for one of the three packets
	c.SetWriteDeadline(time.Now().Add(30 * time.Millisecond))
	start := time.Now()
	n, err := c.Write(make([]byte, 3*1316))
	if n != 1316 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write into a full buffer: %d %v", n, err)
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Fatal("gave up before the deadline")
	}
	// a deadline already past takes what fits without waiting
	c.SetWriteDeadline(time.Now().Add(-time.Second))
	if n, err := c.Write(make([]byte, 1316)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write past the deadline: %d %v", n, err)
	}

	c.SetWriteDeadline(time.Time{})
	done := make(chan error, 1)
	go func() {
		_, err := c.Write(make([]byte, 1316))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("write did not wait: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	ackUpTo(c, 101)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("the ACK did not wake the write")
	}
}

// With MaxSendDelay, Write waits while the oldest unacknowledged packet is
// that old, however little is buffered.
func TestMaxSendDelayHoldsWrite(t *testing.T) {
	c, _ := testConnWith(t, Config{PeerIdleTimeout: time.Minute, MaxSendDelay: 20 * time.Millisecond}, nil)
	c.Write(make([]byte, 1316))
	if n, err := c.Write(make([]byte, 1316)); n != 1316 || err != nil {
		t.Fatalf("a young packet held the write back: %d %v", n, err)
	}
	time.Sleep(30 * time.Millisecond)
	c.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	if n, err := c.Write(make([]byte, 1316)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write behind an old packet: %d %v", n, err)
	}
	ackUpTo(c, 102)
	if n, err := c.Write(make([]byte, 1316)); n != 1316 || err != nil {
		t.Fatalf("write after the ACK: %d %v", n, err)
	}
}

// Close ends a Write waiting for room at once, and takes no more writes
// while it lets the last packets go out.
func TestCloseEndsWaitingWrite(t *testing.T) {
	c, _ := testConnWith(t, Config{PeerIdleTimeout: time.Minute, SendBufferSize: 1316}, nil)
	done := make(chan error, 1)
	go func() {
		_, err := c.Write(make([]byte, 100*1316))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	closed := make(chan struct{})
	go func() {
		c.Close()
		close(closed)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("write ended with %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("write still waiting after Close")
	}
	// the unacknowledged packet keeps Close lingering for the latency
	select {
	case <-closed:
		t.Fatal("Close did not linger")
	default:
	}
	if n, err := c.Write(make([]byte, 1316)); n != 0 || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("write while Close lingers: %d %v", n, err)
	}
	<-closed
}

// Done and Err tell when and why a connection closed, and Write returns
// the same reason.
func TestCloseReasons(t *testing.T) {
	caller, accepted := pair(t, ListenConfig{}, Config{}, 0)
	if err := caller.Err(); err != nil {
		t.Fatalf("open connection: %v", err)
	}
	caller.Close()
	if err := caller.Err(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("after Close: %v", err)
	}
	for _, b := range [][]byte{[]byte("x"), nil} {
		if _, err := caller.Write(b); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("write of %d bytes after Close: %v", len(b), err)
		}
	}
	select {
	case <-accepted.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("peer shutdown not seen")
	}
	if err := accepted.Err(); err != ErrPeerShutdown {
		t.Fatalf("peer shutdown: %v", err)
	}
	for _, b := range [][]byte{[]byte("x"), nil} {
		if _, err := accepted.Write(b); err != ErrPeerShutdown {
			t.Fatalf("write of %d bytes after peer shutdown: %v", len(b), err)
		}
	}
	if _, err := accepted.Read(make([]byte, 1500)); err != io.EOF {
		t.Fatalf("read after peer shutdown: %v", err)
	}
	// closing it as well, the application is done with it
	accepted.Close()
	if _, err := accepted.Read(make([]byte, 1500)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read after Close: %v", err)
	}

	idle, _ := testConnWith(t, Config{PeerIdleTimeout: 50 * time.Millisecond}, nil)
	select {
	case <-idle.Done():
	case <-time.After(time.Second):
		t.Fatal("silent peer not given up")
	}
	if err := idle.Err(); err != ErrPeerIdle {
		t.Fatalf("silent peer: %v", err)
	}
}

func TestMaxSendDelayLimits(t *testing.T) {
	if _, err := (Config{MaxSendDelay: 60 * time.Millisecond}).withDefaults(); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{
		{MaxSendDelay: -time.Millisecond},
		// packets are kept 170 ms with the default 120 ms latency
		{MaxSendDelay: 170 * time.Millisecond},
		// the handshake carries 120 ms, not 120.9
		{Latency: 120900 * time.Microsecond, MaxSendDelay: 170500 * time.Microsecond},
	} {
		if _, err := cfg.withDefaults(); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
}

func TestSendBufferSizeLimits(t *testing.T) {
	if _, err := (Config{SendBufferSize: 1316}).withDefaults(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Config{SendBufferSize: 1315}).withDefaults(); err == nil {
		t.Error("a send buffer smaller than one packet accepted")
	}
}

// newThrottledProxy relays UDP between a client and a server like a narrow
// link: the client's packets queue up and leave at rate a second, and what
// does not fit in the queue is lost. The way back is not limited.
func newThrottledProxy(t *testing.T, server string, rate, queue int) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	saddr, _ := net.ResolveUDPAddr("udp", server)
	toSrv, err := net.DialUDP("udp", nil, saddr)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	t.Cleanup(func() { close(closed); conn.Close(); toSrv.Close() })

	var mu sync.Mutex
	var client *net.UDPAddr
	q := make(chan []byte, queue)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			mu.Lock()
			client = from
			mu.Unlock()
			select {
			case q <- append([]byte(nil), buf[:n]...):
			default:
			}
		}
	}()
	go func() {
		// a token bucket topped up every millisecond; sleeping per packet
		// would run the link slower than rate
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		start := time.Now()
		sent := 0
		for {
			select {
			case <-closed:
				return
			case now := <-tick.C:
				due := int(now.Sub(start).Seconds() * float64(rate))
				sent = max(sent, due-4) // an idle link saves up no more than a few packets
				for sent < due {
					select {
					case b := <-q:
						toSrv.Write(b)
						sent++
						continue
					default:
					}
					break
				}
			}
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := toSrv.Read(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				continue
			}
			mu.Lock()
			to := client
			mu.Unlock()
			if to != nil {
				conn.WriteToUDP(buf[:n], to)
			}
		}
	}()
	return conn.LocalAddr().String()
}

// slowLinkRun sends n messages paced at twice what the link carries and
// reports how many arrived before the first gap, and the sender's stats.
func slowLinkRun(t *testing.T, maxSendDelay time.Duration) (inOrder int, sender Stats) {
	const (
		n        = 1000
		linkRate = 1000 // packets a second
		latency  = 200 * time.Millisecond
	)
	l, err := Listen("127.0.0.1:0", ListenConfig{Config: Config{Latency: latency}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	accepted := make(chan *Conn, 1)
	go func() {
		c, _ := l.Accept()
		accepted <- c
	}()
	addr := newThrottledProxy(t, l.Addr().String(), linkRate, 256)
	caller, err := Dial(addr, Config{Latency: latency, MaxSendDelay: maxSendDelay})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { caller.Close() })
	var to *Conn
	select {
	case to = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("nothing accepted")
	}

	go func() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		msg := make([]byte, 1316)
		for i := 0; i < n; {
			<-tick.C
			for k := 0; k < 2*linkRate/1000 && i < n; k, i = k+1, i+1 {
				binaryPut(msg, i)
				if _, err := caller.Write(msg); err != nil {
					return
				}
			}
		}
	}()
	buf := make([]byte, 1500)
	to.SetReadDeadline(time.Now().Add(5 * time.Second))
	for inOrder < n {
		k, err := to.Read(buf)
		if err != nil || k != 1316 || binaryGet(buf) != inOrder {
			break
		}
		inOrder++
	}
	return inOrder, caller.Stats()
}

// A live sender producing twice what its link carries is held back by
// MaxSendDelay instead of losing packets: nothing is given up and every
// message arrives, in order, at the pace of the link. Without it the same
// stream loses packets.
func TestMaxSendDelayOnSlowLink(t *testing.T) {
	got, s := slowLinkRun(t, 100*time.Millisecond)
	if got != 1000 || s.PacketsSendDropped != 0 {
		t.Fatalf("with MaxSendDelay: %d messages in order, sender %+v", got, s)
	}
	if got, s := slowLinkRun(t, 0); got == 1000 {
		t.Fatalf("without MaxSendDelay nothing was lost, the link is not narrow enough: sender %+v", s)
	}
}

// Close ends every Read waiting at once, even while it lingers for the last
// packets written, and from then on drops what the peer sends.
func TestCloseEndsWaitingReads(t *testing.T) {
	c, _ := testConn(t, nil)
	// an unacknowledged packet keeps Close lingering for the latency
	c.Write(make([]byte, 1316))
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := c.Read(make([]byte, 1500))
			done <- err
		}()
	}
	time.Sleep(20 * time.Millisecond)
	closed := make(chan struct{})
	go func() {
		c.Close()
		close(closed)
	}()
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("read ended with %v", err)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("read still waiting after Close")
		}
	}
	select {
	case <-closed:
		t.Fatal("Close did not linger")
	default:
	}
	c.handlePacket(&packet{seq: 100, position: positionSolo, msgNo: 1, payload: []byte("late")})
	c.mu.Lock()
	held := len(c.rcvBuf) + len(c.ready)
	c.mu.Unlock()
	if held != 0 {
		t.Fatal("data taken in while Close lingers")
	}
	<-closed
}

// What arrived and was not read is dropped by Close: a Read after it fails
// rather than hand over data the application closed the connection on.
func TestCloseDropsUnread(t *testing.T) {
	caller, accepted := pair(t, ListenConfig{}, Config{}, 0)
	caller.Write([]byte("unread"))
	deadline := time.Now().Add(2 * time.Second)
	for {
		accepted.mu.Lock()
		n := len(accepted.ready)
		accepted.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nothing arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	accepted.Close()
	if n, err := accepted.Read(make([]byte, 1500)); n != 0 || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read after Close: %d %v", n, err)
	}
}
