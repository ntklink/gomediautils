package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// mfro carries the size of the whole mfra box so a player can find mfra by
// seeking back from the end of the file. Declaring anything else points the
// player at the wrong offset.
func TestMfroDeclaresRealMfraSize(t *testing.T) {
	ws := newFmp4WriterSeeker(1024)
	muxer := &Movmuxer{
		writer:      ws,
		nextTrackId: 2,
		tracks: map[uint32]*mp4track{
			1: {trackId: 1, fragments: []movFragment{{offset: 100, firstPts: 0}, {offset: 900, firstPts: 3000}}},
		},
	}
	if err := muxer.writeMfra(); err != nil {
		t.Fatal(err)
	}
	buf := ws.buffer
	if got := binary.BigEndian.Uint32(buf); int(got) != len(buf) {
		t.Fatalf("mfra box size field = %d, wrote %d bytes", got, len(buf))
	}
	if got := binary.BigEndian.Uint32(buf[len(buf)-4:]); int(got) != len(buf) {
		t.Errorf("mfro.SizeOfMfra = %d, mfra box is %d bytes", got, len(buf))
	}
}

// track.duration is the span from the first sample of the run to the last, so
// every interval counts, including the one between the first two samples.
func TestTrackDurationCoversEveryInterval(t *testing.T) {
	tr := &mp4track{timescale: 1000}
	for i := 0; i < 5; i++ {
		tr.addSampleEntry(sampleEntry{dts: uint64(i) * 40, pts: uint64(i) * 40})
	}
	if tr.duration != 160 {
		t.Errorf("duration = %d, want 160 (4 intervals of 40)", tr.duration)
	}
}

// A composition offset is signed. pts ahead of dts needs the version 1 ctts
// box; encoding it as a version 0 unsigned value turns a small negative
// offset into a ~4 billion tick one.
func TestNegativeCompositionOffsetUsesCttsVersion1(t *testing.T) {
	track := &mp4track{cid: MP4_CODEC_H264, timescale: 1000}
	for i := 0; i < 3; i++ {
		track.addSampleEntry(sampleEntry{dts: uint64(i) * 40, pts: uint64(i)*40 - 10, size: 10})
	}
	track.makeStblTable()
	if track.stbltable.ctts.version != 1 {
		t.Fatalf("ctts version = %d, want 1", track.stbltable.ctts.version)
	}
	box := makeCtts(track.stbltable.ctts)
	if box[8] != 1 {
		t.Errorf("encoded ctts version byte = %d, want 1", box[8])
	}
	// size(4) type(4) version(1) flags(3) entry_count(4) sample_count(4) sample_offset(4)
	if got := binary.BigEndian.Uint32(box[16:]); got != 3 {
		t.Errorf("sample_count = %d, want 3", got)
	}
	if got := int32(binary.BigEndian.Uint32(box[20:])); got != -10 {
		t.Errorf("sample_offset = %d, want -10", got)
	}
}

// A sync sample table lets SeekTime land on a frame that can actually be
// decoded rather than in the middle of a gop.
func TestSeekTimeSnapsVideoToSyncSample(t *testing.T) {
	track := &mp4track{
		cid:       MP4_CODEC_H264,
		timescale: 1000,
		stbltable: &movstbl{stss: &movstss{sampleNumber: []uint32{1, 11, 21}}},
	}
	for i := 0; i < 30; i++ {
		track.samplelist = append(track.samplelist, sampleEntry{dts: uint64(i) * 40, pts: uint64(i) * 40})
	}
	d := &MovDemuxer{tracks: []*mp4track{track}, readSampleIdx: make([]uint32, 1)}
	if err := d.SeekTime(600); err != nil { // sample 15, gop starts at sample 10
		t.Fatal(err)
	}
	if d.readSampleIdx[0] != 10 {
		t.Errorf("seek landed on sample %d, want the sync sample at 10", d.readSampleIdx[0])
	}
}

// A track whose samples are all zero bytes long cannot use the uniform form
// of stsz: sample_size == 0 is the sentinel that says a size table follows.
// Writing the uniform form made Encode walk a table that was never built.
func TestZeroLengthSamplesDoNotBreakStsz(t *testing.T) {
	muxer, err := CreateMp4Muxer(newMemWriteSeeker())
	if err != nil {
		t.Fatal(err)
	}
	tid, err := muxer.AddAudioTrack(MP4_CODEC_MP3, WithAudioSampleRate(8000), WithAudioChannelCount(1))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := muxer.Write(tid, nil, uint64(i)*40, uint64(i)*40); err != nil {
			t.Fatal(err)
		}
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
}

