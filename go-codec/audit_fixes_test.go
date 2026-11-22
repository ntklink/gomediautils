package codec

import (
	"bytes"
	"testing"
)

func TestSEIDecode0xFF(t *testing.T) {
	// PayloadType = 255 + 5 = 260, PayloadSize = 255 + 2 = 257
	bs := NewBitStream([]byte{0xFF, 0x05, 0xFF, 0x02})
	var sei SEI
	sei.Decode(bs)
	if bs.Err() != nil {
		t.Fatalf("unexpected err: %v", bs.Err())
	}
	if sei.PayloadType != 260 || sei.PayloadSize != 257 {
		t.Fatalf("got type=%d size=%d, want 260, 257", sei.PayloadType, sei.PayloadSize)
	}

	// Truncated stream of 0xFF must terminate with error, not hang.
	bs2 := NewBitStream([]byte{0xFF, 0xFF})
	var sei2 SEI
	sei2.Decode(bs2)
	if bs2.Err() == nil {
		t.Fatalf("expected error on truncated 0xFF SEI")
	}
}

func TestH265ParseRPSOverflowAndBounds(t *testing.T) {
	// Exceeds H.265 max 16 reference pictures
	w := newTestBsw(16)
	w.PutUE(17) // num_negative_pics
	w.PutUE(0)  // num_positive_pics
	w.PutUint64(0, 64)
	bs := NewBitStream(w.Bits())
	var delta [64]uint32
	parse_rps(0, 1, &delta, bs)
	if bs.Err() == nil {
		t.Fatalf("expected error for num_negative_pics > 16")
	}

	// Valid count (2) but insufficient remaining bits
	w2 := newTestBsw(8)
	w2.PutUE(2)
	w2.PutUE(0)
	bs2 := NewBitStream(w2.Bits())
	parse_rps(0, 1, &delta, bs2)
	if bs2.Err() == nil {
		t.Fatalf("expected error for insufficient bits in parse_rps")
	}
}

func TestBitStreamWriterPutByteUnaligned(t *testing.T) {
	bsw := NewBitStreamWriter(8)
	bsw.PutUint8(0x05, 3) // 101
	bsw.PutByte(0xAB)     // 10101011
	bsw.PutUint8(0x03, 5) // 00011
	if bsw.Err() != nil {
		t.Fatalf("unexpected err: %v", bsw.Err())
	}
	bs := NewBitStream(bsw.Bits())
	if got := bs.Uint8(3); got != 0x05 {
		t.Fatalf("got %#x, want 0x05", got)
	}
	if got := bs.Uint8(8); got != 0xAB {
		t.Fatalf("got %#x, want 0xAB", got)
	}
	if got := bs.Uint8(5); got != 0x03 {
		t.Fatalf("got %#x, want 0x03", got)
	}

	// PutByte after writer error must be a no-op
	bsw2 := NewBitStreamWriter(4)
	bsw2.PutUint8(1, 1)
	bsw2.PutBytes([]byte{0xFF}) // triggers ErrNotByteAligned
	before := append([]byte(nil), bsw2.Bits()...)
	bsw2.PutByte(0x55)
	if !bytes.Equal(bsw2.Bits(), before) {
		t.Fatalf("PutByte modified buffer after error")
	}
}

func TestOpusCELT5msDuration(t *testing.T) {
	// Config 17 = CELT NB 5ms (code 0 => 1 frame)
	if got := OpusPacketDuration([]byte{17 << 3}); got != 240 {
		t.Fatalf("got %d, want 240", got)
	}
}

func buildSyntheticH265SPS(w, h, cropBottom, log2MaxPocMinus4 uint64) []byte {
	b := newTestBsw(64)
	// 2-byte HEVC NALU header: forbidden=0, type=33 (SPS), layer=0, tid=1
	b.PutUint8(0, 1)
	b.PutUint8(33, 6)
	b.PutUint8(0, 6)
	b.PutUint8(1, 3)

	b.PutUint8(0, 4) // sps_video_parameter_set_id
	b.PutUint8(0, 3) // sps_max_sub_layers_minus1
	b.PutBit(1)      // sps_temporal_id_nesting_flag

	// profile_tier_level(1, 0): 11 bytes + 1 byte level_idc = 96 bits
	b.PutUint64(0, 64)
	b.PutUint32(0, 32)

	b.PutUE(0) // sps_seq_parameter_set_id
	b.PutUE(1) // chroma_format_idc = 1 (4:2:0)
	b.PutUE(w)
	b.PutUE(h)
	if cropBottom > 0 {
		b.PutBit(1) // conformance_window_flag
		b.PutUE(0)  // left
		b.PutUE(0)  // right
		b.PutUE(0)  // top
		b.PutUE(cropBottom)
	} else {
		b.PutBit(0)
	}
	b.PutUE(0) // bit_depth_luma_minus8
	b.PutUE(0) // bit_depth_chroma_minus8
	b.PutUE(log2MaxPocMinus4)
	b.PutBit(0) // sps_sub_layer_ordering_info_present_flag
	b.PutUE(0)
	b.PutUE(0)
	b.PutUE(0)
	// coding block / transform block / hierarchy depths (6 UEs)
	for i := 0; i < 6; i++ {
		b.PutUE(0)
	}
	b.PutBit(0) // scaling_list_enabled_flag
	b.PutBit(0) // amp_enabled_flag
	b.PutBit(0) // sample_adaptive_offset_enabled_flag
	b.PutBit(0) // pcm_enabled_flag
	b.PutUE(0)  // num_short_term_ref_pic_sets
	b.PutBit(0) // long_term_ref_pics_present_flag
	b.PutBit(0) // sps_temporal_mvp_enabled_flag
	b.PutBit(0) // strong_intra_smoothing_enabled_flag
	b.PutBit(0) // vui_parameters_present_flag
	b.PutBit(1) // rbsp trailing stop bit
	return b.Bits()
}

