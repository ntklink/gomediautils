package mpeg2

import (
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

var ps1 []byte = []byte{0x00, 0x00, 0x01, 0xBA}
var ps2 []byte = []byte{0x00, 0x00, 0x01, 0xBA, 0x40, 0x01, 0x00, 0x01, 0x33, 0x44, 0xFF, 0xFF, 0xFF, 0xF1, 0xFF}

var ps3 []byte = []byte{0x00, 0x00, 0x01, 0xBA, 0x40, 0x01, 0x00, 0x01, 0x33, 0x44, 0xFF, 0xFF, 0xFF, 0xF0, 0x00, 0x00, 0x01, 0xBB}
var ps4 []byte = []byte{0x00, 0x00, 0x01, 0xBA, 0x40, 0x01, 0x00, 0x01, 0x33, 0x44, 0xFF, 0xFF, 0xFF, 0xF1, 0x34, 0x00, 0x00, 0x01, 0xBB, 0x00, 0x01, 0x00, 0x01, 0x33, 0x44, 0xFF, 0x34}
var ps5 []byte = []byte{0x00, 0x00, 0x01, 0xBA, 0x40, 0x01, 0x00, 0x01, 0x33, 0x44, 0xFF, 0xFF, 0xFF, 0xF1, 0x34, 0x00, 0x00, 0x01, 0xBB, 0x00, 0x09, 0x00, 0x01, 0x33, 0x44, 0xFF, 0x34, 0x81, 0x00, 0x00}

// program stream map: length 14 = 6 + program_stream_info(0) + elementary_stream_map(4) + CRC(4)
var ps6 []byte = []byte{0x00, 0x00, 0x01, 0xBC, 0x00, 0x0E, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x1B, 0xE0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
var ps7 []byte = []byte{0x00, 0x00, 0x01, 0xBA, 0x20, 0x0a, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03}

func TestPSDemuxer_Input(t *testing.T) {
	type fields struct {
		streamMap map[uint8]*psstream
		pkg       *PSPacket
		cache     []byte
		OnPacket  func(pkg Display, decodeResult error)
		OnFrame   func(frame []byte, cid PS_STREAM_TYPE, pts uint64, dts uint64)
	}
	type args struct {
		data []byte
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{name: "test1", fields: fields{
			streamMap: make(map[uint8]*psstream),
			pkg:       new(PSPacket),
		}, args: args{data: ps1}, wantErr: true},

		{name: "test2", fields: fields{
			streamMap: make(map[uint8]*psstream),
			pkg:       new(PSPacket),
		}, args: args{data: ps2}, wantErr: false},

		{name: "test3", fields: fields{
			streamMap: make(map[uint8]*psstream),
			pkg:       new(PSPacket),
		}, args: args{data: ps3}, wantErr: true},

		{name: "test4", fields: fields{
			streamMap: make(map[uint8]*psstream),
			pkg:       new(PSPacket),
		}, args: args{data: ps4}, wantErr: true},

		{name: "test5", fields: fields{
			streamMap: make(map[uint8]*psstream),
			pkg:       new(PSPacket),
		}, args: args{data: ps5}, wantErr: false},
		{name: "test6", fields: fields{
			streamMap: make(map[uint8]*psstream),
			pkg:       new(PSPacket),
		}, args: args{data: ps6}, wantErr: false},
		{name: "test-mpeg1", fields: fields{
			streamMap: make(map[uint8]*psstream),
			pkg:       new(PSPacket),
		}, args: args{data: ps7}, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			psdemuxer := &PSDemuxer{
				streamMap: tt.fields.streamMap,
				pkg:       tt.fields.pkg,
				cache:     tt.fields.cache,
				OnPacket:  tt.fields.OnPacket,
				OnFrame:   tt.fields.OnFrame,
			}
			if err := psdemuxer.Input(tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("PSDemuxer.Input() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

var psEndCode = []byte{0x00, 0x00, 0x01, 0xB9}

// PES (stream 0xE0) with PES_packet_length == 0, PTS only, followed by one H264 nalu
var pesLen0 = []byte{
	0x00, 0x00, 0x01, 0xE0, 0x00, 0x00, // start code, stream id, PES_packet_length = 0
	0x80, 0x80, 0x05, // '10', flags: PTS only, PES_header_data_length = 5
	0x21, 0x00, 0x01, 0x00, 0x01, // PTS = 0
	0x00, 0x00, 0x00, 0x01, 0x65, 0xAA, 0xBB, 0xCC, // payload
}

func TestPSDemuxer_EndCodeTerminates(t *testing.T) {
	demuxer := NewPSDemuxer()
	data := concat(ps2, psEndCode, ps2, psEndCode)
	if err := demuxer.Input(data); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if len(demuxer.cache) != 0 {
		t.Fatalf("cache must be empty, got %d bytes", len(demuxer.cache))
	}
}

func TestPSDemuxer_PesPacketLengthZero(t *testing.T) {
	demuxer := NewPSDemuxer()
	// the unbounded PES is delimited by the following program end code
	data := concat(ps2, ps6, pesLen0, psEndCode)
	if err := demuxer.Input(data); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	stream, found := demuxer.streamMap[0xE0]
	if !found {
		t.Fatalf("stream 0xE0 not created from psm")
	}
	want := pesLen0[14:]
	if string(stream.streamBuf) != string(want) {
		t.Fatalf("payload = % x, want % x", stream.streamBuf, want)
	}
	if len(demuxer.cache) != 0 {
		t.Fatalf("cache must be empty, got %d bytes", len(demuxer.cache))
	}

	// without a following start code the demuxer must wait for more data
	demuxer2 := NewPSDemuxer()
	err := demuxer2.Input(concat(ps2, ps6, pesLen0))
	if e, ok := err.(Error); !ok || !e.NeedMore() {
		t.Fatalf("expected need-more error, got %v", err)
	}
	if err := demuxer2.Input(psEndCode); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if len(demuxer2.cache) != 0 {
		t.Fatalf("cache must be empty after completion, got %d", len(demuxer2.cache))
	}
}

func TestPSDemuxer_ResyncAfterParserError(t *testing.T) {
	demuxer := NewPSDemuxer()
	// ps4 carries a malformed system header; the psm after it must still be parsed
	data := concat(ps4, ps6, psEndCode)
	err := demuxer.Input(data)
	if e, ok := err.(Error); !ok || !e.ParserError() {
		t.Fatalf("expected parser error, got %v", err)
	}
	if _, found := demuxer.streamMap[0xE0]; !found {
		t.Fatalf("demuxer did not resync on the psm after the bad system header")
	}
	if len(demuxer.cache) != 0 {
		t.Fatalf("cache must not keep data after a parser error, got %d bytes", len(demuxer.cache))
	}
	// feeding again must not get stuck on the stale data
	if err := demuxer.Input(concat(ps2, psEndCode)); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestPesPacket_DecodeFlagsExceedHeaderLength(t *testing.T) {
	// PTS_DTS_flags = '11' needs 10 bytes but PES_header_data_length is 5
	data := []byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x08, 0x80, 0xC0, 0x05, 0x31, 0x00, 0x01, 0x00, 0x01}
	pes := NewPesPacket()
	err := pes.Decode(codec.NewBitStream(data))
	if e, ok := err.(Error); !ok || !e.ParserError() {
		t.Fatalf("expected parser error, got %v", err)
	}
	// PES_packet_length smaller than 3 + PES_header_data_length
	data = []byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x05, 0x80, 0x80, 0x05, 0x21, 0x00, 0x01, 0x00, 0x01}
	err = pes.Decode(codec.NewBitStream(data))
	if e, ok := err.(Error); !ok || !e.ParserError() {
		t.Fatalf("expected parser error, got %v", err)
	}
}

func TestPesPacket_DecodeLengthZero(t *testing.T) {
	pes := NewPesPacket()
	bs := codec.NewBitStream(pesLen0)
	if err := pes.Decode(bs); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if string(pes.Pes_payload) != string(pesLen0[14:]) {
		t.Fatalf("payload = % x", pes.Pes_payload)
	}
	if !bs.EOS() {
		t.Fatalf("bitstream must be fully consumed")
	}
}

func TestPesPacket_DecodeMpeg1Dts(t *testing.T) {
	// PTS = 0x1234, DTS = 0x1000
	data := []byte{
		0x00, 0x00, 0x01, 0xE0, 0x00, 0x0A,
		0x31, 0x00, 0x01, 0x24, 0x69, // '0011' PTS
		0x11, 0x00, 0x01, 0x20, 0x01, // '0001' DTS
	}
	pes := NewPesPacket()
	if err := pes.DecodeMpeg1(codec.NewBitStream(data)); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if pes.Pts != 0x1234 {
		t.Fatalf("pts = %#x, want 0x1234", pes.Pts)
	}
	if pes.Dts != 0x1000 {
		t.Fatalf("dts = %#x, want 0x1000", pes.Dts)
	}
	if len(pes.Pes_payload) != 0 {
		t.Fatalf("payload must be empty, got %d bytes", len(pes.Pes_payload))
	}
}
