package mpeg2

import (
	"bytes"
	"math/rand"
	"testing"
)

func h264Nalu(typ byte, n int, firstSlice bool) []byte {
	b := []byte{0, 0, 0, 1, typ}
	if firstSlice {
		b = append(b, 0x80)
	} else {
		b = append(b, 0x00)
	}
	for range n {
		b = append(b, byte(rand.Intn(250))+2)
	}
	return b
}

// The access unit scanner resumes where the previous ts packet left off. It
// has to produce exactly what a full rescan would, in particular across
// frames large enough to span thousands of ts packets.
func TestTSRoundTripAcrossLargeFrames(t *testing.T) {
	rand.Seed(7)
	mux := NewTSMuxer()
	pid := mux.AddStream(TS_STREAM_H264)
	var ts bytes.Buffer
	mux.OnPacket = func(p []byte) { ts.Write(p) }

	var want [][]byte
	for n := 0; n < 40; n++ {
		var f []byte
		if n%10 == 0 {
			f = append(f, h264Nalu(0x67, 20, false)...)
			f = append(f, h264Nalu(0x68, 8, false)...)
			f = append(f, h264Nalu(0x65, 3000+rand.Intn(300000), true)...)
		} else {
			f = append(f, h264Nalu(0x61, 500+rand.Intn(9000), true)...)
		}
		want = append(want, f)
		if err := mux.Write(pid, f, uint64(n*40), uint64(n*40)); err != nil {
			t.Fatal(err)
		}
	}

	var got [][]byte
	demux := NewTSDemuxer()
	demux.OnFrame = func(cid TS_STREAM_TYPE, f []byte, pts, dts uint64) {
		got = append(got, append([]byte(nil), f...))
	}
	if err := demux.Input(bytes.NewReader(ts.Bytes())); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("demuxed %d frames, muxed %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(want[i], got[i]) {
			t.Fatalf("frame %d differs: in %d bytes, out %d bytes", i, len(want[i]), len(got[i]))
		}
	}
}

// A frame is fed in as many ts packets; the scanner must not be quadratic in
// the frame size. Two megabytes of key frame is a few seconds of work when it
// is, and milliseconds when it is not.
func BenchmarkTSDemuxLargeFrames(b *testing.B) {
	mux := NewTSMuxer()
	pid := mux.AddStream(TS_STREAM_H264)
	var ts bytes.Buffer
	mux.OnPacket = func(p []byte) { ts.Write(p) }
	for n := 0; n < 8; n++ {
		f := append(h264Nalu(0x67, 20, false), h264Nalu(0x65, 2<<20, true)...)
		if err := mux.Write(pid, f, uint64(n*40), uint64(n*40)); err != nil {
			b.Fatal(err)
		}
	}
	data := ts.Bytes()
	b.SetBytes(int64(len(data)))

	for b.Loop() {
		d := NewTSDemuxer()
		d.OnFrame = func(cid TS_STREAM_TYPE, f []byte, pts, dts uint64) {}
		if err := d.Input(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
