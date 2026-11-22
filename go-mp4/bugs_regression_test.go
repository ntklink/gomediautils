package mp4

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math"
	"testing"
)

func TestMdatPayloadExceedsInt64Rejected(t *testing.T) {
	// 16-byte BasicBox header: size=1, type="mdat", largesize = MaxInt64 + 17 (payloadLen = MaxInt64 + 1)
	var buf [16]byte
	binary.BigEndian.PutUint32(buf[0:4], 1)
	copy(buf[4:8], "mdat")
	binary.BigEndian.PutUint64(buf[8:16], uint64(math.MaxInt64)+17)

	d := CreateMp4Demuxer(bytes.NewReader(buf[:]))
	if _, err := d.ReadHead(); err == nil {
		t.Fatal("expected error for mdat payloadLen > math.MaxInt64")
	}
}

func TestTkhdEncodeVersion1OverflowDuration(t *testing.T) {
	tkhd := NewTrackHeaderBox()
	tkhd.Track_ID = 7
	tkhd.Duration = 0x1_0000_0005 // > 0xFFFFFFFF

	n, buf := tkhd.Encode()
	if tkhd.Box.Version != 1 {
		t.Fatalf("version = %d, want 1", tkhd.Box.Version)
	}
	if n != 104 || len(buf) != 104 {
		t.Fatalf("Encode returned n=%d len=%d, want 104", n, len(buf))
	}

	var decoded TrackHeaderBox
	decoded.Box = new(FullBox)
	if _, err := decoded.Decode(bytes.NewReader(buf[8:])); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.Track_ID != 7 || decoded.Duration != 0x1_0000_0005 {
		t.Fatalf("decoded Track_ID=%d Duration=%#x, want 7, 0x100000005", decoded.Track_ID, decoded.Duration)
	}
}

func TestHmhdConsumedCompletely(t *testing.T) {
	// Build minimal moov: mvhd + trak(tkhd + mdhd + hmhd + stbl)
	mvhd := makeMvhdBox(2, 1000)
	tkhd := makeTkhdBox(&mp4track{trackId: 1, cid: MP4_CODEC_H264}, 1000)
	mdhd := makeMdhdBox(1000)
	hmhd := makeHmhdBox()

	// Wrap inside trak and moov
	var trakBody []byte
	trakBody = append(trakBody, tkhd...)
	trakBody = append(trakBody, mdhd...)
	trakBody = append(trakBody, hmhd...)
	// Add a free box after hmhd to ensure stream alignment is exact
	free := NewFreeBox()
	free.Data = []byte("ok")
	_, freeBytes := free.Encode()
	trakBody = append(trakBody, freeBytes...)

	trakHdr := BasicBox{Size: uint64(8 + len(trakBody)), Type: [4]byte{'t', 'r', 'a', 'k'}}
	_, trakBox := trakHdr.Encode()
	copy(trakBox[8:], trakBody)

	var moovBody []byte
	moovBody = append(moovBody, mvhd...)
	moovBody = append(moovBody, trakBox...)
	moovHdr := BasicBox{Size: uint64(8 + len(moovBody)), Type: [4]byte{'m', 'o', 'o', 'v'}}
	_, moovBox := moovHdr.Encode()
	copy(moovBox[8:], moovBody)

	infos, err := CreateMp4Demuxer(bytes.NewReader(moovBox)).ReadHead()
	if err != nil {
		t.Fatalf("ReadHead failed with hmhd box: %v", err)
	}
	if len(infos) != 1 || infos[0].TrackId != 1 {
		t.Fatalf("unexpected tracks: %+v", infos)
	}
}

func TestMvhdRoundTrip(t *testing.T) {
	in := NewMovieHeaderBox()
	in.Timescale = 90000
	in.Duration = 180000
	in.Next_track_ID = 42

	n, buf := in.Encode()
	if n != len(buf) {
		t.Fatalf("Encode returned n=%d, len(buf)=%d", n, len(buf))
	}

	out := MovieHeaderBox{Box: new(FullBox)}
	if _, err := out.Decode(bytes.NewReader(buf[8:])); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if out.Timescale != in.Timescale || out.Duration != in.Duration || out.Next_track_ID != in.Next_track_ID {
		t.Fatalf("mvhd mismatch: got Timescale=%d Duration=%d Next_track_ID=%d", out.Timescale, out.Duration, out.Next_track_ID)
	}
	if out.Matrix != in.Matrix {
		t.Fatalf("mvhd Matrix mismatch: got %v, want %v", out.Matrix, in.Matrix)
	}
}

