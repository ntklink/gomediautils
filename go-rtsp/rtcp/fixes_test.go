package rtcp

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ntklink/gomediautils/go-rtsp/rtp"
)

func TestRtcpContextLSRAndJitterWrap(t *testing.T) {
	ctx := NewRtcpContext(0x1111, 1, 90000)

	// Before any SR, LSR and DLSR must be 0
	rr := ctx.GenerateRR()
	if rr.Blocks[0].Lsr != 0 || rr.Blocks[0].Dlsr != 0 {
		t.Fatalf("expected Lsr=0, Dlsr=0 before SR, got %x %x", rr.Blocks[0].Lsr, rr.Blocks[0].Dlsr)
	}

	// After SR, LSR must be middle 32 bits of 64-bit NTP timestamp
	ntp := uint64(0x123456789ABCDEF0)
	ctx.ReceivedSR(&SenderReport{SSRC: 0x2222, NTP: ntp})
	rr = ctx.GenerateRR()
	wantLsr := uint32((ntp >> 16) & 0xFFFFFFFF)
	if rr.Blocks[0].Lsr != wantLsr {
		t.Fatalf("Lsr=%08x, want %08x", rr.Blocks[0].Lsr, wantLsr)
	}

	// Timestamp wraparound (0xFFFFFF00 -> 0x00000100, delta = +512) must not explode jitter
	for i := uint16(1); i <= 4; i++ {
		ts := uint32(0xFFFFFF00) + uint32(i-1)*256
		ctx.ReceivedRtp(&rtp.RtpPacket{Header: rtp.RtpHdr{SequenceNumber: i, Timestamp: ts}})
	}
	rr = ctx.GenerateRR()
	if rr.Blocks[0].Jitter > 100000 {
		t.Fatalf("jitter exploded across timestamp wrap: %d", rr.Blocks[0].Jitter)
	}
}

func TestRtcpCommLargeLengthNoUint16Overflow(t *testing.T) {
	// Length = 16384 => payloadLen = 65536 (would wrap to 0 in uint16)
	raw := make([]byte, 4+16384*4)
	raw[0] = 0x80
	raw[1] = RTCP_APP
	binary.BigEndian.PutUint16(raw[2:4], 16384)
	copy(raw[8:12], "TEST")

	var app App
	if err := app.Decode(raw); err != nil {
		t.Fatalf("App.Decode failed: %v", err)
	}
	if len(app.AppData) != 65536-8 {
		t.Fatalf("AppData len=%d, want %d", len(app.AppData), 65536-8)
	}
}

func TestSdesMakeCNameAndMultiChunkAlignment(t *testing.T) {
	item := MakeCNameItem([]byte("alice@host"))
	if !bytes.Equal(item.Txt, []byte("alice@host")) || item.Length != 10 {
		t.Fatalf("MakeCNameItem did not copy name: len=%d txt=%q", item.Length, item.Txt)
	}

	longName := bytes.Repeat([]byte("x"), 300)
	capped := MakeCNameItem(longName)
	if len(capped.Txt) != 255 || capped.Length != 255 {
		t.Fatalf("expected capped to 255, got len=%d Length=%d", len(capped.Txt), capped.Length)
	}

	sdes := NewSourceDescription()
	sdes.Chunks = []SDESChunk{
		{SSRC: 0x11111111, Item: MakeCNameItem([]byte("ab"))},
		{SSRC: 0x22222222, Item: MakeCNameItem([]byte("hello"))},
	}
	wire := sdes.Encode()
	if len(wire)%4 != 0 {
		t.Fatalf("encoded SDES not 32-bit aligned: len=%d", len(wire))
	}
	var decoded SourceDescription
	if err := decoded.Decode(wire); err != nil {
		t.Fatalf("Decode multi-chunk SDES failed: %v", err)
	}
	if len(decoded.Chunks) != 2 ||
		decoded.Chunks[0].SSRC != 0x11111111 || string(decoded.Chunks[0].Item.Txt) != "ab" ||
		decoded.Chunks[1].SSRC != 0x22222222 || string(decoded.Chunks[1].Item.Txt) != "hello" {
		t.Fatalf("decoded chunks mismatch: %+v", decoded.Chunks)
	}
}
