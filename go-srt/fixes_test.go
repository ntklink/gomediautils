package srt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestSkipRangeBoundedToMaxRecvWindow(t *testing.T) {
	c, _ := testConn(t, nil)
	c.mu.Lock()
	base := c.rcvNext
	// Far future range beyond maxRecvWindow must be ignored
	c.skipRange(seqAdd(base, maxRecvWindow+10), seqAdd(base, maxRecvWindow+20))
	if len(c.rcvBuf) != 0 {
		t.Fatalf("expected far skipRange ignored, got %d entries", len(c.rcvBuf))
	}
	// Range straddling maxRecvWindow stops at maxRecvWindow
	c.skipRange(seqAdd(base, maxRecvWindow-2), seqAdd(base, maxRecvWindow+5))
	if len(c.rcvBuf) != 3 {
		t.Fatalf("expected 3 entries up to maxRecvWindow, got %d", len(c.rcvBuf))
	}
	c.mu.Unlock()
}

func TestDecodeLossListCapAndNoDuplicateRetransmit(t *testing.T) {
	// Build a NAK with multiple huge ranges exceeding maxDecodedLossPerNak
	var payload []byte
	for i := 0; i < 4; i++ {
		start := uint32(i * 10000)
		end := start + 9999
		payload = binary.BigEndian.AppendUint32(payload, start|0x80000000)
		payload = binary.BigEndian.AppendUint32(payload, end)
	}
	count := 0
	decodeLossList(payload, func(uint32) { count++ })
	if count != maxDecodedLossPerNak {
		t.Fatalf("decoded=%d, want cap %d", count, maxDecodedLossPerNak)
	}

	// Duplicate seqs in one NAK must retransmit only once
	c, sent := testConn(t, nil)
	if _, err := c.Write(bytes.Repeat([]byte("a"), 1316)); err != nil {
		t.Fatal(err)
	}
	var dupNak []byte
	for i := 0; i < 5; i++ {
		dupNak = binary.BigEndian.AppendUint32(dupNak, 100)
	}
	before := len(sent())
	c.handlePacket(&packet{control: true, ctrlType: ctrlNAK, payload: dupNak})
	after := len(sent())
	if after-before != 1 {
		t.Fatalf("expected 1 retransmit for duplicate NAK entries, got %d", after-before)
	}
}

func TestApplyKMAtomicOnFailure(t *testing.T) {
	ctx, err := newCryptoCtx("correct-passphrase", 16)
	if err != nil {
		t.Fatal(err)
	}
	origSalt := append([]byte(nil), ctx.salt...)
	origKeyLen := ctx.keyLen
	origKey0 := ctx.keys[0]

	other, err := newCryptoCtx("different-passphrase", 32)
	if err != nil {
		t.Fatal(err)
	}
	badKM, err := other.marshalKM()
	if err != nil {
		t.Fatal(err)
	}
	if err := ctx.applyKM(badKM); !errors.Is(err, errBadSecret) {
		t.Fatalf("expected errBadSecret, got %v", err)
	}
	if !bytes.Equal(ctx.salt, origSalt) || ctx.keyLen != origKeyLen || ctx.keys[0] != origKey0 {
		t.Fatal("failed applyKM mutated cryptoCtx state")
	}
}

func TestListenerOffloadsSlowAuthorizeAndClosesCleanly(t *testing.T) {
	var inAuth atomic.Int32
	unblock := make(chan struct{})
	l, err := Listen("127.0.0.1:0", ListenConfig{
		Authorize: func(req *ConnRequest) error {
			if req.StreamID == "slow" {
				inAuth.Add(1)
				<-unblock
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Start a slow caller that blocks inside Authorize
	slowDone := make(chan *Conn, 1)
	go func() {
		c, err := Dial(l.Addr().String(), Config{StreamID: "slow", ConnectTimeout: 2 * time.Second})
		if err == nil {
			slowDone <- c
		}
	}()

	// Wait until slow caller is inside Authorize
	deadline := time.Now().Add(time.Second)
	for inAuth.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// Fast caller must still connect immediately while slow caller is blocked
	fast, err := Dial(l.Addr().String(), Config{StreamID: "fast", ConnectTimeout: time.Second})
	if err != nil {
		close(unblock)
		l.Close()
		t.Fatalf("fast caller blocked by slow Authorize: %v", err)
	}
	accepted, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if accepted.StreamID() != "fast" {
		t.Fatalf("expected fast stream, got %q", accepted.StreamID())
	}
	close(unblock)
	fast.Close()
	accepted.Close()
	select {
	case sc := <-slowDone:
		sc.Close()
	case <-time.After(time.Second):
	}
	if err := l.Close(); err != nil {
		t.Fatalf("listener close: %v", err)
	}
}

func TestConnReadDeadlineReuseTimer(t *testing.T) {
	c, _ := testConn(t, nil)
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	// Trigger multiple spurious notifications before deadline expires
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(3 * time.Millisecond)
			c.notify()
		}
	}()
	buf := make([]byte, 16)
	_, err := c.Read(buf)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected ErrDeadlineExceeded, got %v", err)
	}
}
