package rtp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRtpExtensionLengthNoUint16Overflow(t *testing.T) {
	// V=2, X=1, 12-byte header + 4-byte ext header with length=16384 (4+4*16384 overflows uint16)
	raw := make([]byte, 20)
	raw[0] = 0x90 // V=2, X=1
	binary.BigEndian.PutUint16(raw[14:16], 16384)
	var pkt RtpPacket
	if err := pkt.Decode(raw); err == nil {
		t.Fatal("expected error for overflowing extension length")
	}
}

func TestH264AndH265FuPacketLossHandling(t *testing.T) {
	t.Run("h264", func(t *testing.T) {
		u := NewH264UnPacker()
		var frames [][]byte
		u.OnFrame(func(frame []byte, ts uint32, lost bool) {
			frames = append(frames, append([]byte(nil), frame...))
		})

		// Stray S=0 FU-A packet before any S=1 must be discarded
		stray := RtpPacket{
			Header:  RtpHdr{SequenceNumber: 1, Timestamp: 100},
			Payload: []byte{0x7c, 0x45, 0xaa, 0xbb}, // E=1, S=0
		}
		if err := u.UnPack(stray.Encode()); err != nil {
			t.Fatal(err)
		}
		if len(frames) != 0 {
			t.Fatalf("expected stray FU-A to be discarded, got %d frames", len(frames))
		}

		// Incomplete FU-A (S=1, E=0) followed by single NALU must not leak FU bytes into single NALU
		startFu := RtpPacket{
			Header:  RtpHdr{SequenceNumber: 2, Timestamp: 200},
			Payload: []byte{0x7c, 0x85, 0x11, 0x22},
		}
		if err := u.UnPack(startFu.Encode()); err != nil {
			t.Fatal(err)
		}
		single := RtpPacket{
			Header:  RtpHdr{SequenceNumber: 3, Timestamp: 300},
			Payload: []byte{0x67, 0x42, 0x00, 0x1e},
		}
		if err := u.UnPack(single.Encode()); err != nil {
			t.Fatal(err)
		}
		want := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1e}
		if len(frames) != 1 || !bytes.Equal(frames[0], want) {
			t.Fatalf("single NALU corrupted: got %x, want %x", frames, want)
		}
	})

	t.Run("h265", func(t *testing.T) {
		u := NewH265UnPacker()
		var frames [][]byte
		u.OnFrame(func(frame []byte, ts uint32, lost bool) {
			frames = append(frames, append([]byte(nil), frame...))
		})

		stray := RtpPacket{
			Header:  RtpHdr{SequenceNumber: 1, Timestamp: 100},
			Payload: []byte{49 << 1, 0x01, 0x40 | 19, 0xaa, 0xbb},
		}
		if err := u.UnPack(stray.Encode()); err != nil {
			t.Fatal(err)
		}
		if len(frames) != 0 {
			t.Fatalf("expected stray H265 FU to be discarded, got %d frames", len(frames))
		}

		startFu := RtpPacket{
			Header:  RtpHdr{SequenceNumber: 2, Timestamp: 200},
			Payload: []byte{49 << 1, 0x01, 0x80 | 19, 0x11, 0x22},
		}
		if err := u.UnPack(startFu.Encode()); err != nil {
			t.Fatal(err)
		}
		single := RtpPacket{
			Header:  RtpHdr{SequenceNumber: 3, Timestamp: 300},
			Payload: []byte{32 << 1, 0x01, 0x99},
		}
		if err := u.UnPack(single.Encode()); err != nil {
			t.Fatal(err)
		}
		want := []byte{0x00, 0x00, 0x00, 0x01, 32 << 1, 0x01, 0x99}
		if len(frames) != 1 || !bytes.Equal(frames[0], want) {
			t.Fatalf("single H265 NALU corrupted: got %x, want %x", frames, want)
		}
	})
}

func TestUnPackerMaxFrameBufferOverflow(t *testing.T) {
	u264 := NewH264UnPacker()
	u264.inFu = true
	u264.frameBuffer.Write(make([]byte, maxRtpFrameBufferSize-10))
	pkt := RtpPacket{
		Header:  RtpHdr{SequenceNumber: 1, Timestamp: 100},
		Payload: append([]byte{0x7c, 0x05}, make([]byte, 32)...),
	}
	if err := u264.UnPack(pkt.Encode()); err != errFrameBufferOverflow {
		t.Fatalf("h264 expected errFrameBufferOverflow, got %v", err)
	}
	if u264.frameBuffer.Len() != 4 || u264.inFu {
		t.Fatalf("h264 buffer not reset: len=%d inFu=%v", u264.frameBuffer.Len(), u264.inFu)
	}

	u265 := NewH265UnPacker()
	u265.inFu = true
	u265.frameBuffer.Write(make([]byte, maxRtpFrameBufferSize-10))
	pkt265 := RtpPacket{
		Header:  RtpHdr{SequenceNumber: 1, Timestamp: 100},
		Payload: append([]byte{49 << 1, 0x01, 19}, make([]byte, 32)...),
	}
	if err := u265.UnPack(pkt265.Encode()); err != errFrameBufferOverflow {
		t.Fatalf("h265 expected errFrameBufferOverflow, got %v", err)
	}
	if u265.frameBuffer.Len() != 4 || u265.inFu {
		t.Fatalf("h265 buffer not reset: len=%d inFu=%v", u265.frameBuffer.Len(), u265.inFu)
	}

	uts := NewTsUnPacker()
	uts.timestamp = 100
	uts.frameBuffer.Write(make([]byte, maxRtpFrameBufferSize-10))
	pktTs := RtpPacket{
		Header:  RtpHdr{SequenceNumber: 1, Timestamp: 100},
		Payload: make([]byte, 32),
	}
	if err := uts.UnPack(pktTs.Encode()); err != errFrameBufferOverflow {
		t.Fatalf("ts expected errFrameBufferOverflow, got %v", err)
	}
	if uts.frameBuffer.Len() != 0 {
		t.Fatalf("ts buffer not reset: len=%d", uts.frameBuffer.Len())
	}
}
