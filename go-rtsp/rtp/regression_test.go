package rtp

import "testing"

// The rtp marker bit delimits an access unit: exactly the last packet of a
// frame carries it, whatever mix of single nalu / STAP-A / FU packets the
// frame was split into.
func TestMarkerOnlyOnLastPacketOfAccessUnit(t *testing.T) {
	nalu := func(t byte, n int) []byte {
		b := []byte{0, 0, 0, 1, t, 0x80}
		for i := 0; i < n; i++ {
			b = append(b, 0x42)
		}
		return b
	}
	frame := append(nalu(0x67, 10), nalu(0x68, 6)...)
	frame = append(frame, nalu(0x06, 20)...)   // sei
	frame = append(frame, nalu(0x65, 4000)...) // idr, needs FU-A
	frame = append(frame, nalu(0x65, 30)...)   // second slice

	for _, stapa := range []bool{false, true} {
		var markers []bool
		pk := NewH264Packer(96, 1, 0, 1400)
		if stapa {
			pk.EnableStapA()
		}
		pk.HookRtp(func(p *RtpPacket) { markers = append(markers, p.Header.Marker == 1) })
		if err := pk.Pack(frame, 1000); err != nil {
			t.Fatal(err)
		}
		if len(markers) < 3 {
			t.Fatalf("stapa=%v: expected several packets, got %d", stapa, len(markers))
		}
		set := 0
		for _, m := range markers {
			if m {
				set++
			}
		}
		if set != 1 || !markers[len(markers)-1] {
			t.Errorf("stapa=%v: %d packets, %d markers set, last=%v; want exactly 1 on the last packet",
				stapa, len(markers), set, markers[len(markers)-1])
		}
	}
}

func TestH265MarkerOnlyOnLastPacketOfAccessUnit(t *testing.T) {
	nalu := func(hi, lo byte, n int) []byte {
		b := []byte{0, 0, 0, 1, hi, lo, 0x80}
		for i := 0; i < n; i++ {
			b = append(b, 0x42)
		}
		return b
	}
	frame := append(nalu(0x40, 0x01, 10), nalu(0x42, 0x01, 12)...) // vps, sps
	frame = append(frame, nalu(0x44, 0x01, 8)...)                  // pps
	frame = append(frame, nalu(0x26, 0x01, 4000)...)               // idr, needs FU

	var markers []bool
	pk := NewH265Packer(96, 1, 0, 1400)
	pk.HookRtp(func(p *RtpPacket) { markers = append(markers, p.Header.Marker == 1) })
	if err := pk.Pack(frame, 1000); err != nil {
		t.Fatal(err)
	}
	set := 0
	for _, m := range markers {
		if m {
			set++
		}
	}
	if set != 1 || !markers[len(markers)-1] {
		t.Errorf("%d packets, %d markers set; want exactly 1 on the last packet", len(markers), set)
	}
}