func TestSgpdSeigSkipByteBlockAndCurrentTrack(t *testing.T) {
	// Test DecodeSeigSampleGroupEntry bitmask: byteTwo = 0x1f -> crypt=1, skip=15
	entry := make([]byte, 20)
	entry[1] = 0x1f
	entry[2] = 1 // IsProtected
	entry[3] = 8 // PerSampleIVSize
	for i := 0; i < 16; i++ {
		entry[4+i] = byte(i + 1)
	}
	got, _, err := DecodeSeigSampleGroupEntry("seig", 20, entry)
	if err != nil {
		t.Fatalf("DecodeSeigSampleGroupEntry: %v", err)
	}
	seig := got.(*SeigSampleGroupEntry)
	if seig.CryptByteBlock != 1 || seig.SkipByteBlock != 15 {
		t.Fatalf("got crypt=%d skip=%d, want 1, 15", seig.CryptByteBlock, seig.SkipByteBlock)
	}

	// Test decodeSgpdBox uses demuxer.currentTrack instead of lastTrack
	t1 := &mp4track{trackId: 1}
	t2 := &mp4track{trackId: 2}
	var payload []byte
	payload = binary.BigEndian.AppendUint32(payload, 0x01000000) // version=1, flags=0
	payload = append(payload, "seig"...)
	payload = binary.BigEndian.AppendUint32(payload, 20) // defaultLength
	payload = binary.BigEndian.AppendUint32(payload, 1)  // entryCount
	payload = append(payload, entry...)

	d := &MovDemuxer{
		reader:       bytes.NewReader(payload),
		tracks:       []*mp4track{t1, t2},
		currentTrack: t1,
	}
	if err := decodeSgpdBox(d, uint32(BasicBoxLen+len(payload))); err != nil {
		t.Fatalf("decodeSgpdBox: %v", err)
	}
	if t1.lastSeig == nil || t2.lastSeig != nil {
		t.Fatalf("expected sgpd to attach to currentTrack (t1), got t1=%v t2=%v", t1.lastSeig, t2.lastSeig)
	}
}

func TestTrackDurationFromMdhdNotTkhd(t *testing.T) {
	// Movie timescale = 1000, tkhd.Duration = 2000 (2s in movie timescale)
	// Media timescale = 90000, mdhd.Duration = 180000 (2s in media timescale)
	mvhd := makeMvhdBox(2, 2000)
	tkhd := makeTkhdBox(&mp4track{trackId: 1, cid: MP4_CODEC_H264}, 2000)
	mdhdBox := NewMediaHeaderBox()
	mdhdBox.Timescale = 90000
	mdhdBox.Duration = 180000
	_, mdhd := mdhdBox.Encode()

	var trakBody []byte
	trakBody = append(trakBody, tkhd...)
	trakBody = append(trakBody, mdhd...)
	trakHdr := BasicBox{Size: uint64(8 + len(trakBody)), Type: [4]byte{'t', 'r', 'a', 'k'}}
	_, trakBox := trakHdr.Encode()
	copy(trakBox[8:], trakBody)

	var moovBody []byte
	moovBody = append(moovBody, mvhd...)
	moovBody = append(moovBody, trakBox...)
	moovHdr := BasicBox{Size: uint64(8 + len(moovBody)), Type: [4]byte{'m', 'o', 'o', 'v'}}
	_, moovBox := moovHdr.Encode()
	copy(moovBox[8:], moovBody)

	infos, err := CreateMp4Demuxer(bytes.NewReader(moovBox)).ReadHead()
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 track, got %d", len(infos))
	}
	if infos[0].Duration != 180000 {
		t.Fatalf("TrackInfo.Duration = %d, want 180000 (from mdhd, not tkhd)", infos[0].Duration)
	}
}

