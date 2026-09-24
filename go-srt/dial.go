package srt

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

const handshakeRetry = 250 * time.Millisecond

func randomUint32() uint32 {
	var b [4]byte
	rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

func newSocketID() uint32 {
	for {
		// socket ids are 31 bit; 0 addresses a listener
		if id := randomUint32() & seqMask; id != 0 {
			return id
		}
	}
}

// peerIPField lays out an address the way libsrt fills the peer IP field
// of a handshake: an IPv4 address in the first 32 bit word, byte reversed.
func peerIPField(addr net.Addr) [16]byte {
	var out [16]byte
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return out
	}
	if ip4 := ua.IP.To4(); ip4 != nil {
		out[0], out[1], out[2], out[3] = ip4[3], ip4[2], ip4[1], ip4[0]
		return out
	}
	copy(out[:], ua.IP.To16())
	return out
}

// Dial connects to an SRT listener as a caller.
func Dial(address string, cfg Config) (*Conn, error) {
	return DialContext(context.Background(), address, cfg)
}

// DialContext is Dial with a context that can abandon the handshake.
func DialContext(ctx context.Context, address string, cfg Config) (*Conn, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	raddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	udp, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, err
	}
	c, err := handshakeCaller(ctx, udp, raddr, cfg)
	if err != nil {
		udp.Close()
		return nil, err
	}
	go readLoop(udp, c)
	return c, nil
}

// readLoop feeds the packets of a caller's socket to its connection.
func readLoop(udp *net.UDPConn, c *Conn) {
	buf := make([]byte, 2048)
	for {
		n, err := udp.Read(buf)
		if err != nil {
			c.mu.Lock()
			c.closeLocked(err)
			c.mu.Unlock()
			return
		}
		p, err := parsePacket(append([]byte(nil), buf[:n]...))
		if err != nil || p.dstSocket != c.socketID {
			continue
		}
		c.handlePacket(p)
	}
}

func handshakeCaller(ctx context.Context, udp *net.UDPConn, raddr *net.UDPAddr, cfg Config) (*Conn, error) {
	start := time.Now()
	deadline := start.Add(cfg.ConnectTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	socketID := newSocketID()
	isn := randomUint32() & seqMask
	ts := func() uint32 { return uint32(time.Since(start) / time.Microsecond) }

	var crypto *cryptoCtx
	var km []byte
	if cfg.Passphrase != "" {
		var err error
		if crypto, err = newCryptoCtx(cfg.Passphrase, cfg.PBKeyLen); err != nil {
			return nil, err
		}
		if km, err = crypto.marshalKM(); err != nil {
			return nil, err
		}
	}

	hs := &handshake{
		version:   4,
		extension: udtDgram,
		isn:       isn,
		mtu:       defaultMTU,
		window:    defaultWindow,
		hsType:    hsInduction,
		socketID:  socketID,
		peerIP:    peerIPField(raddr),
	}
	var conclusion bool
	buf := make([]byte, 2048)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("srt: no handshake answer from %s", raddr)
		}
		p := packet{control: true, ctrlType: ctrlHandshake, timestamp: ts(), payload: hs.marshal()}
		if _, err := udp.Write(p.marshal()); err != nil {
			return nil, err
		}
		udp.SetReadDeadline(time.Now().Add(handshakeRetry))
		n, err := udp.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if errors.Is(err, syscall.ECONNREFUSED) {
				// nothing listens there yet; like libsrt, keep trying
				// until the connect timeout
				time.Sleep(handshakeRetry)
				continue
			}
			return nil, err
		}
		rp, err := parsePacket(append([]byte(nil), buf[:n]...))
		if err != nil || !rp.control || rp.ctrlType != ctrlHandshake || rp.dstSocket != socketID {
			continue
		}
		resp, err := parseHandshake(rp.payload)
		if err != nil {
			continue
		}
		if resp.hsType >= hsRejectBase && resp.hsType < hsRejectBase+1000 {
			return nil, &RejectError{Reason: int(resp.hsType - hsRejectBase)}
		}
		if !conclusion {
			if resp.hsType != hsInduction {
				continue
			}
			if resp.version < 5 || resp.extension != hsMagic {
				return nil, &RejectError{Reason: RejectVersion}
			}
			// the conclusion carries the cookie back with everything the
			// connection needs
			conclusion = true
			ext := uint16(1)
			if km != nil {
				ext |= 2
			}
			if cfg.StreamID != "" {
				ext |= extConfig
			}
			flags := uint32(flagTSBPDSnd | flagTSBPDRcv | flagTLPktDrop | flagPeriodicNAK | flagRexmit)
			if crypto != nil {
				flags |= flagCrypt
			}
			latency := uint16(cfg.Latency / time.Millisecond)
			*hs = handshake{
				version:   5,
				extension: ext,
				isn:       isn,
				mtu:       defaultMTU,
				window:    defaultWindow,
				hsType:    hsConclusion,
				socketID:  socketID,
				cookie:    resp.cookie,
				peerIP:    peerIPField(raddr),
				hsReq:     &hsReq{version: srtVersion, flags: flags, recvDelay: latency, sendDelay: latency},
				kmReq:     km,
				streamID:  cfg.StreamID,
			}
			if crypto != nil {
				hs.encryption = uint16(cfg.PBKeyLen / 8)
			}
			continue
		}
		if resp.hsType != hsConclusion {
			continue
		}
		if resp.hsRsp == nil {
			return nil, &RejectError{Reason: RejectVersion}
		}
		if crypto != nil {
			if len(resp.kmRsp) < 16 {
				// a one word answer is a key material state: no secret or a
				// wrong one on the listener's side
				return nil, &RejectError{Reason: RejectBadSecret}
			}
		}
		latency := uint16(cfg.Latency / time.Millisecond)
		params := connParams{
			socketID:      socketID,
			peerSocketID:  resp.socketID,
			isn:           resp.isn,
			peerTimestamp: rp.timestamp,
			rcvLatency:    time.Duration(max(latency, resp.hsRsp.sendDelay)) * time.Millisecond,
			sndLatency:    time.Duration(max(latency, resp.hsRsp.recvDelay)) * time.Millisecond,
			crypto:        crypto,
			streamID:      cfg.StreamID,
			local:         udp.LocalAddr(),
			remote:        raddr,
		}
		udp.SetReadDeadline(time.Time{})
		send := func(b []byte) error {
			_, err := udp.Write(b)
			return err
		}
		return newConn(cfg, params, send, func() { udp.Close() }, start), nil
	}
}
