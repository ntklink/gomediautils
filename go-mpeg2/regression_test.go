package mpeg2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
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

func TestTSDemuxerConsecutiveVCLFramesWithoutAUD(t *testing.T) {
	for _, cid := range []TS_STREAM_TYPE{TS_STREAM_H264, TS_STREAM_H265} {
		var es []byte
		for i := 0; i < 4; i++ {
			if cid == TS_STREAM_H264 {
				// Non-IDR slice with first_mb_in_slice == 0 (bit 7 of byte after nal header == 1)
				es = append(es, 0, 0, 0, 1, 0x41, 0x80, byte(i+1))
			} else {
				// TRAIL_R (type 1) with first_slice_segment_in_pic_flag == 1
				es = append(es, 0, 0, 0, 1, 0x02, 0x01, 0x80, byte(i+1))
			}
		}

		// Build minimal TS stream (PAT + PMT + PES without AUD)
		mux := NewTSMuxer()
		pid := mux.AddStream(cid)
		var ts bytes.Buffer
		mux.OnPacket = func(p []byte) { ts.Write(p) }
		// Pass withaud=true by prepending nothing? Write checks withaud, so call writePES directly after PAT/PMT
		pat := NewPat()
		pat.Pmts = append(pat.Pmts, PmtPair{Program_number: 1, PID: mux.pat.pmts[0].pid})
		if err := mux.writePat(pat); err != nil {
			t.Fatal(err)
		}
		pmt := NewPmt()
		pmt.Program_number = 1
		pmt.PCR_PID = pid
		pmt.Streams = append(pmt.Streams, StreamPair{StreamType: uint8(cid), Elementary_PID: pid})
		if err := mux.writePmt(pmt, mux.pat.pmts[0]); err != nil {
			t.Fatal(err)
		}
		if err := mux.writePES(mux.pat.pmts[0].streams[0], mux.pat.pmts[0], es, 9000, 9000, false, true); err != nil {
			t.Fatal(err)
		}

		var got int
		demux := NewTSDemuxer()
		demux.OnFrame = func(c TS_STREAM_TYPE, f []byte, pts, dts uint64) {
			got++
		}
		if err := demux.Input(bytes.NewReader(ts.Bytes())); err != nil {
			t.Fatalf("cid %d: Input: %v", cid, err)
		}
		if got != 4 {
			t.Fatalf("cid %d: got %d frames, want 4", cid, got)
		}
	}
}

func TestPSMEncodeCRC32IncludesPrefix(t *testing.T) {
	bsw := codec.NewBitStreamWriter(128)
	// Write a non-zero prefix first to verify start offset handling
	bsw.PutBytes([]byte{0xAA, 0xBB, 0xCC})
	start := bsw.ByteOffset()
	psm := &Program_stream_map{
		Current_next_indicator:     1,
		Program_stream_map_version: 1,
		Stream_map:                 []*Elementary_stream_elem{NewElementary_stream_elem(uint8(PS_STREAM_H264), 0xE0)},
	}
	psm.Encode(bsw)
	raw := bsw.Bits()[start:]
	want := codec.CalcCrc32(0xffffffff, raw[:len(raw)-4])
	got := binary.LittleEndian.Uint32(raw[len(raw)-4:])
	if got != want {
		t.Fatalf("PSM CRC32 = 0x%08x, want 0x%08x", got, want)
	}
}

func TestPSMuxerAUDDoesNotHideIDR(t *testing.T) {
	mux := NewPsMuxer()
	sid := mux.AddStream(PS_STREAM_H264)
	var packs [][]byte
	mux.OnPacket = func(pkg []byte) {
		packs = append(packs, append([]byte(nil), pkg...))
	}
	// First frame (non-IDR) clears firstframe
	if err := mux.Write(sid, []byte{0, 0, 0, 1, 0x41, 0x9a, 0x01}, 40, 40); err != nil {
		t.Fatal(err)
	}
	// Second frame: AUD + IDR
	audIDR := append(append([]byte(nil), H264_AUD_NALU...), 0, 0, 0, 1, 0x65, 0x88, 0x84)
	if err := mux.Write(sid, audIDR, 80, 80); err != nil {
		t.Fatal(err)
	}
	if len(packs) < 2 {
		t.Fatalf("expected at least 2 packs, got %d", len(packs))
	}
	// Second pack must contain system header (00 00 01 BB) and PSM (00 00 01 BC) because idr_flag was detected
	if !bytes.Contains(packs[1], []byte{0x00, 0x00, 0x01, 0xBB}) || !bytes.Contains(packs[1], []byte{0x00, 0x00, 0x01, 0xBC}) {
		t.Fatal("PSMuxer did not emit System Header + PSM for AUD+IDR frame")
	}
}

