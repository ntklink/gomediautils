package srt

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"sync"
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
	if info := ParseStreamID("live/cam2"); info.Resource != "live/cam2" || info.Mode != "request" {
		t.Fatalf("%+v", info)
	}
}