func TestSidxVersion0AndSAPBitPacking(t *testing.T) {
	in := &SegmentIndexBox{
		Box:                      NewFullBox([4]byte{'s', 'i', 'd', 'x'}, 0),
		ReferenceID:              1,
		TimeScale:                90000,
		EarliestPresentationTime: 1000,
		FirstOffset:              200,
		ReferenceCount:           1,
		Entrys: []sidxEntry{
			{
				ReferenceType:      0,
				ReferencedSize:     12345,
				SubsegmentDuration: 90000,
				StartsWithSAP:      1,
				SAPType:            3,
				SAPDeltaTime:       0x05ABCDEF, // top nibble of 28-bit field is 0x05
			},
		},
	}
	wantSize := in.Size()
	n, buf := in.Encode()
	if uint64(n) != wantSize || uint64(len(buf)) != wantSize {
		t.Fatalf("v0 sidx Encode n=%d len=%d Size=%d", n, len(buf), wantSize)
	}

	out := &SegmentIndexBox{Box: &FullBox{Box: &BasicBox{Size: wantSize}}}
	if _, err := out.Decode(bytes.NewReader(buf[8:])); err != nil {
		t.Fatalf("v0 sidx Decode: %v", err)
	}
	if len(out.Entrys) != 1 {
		t.Fatalf("entries = %d, want 1", len(out.Entrys))
	}
	e := out.Entrys[0]
	if e.StartsWithSAP != 1 || e.SAPType != 3 || e.SAPDeltaTime != 0x05ABCDEF {
		t.Fatalf("decoded entry = %+v, want StartsWithSAP=1 SAPType=3 SAPDeltaTime=0x05ABCDEF", e)
	}
}

func TestTfhdResetsCurrentTrackOnUnknownID(t *testing.T) {
	t1 := &mp4track{trackId: 1}
	tfhd := NewTrackFragmentHeaderBox(999) // unknown track ID
	_, raw := tfhd.Encode()

	d := &MovDemuxer{
		reader:       bytes.NewReader(raw[8:]),
		tracks:       []*mp4track{t1},
		currentTrack: t1,
	}
	if err := decodeTfhdBox(d, uint32(len(raw))); err != nil {
		t.Fatalf("decodeTfhdBox: %v", err)
	}
	if d.currentTrack != nil {
		t.Fatalf("expected currentTrack to be reset to nil for unknown track ID")
	}
}

func TestSeekTimePastEOFPositionsAtEOF(t *testing.T) {
	track := &mp4track{
		cid:       MP4_CODEC_H264,
		timescale: 1000,
		stbltable: &movstbl{stss: &movstss{sampleNumber: []uint32{1, 5}}},
	}
	for i := 0; i < 10; i++ {
		track.samplelist = append(track.samplelist, sampleEntry{dts: uint64(i) * 40, pts: uint64(i) * 40})
	}
	d := &MovDemuxer{tracks: []*mp4track{track}, readSampleIdx: make([]uint32, 1)}
	if err := d.SeekTime(999999); err != nil {
		t.Fatal(err)
	}
	if int(d.readSampleIdx[0]) != len(track.samplelist) {
		t.Fatalf("readSampleIdx = %d, want %d (EOF)", d.readSampleIdx[0], len(track.samplelist))
	}
	if _, err := d.ReadPacket(); err != io.EOF {
		t.Fatalf("ReadPacket after seek past EOF = %v, want io.EOF", err)
	}
}

