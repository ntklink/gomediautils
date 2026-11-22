package flv

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

var (
	regH264SPS = []byte{0, 0, 0, 1, 0x67, 0x64, 0x00, 0x0a, 0xac, 0xd9, 0x44, 0x7b, 0x01, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x03, 0x20, 0xf1, 0x22, 0x59, 0x60}
	regH265VPS = []byte{0, 0, 0, 1, 0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x1e, 0x95, 0x98, 0x09}
	regH265SPS = []byte{0, 0, 0, 1, 0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x1e, 0xa0, 0x20, 0x83, 0x16, 0x59, 0x59, 0xae, 0x4c, 0xaf, 0x01, 0x68, 0x08, 0x00, 0x00, 0x03, 0x00, 0x08, 0x00, 0x00, 0x03, 0x00, 0xc8, 0x40}
	regH265PPS = []byte{0, 0, 0, 1, 0x44, 0x01, 0xc1, 0x73, 0xd0, 0x89}
)

// CompositionTime is an SI24. Reading it as unsigned turns the small negative
// offset that reordered streams carry into a ~16.7 million ms one.
func TestCompositionTimeIsSigned(t *testing.T) {
	for _, cts := range []int32{-40, -1, 0, 1, 40, 0x7FFFFF, -0x800000} {
		var enc VideoTag
		enc.CodecId = uint8(FLV_AVC)
		enc.FrameType = uint8(INTER_FRAME)
		enc.AVCPacketType = uint8(AVC_NALU)
		enc.CompositionTime = cts

		var dec VideoTag
		dec.Decode(enc.Encode())
		if dec.CompositionTime != cts {
			t.Errorf("cts %d round tripped to %d", cts, dec.CompositionTime)
		}
	}
}

// Codecs other than AVC/HEVC have a one byte video tag header, so a one byte
// buffer is a complete tag and has to decode.
func TestVideoTagDecodeShortNonAvcHeader(t *testing.T) {
	var vtag VideoTag
	vtag.Decode([]byte{0x14}) // key frame, codec id 4 (VP6)
	if vtag.FrameType != uint8(KEY_FRAME) || vtag.CodecId != 4 {
		t.Errorf("frameType=%d codecId=%d, want %d and 4", vtag.FrameType, vtag.CodecId, KEY_FRAME)
	}
}

func TestHevcMuxerVCLNotOverwrittenByNonVCL(t *testing.T) {
	m := NewHevcMuxer()
	var au []byte
	au = append(au, regH265VPS...)
	au = append(au, regH265SPS...)
	au = append(au, regH265PPS...)
	// IDR_W_RADL (type 19) followed by SUFFIX_SEI (type 40, non-VCL)
	au = append(au, 0, 0, 0, 1, 0x26, 0x01, 0xaf, 0x06, 0xb8)
	au = append(au, 0, 0, 0, 1, 0x50, 0x01, 0x01, 0x02)

	tags, err := m.Write(au, 100, 100)
	if err != nil {
		t.Fatalf("HevcMuxer.Write: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("got %d tags, want 2 (seq hdr + video frame)", len(tags))
	}
}

func TestHevcMuxerWaitsForVPSSPSPPS(t *testing.T) {
	m := NewHevcMuxer()
	if tags, err := m.Write(regH265VPS, 0, 0); err != nil || len(tags) != 0 {
		t.Fatalf("after VPS only: tags=%d err=%v, want 0 nil", len(tags), err)
	}
	if tags, err := m.Write(regH265SPS, 0, 0); err != nil || len(tags) != 0 {
		t.Fatalf("after VPS+SPS: tags=%d err=%v, want 0 nil", len(tags), err)
	}
	tags, err := m.Write(regH265PPS, 0, 0)
	if err != nil || len(tags) != 1 {
		t.Fatalf("after VPS+SPS+PPS: tags=%d err=%v, want 1 nil", len(tags), err)
	}
}

func TestAACMuxerADTSWithCRC(t *testing.T) {
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	frameLen := 9 + len(payload)
	// ADTS with protection_absent = 0 (9-byte header including 2-byte CRC)
	adts := []byte{
		0xFF, 0xF0, // syncword + ID=0 + layer=0 + protection_absent=0
		0x50, // profile=1 (LC), sf_idx=4 (44100), priv=0, ch_high=0
		byte(0x80 | ((frameLen >> 11) & 0x03)),
		byte((frameLen >> 3) & 0xFF),
		byte(((frameLen & 0x07) << 5) | 0x1F),
		0xFC,
		0x12, 0x34, // 2-byte CRC
	}
	adts = append(adts, payload...)

	m := NewAACMuxer()
	tags, err := m.Write(adts, 0, 0)
	if err != nil {
		t.Fatalf("AACMuxer.Write: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("got %d tags, want 2", len(tags))
	}
	// tags[1] has 2-byte FLV AAC audio header followed by raw AAC payload
	if !bytes.Equal(tags[1][2:], payload) {
		t.Fatalf("raw AAC payload = %x, want %x", tags[1][2:], payload)
	}
}

func TestAVCHevcMuxerCacheRollbackOnError(t *testing.T) {
	avc := NewAVCMuxer()
	// Valid NALU followed by truncated SPS
	badAVC := append([]byte{0, 0, 0, 1, 0x41, 0x9a}, []byte{0, 0, 0, 1, 0x67, 0x64}...)
	if _, err := avc.Write(badAVC, 0, 0); err == nil {
		t.Fatal("expected error for truncated H264 SPS")
	}
	if len(avc.cache) != 0 {
		t.Fatalf("AVCMuxer cache leaked %d bytes on error", len(avc.cache))
	}

	hevc := NewHevcMuxer()
	// Valid NALU followed by truncated VPS
	badHEVC := append([]byte{0, 0, 0, 1, 0x02, 0x01, 0xd0}, []byte{0, 0, 0, 1, 0x40, 0x01}...)
	if _, err := hevc.Write(badHEVC, 0, 0); err == nil {
		t.Fatal("expected error for truncated H265 VPS")
	}
	if len(hevc.cache) != 0 {
		t.Fatalf("HevcMuxer cache leaked %d bytes on error", len(hevc.cache))
	}
}

func TestFlvReaderHeaderDataOffset(t *testing.T) {
	// Reject DataOffset < 9
	r := CreateFlvReader()
	badHdr := []byte{'F', 'L', 'V', 1, 5, 0, 0, 0, 8}
	if err := r.Input(badHdr); err == nil {
		t.Fatal("expected error for DataOffset < 9")
	}

	// Accept DataOffset > 9 across chunked inputs
	r = CreateFlvReader()
	var got []byte
	r.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		got = append([]byte(nil), frame...)
	}
	var stream bytes.Buffer
	stream.Write([]byte{'F', 'L', 'V', 1, 4, 0, 0, 0, 13, 0xAA, 0xBB, 0xCC, 0xDD}) // 13-byte header
	stream.Write([]byte{0, 0, 0, 0})                                               // PreviousTagSize0
	body := WriteAudioTag([]byte{1, 2, 3}, FLV_G711A, 5500, 1, false)
	ftag := FlvTag{TagType: uint8(AUDIO_TAG), DataSize: uint32(len(body))}
	stream.Write(ftag.Encode())
	stream.Write(body)
	var ptsz [4]byte
	binary.BigEndian.PutUint32(ptsz[:], FLVTAG_SIZE+uint32(len(body)))
	stream.Write(ptsz[:])

	raw := stream.Bytes()
	// Split inside the extended header (10 bytes, then rest)
	if err := r.Input(raw[:10]); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if err := r.Input(raw[10:]); err != nil {
		t.Fatalf("second chunk: %v", err)
	}
	if !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("got frame %x, want 010203", got)
	}
}

