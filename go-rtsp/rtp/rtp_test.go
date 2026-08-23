package rtp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func h264Frame(nalus ...[]byte) []byte {
	var buf bytes.Buffer
	for _, n := range nalus {
		buf.Write([]byte{0, 0, 0, 1})
		buf.Write(n)
	}
	return buf.Bytes()
}

func TestH264PackerStapA(t *testing.T) {
	sps := []byte{0x67, 1, 2, 3}
	pps := []byte{0x68, 4, 5}
	idr := make([]byte, 100)
	idr[0] = 0x65
	pk := NewH264Packer(96, 1234, 10, 1400)
	pk.EnableStapA()
	var pkts []RtpPacket
	pk.OnPacket(func(b []byte) error {
		var p RtpPacket
		if err := p.Decode(b); err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, p)
		return nil
	})
	if err := pk.Pack(h264Frame(sps, pps, idr), 900); err != nil {
		t.Fatal(err)
	}
	if len(pkts) != 2 {
		t.Fatalf("want 2 packets (STAP-A + IDR), got %d", len(pkts))
	}
	stap := pkts[0]
	if stap.Header.PayloadType != 96 || stap.Header.SequenceNumber != 10 || stap.Payload[0]&0x1f != 24 {
		t.Fatalf("bad STAP-A packet %+v", stap.Header)
	}
	want := []byte{0x60 | 24, 0, 4, 0x67, 1, 2, 3, 0, 3, 0x68, 4, 5}
	if !bytes.Equal(stap.Payload, want) {
		t.Fatalf("STAP-A payload %x want %x", stap.Payload, want)
	}
	if pkts[1].Header.SequenceNumber != 11 || pkts[1].Payload[0] != 0x65 {
		t.Fatalf("IDR must follow with incremented seq: %+v", pkts[1].Header)
	}

	// the unpacker must yield sps, pps, idr from that stream
	up := NewH264UnPacker()
	var frames [][]byte
	up.OnFrame(func(f []byte, ts uint32, lost bool) {
		frames = append(frames, append([]byte(nil), f...))
	})
	for _, p := range pkts {
		if err := up.UnPack(p.Encode()); err != nil {
			t.Fatal(err)
		}
	}
	if len(frames) != 3 || frames[0][4] != 0x67 || frames[1][4] != 0x68 || frames[2][4] != 0x65 {
		t.Fatalf("unexpected frames %d", len(frames))
	}
}

func TestH264PackerFuADecisionAndSequence(t *testing.T) {
	small := []byte{0x41, 1, 2, 3}
	big := make([]byte, 3000)
	big[0] = 0x65
	pk := NewH264Packer(96, 1, 100, 1400)
	var pkts []RtpPacket
	pk.OnPacket(func(b []byte) error {
		var p RtpPacket
		if err := p.Decode(b); err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, p)
		return nil
	})
	// frame is large, but first nalu is small: it must be sent as a single nalu
	if err := pk.Pack(h264Frame(small, big), 1); err != nil {
		t.Fatal(err)
	}
	if len(pkts) < 3 {
		t.Fatalf("want >=3 packets, got %d", len(pkts))
	}
	if pkts[0].Payload[0]&0x1f != 1 || len(pkts[0].Payload) != 4 {
		t.Fatalf("small nalu should be single nalu packet: %x", pkts[0].Payload)
	}
	for i := 1; i < len(pkts); i++ {
		if pkts[i].Payload[0]&0x1f != 28 {
			t.Fatalf("packet %d is not FU-A", i)
		}
		if pkts[i].Header.SequenceNumber != uint16(100+i) {
			t.Fatalf("sequence not incremented: %d", pkts[i].Header.SequenceNumber)
		}
		if len(pkts[i].Payload)+RTP_FIX_HEAD_LEN > 1400 {
			t.Fatalf("packet %d exceeds mtu", i)
		}
	}
	if pkts[1].Payload[1]&0x80 == 0 || pkts[len(pkts)-1].Payload[1]&0x40 == 0 {
		t.Fatalf("FU-A start/end bits missing")
	}
	// reassemble
	up := NewH264UnPacker()
	var got []byte
	up.OnFrame(func(f []byte, ts uint32, lost bool) {
		if lost {
			t.Fatalf("unexpected lost flag")
		}
		got = append([]byte(nil), f...)
	})
	for _, p := range pkts {
		if err := up.UnPack(p.Encode()); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got[4:], big) {
		t.Fatalf("reassembled nalu mismatch")
	}
	// empty nalu must not panic
	if err := pk.packFuA(nil, 1, true); err == nil {
		t.Fatalf("empty nalu should error")
	}
}

func rtpWithPayload(payload []byte) []byte {
	p := RtpPacket{}
	p.Header.PayloadType = 96
	p.Payload = payload
	return p.Encode()
}

func TestH264UnpackBounds(t *testing.T) {
	up := NewH264UnPacker()
	if err := up.UnPack(rtpWithPayload([]byte{0x7c})); err == nil {
		t.Fatalf("FU-A with 1 byte must error")
	}
	if err := up.UnPack(rtpWithPayload([]byte{0x78, 0, 10, 0x67})); err == nil {
		t.Fatalf("STAP-A with oversized nalu must error")
	}
	if err := up.UnPack(rtpWithPayload([]byte{0x78, 0})); err == nil {
		t.Fatalf("STAP-A with truncated size must error")
	}
	if err := up.UnPack(rtpWithPayload(nil)); err != nil {
		t.Fatalf("empty payload: %v", err)
	}
}

