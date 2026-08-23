package mpeg2

import (
	"bytes"
	"testing"
)

func h264Frame(naluType byte, payload ...byte) []byte {
	frame := []byte{0x00, 0x00, 0x00, 0x01, naluType, 0x80}
	return append(frame, payload...)
}

// collect muxes a few frames and returns every packet the muxer produced.
func collect(t *testing.T, frames [][]byte) []byte {
	t.Helper()
	muxer := NewTSMuxer()
	var out bytes.Buffer
	muxer.OnPacket = func(pkg []byte) {
		if len(pkg) != TS_PAKCET_SIZE {
			t.Fatalf("packet size %d, want %d", len(pkg), TS_PAKCET_SIZE)
		}
		if pkg[0] != 0x47 {
			t.Fatalf("packet does not start with the sync byte: %#x", pkg[0])
		}
		out.Write(pkg)
	}
	pid := muxer.AddStream(TS_STREAM_H264)
	for i, frame := range frames {
		if err := muxer.Write(pid, frame, uint64(i*40), uint64(i*40)); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
	}
	return out.Bytes()
}

func TestTSMuxerPacketsAreWellFormed(t *testing.T) {
	frames := [][]byte{
		append(h264Frame(0x67, 0x42, 0x00, 0x1e), append(h264Frame(0x68, 0xce), h264Frame(0x65, bytes.Repeat([]byte{0xAA}, 400)...)...)...),
		h264Frame(0x41, bytes.Repeat([]byte{0xBB}, 300)...),
		h264Frame(0x41, bytes.Repeat([]byte{0xCC}, 1000)...),
	}
	stream := collect(t, frames)
	if len(stream) == 0 || len(stream)%TS_PAKCET_SIZE != 0 {
		t.Fatalf("muxed %d bytes, not a multiple of %d", len(stream), TS_PAKCET_SIZE)
	}

	demuxer := NewTSDemuxer()
	got := 0
	demuxer.OnFrame = func(cid TS_STREAM_TYPE, frame []byte, pts uint64, dts uint64) {
		if cid != TS_STREAM_H264 {
			t.Errorf("unexpected codec %d", cid)
		}
		got++
	}
	if err := demuxer.Input(bytes.NewReader(stream)); err != nil {
		t.Fatalf("demux: %v", err)
	}
	if got != len(frames) {
		t.Fatalf("demuxed %d frames, want %d", got, len(frames))
	}
}

// A pmt that does not fit in a single transport packet used to be emitted as
// an oversized packet (and tripped an internal panic in writePES); it must be
// reported instead.
func TestTSMuxerRejectsOversizedTable(t *testing.T) {
	muxer := NewTSMuxer()
	var pid uint16
	for i := 0; i < 64; i++ {
		pid = muxer.AddStream(TS_STREAM_H264)
	}
	packets := 0
	muxer.OnPacket = func(pkg []byte) {
		packets++
		if len(pkg) != TS_PAKCET_SIZE {
			t.Fatalf("emitted a %d byte packet", len(pkg))
		}
	}
	err := muxer.Write(pid, h264Frame(0x65, 0x01, 0x02), 0, 0)
	if err == nil {
		t.Fatalf("expected an error for a pmt that does not fit in one packet")
	}
}

func TestTSMuxerUnknownPid(t *testing.T) {
	muxer := NewTSMuxer()
	muxer.AddStream(TS_STREAM_H264)
	if err := muxer.Write(0xFFFF, h264Frame(0x65, 0x01), 0, 0); err == nil {
		t.Fatalf("expected an error for an unknown pid")
	}
}