func TestGetH265ResolutionConformanceWindow(t *testing.T) {
	sps := buildSyntheticH265SPS(1920, 1088, 4, 4)
	w, h, err := GetH265Resolution(sps)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if w != 1920 || h != 1080 {
		t.Fatalf("got %dx%d, want 1920x1080", w, h)
	}

	// Oversized crop must not underflow
	spsBadCrop := buildSyntheticH265SPS(16, 16, 100, 4)
	w, h, err = GetH265Resolution(spsBadCrop)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if w != 16 || h != 16 {
		t.Fatalf("got %dx%d, want 16x16", w, h)
	}
}

func TestADTSDecodeIDMask(t *testing.T) {
	var h ADTS_Frame_Header
	if err := h.Decode([]byte{0xFF, 0xF1, 0x50, 0x80, 0x01, 0x1F, 0xFC}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.Fix_Header.ID != 0 {
		t.Fatalf("got ID=%d, want 0", h.Fix_Header.ID)
	}
	if err := h.Decode([]byte{0xFF, 0xF9, 0x50, 0x80, 0x01, 0x1F, 0xFC}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.Fix_Header.ID != 1 {
		t.Fatalf("got ID=%d, want 1", h.Fix_Header.ID)
	}
}

func TestMP3BitRateTableAndShortHeader(t *testing.T) {
	if BitRateTable[0][1][14] != 384 {
		t.Fatalf("BitRateTable[0][1][14] = %d, want 384", BitRateTable[0][1][14])
	}
	for n := 0; n < 4; n++ {
		if _, err := DecodeMp3Head(make([]byte, n)); err == nil {
			t.Fatalf("expected error for %d-byte MP3 header", n)
		}
	}
}

func TestHEVCRecordConfigurationUpdateVPSTemporalLayers(t *testing.T) {
	b := newTestBsw(64)
	// 2-byte HEVC NALU header: type=32 (VPS)
	b.PutUint8(0, 1)
	b.PutUint8(32, 6)
	b.PutUint8(0, 6)
	b.PutUint8(1, 3)

	b.PutUint8(0, 4) // vps_video_parameter_set_id
	b.PutUint8(3, 2) // vps_reserved_three_2bits
	b.PutUint8(3, 6) // vps_max_layers_minus1 = 3 (4 layers)
	b.PutUint8(1, 3) // vps_max_sub_layers_minus1 = 1 (2 temporal layers)
	b.PutBit(1)      // vps_temporal_id_nesting_flag
	b.PutUint16(0xFFFF, 16)
	// profile_tier_level(1, 1): 12 bytes + 16 bits sub_layer flags/reserved
	b.PutUint64(0, 64)
	b.PutUint32(0, 32)
	b.PutUint16(0, 16)
	b.PutBit(0)       // vps_sub_layer_ordering_info_present_flag
	b.PutUE(0)        // vps_max_dec_pic_buffering_minus1
	b.PutUE(0)        // vps_max_num_reorder_pics
	b.PutUE(0)        // vps_max_latency_increase_plus1
	b.PutUint8(0, 6)  // vps_max_layer_id
	b.PutUE(0)        // vps_num_layer_sets_minus1
	b.PutBit(0)       // vps_timing_info_present_flag
	b.PutBit(1)       // rbsp stop bit

	hvcc := NewHEVCRecordConfiguration()
	if err := hvcc.UpdateVPS(b.Bits()); err != nil {
		t.Fatalf("UpdateVPS: %v", err)
	}
	if hvcc.NumTemporalLayers != 2 {
		t.Fatalf("NumTemporalLayers = %d, want 2", hvcc.NumTemporalLayers)
	}
}

func TestSPSLog2MaxBounds(t *testing.T) {
	// H.265 SPS with Log2_max_pic_order_cnt_lsb_minus4 = 13
	h265Bad := buildSyntheticH265SPS(64, 64, 0, 13)
	var s265 H265RawSPS
	if err := s265.Decode(h265Bad); err == nil {
		t.Fatalf("expected error for H.265 log2_max_pic_order_cnt_lsb_minus4 > 12")
	}

	// H.264 SPS with Log2_max_frame_num_minus4 = 13
	b := newTestBsw(32)
	b.PutUint8(66, 8) // Baseline profile
	b.PutUint8(0, 8)  // constraint flags
	b.PutUint8(30, 8) // level_idc
	b.PutUE(0)        // sps_id
	b.PutUE(13)       // log2_max_frame_num_minus4 > 12
	bs := NewBitStream(b.Bits())
	var s264 SPS
	s264.Decode(bs)
	if bs.Err() == nil {
		t.Fatalf("expected error for H.264 log2_max_frame_num_minus4 > 12")
	}

	// H.264 SPS with Log2_max_pic_order_cnt_lsb_minus4 = 13
	b2 := newTestBsw(32)
	b2.PutUint8(66, 8)
	b2.PutUint8(0, 8)
	b2.PutUint8(30, 8)
	b2.PutUE(0)  // sps_id
	b2.PutUE(0)  // log2_max_frame_num_minus4
	b2.PutUE(0)  // pic_order_cnt_type = 0
	b2.PutUE(13) // log2_max_pic_order_cnt_lsb_minus4 > 12
	bs2 := NewBitStream(b2.Bits())
	var s264b SPS
	s264b.Decode(bs2)
	if bs2.Err() == nil {
		t.Fatalf("expected error for H.264 log2_max_pic_order_cnt_lsb_minus4 > 12")
	}
}
