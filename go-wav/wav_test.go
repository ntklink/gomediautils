package wav

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

type memWriteSeeker struct {
	buf []byte
	pos int
}

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	if need := m.pos + len(p); need > len(m.buf) {
		m.buf = append(m.buf, make([]byte, need-len(m.buf))...)
	}
	copy(m.buf[m.pos:], p)
	m.pos += len(p)
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		offset += int64(m.pos)
	case io.SeekEnd:
		offset += int64(len(m.buf))
	}
	if offset < 0 {
		return 0, errors.New("negative seek")
	}
	m.pos = int(offset)
	return offset, nil
}

func TestRoundTrip(t *testing.T) {
	alaw, _ := FormatOf(codec.CODECID_AUDIO_G711A, 8000, 1)
	pcm := Format{AudioFormat: FormatPCM, Channels: 2, SampleRate: 44100, BitsPerSample: 16}
	for name, tc := range map[string]struct {
		format   Format
		seekable bool
		samples  int
	}{
		"alaw file":        {alaw, true, 801}, // odd: the chunk gets a pad byte
		"alaw stream":      {alaw, false, 800},
		"pcm stereo file":  {pcm, true, 4410 * 4},
		"pcm stereo steam": {pcm, false, 4410 * 4},
	} {
		data := make([]byte, tc.samples)
		for i := range data {
			data[i] = byte(i * 7)
		}
		ws := &memWriteSeeker{}
		var out bytes.Buffer
		var w io.Writer = &out
		if tc.seekable {
			w = ws
		}
		wr, err := NewWriter(w, tc.format)
		if err != nil {
			t.Fatal(err)
		}
		wr.Write(data[:100])
		wr.Write(data[100:])
		if err := wr.Close(); err != nil {
			t.Fatal(err)
		}
		file := out.Bytes()
		if tc.seekable {
			file = ws.buf
			if got := binary.LittleEndian.Uint32(file[4:]); int(got) != len(file)-8 {
				t.Errorf("%s: RIFF size %d, file is %d", name, got, len(file))
			}
		}
		if len(file)%2 != 0 {
			t.Errorf("%s: file of odd length", name)
		}

		rd, err := NewReader(bytes.NewReader(file))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rd.Format() != tc.format {
			t.Errorf("%s: format %+v", name, rd.Format())
		}
		wantDur := uint64(tc.samples/tc.format.BlockAlign()) * 1000 / uint64(tc.format.SampleRate)
		if !tc.seekable {
			wantDur = 0
		}
		if rd.Duration() != wantDur {
			t.Errorf("%s: duration %d, want %d", name, rd.Duration(), wantDur)
		}
		got, err := io.ReadAll(rd)
		if err != nil {
			t.Fatal(err)
		}
		// a stream has no size, so its reader cannot tell the pad byte from
		// a sample; the sizes of a finished file can
		if !bytes.Equal(got, data) {
			t.Errorf("%s: %d samples back, want %d", name, len(got), len(data))
		}
	}
}

func TestReadFrame(t *testing.T) {
	ws := &memWriteSeeker{}
	f, _ := FormatOf(codec.CODECID_AUDIO_G711U, 8000, 1)
	wr, _ := NewWriter(ws, f)
	wr.Write(make([]byte, 400))
	wr.Close()
	rd, err := NewReader(bytes.NewReader(ws.buf))
	if err != nil {
		t.Fatal(err)
	}
	if rd.Codec() != codec.CODECID_AUDIO_G711U {
		t.Fatal(codec.CodecString(rd.Codec()))
	}
	var sizes []int
	var pts []uint64
	for {
		fr, err := rd.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(fr.Data))
		pts = append(pts, fr.Pts)
	}
	// 20 ms at 8 kHz is 160 samples
	if len(sizes) != 3 || sizes[2] != 80 || pts[2] != 40 {
		t.Fatalf("frames %v at %v", sizes, pts)
	}
}

