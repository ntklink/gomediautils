package codec

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

// reference implementation of the old bit-stream based CovertRbspToSodb
// (with the trailing-sequence fix), used to validate the fast byte loop.
func oldCovertRbspToSodb(rbsp []byte) []byte {
	bs := NewBitStream(rbsp)
	bsw := NewBitStreamWriter(len(rbsp))
	for !bs.EOS() {
		if bs.RemainBytes() >= 3 && bs.NextBits(24) == 0x000003 {
			bsw.PutByte(bs.Uint8(8))
			bsw.PutByte(bs.Uint8(8))
			bs.SkipBits(8)
		} else {
			bsw.PutByte(bs.Uint8(8))
		}
	}
	return bsw.Bits()
}

// small exp-golomb writer helpers for building synthetic parameter sets
type testBsw struct{ *BitStreamWriter }

func newTestBsw(n int) *testBsw { return &testBsw{NewBitStreamWriter(n)} }

func (b *testBsw) PutBit(v uint8) { b.PutUint8(v, 1) }

func (b *testBsw) PutUE(v uint64) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	for i := 0; i < n; i++ {
		b.PutBit(0)
	}
	b.PutUint64(v, n+1)
}

func (b *testBsw) PutSE(v int64) {
	if v > 0 {
		b.PutUE(uint64(2*v - 1))
	} else {
		b.PutUE(uint64(-2 * v))
	}
}

func (b *testBsw) Flush() {}

func TestCovertRbspToSodbEquivalence(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	alphabet := []byte{0x00, 0x00, 0x00, 0x03, 0x01, 0xFF}
	for n := 0; n < 5000; n++ {
		l := r.Intn(40)
		in := make([]byte, l)
		for i := range in {
			in[i] = alphabet[r.Intn(len(alphabet))]
		}
		got := CovertRbspToSodb(in)
		want := oldCovertRbspToSodb(in)
		if !bytes.Equal(got, want) {
			t.Fatalf("input %x: got %x want %x", in, got, want)
		}
	}
	// trailing 00 00 03 must be stripped
	if got := CovertRbspToSodb([]byte{0x11, 0x00, 0x00, 0x03}); !bytes.Equal(got, []byte{0x11, 0x00, 0x00}) {
		t.Fatalf("trailing emulation byte not removed: %x", got)
	}
	// 00 00 03 00 00 03 -> 00 00 00 00
	if got := CovertRbspToSodb([]byte{0x00, 0x00, 0x03, 0x00, 0x00, 0x03}); !bytes.Equal(got, []byte{0x00, 0x00, 0x00, 0x00}) {
		t.Fatalf("double emulation: %x", got)
	}
}

func TestSplitAACFrameZeroLengthTerminates(t *testing.T) {
	// ADTS header with frame_length == 0
	frames := []byte{0xFF, 0xF1, 0x50, 0x80, 0x00, 0x00, 0xFC, 0x01, 0x02, 0x03}
	count := 0
	done := make(chan struct{})
	go func() {
		_ = SplitAACFrame(frames, func(aac []byte) { count++ })
		close(done)
	}()
	<-done
	if count != 0 {
		t.Fatalf("expected no frames, got %d", count)
	}
	if err := SplitAACFrame(frames, nil); err == nil {
		t.Fatalf("expected error for zero frame_length")
	}
	// truncated frame
	hdr := NewAdtsFrameHeader()
	hdr.Variable_Header.Frame_length = 100
	if err := SplitAACFrame(hdr.Encode(), nil); err == nil {
		t.Fatalf("expected error for truncated frame")
	}
	// valid: two frames back to back
	hdr.Variable_Header.Frame_length = 9
	one := append(hdr.Encode(), 0xAA, 0xBB)
	count = 0
	err := SplitAACFrame(append(append([]byte{}, one...), one...), func(aac []byte) {
		count++
		if len(aac) != 9 {
			t.Fatalf("bad frame len %d", len(aac))
		}
	})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 frames, got %d", count)
	}
}