func TestFlvReaderNegativeCTSClamp(t *testing.T) {
	var buf bytes.Buffer
	w := CreateFlvWriter(&buf)
	if err := w.WriteFlvHeader(); err != nil {
		t.Fatal(err)
	}
	// Write SPS+PPS+IDR with pts=0, dts=10 => cts = -10; and then dts=5, pts=0 => cts = -5.
	// Manually write a tag with dts=10, cts=-40 so dts+cts = -30 < 0.
	pps := []byte{0, 0, 0, 1, 0x68, 0xeb, 0xe3, 0xcb, 0x22, 0xc0}
	extra, err := codec.CreateH264AVCCExtradata([][]byte{regH264SPS}, [][]byte{pps})
	if err != nil {
		t.Fatal(err)
	}
	seqBody := WriteVideoTag(extra, true, FLV_AVC, 0, true)
	ftag := FlvTag{TagType: uint8(VIDEO_TAG), DataSize: uint32(len(seqBody)), Timestamp: 10}
	buf.Write(ftag.Encode())
	buf.Write(seqBody)
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], FLVTAG_SIZE+uint32(len(seqBody)))
	buf.Write(sz[:])

	nalu := []byte{0, 0, 0, 3, 0x65, 0x88, 0x84}
	frameBody := WriteVideoTag(nalu, true, FLV_AVC, -40, false)
	ftag = FlvTag{TagType: uint8(VIDEO_TAG), DataSize: uint32(len(frameBody)), Timestamp: 10}
	buf.Write(ftag.Encode())
	buf.Write(frameBody)
	binary.BigEndian.PutUint32(sz[:], FLVTAG_SIZE+uint32(len(frameBody)))
	buf.Write(sz[:])

	r := CreateFlvReader()
	var gotPts, gotDts uint32
	called := false
	r.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		called = true
		gotPts, gotDts = pts, dts
	}
	if err := r.Input(buf.Bytes()); err != nil {
		t.Fatalf("FlvReader.Input: %v", err)
	}
	if !called {
		t.Fatal("OnFrame not called")
	}
	if gotPts != 0 || gotDts != 10 {
		t.Fatalf("got pts=%d dts=%d, want pts=0 dts=10", gotPts, gotDts)
	}
}

func TestFlvTagSizeLimit(t *testing.T) {
	huge := make([]byte, 0xFFFFFF) // +1 byte audio tag header => 0x1000000 > 0xFFFFFF
	muxer, err := NewFlvMuxer(FLV_AVC, FLV_G711A)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := muxer.WriteAudio(huge, 0, 0); err == nil {
		t.Fatal("FlvMuxer.WriteAudio: expected error for tag > 24-bit DataSize")
	}
	var buf bytes.Buffer
	w := CreateFlvWriter(&buf)
	if err := w.WriteG711A(huge, 0, 0); err == nil {
		t.Fatal("FlvWriter.WriteG711A: expected error for tag > 24-bit DataSize")
	}
}

func TestHevcTagDemuxerRejectsWrongCodec(t *testing.T) {
	d := NewHevcTagDemuxer()
	// Enhanced header with AV1 FourCC instead of hvc1
	av1Tag := []byte{0x80 | (1 << 4) | PacketTypeSequenceStart, 'a', 'v', '0', '1', 0x00}
	if err := d.Decode(av1Tag); err == nil {
		t.Fatal("expected error for non-hvc1 enhanced video tag")
	}
	// Legacy AVC header
	avcTag := []byte{(1 << 4) | uint8(FLV_AVC), uint8(AVC_NALU), 0, 0, 0}
	if err := d.Decode(avcTag); err == nil {
		t.Fatal("expected error for legacy AVC tag in HevcTagDemuxer")
	}
}