func TestWriteAACStrips9ByteADTSWithCRC(t *testing.T) {
	// ADTS header with protection_absent = 0 (CRC present -> 9-byte header)
	// Syncword=0xFFF, ID=0, Layer=0, protection_absent=0 -> bytes 0..1 = 0xFF, 0xF0
	// profile=1 (AAC-LC), freqIdx=4 (44100Hz), priv=0, ch=2 -> byte2 = (1<<6)|(4<<2)|0 = 0x50, byte3 = (2<<6)|...
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	frameLen := 9 + len(payload)
	adts := make([]byte, frameLen)
	adts[0] = 0xFF
	adts[1] = 0xF0 // protection_absent = 0 -> 9-byte header
	adts[2] = 0x50
	adts[3] = 0x80 | byte((frameLen>>11)&0x03)
	adts[4] = byte((frameLen >> 3) & 0xFF)
	adts[5] = byte((frameLen&0x07)<<5) | 0x1F
	adts[6] = 0xFC
	adts[7] = 0x12 // CRC byte 1
	adts[8] = 0x34 // CRC byte 2
	copy(adts[9:], payload)

	ws := newFmp4WriterSeeker(64)
	track := newmp4track(MP4_CODEC_AAC, ws)
	if err := track.writeAAC(adts, 0, 0); err != nil {
		t.Fatalf("writeAAC: %v", err)
	}
	if !bytes.Equal(ws.buffer, payload) {
		t.Fatalf("written AAC payload = %x, want %x (9-byte ADTS header with CRC should be stripped)", ws.buffer, payload)
	}
}

func TestNegativePtsClampedToZero(t *testing.T) {
	// Test trun v1 negative composition time offset clamping
	trun := NewTrackRunBox()
	trun.Box.Version = 1
	flags := TR_FLAG_DATA_OFFSET | TR_FLAG_DATA_SAMPLE_DURATION | TR_FLAG_DATA_SAMPLE_SIZE | TR_FLAG_DATA_SAMPLE_COMPOSITION_TIME
	trun.Box.Flags = [3]byte{byte(flags >> 16), byte(flags >> 8), byte(flags)}
	trun.SampleCount = 1
	trun.Dataoffset = 0
	negOffset := int32(-500)
	trun.EntryList = &movtrun{
		entrys: []trunEntry{
			{sampleDuration: 40, sampleSize: 10, sampleCompositionTimeOffset: uint32(negOffset)},
		},
	}
	_, raw := trun.Encode()

	track := &mp4track{trackId: 1, startDts: 100}
	d := &MovDemuxer{
		reader:       bytes.NewReader(raw[8:]),
		currentTrack: track,
	}
	if err := decodeTrunBox(d, uint32(len(raw))); err != nil {
		t.Fatalf("decodeTrunBox: %v", err)
	}
	if len(track.samplelist) != 1 || track.samplelist[0].pts != 0 {
		t.Fatalf("trun pts = %d, want 0 (clamped)", track.samplelist[0].pts)
	}
}

func TestMakeTrunBoxNilOrSmallerLastSample(t *testing.T) {
	// nil lastSample should not panic
	tr1 := &mp4track{
		cid:             MP4_CODEC_AAC,
		defaultDuration: 23,
		defaultSize:     100,
		samplelist:      []sampleEntry{{dts: 100, pts: 100, size: 100, offset: 0}},
		lastSample:      nil,
	}
	box := makeTrunBox(tr1, 0, 1, 64)
	if len(box) == 0 {
		t.Fatal("expected non-empty trun box")
	}

	// lastSample.dts < samplelist[last].dts should not underflow
	tr2 := &mp4track{
		cid:             MP4_CODEC_AAC,
		defaultDuration: 23,
		defaultSize:     100,
		samplelist:      []sampleEntry{{dts: 100, pts: 100, size: 100, offset: 0}},
		lastSample:      &sampleCache{dts: 50},
	}
	_ = makeTrunBox(tr2, 0, 1, 64)
	if tr2.runDuration != 0 {
		// makeTrunBox adds sampleDuration to track.runDuration; should be defaultDuration (23), not underflow
		if tr2.runDuration != 23 {
			t.Fatalf("runDuration = %d, want 23", tr2.runDuration)
		}
	}
}

func TestPsshFairPlayLowercaseUUID(t *testing.T) {
	id, err := hex.DecodeString("94ce86fb07ff4f43adb893d2fa968ca2")
	if err != nil {
		t.Fatal(err)
	}
	var pssh PsshBox
	copy(pssh.SystemID[:], id)
	if !pssh.IsFairPlay() {
		t.Fatal("IsFairPlay() returned false for FairPlay SystemID")
	}
}