func TestPESDecodeClearsStalePTSAndPSDemuxerPreservesPTS(t *testing.T) {
	// 1. PesPacket.Decode clears stale Pts/Dts when PTS_DTS_flags == 0
	bsw := codec.NewBitStreamWriter(64)
	p1 := NewPesPacket()
	p1.Stream_id = 0xE0
	p1.PTS_DTS_flags = 0x02
	p1.PES_header_data_length = 5
	p1.Pts = 90000
	p1.Pes_payload = []byte{1, 2, 3}
	p1.PES_packet_length = uint16(3 + 5 + len(p1.Pes_payload))
	p1.Encode(bsw)

	reused := NewPesPacket()
	if err := reused.Decode(codec.NewBitStream(bsw.Bits())); err != nil {
		t.Fatal(err)
	}
	if reused.Pts != 90000 || reused.Dts != 90000 {
		t.Fatalf("first decode pts=%d dts=%d, want 90000", reused.Pts, reused.Dts)
	}

	bsw.Reset()
	p2 := NewPesPacket()
	p2.Stream_id = 0xE0
	p2.PTS_DTS_flags = 0x00
	p2.PES_header_data_length = 0
	p2.Pes_payload = []byte{4, 5, 6}
	p2.PES_packet_length = uint16(3 + len(p2.Pes_payload))
	p2.Encode(bsw)
	if err := reused.Decode(codec.NewBitStream(bsw.Bits())); err != nil {
		t.Fatal(err)
	}
	if reused.Pts != 0 || reused.Dts != 0 {
		t.Fatalf("second decode stale pts=%d dts=%d, want 0", reused.Pts, reused.Dts)
	}

	// 2. PSDemuxer preserves stream.pts across continuation PES with no PTS
	demux := NewPSDemuxer()
	var gotPts uint64
	var gotFrames int
	demux.OnFrame = func(frame []byte, cid PS_STREAM_TYPE, pts, dts uint64) {
		gotPts = pts
		gotFrames++
	}
	bsw = codec.NewBitStreamWriter(256)
	var pack PSPackHeader
	pack.Encode(bsw)
	psm := &Program_stream_map{
		Current_next_indicator:     1,
		Program_stream_map_version: 1,
		Stream_map:                 []*Elementary_stream_elem{NewElementary_stream_elem(uint8(PS_STREAM_G711A), 0xC0)},
	}
	psm.Encode(bsw)
	a1 := NewPesPacket()
	a1.Stream_id = 0xC0
	a1.PTS_DTS_flags = 0x02
	a1.PES_header_data_length = 5
	a1.Pts = 90000 // 1000 ms
	a1.Pes_payload = []byte{0x11, 0x22}
	a1.PES_packet_length = uint16(3 + 5 + len(a1.Pes_payload))
	a1.Encode(bsw)

	a2 := NewPesPacket()
	a2.Stream_id = 0xC0
	a2.PTS_DTS_flags = 0x00
	a2.PES_header_data_length = 0
	a2.Pes_payload = []byte{0x33, 0x44}
	a2.PES_packet_length = uint16(3 + len(a2.Pes_payload))
	a2.Encode(bsw)

	if err := demux.Input(bsw.Bits()); err != nil {
		t.Fatalf("PSDemuxer.Input: %v", err)
	}
	demux.Flush()
	if gotFrames != 1 || gotPts != 1000 {
		t.Fatalf("gotFrames=%d gotPts=%d, want 1 and 1000", gotFrames, gotPts)
	}
}