func TestADTSHeaderRoundTrip(t *testing.T) {
	for _, l := range []uint16{7, 100, 2047, 2048, 4095, 8191} {
		hdr := NewAdtsFrameHeader()
		hdr.Fix_Header.Profile = uint8(LC)
		hdr.Fix_Header.Channel_configuration = 2
		hdr.Variable_Header.Frame_length = l
		hdr.Variable_Header.Adts_buffer_fullness = 0x7FF
		hdr.Variable_Header.Copyright_identification_bit = 1
		hdr.Variable_Header.Number_of_raw_data_blocks_in_frame = 3
		var dec ADTS_Frame_Header
		dec.Decode(hdr.Encode())
		if dec.Variable_Header.Frame_length != l {
			t.Fatalf("frame_length %d -> %d", l, dec.Variable_Header.Frame_length)
		}
		if dec.Variable_Header.Adts_buffer_fullness != 0x7FF {
			t.Fatalf("fullness %x", dec.Variable_Header.Adts_buffer_fullness)
		}
		if dec.Variable_Header.Copyright_identification_bit != 1 || dec.Variable_Header.Number_of_raw_data_blocks_in_frame != 3 {
			t.Fatalf("copyright/raw blocks mismatch: %+v", dec.Variable_Header)
		}
		if dec.Fix_Header.Channel_configuration != 2 || dec.Fix_Header.Profile != uint8(LC) {
			t.Fatalf("fix header mismatch: %+v", dec.Fix_Header)
		}
	}
	asc := (&AudioSpecificConfiguration{Audio_object_type: 2, Sample_freq_index: 4, Channel_configuration: 2}).Encode()
	adts, err := ConvertASCToADTS(asc, 300)
	if err != nil {
		t.Fatal(err)
	}
	if adts.Variable_Header.Adts_buffer_fullness != 0x7FF {
		t.Fatalf("ConvertASCToADTS fullness = %x, want 0x7FF", adts.Variable_Header.Adts_buffer_fullness)
	}
}

func TestMp3Head(t *testing.T) {
	// MPEG1 layer3, 128kbps, 44100, mono
	head, err := DecodeMp3Head([]byte{0xFF, 0xFB, 0x90, 0xC0})
	if err != nil {
		t.Fatal(err)
	}
	if head.GetChannelCount() != 1 {
		t.Fatalf("mono expected, got %d", head.GetChannelCount())
	}
	if head.FrameSize != 417 {
		t.Fatalf("frame size %d", head.FrameSize)
	}
	// stereo
	head, _ = DecodeMp3Head([]byte{0xFF, 0xFB, 0x90, 0x00})
	if head.GetChannelCount() != 2 {
		t.Fatalf("stereo expected")
	}
	bad := [][]byte{
		{0xFF, 0xEB, 0x90, 0x00}, // reserved version
		{0xFF, 0xF9, 0x90, 0x00}, // reserved layer
		{0xFF, 0xFB, 0x9C, 0x00}, // reserved samplerate index
		{0xFF, 0xFB, 0xF0, 0x00}, // bad bitrate index
		{0xFF, 0xFB, 0x00, 0x00}, // free bitrate
	}
	for _, b := range bad {
		if _, err := DecodeMp3Head(b); err == nil {
			t.Fatalf("expected error for %x", b)
		}
	}
	// SplitMp3Frames must terminate and not panic on these
	for _, b := range bad {
		if err := SplitMp3Frames(b, nil); err == nil {
			t.Fatalf("expected error for %x", b)
		}
	}
	// truncated frame
	if err := SplitMp3Frames([]byte{0xFF, 0xFB, 0x90, 0xC0, 0x00}, nil); err == nil {
		t.Fatal("expected truncation error")
	}
	// ID3v2 size uses all 4 syncsafe bytes
	id3 := []byte{'I', 'D', '3', 3, 0, 0, 0, 0, 0x01, 0x00}
	id3 = append(id3, make([]byte, 128)...)
	frame := append([]byte{0xFF, 0xFB, 0x90, 0xC0}, make([]byte, 413)...)
	n := 0
	if err := SplitMp3Frames(append(id3, frame...), func(h *MP3FrameHead, f []byte) { n++ }); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 frame, got %d", n)
	}
	if err := SplitMp3Frames([]byte{'I', 'D', '3', 3, 0, 0, 0, 0, 0x7F, 0x7F}, nil); err == nil {
		t.Fatal("expected error for oversized ID3 tag")
	}
}