func TestH265FuHeader(t *testing.T) {
	nalu := make([]byte, 3000)
	nalu[0] = 19 << 1 // IDR_W_RADL
	nalu[1] = 1
	pk := NewH265Packer(97, 1, 5, 1400)
	var pkts []RtpPacket
	pk.OnPacket(func(b []byte) error {
		var p RtpPacket
		if err := p.Decode(b); err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, p)
		return nil
	})
	if err := pk.Pack(h264Frame(nalu), 1); err != nil {
		t.Fatal(err)
	}
	if len(pkts) != 3 {
		t.Fatalf("want 3 fragments, got %d", len(pkts))
	}
	for i, p := range pkts {
		if p.Payload[0]>>1&0x3f != 49 {
			t.Fatalf("fragment %d not FU", i)
		}
		if p.Payload[2]&0x3f != 19 {
			t.Fatalf("fragment %d FU header lost FuType: %02x", i, p.Payload[2])
		}
		if p.Header.SequenceNumber != uint16(5+i) {
			t.Fatalf("sequence %d", p.Header.SequenceNumber)
		}
	}
	if pkts[0].Payload[2]&0x80 == 0 || pkts[1].Payload[2]&0xc0 != 0 || pkts[2].Payload[2]&0x40 == 0 {
		t.Fatalf("S/E bits wrong: %02x %02x %02x", pkts[0].Payload[2], pkts[1].Payload[2], pkts[2].Payload[2])
	}
	up := NewH265UnPacker()
	var got []byte
	up.OnFrame(func(f []byte, ts uint32, lost bool) { got = append([]byte(nil), f...) })
	for _, p := range pkts {
		if err := up.UnPack(p.Encode()); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got[4:], nalu) {
		t.Fatalf("h265 reassembly mismatch")
	}
	if err := up.UnPack(rtpWithPayload(nil)); err != nil {
		t.Fatalf("empty payload: %v", err)
	}
	if err := up.UnPack(rtpWithPayload([]byte{49 << 1, 1})); err == nil {
		t.Fatalf("FU with 2 bytes must error")
	}
}

func TestAACUnpackBogusHeaderLength(t *testing.T) {
	up := NewAACUnPacker(13, 3, nil)
	// AU-headers-length claims 800 bits but payload has only 2 more bytes
	payload := []byte{0x03, 0x20, 0x00, 0x10}
	if err := up.UnPack(rtpWithPayload(payload)); err == nil {
		t.Fatalf("bogus AU-headers-length must error")
	}
	// AU size larger than the payload
	payload = []byte{0x00, 0x10, 0xff, 0xf8, 1, 2, 3}
	if err := up.UnPack(rtpWithPayload(payload)); err == nil {
		t.Fatalf("oversized AU must error")
	}
	// valid
	var frames int
	up.OnFrame(func(f []byte, ts uint32, lost bool) {
		frames++
		if !bytes.Equal(f, []byte{1, 2, 3}) {
			t.Fatalf("frame %x", f)
		}
	})
	payload = []byte{0x00, 0x10, 0x00, 0x18, 1, 2, 3}
	if err := up.UnPack(rtpWithPayload(payload)); err != nil || frames != 1 {
		t.Fatalf("valid AU: %v frames=%d", err, frames)
	}
	// bogus asc must return an error, not panic
	up2 := NewAACUnPacker(13, 3, []byte{0x00})
	if err := up2.UnPack(rtpWithPayload(payload)); err == nil {
		t.Fatalf("bad asc must error")
	}
	// aac packer round trip through its own output
	pk := NewAACPacker(97, 1, 1, 1400)
	var out []byte
	pk.OnPacket(func(b []byte) error { out = b; return nil })
	if err := pk.Pack([]byte{1, 2, 3}, 0); err != nil {
		t.Fatal(err)
	}
	frames = 0
	if err := up.UnPack(out); err != nil || frames != 1 {
		t.Fatalf("packer output: %v frames=%d", err, frames)
	}
}

func TestG711AndTsSequence(t *testing.T) {
	g := NewG711Packer(0, 1, 7, 1400)
	var seqs []uint16
	g.OnPacket(func(b []byte) error {
		seqs = append(seqs, binary.BigEndian.Uint16(b[2:]))
		return nil
	})
	_ = g.Pack([]byte{1, 2}, 0)
	_ = g.Pack([]byte{1, 2}, 160)
	if len(seqs) != 2 || seqs[0] != 7 || seqs[1] != 8 {
		t.Fatalf("g711 sequence %v", seqs)
	}

	ts := NewTsPacker(33, 1, 0, 200)
	seqs = nil
	total := 0
	ts.OnPacket(func(b []byte) error {
		seqs = append(seqs, binary.BigEndian.Uint16(b[2:]))
		total += len(b) - RTP_FIX_HEAD_LEN
		return nil
	})
	data := make([]byte, 188*7)
	if err := ts.Pack(data, 0); err != nil {
		t.Fatal(err)
	}
	if total != len(data) {
		t.Fatalf("ts packer sent %d of %d bytes", total, len(data))
	}
	for i, s := range seqs {
		if s != uint16(i) {
			t.Fatalf("ts sequence %v", seqs)
		}
	}
}

func TestRtpEncodeWithCSRC(t *testing.T) {
	p := RtpPacket{}
	p.Header.CSRC = []uint32{1, 2}
	p.Payload = []byte{9}
	b := p.Encode()
	var d RtpPacket
	if err := d.Decode(b); err != nil {
		t.Fatal(err)
	}
	if d.Header.CC != 2 || len(d.Header.CSRC) != 2 || d.Header.CSRC[1] != 2 || !bytes.Equal(d.Payload, []byte{9}) {
		t.Fatalf("csrc round trip %+v", d)
	}
}
