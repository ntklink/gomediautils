package es

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

var (
	h264SPS = []byte{0, 0, 0, 1, 0x67, 0x64, 0x00, 0x0a, 0xac, 0xd9, 0x44, 0x7b, 0x01, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x03, 0x20, 0xf1, 0x22, 0x59, 0x60}
	h264PPS = []byte{0, 0, 0, 1, 0x68, 0xeb, 0xe3, 0xcb, 0x22, 0xc0}
	h265VPS = []byte{0, 0, 0, 1, 0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x1e, 0x95, 0x98, 0x09}
)

// The orderer places frames by their picture order count: the presentation
// index is the rank in presentation order plus the reordering delay, the
// decode index the position in the stream.
func TestOrderer(t *testing.T) {
	for name, tc := range map[string]struct {
		pocs      []int64
		periods   []int64
		wantPts   []int64
		wantDelay int64
	}{
		"no reordering": {
			pocs: []int64{0, 2, 4, 6}, wantPts: []int64{0, 1, 2, 3},
		},
		// I0 P3 B1 B2: the P frame is decoded one frame before its turn
		"two b frames": {
			pocs:    []int64{0, 6, 2, 4, 12, 8, 10},
			wantPts: []int64{1, 4, 2, 3, 7, 5, 6}, wantDelay: 1,
		},
		// I0 P4 B2 b1 b3 with a b-pyramid: two frames of delay
		"pyramid": {
			pocs:    []int64{0, 8, 4, 2, 6},
			wantPts: []int64{2, 6, 4, 3, 5}, wantDelay: 2,
		},
		// the second IDR restarts the count; everything after it is shown
		// after everything before it
		"idr resets the count": {
			pocs:    []int64{0, 4, 2, 0, 4, 2},
			periods: []int64{0, 0, 0, 1, 1, 1},
			wantPts: []int64{1, 3, 2, 4, 6, 5}, wantDelay: 1,
		},
	} {
		o := newOrderer()
		var got []*orderedFrame
		for i, poc := range tc.pocs {
			var period int64
			if tc.periods != nil {
				period = tc.periods[i]
			}
			got = append(got, o.push(&Frame{}, orderKey{period: period, poc: poc})...)
		}
		got = append(got, o.flush()...)
		if o.delay != tc.wantDelay {
			t.Errorf("%s: delay %d, want %d", name, o.delay, tc.wantDelay)
		}
		for i, f := range got {
			if f.dtsIndex != int64(i) || f.ptsIndex != tc.wantPts[i] {
				t.Errorf("%s: frame %d dts %d pts %d, want %d %d", name, i, f.dtsIndex, f.ptsIndex, i, tc.wantPts[i])
			}
			if f.ptsIndex < f.dtsIndex {
				t.Errorf("%s: frame %d shown before it is decoded", name, i)
			}
		}
	}
}

func TestProbe(t *testing.T) {
	adts, _ := codec.ConvertASCToADTS([]byte{0x12, 0x10}, 10)
	for name, tc := range map[string]struct {
		head []byte
		want codec.CodecID
	}{
		"h264 sps":        {h264SPS, codec.CODECID_VIDEO_H264},
		"h264 aud":        {[]byte{0, 0, 0, 1, 0x09, 0xf0, 0, 0, 0, 1, 0x67}, codec.CODECID_VIDEO_H264},
		"h265 vps":        {h265VPS, codec.CODECID_VIDEO_H265},
		"h265 aud":        {[]byte{0, 0, 0, 1, 0x46, 0x01, 0x50}, codec.CODECID_VIDEO_H265},
		"adts":            {append(adts.Encode(), 1, 2, 3), codec.CODECID_AUDIO_AAC},
		"mp3":             {[]byte{0xff, 0xfb, 0x90, 0x64, 0, 0}, codec.CODECID_AUDIO_MP3},
		"mp3 with id3 v2": {[]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), codec.CODECID_AUDIO_MP3},
		"text":            {[]byte("hello world"), codec.CODECID_UNRECOGNIZED},
		"empty":           {nil, codec.CODECID_UNRECOGNIZED},
	} {
		if got := Probe(tc.head); got != tc.want {
			t.Errorf("%s: %s, want %s", name, codec.CodecString(got), codec.CodecString(tc.want))
		}
	}
}

