package flv

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

func TestAVCMuxerPPSInsertion(t *testing.T) {
	m := NewAVCMuxer()
	frame := append(append(append([]byte{}, testSPS...), testPPS0...), testIDR...)
	tags, err := m.Write(frame, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 {
		t.Fatalf("want 2 tags got %d", len(tags))
	}
	if len(m.ppsset) != 1 {
		t.Fatalf("ppsset %d", len(m.ppsset))
	}
	avccPPS1 := codec.ConvertAnnexBToAVCC(testPPS1)
	avccPPS0 := codec.ConvertAnnexBToAVCC(testPPS0)

	// new PPS id -> inserted into the tag and remembered
	tags, err = m.Write(append(append([]byte{}, testPPS1...), testP...), 40, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || !bytes.Contains(tags[0], avccPPS1) {
		t.Fatalf("new pps not inserted: %d tags", len(tags))
	}
	if len(m.ppsset) != 2 {
		t.Fatalf("ppsset %d", len(m.ppsset))
	}

	// same PPS again -> not duplicated
	tags, err = m.Write(append(append([]byte{}, testPPS0...), testP...), 80, 80)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || bytes.Contains(tags[0], avccPPS0) {
		t.Fatal("unchanged pps must not be re-inserted")
	}

	// changed PPS with same id -> inserted
	changed := append([]byte{}, testPPS0...)
	changed[len(changed)-1] ^= 0x01
	tags, err = m.Write(append(append([]byte{}, changed...), testP...), 120, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || !bytes.Contains(tags[0], codec.ConvertAnnexBToAVCC(changed)) {
		t.Fatal("changed pps must be inserted")
	}
	// stored copies are Annex-B and do not alias the input
	changed[5] = 0xAA
	if !bytes.HasPrefix(m.ppsset[0], []byte{0, 0, 0, 1}) || m.ppsset[0][5] == 0xAA {
		t.Fatal("stored pps aliases caller buffer or lacks start code")
	}
}

func TestMuxersDoNotModifyInput(t *testing.T) {
	frame := append(append(append([]byte{}, testSPS...), testPPS0...), testIDR...)
	orig := append([]byte{}, frame...)
	if _, err := NewAVCMuxer().Write(frame, 0, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, orig) {
		t.Fatal("AVCMuxer modified its input")
	}
	hevc := []byte{0, 0, 0, 1, 0x26, 0x01, 0xAF, 0x00, 0x00, 0x01, 0x02, 0x01, 0xD0}
	orig = append([]byte{}, hevc...)
	if _, err := NewHevcMuxer().Write(hevc, 0, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hevc, orig) {
		t.Fatal("HevcMuxer modified its input")
	}
}

func TestMp3MuxerTagCount(t *testing.T) {
	// MPEG-1 Layer III, 128 kbps, 44.1 kHz, no padding: 417 byte frames
	frame := make([]byte, 417)
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00})
	frames := append(append([]byte{}, frame...), frame...)
	tags, err := new(Mp3Muxer).Write(frames, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 {
		t.Fatalf("want 2 tags got %d", len(tags))
	}
	for i, tag := range tags {
		if tag == nil || len(tag) != 1+417 {
			t.Fatalf("tag %d len %d", i, len(tag))
		}
		if tag[0]>>4 != uint8(FLV_MP3) {
			t.Fatalf("tag %d format %#x", i, tag[0])
		}
	}
	g711, err := CreateAudioMuxer(FLV_G711A)
	if err != nil {
		t.Fatal(err)
	}
	if tags, err := g711.Write([]byte{1, 2, 3}, 0, 0); err != nil || len(tags) != 1 || tags[0][0] != 0x72 {
		t.Fatalf("g711a tag % x err %v", tags, err)
	}
}

func TestFlvMuxerWriteFramesHeader(t *testing.T) {
	m, err := NewFlvMuxer(FLV_AVC, FLV_AAC)
	if err != nil {
		t.Fatal(err)
	}
	frame := append(append(append([]byte{}, testSPS...), testPPS0...), testIDR...)
	tags, err := m.WriteVideo(frame, 0x01020304, 0x01020300)
	if err != nil || len(tags) != 2 {
		t.Fatalf("err %v tags %d", err, len(tags))
	}
	for _, tag := range tags {
		var ft FlvTag
		if err := ft.Decode(tag); err != nil {
			t.Fatalf("decode tag header: %v", err)
		}
		if ft.TagType != uint8(VIDEO_TAG) || int(ft.DataSize) != len(tag)-11 || ft.Timestamp != 0x020300 || ft.TimestampExtended != 1 {
			t.Fatalf("bad tag header %+v", ft)
		}
	}
}

func TestFlvReaderWithoutOnFrame(t *testing.T) {
	var buf bytes.Buffer
	w := CreateFlvWriter(&buf)
	if err := w.WriteFlvHeader(); err != nil {
		t.Fatal(err)
	}
	frame := append(append(append([]byte{}, testSPS...), testPPS0...), testIDR...)
	if err := w.WriteH264(frame, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteH264(testP, 40, 40); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	r := CreateFlvReader()
	// feed byte by byte to exercise the cache path as well
	for i := range data {
		if err := mustNotPanic(t, "Input", func() error { return r.Input(data[i : i+1]) }); err != nil {
			t.Fatal(err)
		}
	}
	// the stream ends with a PreviousTagSize, so the parser is left waiting
	// for the header of the next tag
	if len(r.cache) != 0 || r.state != FLV_PARSER_FLV_TAG {
		t.Fatalf("cache %d state %d", len(r.cache), r.state)
	}

	// with a callback the same stream yields the two frames
	r = CreateFlvReader()
	var frames [][]byte
	r.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		frames = append(frames, append([]byte{}, frame...))
	}
	if err := r.Input(data); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || !bytes.Equal(frames[0], frame) || !bytes.Equal(frames[1], testP) {
		t.Fatalf("frames %d", len(frames))
	}
}

func TestFlvReaderErrorAdvances(t *testing.T) {
	var hdr = []byte{'F', 'L', 'V', 1, 5, 0, 0, 0, 9, 0, 0, 0, 0}
	// video tag with a truncated NALU length, followed by a valid audio tag
	bad := []byte{0x17, 0x01, 0, 0, 0, 0x00, 0x00, 0x00, 0x10, 0x65}
	tag := FlvTag{TagType: uint8(VIDEO_TAG), DataSize: uint32(len(bad))}
	stream := append(append(append([]byte{}, hdr...), tag.Encode()...), bad...)
	stream = append(stream, 0, 0, 0, 0)
	g711 := []byte{0x72, 0x01, 0x02}
	tag = FlvTag{TagType: uint8(AUDIO_TAG), DataSize: uint32(len(g711))}
	stream = append(append(stream, tag.Encode()...), g711...)

	r := CreateFlvReader()
	var audio int
	r.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		if cid == codec.CODECID_AUDIO_G711A {
			audio++
		}
	}
	if err := r.Input(stream); err == nil {
		t.Fatal("expected error from truncated video tag")
	}
	// the remaining bytes are cached, a further (empty) Input resumes after the bad tag
	if err := r.Input(nil); err != nil {
		t.Fatal(err)
	}
	if audio != 1 {
		t.Fatalf("audio frames %d", audio)
	}
}

func TestCreateMuxerUnsupported(t *testing.T) {
	if _, err := CreateVideoMuxer(FLV_VIDEO_CODEC_UNKNOWN); !errors.Is(err, ErrUnsupportedVideoCodec) {
		t.Fatalf("want ErrUnsupportedVideoCodec, got %v", err)
	}
	if _, err := CreateAudioMuxer(FLV_SOUND_FORMAT_UNKNOWN); !errors.Is(err, ErrUnsupportedSoundFormat) {
		t.Fatalf("want ErrUnsupportedSoundFormat, got %v", err)
	}
	if _, err := NewFlvMuxer(FLV_VIDEO_CODEC_UNKNOWN, FLV_AAC); !errors.Is(err, ErrUnsupportedVideoCodec) {
		t.Fatalf("want ErrUnsupportedVideoCodec, got %v", err)
	}
	if _, err := NewFlvMuxer(FLV_AVC, FLV_SOUND_FORMAT_UNKNOWN); !errors.Is(err, ErrUnsupportedSoundFormat) {
		t.Fatalf("want ErrUnsupportedSoundFormat, got %v", err)
	}
}

func TestAVCMuxerShortSPS(t *testing.T) {
	// an SPS with only the nal header cannot produce an avcC record, the muxer
	// must say so instead of letting the codec index past the end
	short := []byte{0x00, 0x00, 0x00, 0x01, 0x67}
	frame := append(append(append([]byte{}, short...), testPPS0...), testIDR...)
	if _, err := NewAVCMuxer().Write(frame, 0, 0); !errors.Is(err, errShortSPS) {
		t.Fatalf("want errShortSPS, got %v", err)
	}
}

func TestAACMuxerBadFrame(t *testing.T) {
	// syncword with a frame_length below the header size
	bad := []byte{0xFF, 0xF1, 0x50, 0x80, 0x00, 0x1F, 0xFC, 0x00, 0x00}
	if _, err := NewAACMuxer().Write(bad, 0, 0); err == nil {
		t.Fatal("expected an error for an invalid adts frame length")
	}
	// a frame that claims more bytes than it has
	truncated := []byte{0xFF, 0xF1, 0x50, 0x80, 0x1F, 0xFF, 0xFC, 0xDE, 0xAD}
	if _, err := NewAACMuxer().Write(truncated, 0, 0); err == nil {
		t.Fatal("expected an error for a truncated adts frame")
	}
}

func TestMp3MuxerBadFrame(t *testing.T) {
	if _, err := new(Mp3Muxer).Write([]byte{0x00, 0x01, 0x02, 0x03}, 0, 0); err == nil {
		t.Fatal("expected an error for a non mp3 buffer")
	}
	// valid header, but the frame is cut short
	truncated := make([]byte, 100)
	copy(truncated, []byte{0xFF, 0xFB, 0x90, 0x00})
	if _, err := new(Mp3Muxer).Write(truncated, 0, 0); err == nil {
		t.Fatal("expected an error for a truncated mp3 frame")
	}
}

func TestFlvWriterPropagatesMuxerError(t *testing.T) {
	var buf bytes.Buffer
	w := CreateFlvWriter(&buf)
	if err := w.WriteFlvHeader(); err != nil {
		t.Fatal(err)
	}
	before := buf.Len()
	if err := w.WriteMp3([]byte{0x00, 0x01, 0x02, 0x03}, 0, 0); err == nil {
		t.Fatal("WriteMp3 must report the muxer error")
	}
	if buf.Len() != before {
		t.Fatal("a failed frame must not write anything")
	}
	if err := w.WriteAAC([]byte{0xFF, 0xF1, 0x50, 0x80, 0x00, 0x1F, 0xFC}, 0, 0); err == nil {
		t.Fatal("WriteAAC must report the muxer error")
	}
}