// stsz.Size and stsz.Encode have to agree even when handed a table that does
// not match sample_count, otherwise Encode writes past the buffer it sized.
func TestStszSizeAndEncodeAgree(t *testing.T) {
	for _, tc := range []struct {
		name string
		stsz *movstsz
	}{
		{"uniform", &movstsz{sampleSize: 100, sampleCount: 5}},
		{"table", &movstsz{sampleSize: 0, sampleCount: 3, entrySizelist: []uint32{1, 2, 3}}},
		{"table shorter than count", &movstsz{sampleSize: 0, sampleCount: 9, entrySizelist: []uint32{1}}},
		{"count with no table", &movstsz{sampleSize: 0, sampleCount: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box := NewSampleSizeBox()
			box.stsz = tc.stsz
			// Size must be read before Encode: Encode caches its result in
			// box.Box.Size, and FullBox.Size then returns the cached value
			want := box.Size()
			n, buf := box.Encode()
			if uint64(n) != want || uint64(len(buf)) != want {
				t.Errorf("Encode wrote %d bytes into a %d byte buffer, Size said %d", n, len(buf), want)
			}
		})
	}
}

// Every multi byte field of dOps is big endian, while the OpusHead the values
// come from is little endian. Writing PreSkip in the source byte order turns
// a typical 312 sample priming into 14337 and a decoder throws away the first
// third of a second of audio.
func TestOpusSpecificBoxIsBigEndian(t *testing.T) {
	// OpusHead: version 1, 2 channels, pre-skip 312, 48000 Hz, gain 0, family 0
	head := []byte{
		'O', 'p', 'u', 's', 'H', 'e', 'a', 'd',
		0x01, 0x02, 0x38, 0x01, 0x80, 0xBB, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	box := makeOpusSpecificBox(head)

	if got := string(box[4:8]); got != "dOps" {
		t.Fatalf("box type %q, want dOps", got)
	}
	if got := binary.BigEndian.Uint32(box); int(got) != len(box) {
		t.Errorf("box size field %d, wrote %d bytes", got, len(box))
	}
	payload := box[8:]
	if payload[1] != 2 {
		t.Errorf("OutputChannelCount %d, want 2", payload[1])
	}
	if got := binary.BigEndian.Uint16(payload[2:]); got != 312 {
		t.Errorf("PreSkip %d, want 312", got)
	}
	if got := binary.BigEndian.Uint32(payload[4:]); got != 48000 {
		t.Errorf("InputSampleRate %d, want 48000", got)
	}
	if got := binary.BigEndian.Uint16(payload[8:]); got != 0 {
		t.Errorf("OutputGain %d, want 0", got)
	}
	if payload[10] != 0 {
		t.Errorf("ChannelMappingFamily %d, want 0", payload[10])
	}
	// family 0 carries no mapping table, so the box ends right here
	if len(payload) != dOpsFixedLen {
		t.Errorf("payload is %d bytes, want %d", len(payload), dOpsFixedLen)
	}
}

// Encode and Decode have to agree, which they did not while one wrote little
// endian and the other read big endian.
func TestOpusSpecificBoxRoundTrip(t *testing.T) {
	in := NewdOpsBox()
	in.OutputChannelCount = 6
	in.PreSkip = 312
	in.InputSampleRate = 48000
	in.OutputGain = -256
	in.ChannelMappingFamily = 1
	in.ChanMapTable = &ChannelMappingTable{
		StreamCount:    4,
		CoupledCount:   2,
		ChannelMapping: []byte{0, 4, 1, 2, 3, 5},
	}
	n, data := in.Encode()
	if n != len(data) {
		t.Fatalf("Encode wrote %d bytes into a %d byte buffer", n, len(data))
	}

	out := NewdOpsBox()
	if _, err := out.Decode(bytes.NewReader(data[8:]), uint32(len(data))); err != nil {
		t.Fatal(err)
	}
	if out.OutputChannelCount != in.OutputChannelCount || out.PreSkip != in.PreSkip ||
		out.InputSampleRate != in.InputSampleRate || out.OutputGain != in.OutputGain ||
		out.ChannelMappingFamily != in.ChannelMappingFamily {
		t.Errorf("round trip changed the header:\n got %+v\nwant %+v", out, in)
	}
	if out.ChanMapTable == nil {
		t.Fatal("the channel mapping table was lost")
	}
	if out.ChanMapTable.StreamCount != 4 || out.ChanMapTable.CoupledCount != 2 ||
		!bytes.Equal(out.ChanMapTable.ChannelMapping, in.ChanMapTable.ChannelMapping) {
		t.Errorf("round trip changed the mapping table: %+v", out.ChanMapTable)
	}
}