func TestOpusPacket(t *testing.T) {
	// code 2 with 2-byte length: N1 = 252 + 1*4 = 256
	pkt := make([]byte, 3+256+10)
	pkt[0] = 0x02
	pkt[1] = 252
	pkt[2] = 1
	p := DecodeOpusPacket(pkt)
	if p.FrameCount != 2 || p.FrameLen[0] != 256 || p.FrameLen[1] != 10 || len(p.Frame) != 266 {
		t.Fatalf("code2: %+v", p)
	}
	if p.Duration != uint64(2*SLKOpusSampleSize[0]) {
		t.Fatalf("duration %d", p.Duration)
	}
	// code 3 CBR, 3 frames of 4 bytes, frame count uses 6 bits (M=35 -> 0x23)
	pkt = []byte{0x03, 0x23}
	pkt = append(pkt, make([]byte, 35*2)...)
	p = DecodeOpusPacket(pkt)
	if p.FrameCount != 35 || p.FrameLen[0] != 2 || len(p.Frame) != 70 {
		t.Fatalf("code3 cbr: %+v", p)
	}
	if OpusPacketDuration(pkt) != uint64(35*480) {
		t.Fatalf("duration %d", OpusPacketDuration(pkt))
	}
	// code 3 CBR with M=0 must not divide by zero
	p = DecodeOpusPacket([]byte{0x03, 0x00, 1, 2})
	if p.FrameCount != 0 {
		t.Fatalf("M=0: %+v", p)
	}
	// code 3 VBR with padding: 2 frames (3 bytes, 2 bytes) + 1 padding byte
	pkt = []byte{0x03, 0xC2, 0x01, 0x03, 1, 2, 3, 4, 5, 0}
	p = DecodeOpusPacket(pkt)
	if p.FrameCount != 2 || p.FrameLen[0] != 3 || p.FrameLen[1] != 2 || len(p.Frame) != 5 {
		t.Fatalf("code3 vbr: %+v", p)
	}
	// truncated inputs must not panic
	for _, b := range [][]byte{{}, {0x02}, {0x02, 252}, {0x03}, {0x03, 0x42}, {0x03, 0x82, 5}} {
		_ = DecodeOpusPacket(b)
		_ = OpusPacketDuration(b)
	}
}

func TestOpusExtraData(t *testing.T) {
	var ctx OpusContext
	if err := ctx.ParseExtranData([]byte("OpusHead")); err == nil {
		t.Fatal("expected error for short extradata")
	}
	// family 0, stereo
	ed := WriteDefaultOpusExtraData()[:19]
	ed[9] = 2
	if err := ctx.ParseExtranData(ed); err != nil {
		t.Fatal(err)
	}
	if len(ctx.ChannelMaps) != 2 || ctx.ChannelMaps[1].ChannelIdx != RIGHT_CHANNEL {
		t.Fatalf("%+v", ctx.ChannelMaps)
	}
	// family 0 with 3 channels is invalid
	ed[9] = 3
	if err := ctx.ParseExtranData(ed); err == nil {
		t.Fatal("expected error for family 0 with 3 channels")
	}
	// family 1, 3 channels, mapping table truncated
	ed = append(ed[:19], 1, 2, 1)
	ed[18] = 1
	if err := ctx.ParseExtranData(ed); err == nil {
		t.Fatal("expected error for truncated mapping table")
	}
	// family 1, 3 channels: streams=2, stereo=1, map {0, 255, 0} (channel 2 copies channel 0)
	ed = append(ed[:19], 2, 1, 0, 255, 0)
	ctx = OpusContext{}
	if err := ctx.ParseExtranData(ed); err != nil {
		t.Fatal(err)
	}
	if len(ctx.ChannelMaps) != 3 {
		t.Fatalf("ChannelMaps len %d", len(ctx.ChannelMaps))
	}
	// vorbis order for 3 channels is {0,2,1}: output 0 <- map[0]=0, output 1 <- map[2]=0 (copy of 0), output 2 <- map[1]=255
	if ctx.ChannelMaps[0].Copy || !ctx.ChannelMaps[1].Copy || ctx.ChannelMaps[1].CopyFrom != 0 || !ctx.ChannelMaps[2].Silence {
		t.Fatalf("%+v", ctx.ChannelMaps)
	}
	// index == stream count + stereo count is out of range
	ed = append(ed[:19], 2, 1, 0, 3, 1)
	if err := ctx.ParseExtranData(ed); err == nil {
		t.Fatal("expected index range error")
	}
}

