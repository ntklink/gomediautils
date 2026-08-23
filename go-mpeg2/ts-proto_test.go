package mpeg2

import (
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

func TestAdaptationField_DecodeShort(t *testing.T) {
	// PCR_flag set but the field is only 1 byte long
	var af Adaptation_field
	if err := af.Decode(codec.NewBitStream([]byte{0x01, 0x10, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})); err == nil {
		t.Fatalf("expected error for adaptation field shorter than its flags")
	}
	// extension flag set with an extension length larger than the field
	af = Adaptation_field{}
	if err := af.Decode(codec.NewBitStream([]byte{0x02, 0x01, 0x10, 0xff, 0xff, 0xff})); err == nil {
		t.Fatalf("expected error for oversized extension length")
	}
	// Adaptation_field_length larger than the packet
	af = Adaptation_field{}
	if err := af.Decode(codec.NewBitStream([]byte{0x20, 0x00})); err == nil {
		t.Fatalf("expected error for Adaptation_field_length > data")
	}
	// valid field with PCR and stuffing; the reader must end right after it
	af = Adaptation_field{}
	data := []byte{0x09, 0x10, 0x00, 0x00, 0x00, 0x00, 0x7e, 0x00, 0xff, 0xff, 0xAB}
	bs := codec.NewBitStream(data)
	if err := af.Decode(bs); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if af.PCR_flag != 1 || bs.RemainBytes() != 1 || bs.Uint8(8) != 0xAB {
		t.Fatalf("adaptation field not consumed exactly")
	}
}

func TestPat_DecodeOversizedSection(t *testing.T) {
	// Section_length = 0xFFF while only a few bytes follow
	data := []byte{0x00, 0xBF, 0xFF, 0x00, 0x01, 0xC1, 0x00, 0x00}
	if err := NewPat().Decode(codec.NewBitStream(data)); err == nil {
		t.Fatalf("expected error for oversized pat section")
	}
	// a valid one program pat
	data = []byte{0x00, 0xB0, 0x0D, 0x00, 0x01, 0xC1, 0x00, 0x00, 0x00, 0x01, 0xE2, 0x00, 0x00, 0x00, 0x00, 0x00}
	pat := NewPat()
	if err := pat.Decode(codec.NewBitStream(data)); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if len(pat.Pmts) != 1 || pat.Pmts[0].PID != 0x200 {
		t.Fatalf("pat not decoded: %+v", pat.Pmts)
	}
}

func TestPmt_DecodeOversizedLengths(t *testing.T) {
	// Section_length exceeds the data
	data := []byte{0x02, 0xB0, 0xFF, 0x00, 0x01, 0xC1, 0x00, 0x00, 0xE1, 0x00, 0xF0, 0x00}
	if err := NewPmt().Decode(codec.NewBitStream(data)); err == nil {
		t.Fatalf("expected error for oversized pmt section")
	}
	// Program_info_length larger than the section
	data = []byte{0x02, 0xB0, 0x12, 0x00, 0x01, 0xC1, 0x00, 0x00, 0xE1, 0x00, 0xFF, 0xFF,
		0x1B, 0xE1, 0x00, 0xF0, 0x00, 0x00, 0x00, 0x00, 0x00}
	if err := NewPmt().Decode(codec.NewBitStream(data)); err == nil {
		t.Fatalf("expected error for oversized Program_info_length")
	}
	// ES_Info_Length larger than the section
	data = []byte{0x02, 0xB0, 0x12, 0x00, 0x01, 0xC1, 0x00, 0x00, 0xE1, 0x00, 0xF0, 0x00,
		0x1B, 0xE1, 0x00, 0xFF, 0xFF, 0x00, 0x00, 0x00, 0x00}
	if err := NewPmt().Decode(codec.NewBitStream(data)); err == nil {
		t.Fatalf("expected error for oversized ES_Info_Length")
	}
	// valid pmt with one h264 stream
	data = []byte{0x02, 0xB0, 0x12, 0x00, 0x01, 0xC1, 0x00, 0x00, 0xE1, 0x00, 0xF0, 0x00,
		0x1B, 0xE1, 0x00, 0xF0, 0x00, 0x00, 0x00, 0x00, 0x00}
	pmt := NewPmt()
	if err := pmt.Decode(codec.NewBitStream(data)); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if len(pmt.Streams) != 1 || pmt.Streams[0].Elementary_PID != 0x100 || pmt.Streams[0].StreamType != uint8(TS_STREAM_H264) {
		t.Fatalf("pmt not decoded: %+v", pmt.Streams)
	}
}

func TestReadSection_EmptyPayload(t *testing.T) {
	if _, err := ReadSection(TS_TID_PAS, codec.NewBitStream(nil)); err == nil {
		t.Fatalf("expected error on empty payload")
	}
	if _, err := ReadSection(TS_TID_PAS, codec.NewBitStream([]byte{0xff, 0xff})); err == nil {
		t.Fatalf("expected error on all stuffing payload")
	}
}

func TestReadSection_UnsupportedTableId(t *testing.T) {
	// a table id this package does not decode must be reported, not silently
	// returned as a nil section
	pkg, err := ReadSection(TS_TID_CAS, codec.NewBitStream([]byte{0x01, 0xB0, 0x0D}))
	if err == nil {
		t.Fatalf("expected an error for an unsupported table id, got %v", pkg)
	}
}

func TestTSPacket_DecodeHeaderTruncated(t *testing.T) {
	var pkg TSPacket
	if err := pkg.DecodeHeader(codec.NewBitStream([]byte{0x47, 0x40})); err == nil {
		t.Fatalf("expected an error for a truncated ts header")
	}
	if err := pkg.DecodeHeader(codec.NewBitStream(nil)); err == nil {
		t.Fatalf("expected an error for an empty ts header")
	}
}

func TestAdaptationField_ExtensionOverrunsField(t *testing.T) {
	// adaptation_field_length = 3, extension flag set, extension announces 8
	// bytes: the extension does not fit in the field
	var af Adaptation_field
	err := af.Decode(codec.NewBitStream([]byte{0x03, 0x01, 0x08, 0x00, 0xff, 0xff}))
	if err == nil {
		t.Fatalf("expected an error for an extension longer than the field")
	}
	// same field with an extension that fits: flags, extension length 3,
	// ltw_flag set plus its 2 byte offset
	af = Adaptation_field{}
	bs := codec.NewBitStream([]byte{0x05, 0x01, 0x03, 0x80, 0x80, 0x00, 0xAB, 0xCD})
	if err := af.Decode(bs); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if af.Adaptation_field_extension_flag != 1 || af.Ltw_flag != 1 {
		t.Fatalf("extension not decoded: %+v", af)
	}
	if bs.RemainBytes() != 2 {
		t.Fatalf("adaptation field not consumed exactly, %d bytes left", bs.RemainBytes())
	}
}