// A file from another writer: WAVE_FORMAT_EXTENSIBLE, a LIST chunk in front
// of the data and a chunk after it.
func TestReadExtensibleWithExtraChunks(t *testing.T) {
	fmtChunk := make([]byte, 40)
	binary.LittleEndian.PutUint16(fmtChunk[0:], FormatExtensible)
	binary.LittleEndian.PutUint16(fmtChunk[2:], 2)
	binary.LittleEndian.PutUint32(fmtChunk[4:], 48000)
	binary.LittleEndian.PutUint32(fmtChunk[8:], 48000*4)
	binary.LittleEndian.PutUint16(fmtChunk[12:], 4)
	binary.LittleEndian.PutUint16(fmtChunk[14:], 16)
	binary.LittleEndian.PutUint16(fmtChunk[16:], 22)
	binary.LittleEndian.PutUint16(fmtChunk[18:], 16)
	binary.LittleEndian.PutUint32(fmtChunk[20:], 3)
	binary.LittleEndian.PutUint16(fmtChunk[24:], FormatPCM)
	copy(fmtChunk[26:], extensibleSubFormat)

	chunk := func(id string, payload []byte) []byte {
		out := append([]byte(id), binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))...)
		out = append(out, payload...)
		if len(payload)%2 == 1 {
			out = append(out, 0)
		}
		return out
	}
	body := []byte("WAVE")
	body = append(body, chunk("fmt ", fmtChunk)...)
	body = append(body, chunk("LIST", []byte("INFOISFT\x03\x00\x00\x00abc"))...)
	body = append(body, chunk("data", []byte{1, 2, 3, 4, 5, 6, 7, 8})...)
	body = append(body, chunk("id3 ", []byte("tag"))...)
	file := append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...)
	file = append(file, body...)

	rd, err := NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	want := Format{AudioFormat: FormatPCM, Channels: 2, SampleRate: 48000, BitsPerSample: 16}
	if rd.Format() != want {
		t.Fatalf("format %+v", rd.Format())
	}
	got, _ := io.ReadAll(rd)
	if !bytes.Equal(got, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("samples %x: the chunk after the data leaked in", got)
	}
}

func TestReaderRejects(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":       nil,
		"not riff":    []byte("RIFX\x00\x00\x00\x00WAVE"),
		"no data":     []byte("RIFF\x04\x00\x00\x00WAVE"),
		"data first":  []byte("RIFF\x10\x00\x00\x00WAVEdata\x00\x00\x00\x00"),
		"huge fmt":    []byte("RIFF\x10\x00\x00\x00WAVEfmt \xff\xff\xff\x00"),
		"unknown tag": append([]byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x55\x00\x01\x00\x40\x1f\x00\x00\x40\x1f\x00\x00\x01\x00\x08\x00"), "data\x00\x00\x00\x00"...),
	} {
		if _, err := NewReader(bytes.NewReader(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A fmt chunk that runs into the next chunk reads a sample rate of 1.6 GHz;
// taken at face value it made ReadFrame allocate gigabytes. Found by
// FuzzReader.
func TestRejectsImpossibleSampleRate(t *testing.T) {
	data := []byte("RIFF\x80\x01\x00\x00WAVEfmt \x12\x00\x00\x00\x06\x00\b\x00\x00\x00fact\x04\x00\x00\x00M\x01\x00\x00dataM\x01\x00\x00")
	data = append(data, make([]byte, 333)...)
	if _, err := NewReader(bytes.NewReader(data)); err == nil {
		t.Fatal("accepted a 1.6 GHz sample rate")
	}
}

// A data chunk of size 0 in a finished file is an empty file, even with a
// chunk after it; only a header that was never filled in means "read to the
// end".
func TestZeroSizeDataChunk(t *testing.T) {
	fmtChunk := "fmt \x10\x00\x00\x00\x07\x00\x01\x00\x40\x1f\x00\x00\x40\x1f\x00\x00\x01\x00\x08\x00"
	body := "WAVE" + fmtChunk + "data\x00\x00\x00\x00" + "LIST\x04\x00\x00\x00abcd"
	finished := "RIFF" + string(binary.LittleEndian.AppendUint32(nil, uint32(len(body)))) + body
	rd, err := NewReader(bytes.NewReader([]byte(finished)))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(rd); len(got) != 0 {
		t.Fatalf("%d bytes of the next chunk read as audio", len(got))
	}

	stream := "RIFF\x00\x00\x00\x00WAVE" + fmtChunk + "data\x00\x00\x00\x00" + "\x01\x02\x03"
	rd, err = NewReader(bytes.NewReader([]byte(stream)))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(rd); len(got) != 3 {
		t.Fatalf("stream with unfilled sizes: %d bytes, want 3", len(got))
	}
}
