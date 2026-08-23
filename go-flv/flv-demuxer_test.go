package flv

import (
	"bytes"
	"errors"
	"testing"

	"github.com/yapingcat/gomedia/go-codec"
)

// High profile SPS (1280x720) and two PPS with different ids, Annex-B form.
var (
	testSPS  = []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x64, 0x00, 0x1F, 0xAC, 0xD9, 0x40, 0x50, 0x05, 0xBB, 0x01, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x03, 0x20, 0xF1, 0x83, 0x19, 0x60}
	testPPS0 = []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xEB, 0xE3, 0xCB, 0x22, 0xC0}
	testPPS1 = []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0x4B, 0xE3, 0xCB, 0x22, 0xC0}
	testIDR  = []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x33, 0xFF}
	testP    = []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A, 0x02, 0x04, 0x33, 0xFF}
)

func mustNotPanic(t *testing.T, name string, fn func() error) error {
	t.Helper()
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s panicked: %v", name, r)
			}
		}()
		err = fn()
	}()
	return err
}

func TestAVCDemuxerTruncatedNalu(t *testing.T) {
	d := NewAVCTagDemuxer()
	cases := map[string][]byte{
		"length exceeds payload": {0x17, 0x01, 0, 0, 0, 0x00, 0x00, 0x00, 0x10, 0x65, 0x88},
		"short length field":     {0x17, 0x01, 0, 0, 0, 0x00, 0x00},
		"zero length nalu":       {0x17, 0x01, 0, 0, 0, 0x00, 0x00, 0x00, 0x00, 0x65},
		"huge length":            {0x17, 0x01, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF, 0x65},
	}
	for name, tag := range cases {
		if err := mustNotPanic(t, name, func() error { return d.Decode(tag) }); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestAVCDemuxerMalformedAvcC(t *testing.T) {
	d := NewAVCTagDemuxer()
	cases := map[string][]byte{
		"too short":      {0x17, 0x00, 0, 0, 0, 0x01, 0x64, 0x00, 0x1F},
		"sps overflows":  {0x17, 0x00, 0, 0, 0, 0x01, 0x64, 0x00, 0x1F, 0xFF, 0xE1, 0x00, 0x50, 0x67},
		"no pps count":   {0x17, 0x00, 0, 0, 0, 0x01, 0x64, 0x00, 0x1F, 0xFF, 0xE1, 0x00, 0x04, 0x67, 0x64, 0x00, 0x1F},
		"pps overflows":  {0x17, 0x00, 0, 0, 0, 0x01, 0x64, 0x00, 0x1F, 0xFF, 0xE1, 0x00, 0x04, 0x67, 0x64, 0x00, 0x1F, 0x01, 0x00, 0x09, 0x68},
		"sps count only": {0x17, 0x00, 0, 0, 0, 0x01, 0x64, 0x00, 0x1F, 0xFF, 0xE1},
	}
	for name, tag := range cases {
		if err := mustNotPanic(t, name, func() error { return d.Decode(tag) }); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestAVCDemuxerRoundTrip(t *testing.T) {
	m := NewAVCMuxer()
	frame := append(append(append([]byte{}, testSPS...), testPPS0...), testIDR...)
	tags, err := m.Write(frame, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 {
		t.Fatalf("want seq header + frame, got %d tags", len(tags))
	}
	d := NewAVCTagDemuxer()
	var got []byte
	d.OnFrame(func(codecid codec.CodecID, frame []byte, cts int) { got = append(got[:0], frame...) })
	if err := d.Decode(tags[0]); err != nil {
		t.Fatal(err)
	}
	if d.naluLengthSize != 4 {
		t.Fatalf("nalu length size %d", d.naluLengthSize)
	}
	if err := d.Decode(tags[1]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, frame) {
		t.Fatalf("got\n% x\nwant\n% x", got, frame)
	}

	// IDR without in-band SPS/PPS gets them prepended from the sequence header
	idrOnly, err := m.Write(testIDR, 40, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(idrOnly) != 1 {
		t.Fatalf("want 1 tag, got %d", len(idrOnly))
	}
	if err := d.Decode(idrOnly[0]); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(got, testIDR) || !bytes.Contains(got, testSPS) || !bytes.Contains(got, testPPS0) {
		t.Fatalf("sps/pps not prepended: % x", got)
	}
}

func TestAVCDemuxerNaluLengthSize2(t *testing.T) {
	// avcC declaring 2-byte NALU lengths
	avcc := []byte{0x01, 0x64, 0x00, 0x1F, 0xFC | 0x01, 0xE1}
	avcc = append(avcc, 0x00, byte(len(testSPS)-4))
	avcc = append(avcc, testSPS[4:]...)
	avcc = append(avcc, 0x01, 0x00, byte(len(testPPS0)-4))
	avcc = append(avcc, testPPS0[4:]...)
	d := NewAVCTagDemuxer()
	var got []byte
	d.OnFrame(func(codecid codec.CodecID, frame []byte, cts int) { got = frame })
	if err := d.Decode(append([]byte{0x17, 0x00, 0, 0, 0}, avcc...)); err != nil {
		t.Fatal(err)
	}
	if d.naluLengthSize != 2 {
		t.Fatalf("nalu length size %d", d.naluLengthSize)
	}
	tag := []byte{0x27, 0x01, 0, 0, 0, 0x00, byte(len(testP) - 4)}
	tag = append(tag, testP[4:]...)
	if err := d.Decode(tag); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, testP) {
		t.Fatalf("got % x want % x", got, testP)
	}
}

func TestHevcDemuxerShortEnhancedTag(t *testing.T) {
	d := NewHevcTagDemuxer()
	cases := map[string][]byte{
		"coded frames 5 bytes":  {0x80 | PacketTypeCodedFrames, 'h', 'v', 'c', '1'},
		"coded frames 7 bytes":  {0x80 | PacketTypeCodedFrames, 'h', 'v', 'c', '1', 0, 0},
		"sequence start short":  {0x80 | PacketTypeSequenceStart, 'h', 'v', 'c', '1', 0x01, 0x02},
		"coded frames x trunc":  {0x80 | PacketTypeCodedFramesX, 'h', 'v', 'c', '1', 0x00, 0x00, 0x00, 0x20, 0x26},
		"legacy hvcC short":     {0x1C, 0x00, 0, 0, 0, 0x01, 0x02},
		"legacy nalu truncated": {0x1C, 0x01, 0, 0, 0, 0x00, 0x00, 0x00, 0x20, 0x26, 0x01},
	}
	for name, tag := range cases {
		if err := mustNotPanic(t, name, func() error { return d.Decode(tag) }); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// 4-byte tag is rejected, 8-byte coded frame with empty payload is fine
	if err := d.Decode([]byte{0x80 | PacketTypeCodedFrames, 'h', 'v', 'c'}); err == nil {
		t.Error("4 byte tag must fail")
	}
	if err := d.Decode([]byte{0x80 | PacketTypeCodedFrames, 'h', 'v', 'c', '1', 0, 0, 0}); err != nil {
		t.Errorf("empty coded frame: %v", err)
	}
}

func TestHevcDemuxerMalformedHvcC(t *testing.T) {
	d := NewHevcTagDemuxer()
	hvcc := make([]byte, 23)
	hvcc[21] = 0x03
	hvcc[22] = 0x01 // one array, but no array header
	if err := mustNotPanic(t, "array header", func() error { return d.Decode(append([]byte{0x1C, 0x00, 0, 0, 0}, hvcc...)) }); err == nil {
		t.Error("expected error")
	}
	hvcc = append(hvcc, 0xA0, 0x00, 0x01, 0x00, 0x40) // nalu length 64 with no data
	if err := mustNotPanic(t, "nalu length", func() error { return d.Decode(append([]byte{0x1C, 0x00, 0, 0, 0}, hvcc...)) }); err == nil {
		t.Error("expected error")
	}
}

func TestHevcDemuxerSpsPpsVpsNoAlias(t *testing.T) {
	d := NewHevcTagDemuxer()
	d.SpsPpsVps = make([]byte, 0, 64)
	d.SpsPpsVps = append(d.SpsPpsVps, 0x00, 0x00, 0x00, 0x01, 0x40, 0x01)
	var got []byte
	d.OnFrame(func(codecid codec.CodecID, frame []byte, cts int) { got = frame })
	idr := []byte{0x1C, 0x01, 0, 0, 0, 0x00, 0x00, 0x00, 0x02, 0x26, 0x01}
	if err := d.Decode(idr); err != nil {
		t.Fatal(err)
	}
	if len(d.SpsPpsVps) != 6 || len(got) != 12 {
		t.Fatalf("SpsPpsVps %d frame %d", len(d.SpsPpsVps), len(got))
	}
	got[0] = 0xFF
	if d.SpsPpsVps[0] == 0xFF || cap(d.SpsPpsVps) != 64 {
		t.Fatal("emitted frame aliases SpsPpsVps")
	}
}

func TestGetFLVVideoCodecIdShort(t *testing.T) {
	if GetFLVVideoCodecId(nil) != FLV_VIDEO_CODEC_UNKNOWN {
		t.Error("empty")
	}
	if GetFLVVideoCodecId([]byte{0x81, 'h', 'v'}) != FLV_VIDEO_CODEC_UNKNOWN {
		t.Error("short enhanced")
	}
	if GetFLVVideoCodecId([]byte{0x81, 'h', 'v', 'c', '1'}) != FLV_HEVC {
		t.Error("enhanced hevc")
	}
	if GetFLVVideoCodecId([]byte{0x17}) != FLV_AVC {
		t.Error("legacy avc")
	}
}

func TestCreateDemuxerUnsupported(t *testing.T) {
	if _, err := CreateFlvVideoTagHandle(FLV_VIDEO_CODEC_UNKNOWN); !errors.Is(err, ErrUnsupportedVideoCodec) {
		t.Fatalf("want ErrUnsupportedVideoCodec, got %v", err)
	}
	if d, err := CreateFlvVideoTagHandle(FLV_HEVC); err != nil || d == nil {
		t.Fatalf("hevc demuxer: %v", err)
	}
	if _, err := CreateAudioTagDemuxer(FLV_SOUND_FORMAT_UNKNOWN); !errors.Is(err, ErrUnsupportedSoundFormat) {
		t.Fatalf("want ErrUnsupportedSoundFormat, got %v", err)
	}
	if d, err := CreateAudioTagDemuxer(FLV_AAC); err != nil || d == nil {
		t.Fatalf("aac demuxer: %v", err)
	}
}

func TestFlvReaderUnsupportedCodecIsReported(t *testing.T) {
	hdr := []byte{'F', 'L', 'V', 1, 5, 0, 0, 0, 9, 0, 0, 0, 0}
	// sound format 1 (ADPCM) has no demuxer
	body := []byte{0x12, 0x01, 0x02}
	tag := FlvTag{TagType: uint8(AUDIO_TAG), DataSize: uint32(len(body))}
	stream := append(append(append([]byte{}, hdr...), tag.Encode()...), body...)
	r := CreateFlvReader()
	if err := r.Input(stream); !errors.Is(err, ErrUnsupportedSoundFormat) {
		t.Fatalf("want ErrUnsupportedSoundFormat, got %v", err)
	}
}