// Access units split where a new picture starts: parameter sets and an AUD
// go with the slice after them, a second slice of the same picture stays.
func TestH264AccessUnits(t *testing.T) {
	idr := []byte{0, 0, 1, 0x65, 0x88, 0x84}        // first_mb_in_slice 0
	idrSlice2 := []byte{0, 0, 1, 0x65, 0x40, 0x84}  // first_mb_in_slice 1
	nonIdr := []byte{0, 0, 0, 1, 0x41, 0x9a, 0x21} // first_mb_in_slice 0
	aud := []byte{0, 0, 0, 1, 0x09, 0xf0}
	var stream []byte
	for _, n := range [][]byte{aud, h264SPS, h264PPS, idr, idrSlice2, aud, nonIdr, aud, nonIdr} {
		stream = append(stream, n...)
	}
	r := NewH264Reader(bytes.NewReader(stream), WithFrameRate(25))
	var frames []*Frame
	for {
		f, err := r.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
	if len(frames) != 3 {
		t.Fatalf("%d access units, want 3", len(frames))
	}
	if !frames[0].KeyFrame || frames[1].KeyFrame {
		t.Error("key frame flags wrong")
	}
	nalus := 0
	codec.SplitFrame(frames[0].Data, func([]byte) bool { nalus++; return true })
	if nalus != 5 {
		t.Errorf("first access unit has %d nal units, want aud sps pps and two slices", nalus)
	}
	for i, f := range frames {
		if f.Dts != uint64(i*40) || f.Pts != f.Dts {
			t.Errorf("frame %d at %d/%d", i, f.Pts, f.Dts)
		}
	}
}

func adtsFrame(payload ...byte) []byte {
	hdr, _ := codec.ConvertASCToADTS([]byte{0x11, 0x90}, len(payload)+7) // LC, 48 kHz, stereo
	return append(hdr.Encode(), payload...)
}

func readAll(t *testing.T, r Reader) []*Frame {
	t.Helper()
	var out []*Frame
	for {
		f, err := r.ReadFrame()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
}

func TestAACReader(t *testing.T) {
	var stream []byte
	stream = append(stream, "junk"...) // resynchronised past
	for i := 0; i < 4; i++ {
		stream = append(stream, adtsFrame(byte(i), 1, 2)...)
	}
	stream = append(stream, adtsFrame(9, 9, 9)[:8]...) // cut off
	frames := readAll(t, NewAACReader(bytes.NewReader(stream)))
	if len(frames) != 4 {
		t.Fatalf("%d frames", len(frames))
	}
	for i, f := range frames {
		want := uint64(i) * 1024 * 1000 / 48000
		if f.Pts != want || !bytes.Equal(f.Data, adtsFrame(byte(i), 1, 2)) {
			t.Errorf("frame %d at %d: %x", i, f.Pts, f.Data)
		}
	}
}

func TestMP3ReaderSkipsTags(t *testing.T) {
	// MPEG-1 layer 3, 128 kbit/s, 44.1 kHz: 417 byte frames
	frame := func(fill byte) []byte {
		f := bytes.Repeat([]byte{fill}, 417)
		copy(f, []byte{0xff, 0xfb, 0x90, 0x64})
		return f
	}
	info := frame(0)
	copy(info[36:], "Info")
	id3 := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x05"), 1, 2, 3, 4, 5)
	var stream []byte
	for _, part := range [][]byte{id3, info, frame(1), frame(2), []byte("TAG")} {
		stream = append(stream, part...)
	}
	frames := readAll(t, NewMP3Reader(bytes.NewReader(stream)))
	if len(frames) != 2 || frames[0].Data[5] != 1 || frames[1].Data[5] != 2 {
		t.Fatalf("%d frames", len(frames))
	}
	if frames[1].Pts != 1152*1000/44100 {
		t.Errorf("second frame at %d", frames[1].Pts)
	}
}

func TestG711Reader(t *testing.T) {
	data := make([]byte, 1000)
	frames := readAll(t, NewG711Reader(bytes.NewReader(data), codec.CODECID_AUDIO_G711U, WithChannelCount(2)))
	// 20 ms of stereo at 8 kHz is 320 bytes: three full frames and 40 bytes
	if len(frames) != 4 || len(frames[3].Data) != 40 || frames[3].Pts != 60 {
		t.Fatalf("%d frames, last %d bytes at %d", len(frames), len(frames[len(frames)-1].Data), frames[len(frames)-1].Pts)
	}
}

func TestWriterLengthPrefixedToAnnexB(t *testing.T) {
	avcC, err := codec.CreateH264AVCCExtradata([][]byte{h264SPS}, [][]byte{h264PPS})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w, err := NewWriter(&out, codec.CODECID_VIDEO_H264, avcC)
	if err != nil {
		t.Fatal(err)
	}
	// a 300 byte slice: its length prefix 00 00 01 2c looks like a start code
	slice := append([]byte{0x65, 0x88}, make([]byte, 298)...)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(slice)))
	frame = append(frame, slice...)
	if err := w.WriteFrame(frame); err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte{}, h264SPS...), h264PPS...), 0, 0, 0, 1)
	want = append(want, slice...)
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("got %x", out.Bytes()[:40])
	}

	// Annex-B input with its own parameter sets passes through
	out.Reset()
	annexB := append(append(append([]byte{}, h264SPS...), h264PPS...), 0, 0, 0, 1, 0x65, 0x88, 0x84)
	if err := w.WriteFrame(annexB); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), annexB) {
		t.Fatalf("annex-b frame changed: %x", out.Bytes())
	}
}

func TestWriterAddsADTS(t *testing.T) {
	var out bytes.Buffer
	w, _ := NewWriter(&out, codec.CODECID_AUDIO_AAC, []byte{0x11, 0x90})
	w.WriteFrame([]byte{1, 2})
	w.WriteFrame(adtsFrame(3, 4))
	want := append(adtsFrame(1, 2), adtsFrame(3, 4)...)
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("got %x want %x", out.Bytes(), want)
	}
}

func TestNewReaderProbes(t *testing.T) {
	var stream []byte
	for i := 0; i < 3; i++ {
		stream = append(stream, adtsFrame(byte(i))...)
	}
	r, err := NewReader(bytes.NewReader(stream))
	if err != nil || r.Codec() != codec.CODECID_AUDIO_AAC {
		t.Fatalf("%v %v", r, err)
	}
	if n := len(readAll(t, r)); n != 3 {
		t.Fatalf("%d frames", n)
	}
	if _, err := NewReader(bytes.NewReader([]byte("plain text"))); err != ErrUnknownFormat {
		t.Fatalf("text: %v", err)
	}
}
