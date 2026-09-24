package srt

import (
	"testing"
	"time"
)

func FuzzParsers(f *testing.F) {
	hs := &handshake{version: 5, hsType: hsConclusion, hsReq: &hsReq{version: srtVersion}, streamID: "a/b"}
	f.Add((&packet{control: true, ctrlType: ctrlHandshake, payload: hs.marshal()}).marshal())
	f.Add((&packet{control: true, ctrlType: ctrlNAK, payload: encodeLossList([]uint32{1, 2, 3, 9})}).marshal())
	ctx, _ := newCryptoCtx("0123456789abcdef", 16)
	km, _ := ctx.marshalKM()
	f.Add(km)
	f.Fuzz(func(t *testing.T, data []byte) {
		if p, err := parsePacket(data); err == nil && p.control {
			parseHandshake(p.payload)
		}
		parseHandshake(data)
		n := 0
		decodeLossList(data, func(uint32) { n++ })
		if n > (len(data)/4)*maxLossRange {
			t.Fatalf("%d sequence numbers out of %d bytes", n, len(data))
		}
		(&cryptoCtx{passphrase: "0123456789abcdef"}).applyKM(data)
	})
}

// Whatever a peer sends, a connection neither panics nor grows without
// bound.
func FuzzConnInput(f *testing.F) {
	f.Add((&packet{seq: 100, position: positionSolo, msgNo: 1, payload: []byte("x")}).marshal())
	f.Add((&packet{control: true, ctrlType: ctrlNAK, payload: encodeLossList([]uint32{0x7FFFFF00, 5})}).marshal())
	f.Add((&packet{control: true, ctrlType: ctrlDropReq, payload: []byte{0, 0, 0, 1, 0x7F, 0xFF, 0xFF, 0xFF}}).marshal())
	f.Add((&packet{control: true, ctrlType: ctrlACK, typeInfo: 1, payload: make([]byte, 28)}).marshal())
	f.Fuzz(func(t *testing.T, data []byte) {
		ctx, _ := newCryptoCtx("0123456789abcdef", 16)
		c := newConn(Config{PayloadSize: 1316, PeerIdleTimeout: time.Minute}, connParams{
			socketID: 1, peerSocketID: 2, isn: 100, crypto: ctx,
			rcvLatency: 50 * time.Millisecond, sndLatency: 50 * time.Millisecond,
		}, func([]byte) error { return nil }, nil, time.Now())
		defer func() {
			// shut down without the linger Close gives unacknowledged data
			c.mu.Lock()
			c.closeLocked(nil)
			c.mu.Unlock()
		}()
		c.Write(make([]byte, 3000))
		for len(data) >= headerSize {
			n := min(len(data), headerSize+int(data[0]%64))
			if p, err := parsePacket(data[:n]); err == nil {
				c.handlePacket(p)
			}
			data = data[n:]
		}
		c.mu.Lock()
		n, losses := len(c.rcvBuf), len(c.loss)
		c.mu.Unlock()
		if n > maxRecvWindow+1 || losses > maxRecvWindow+1 {
			t.Fatalf("buffers grew to %d packets, %d losses", n, losses)
		}
	})
}