func TestSaioUsesDefaultPerSampleIVSize16(t *testing.T) {
	// Build a buffer containing a 16-byte IV at offset 0 (moofOffset=0, saio offset=0)
	iv := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// Build saio box payload: version=0, flags=0, entryCount=1, offset=0
	var saioPayload []byte
	saioPayload = binary.BigEndian.AppendUint32(saioPayload, 0) // version+flags
	saioPayload = binary.BigEndian.AppendUint32(saioPayload, 1) // entryCount
	saioPayload = binary.BigEndian.AppendUint32(saioPayload, 0) // offset 0

	// Stream layout: [16-byte IV][saioPayload]
	stream := append(iv[:], saioPayload...)
	r := bytes.NewReader(stream)
	if _, err := r.Seek(16, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	track := &mp4track{
		trackId:                1,
		defaultPerSampleIVSize: 16,
		lastSaiz: &SaizBox{
			DefaultSampleInfoSize: 16,
			SampleCount:           1,
		},
	}
	d := &MovDemuxer{
		reader:       r,
		moofOffset:   0,
		currentTrack: track,
	}
	if err := decodeSaioBox(d, uint32(BasicBoxLen+len(saioPayload))); err != nil {
		t.Fatalf("decodeSaioBox with 16-byte IV: %v", err)
	}
	if len(track.subSamples) != 1 || !bytes.Equal(track.subSamples[0].iv, iv[:]) {
		t.Fatalf("got subSamples=%+v, want 16-byte IV %x", track.subSamples, iv)
	}
}

func TestEsdsURLFlagAndMP3StreamTypeAndVolume(t *testing.T) {
	// 1. makeDecoderConfigDescriptor for MP3/MP2 sets streamType = 0x15
	for _, cid := range []MP4_CODEC_TYPE{MP4_CODEC_MP2, MP4_CODEC_MP3} {
		dcd, err := makeDecoderConfigDescriptor(cid, nil)
		if err != nil {
			t.Fatalf("makeDecoderConfigDescriptor(%v): %v", cid, err)
		}
		if dcd[6] != 0x15 {
			t.Errorf("cid %v streamType = %#x, want 0x15", cid, dcd[6])
		}
		tkhdBytes := makeTkhdBox(&mp4track{trackId: 1, cid: cid}, 1000)
		var tkhd TrackHeaderBox
		tkhd.Box = new(FullBox)
		if _, err := tkhd.Decode(bytes.NewReader(tkhdBytes[8:])); err != nil {
			t.Fatalf("tkhd Decode: %v", err)
		}
		if tkhd.Volume != 0x0100 {
			t.Errorf("cid %v tkhd Volume = %#x, want 0x0100", cid, tkhd.Volume)
		}
	}

	// 2. decodeESDescriptor handles urlFlag == 1
	dcd, err := makeDecoderConfigDescriptor(MP4_CODEC_AAC, []byte{0x12, 0x10})
	if err != nil {
		t.Fatal(err)
	}
	sld := makeSLDescriptor()
	urlStr := []byte("http://example.com")
	// ES_Descriptor body: esId(2) + flags(1, urlFlag=0x40) + urlLen(1) + urlStr + dcd + sld
	esdBodyLen := 2 + 1 + 1 + len(urlStr) + len(dcd) + len(sld)
	esd := makeBaseDescriptor(0x03, uint32(esdBodyLen))
	off := 5
	binary.BigEndian.PutUint16(esd[off:], 1)
	off += 2
	esd[off] = 0x40 // urlFlag = 1
	off++
	esd[off] = byte(len(urlStr))
	off++
	copy(esd[off:], urlStr)
	off += len(urlStr)
	copy(esd[off:], dcd)
	off += len(dcd)
	copy(esd[off:], sld)

	track := &mp4track{}
	vos, err := decodeESDescriptor(esd, track)
	if err != nil {
		t.Fatalf("decodeESDescriptor with urlFlag=1 failed: %v", err)
	}
	if track.cid != MP4_CODEC_AAC || !bytes.Equal(vos, []byte{0x12, 0x10}) {
		t.Fatalf("got cid=%v vos=%x, want AAC [12 10]", track.cid, vos)
	}
}