func TestTSDemuxerAdaptationOnlyAndPointerField(t *testing.T) {
	// Build a PAT with PUSI=1 and pointer_field=3 (3 filler bytes before table_id),
	// plus an adaptation-field-only packet on PID 0.
	var ts bytes.Buffer
	bsw := codec.NewBitStreamWriter(TS_PAKCET_SIZE)
	// Adaptation-field-only packet on PAT PID
	hdr := TSPacket{
		PID:                      0,
		Adaptation_field_control: 0x02,
		Field:                    &Adaptation_field{Stuffing_byte: 182},
	}
	hdr.EncodeHeader(bsw)
	ts.Write(bsw.Bits())

	// PAT packet with pointer_field = 3
	bsw.Reset()
	hdr = TSPacket{
		Payload_unit_start_indicator: 1,
		PID:                          0,
		Adaptation_field_control:     0x01,
	}
	hdr.EncodeHeader(bsw)
	bsw.PutByte(3) // pointer_field
	bsw.PutBytes([]byte{0xFF, 0xFF, 0xFF})
	pat := NewPat()
	pat.Pmts = append(pat.Pmts, PmtPair{Program_number: 1, PID: 0x100})
	pat.Encode(bsw)
	stuffing(bsw)
	ts.Write(bsw.Bits())

	demux := NewTSDemuxer()
	if err := demux.Input(bytes.NewReader(ts.Bytes())); err != nil {
		t.Fatalf("TSDemuxer.Input: %v", err)
	}
	if _, ok := demux.programs[0x100]; !ok {
		t.Fatal("PAT with non-zero pointer_field was not parsed")
	}
}

func TestTSDemuxerProbeUnsyncEOF(t *testing.T) {
	// 400 bytes with no 0x47 sync bytes: hits io.ErrUnexpectedEOF without emitting bogus packets.
	garbage := bytes.Repeat([]byte{0x12}, 400)
	demux := NewTSDemuxer()
	packets := 0
	demux.OnTSPacket = func(pkg *TSPacket) { packets++ }
	if err := demux.Input(bytes.NewReader(garbage)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got err=%v, want io.ErrUnexpectedEOF", err)
	}
	if packets != 0 {
		t.Fatalf("decoded %d packets from unsynchronized garbage", packets)
	}

	// Exact 2*188 bytes with no sync byte: hits io.EOF cleanly with 0 packets.
	demux = NewTSDemuxer()
	demux.OnTSPacket = func(pkg *TSPacket) { packets++ }
	if err := demux.Input(bytes.NewReader(bytes.Repeat([]byte{0x12}, 2*TS_PAKCET_SIZE))); err != nil {
		t.Fatalf("unexpected error on exact EOF: %v", err)
	}
	if packets != 0 {
		t.Fatalf("decoded %d packets from unsynchronized garbage", packets)
	}
}

func TestPesPacketESCRRoundTrip(t *testing.T) {
	enc := NewPesPacket()
	enc.Stream_id = 0xE0
	enc.ESCR_flag = 1
	enc.ESCR_base = 0x12345678
	enc.ESCR_extension = 299
	enc.PES_header_data_length = 6
	enc.Pes_payload = []byte{0xAA, 0xBB, 0xCC}
	enc.PES_packet_length = uint16(3 + 6 + len(enc.Pes_payload))

	bsw := codec.NewBitStreamWriter(64)
	enc.Encode(bsw)

	dec := NewPesPacket()
	if err := dec.Decode(codec.NewBitStream(bsw.Bits())); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if dec.ESCR_base != enc.ESCR_base || dec.ESCR_extension != enc.ESCR_extension {
		t.Fatalf("ESCR got (%d, %d), want (%d, %d)", dec.ESCR_base, dec.ESCR_extension, enc.ESCR_base, enc.ESCR_extension)
	}
	if !bytes.Equal(dec.Pes_payload, enc.Pes_payload) {
		t.Fatalf("payload got %x, want %x", dec.Pes_payload, enc.Pes_payload)
	}
}

func TestPSPackHeaderMpeg2ShortStuffingUnRead(t *testing.T) {
	bsw := codec.NewBitStreamWriter(32)
	hdr := PSPackHeader{
		System_clock_reference_base: 9000,
		Program_mux_rate:            6106,
		Pack_stuffing_length:        2,
	}
	hdr.Encode(bsw)
	// Truncate the 2 stuffing bytes (keep 14 bytes)
	raw := bsw.Bits()[:14]
	bs := codec.NewBitStream(raw)
	var dec PSPackHeader
	if err := dec.Decode(bs); !errors.Is(err, ErrNeedMore) {
		t.Fatalf("got err=%v, want ErrNeedMore", err)
	}
	if bs.ByteOffset() != 0 {
		t.Fatalf("ByteOffset=%d after ErrNeedMore, want 0", bs.ByteOffset())
	}
}