func TestH264CropAndVui(t *testing.T) {
	// 1920x1080 sample sps (profile 100, 4:2:0, frame_crop_bottom_offset = 4)
	w, h, _ := GetH264Resolution(sps2)
	if w != 1920 || h != 1080 {
		t.Fatalf("%dx%d", w, h)
	}
	// synthesize a baseline sps (profile 66) 320x240 with top crop 2, bottom crop 2 -> 320x232
	bsw := newTestBsw(64)
	bsw.PutUint8(66, 8)
	bsw.PutUint8(0, 8)
	bsw.PutUint8(30, 8)
	bsw.PutUE(0)  // sps id
	bsw.PutUE(0)  // log2_max_frame_num_minus4
	bsw.PutUE(2)  // pic_order_cnt_type
	bsw.PutUE(1)  // max_num_ref_frames
	bsw.PutBit(0) // gaps
	bsw.PutUE(19) // width mbs - 1
	bsw.PutUE(14) // height map units - 1
	bsw.PutBit(1) // frame_mbs_only
	bsw.PutBit(1) // direct_8x8
	bsw.PutBit(1) // cropping
	bsw.PutUE(0)
	bsw.PutUE(0)
	bsw.PutUE(2)
	bsw.PutUE(2)
	bsw.PutBit(0) // vui
	bsw.PutBit(1) // rbsp stop
	bsw.Flush()
	sps := append([]byte{0x00, 0x00, 0x00, 0x01, 0x67}, bsw.Bits()...)
	w, h, _ = GetH264Resolution(sps)
	if w != 320 || h != 232 {
		t.Fatalf("crop: %dx%d", w, h)
	}

	// pic_order_cnt_type == 1 must allocate Offset_for_ref_frame
	bsw = newTestBsw(64)
	bsw.PutUint8(66, 8)
	bsw.PutUint8(0, 8)
	bsw.PutUint8(30, 8)
	bsw.PutUE(0)
	bsw.PutUE(0)
	bsw.PutUE(1)  // pic_order_cnt_type
	bsw.PutBit(0) // delta_pic_order_always_zero
	bsw.PutSE(0)
	bsw.PutSE(0)
	bsw.PutUE(2) // num_ref_frames_in_pic_order_cnt_cycle
	bsw.PutSE(3)
	bsw.PutSE(-1)
	bsw.PutUE(1)
	bsw.PutBit(0)
	bsw.PutUE(19)
	bsw.PutUE(14)
	bsw.PutBit(1)
	bsw.PutBit(1)
	bsw.PutBit(0)
	bsw.PutBit(1) // vui present
	bsw.PutBit(1) // aspect_ratio_info_present
	bsw.PutUint8(ExtendedSar, 8)
	bsw.PutUint16(16, 16)
	bsw.PutUint16(11, 16)
	bsw.PutBit(0) // overscan
	bsw.PutBit(0) // video signal
	bsw.PutBit(0) // chroma loc
	bsw.PutBit(0) // timing
	bsw.PutBit(0) // nal hrd
	bsw.PutBit(0) // vcl hrd
	bsw.PutBit(1)
	bsw.Flush()
	var s SPS
	s.Decode(NewBitStream(bsw.Bits()))
	if len(s.Offset_for_ref_frame) != 2 || s.Offset_for_ref_frame[0] != 3 || s.Offset_for_ref_frame[1] != -1 {
		t.Fatalf("offset_for_ref_frame: %v", s.Offset_for_ref_frame)
	}
	if s.VuiParameters.SarWidth != 16 || s.VuiParameters.SarHeight != 11 {
		t.Fatalf("sar %d:%d", s.VuiParameters.SarWidth, s.VuiParameters.SarHeight)
	}
}

func TestConvertAnnexBToAVCCDoesNotMutate(t *testing.T) {
	in := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0xAA, 0xBB}
	orig := append([]byte{}, in...)
	out := ConvertAnnexBToAVCC(in)
	if !bytes.Equal(in, orig) {
		t.Fatal("caller buffer mutated")
	}
	if !bytes.Equal(out, []byte{0x00, 0x00, 0x00, 0x03, 0x65, 0xAA, 0xBB}) {
		t.Fatalf("%x", out)
	}
	out = ConvertAnnexBToAVCC([]byte{0x00, 0x00, 0x01, 0x65, 0xAA})
	if !bytes.Equal(out, []byte{0x00, 0x00, 0x00, 0x02, 0x65, 0xAA}) {
		t.Fatalf("%x", out)
	}
}

func TestSplitFrameNoEmptyNalu(t *testing.T) {
	in := []byte{0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x01, 0x65, 0x01, 0x00, 0x00, 0x01}
	SplitFrame(in, func(nalu []byte) bool {
		if len(nalu) == 0 {
			t.Fatal("empty nalu emitted")
		}
		return true
	})
	if !IsH264IDRFrame(in) {
		t.Fatal("IDR not detected")
	}
	if IsH265IDRFrame([]byte{0x00, 0x00, 0x01}) {
		t.Fatal("unexpected idr")
	}
}

// A read past the end must not panic: it puts the stream into a sticky error
// state and every later read is a no-op.
func TestBitStreamOutOfRangeIsSticky(t *testing.T) {
	bs := NewBitStream([]byte{0x00, 0x01})
	bs.SkipBits(17)
	if bs.Err() == nil {
		t.Fatal("SkipBits past the end must set the error state")
	}
	if bs.ByteOffset() != 0 {
		t.Fatalf("failed skip consumed data, offset %d", bs.ByteOffset())
	}
	if !bs.EOS() {
		t.Fatal("a stream in the error state must report EOS")
	}

	for _, tc := range []struct {
		name string
		read func(*BitStream)
	}{
		{"GetBits", func(b *BitStream) { b.GetBits(64) }},
		{"GetBit at end", func(b *BitStream) { b.SkipBits(16); b.GetBit() }},
		{"GetBytes", func(b *BitStream) { b.GetBytes(8) }},
		{"NextBits", func(b *BitStream) { b.NextBits(40) }},
		{"ReadUE", func(b *BitStream) { b.ReadUE() }},
		{"ReadSE", func(b *BitStream) { b.ReadSE() }},
		{"UnRead", func(b *BitStream) { b.UnRead(100) }},
		{"SkipBits negative", func(b *BitStream) { b.SkipBits(-1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBitStream([]byte{0x00, 0x01})
			tc.read(b)
			if b.Err() == nil {
				t.Fatal("expected the error state to be set")
			}
			// later reads must be no-ops rather than resuming mid-stream
			if v := b.GetBits(1); v != 0 {
				t.Fatalf("read after error returned %d", v)
			}
		})
	}
}

// An all-zero buffer used to make ReadUE spin until it ran off the end.
func TestReadUEBoundedPrefix(t *testing.T) {
	bs := NewBitStream(make([]byte, 16))
	if v := bs.ReadUE(); v != 0 {
		t.Fatalf("ReadUE = %d, want 0", v)
	}
	if bs.Err() == nil {
		t.Fatal("an over long Exp-Golomb prefix must set the error state")
	}
}

func TestGetH265PPSIdWithStartCode(t *testing.T) {
	if got := GetH265PPSIdWithStartCode(pps); got != 0 {
		t.Fatalf("pps id %d, want 0", got)
	}
	if got := GetH265PPSIdWithStartCode(pps2); got != 1 {
		t.Fatalf("pps2 id %d, want 1", got)
	}
}

// An sps too short to carry profile_idc/constraint flags/level_idc cannot
// produce an avcC record; it must be reported, not indexed past.
func TestCreateH264AVCCExtradataShortSPS(t *testing.T) {
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x3c, 0x80}
	if _, err := CreateH264AVCCExtradata([][]byte{sps}, [][]byte{pps}); !errors.Is(err, ErrShortSPS) {
		t.Fatalf("want ErrShortSPS, got %v", err)
	}
}

// The parameter sets belong to the caller: building a record must not rewrite
// the slices it is handed, or the caller's next use of them sees stripped
// start codes.
func TestCreateH264AVCCExtradataDoesNotMutateInput(t *testing.T) {
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1e, 0xd9, 0x00, 0xb0, 0x7b, 0x60, 0x22, 0x00, 0x00}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x3c, 0x80}
	spss := [][]byte{sps}
	ppss := [][]byte{pps}
	if _, err := CreateH264AVCCExtradata(spss, ppss); err != nil {
		t.Fatal(err)
	}
	if len(spss[0]) != len(sps) || len(ppss[0]) != len(pps) {
		t.Errorf("input slices were rewritten: sps %d/%d, pps %d/%d",
			len(spss[0]), len(sps), len(ppss[0]), len(pps))
	}
	// a second call has to produce the same record
	first, err := CreateH264AVCCExtradata(spss, ppss)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateH264AVCCExtradata(spss, ppss)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("record differs between calls:\n%x\n%x", first, second)
	}
}

// An sps that carries no start code is a whole nalu, not a nalu missing two
// bytes.
func TestCreateH264AVCCExtradataWithoutStartCode(t *testing.T) {
	sps := []byte{0x67, 0x42, 0x00, 0x1e, 0xd9, 0x00, 0xb0, 0x7b, 0x60, 0x22, 0x00, 0x00}
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	got, err := CreateH264AVCCExtradata([][]byte{sps}, [][]byte{pps})
	if err != nil {
		t.Fatal(err)
	}
	// bytes 1..3 of the record are profile_idc, compatibility, level_idc
	if got[1] != sps[1] || got[2] != sps[2] || got[3] != sps[3] {
		t.Errorf("record header %x, want %x from the sps", got[1:4], sps[1:4])
	}
}
