package mkv

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

// parameter sets of a 64x48 clip from x264 and x265
var (
	h264SPS = []byte{0, 0, 0, 1, 0x67, 0x64, 0x00, 0x0a, 0xac, 0xd9, 0x44, 0x7b, 0x01, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x03, 0x20, 0xf1, 0x22, 0x59, 0x60}
	h264PPS = []byte{0, 0, 0, 1, 0x68, 0xeb, 0xe3, 0xcb, 0x22, 0xc0}
	h265VPS = []byte{0, 0, 0, 1, 0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x1e, 0x95, 0x98, 0x09}
	h265SPS = []byte{0, 0, 0, 1, 0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x1e, 0xa0, 0x20, 0x83, 0x16, 0x59, 0x59, 0xae, 0x4c, 0xaf, 0x01, 0x68, 0x08, 0x00, 0x00, 0x03, 0x00, 0x08, 0x00, 0x00, 0x03, 0x00, 0xc8, 0x40}
	h265PPS = []byte{0, 0, 0, 1, 0x44, 0x01, 0xc1, 0x73, 0xd0, 0x89}
)

// memWriteSeeker is a seekable in memory file.
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

func TestVintRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 126, 127, 128, 16382, 16383, 1 << 20, 1<<56 - 2} {
		n := sizeLen(v)
		buf := appendSize(nil, v, n)
		got, gn, unknown, err := readVint(buf)
		if err != nil || got != v || gn != n || unknown {
			t.Fatalf("%d: got %d len %d unknown %v err %v", v, got, gn, unknown, err)
		}
	}
	// 127 in one byte would be the reserved all ones value
	if sizeLen(127) != 2 {
		t.Fatal("127 must not be written in one byte")
	}
	if _, _, unknown, _ := readVint(appendSize(nil, unknownSize, 8)); !unknown {
		t.Fatal("8 byte all ones size must read as unknown")
	}
	for _, total := range []int{2, 50, 128, 129, 130, 1000} {
		if got := len(voidElement(total)); got != total {
			t.Fatalf("void of %d bytes is %d long", total, got)
		}
	}
}

func h264Frame(key bool) []byte {
	if key {
		au := append(append([]byte{}, h264SPS...), h264PPS...)
		return append(au, 0, 0, 0, 1, 0x65, 0x88, 0x84, 0x00, 0x33)
	}
	return []byte{0, 0, 0, 1, 0x41, 0x9a, 0x21, 0x6c}
}

func h265Frame(key bool) []byte {
	if key {
		au := append(append(append([]byte{}, h265VPS...), h265SPS...), h265PPS...)
		// IDR_W_RADL
		return append(au, 0, 0, 0, 1, 0x26, 0x01, 0xaf, 0x06, 0xb8)
	}
	// TRAIL_R
	return []byte{0, 0, 0, 1, 0x02, 0x01, 0xd0, 0x1d, 0x70}
}

func adtsFrame(payload []byte) []byte {
	hdr, err := codec.ConvertASCToADTS([]byte{0x12, 0x10}, len(payload)+7) // AAC LC 44.1 kHz stereo
	if err != nil {
		panic(err)
	}
	return append(hdr.Encode(), payload...)
}

type written struct {
	data     []byte
	pts, dts uint64
	key      bool
}

func demuxAll(t *testing.T, data []byte) ([]TrackInfo, map[uint64][]*Packet, *Demuxer) {
	t.Helper()
	d := NewDemuxer(bytes.NewReader(data))
	infos, err := d.ReadHead()
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	got := make(map[uint64][]*Packet)
	for {
		p, err := d.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadPacket: %v", err)
		}
		got[p.TrackNumber] = append(got[p.TrackNumber], p)
	}
	return infos, got, d
}

func TestRoundTripVideoAndAudio(t *testing.T) {
	type trackCase struct {
		cid    codec.CodecID
		opts   []TrackOption
		frames []written
		// what the demuxer hands back for each written frame
		want func(w written) []byte
	}
	same := func(w written) []byte { return w.data }
	// B frames: decode order I0 P3 B1 B2, 40 ms apart
	video := func(frame func(bool) []byte) []written {
		pts := []uint64{0, 120, 40, 80, 240, 160, 200, 280}
		out := make([]written, len(pts))
		for i, p := range pts {
			out[i] = written{data: frame(i == 0), pts: p, key: i == 0}
		}
		return out
	}
	audio := func(n int, dur uint64, frame func(i int) []byte) []written {
		out := make([]written, n)
		for i := range out {
			out[i] = written{data: frame(i), pts: uint64(i) * dur, key: true}
		}
		return out
	}
	cases := map[string]trackCase{
		"h264": {cid: codec.CODECID_VIDEO_H264, frames: video(h264Frame), want: same},
		"h265": {cid: codec.CODECID_VIDEO_H265, frames: video(h265Frame), want: same},
		"vp8": {cid: codec.CODECID_VIDEO_VP8, want: same, frames: []written{
			{data: []byte{0x50, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0x40, 0x00, 0x30, 0x00, 1, 2}, key: true},
			{data: []byte{0x31, 0x02, 0x00, 7, 8}, pts: 40},
		}},
		"aac": {cid: codec.CODECID_AUDIO_AAC, want: same, frames: audio(5, 23, func(i int) []byte {
			return adtsFrame([]byte{byte(i), 0x21, 0x10})
		})},
		"opus": {cid: codec.CODECID_AUDIO_OPUS, want: same, frames: audio(5, 20, func(i int) []byte {
			return []byte{0xfc, byte(i), 0xaa}
		})},
		"g711a": {cid: codec.CODECID_AUDIO_G711A, want: same, frames: audio(4, 20, func(i int) []byte {
			return bytes.Repeat([]byte{byte(i + 1)}, 160)
		})},
		"g711u": {cid: codec.CODECID_AUDIO_G711U, want: same, frames: audio(4, 20, func(i int) []byte {
			return bytes.Repeat([]byte{byte(i + 9)}, 160)
		})},
	}
	for name, c := range cases {
		for _, seekable := range []bool{true, false} {
			var out bytes.Buffer
			ws := &memWriteSeeker{}
			var w io.Writer = &out
			if seekable {
				w = ws
			}
			m, err := NewMuxer(w)
			if err != nil {
				t.Fatal(err)
			}
			var tn uint64
			if isVideo(c.cid) {
				tn, err = m.AddVideoTrack(c.cid, c.opts...)
			} else {
				tn, err = m.AddAudioTrack(c.cid, c.opts...)
			}
			if err != nil {
				t.Fatalf("%s: add track: %v", name, err)
			}
			for _, f := range c.frames {
				if err := m.Write(tn, f.data, f.pts, f.pts); err != nil {
					t.Fatalf("%s: write: %v", name, err)
				}
			}
			if err := m.WriteTrailer(); err != nil {
				t.Fatalf("%s: trailer: %v", name, err)
			}
			data := out.Bytes()
			if seekable {
				data = ws.buf
			}

			infos, got, d := demuxAll(t, data)
			if len(infos) != 1 || infos[0].Cid != c.cid {
				t.Fatalf("%s: tracks %+v", name, infos)
			}
			if isVideo(c.cid) && (infos[0].Width != 64 || infos[0].Height != 48) {
				t.Errorf("%s: size %dx%d, want 64x48", name, infos[0].Width, infos[0].Height)
			}
			if seekable && d.Duration() == 0 {
				t.Errorf("%s: seekable file without a duration", name)
			}
			if !seekable && d.Duration() != 0 {
				t.Errorf("%s: live stream declares a duration", name)
			}
			pkts := got[tn]
			if len(pkts) != len(c.frames) {
				t.Fatalf("%s seekable=%v: %d packets, want %d", name, seekable, len(pkts), len(c.frames))
			}
			// the b frame streams start at zero, so the first decode time
			// needs the timeline moved forward by one frame
			var offset uint64
			if c.cid == codec.CODECID_VIDEO_H264 || c.cid == codec.CODECID_VIDEO_H265 {
				offset = 40
			}
			var lastDts uint64
			for i, p := range pkts {
				f := c.frames[i]
				if !bytes.Equal(p.Data, c.want(f)) {
					t.Errorf("%s: packet %d\n got %x\nwant %x", name, i, p.Data, c.want(f))
				}
				if p.Pts != f.pts+offset || p.KeyFrame != f.key {
					t.Errorf("%s: packet %d pts %d key %v, want %d %v", name, i, p.Pts, p.KeyFrame, f.pts+offset, f.key)
				}
				if p.Dts > p.Pts || (i > 0 && p.Dts <= lastDts) {
					t.Errorf("%s: packet %d dts %d after %d, pts %d", name, i, p.Dts, lastDts, p.Pts)
				}
				lastDts = p.Dts
			}
		}
	}
}

// Frames written before every track knows its configuration are held back,
// and the header only lists tracks that produced something.
func TestHeaderWaitsForEveryTrack(t *testing.T) {
	ws := &memWriteSeeker{}
	m, _ := NewMuxer(ws)
	v, _ := m.AddVideoTrack(codec.CODECID_VIDEO_H264)
	a, _ := m.AddAudioTrack(codec.CODECID_AUDIO_AAC)
	unused, _ := m.AddAudioTrack(codec.CODECID_AUDIO_OPUS)
	if err := m.Write(a, adtsFrame([]byte{1, 2, 3}), 0, 0); err != nil {
		t.Fatal(err)
	}
	if len(ws.buf) != 0 {
		t.Fatal("header written before the video configuration was known")
	}
	if err := m.Write(v, h264Frame(true), 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
	if err := m.Write(unused, []byte{0xfc}, 50, 50); err == nil {
		t.Fatal("write to a finished muxer succeeded")
	}
	infos, got, _ := demuxAll(t, ws.buf)
	if len(infos) != 2 {
		t.Fatalf("%d tracks, want the two that had frames", len(infos))
	}
	if len(got[v]) != 1 || len(got[a]) != 1 {
		t.Fatalf("packets %v", got)
	}
}

func TestWebMRejectsOtherCodecs(t *testing.T) {
	m, _ := NewMuxer(io.Discard, WithDocType(DocTypeWebM))
	if _, err := m.AddVideoTrack(codec.CODECID_VIDEO_H264); !errors.Is(err, ErrUnsupportedCodec) {
		t.Fatalf("h264 in webm: %v", err)
	}
	if _, err := m.AddAudioTrack(codec.CODECID_AUDIO_OPUS); err != nil {
		t.Fatal(err)
	}
}

// ADTS input splits into one block per frame, each timed by its 1024
// samples.
func TestAACSplitsADTS(t *testing.T) {
	ws := &memWriteSeeker{}
	m, _ := NewMuxer(ws)
	a, _ := m.AddAudioTrack(codec.CODECID_AUDIO_AAC)
	buf := append(adtsFrame([]byte{1}), adtsFrame([]byte{2, 2})...)
	buf = append(buf, adtsFrame([]byte{3, 3, 3})...)
	if err := m.Write(a, buf, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	m.WriteTrailer()
	infos, got, _ := demuxAll(t, ws.buf)
	if infos[0].SampleRate != 44100 || infos[0].ChannelCount != 2 {
		t.Fatalf("rate %d channels %d", infos[0].SampleRate, infos[0].ChannelCount)
	}
	want := []uint64{1000, 1023, 1046}
	for i, p := range got[a] {
		if p.Pts != want[i] {
			t.Errorf("frame %d pts %d want %d", i, p.Pts, want[i])
		}
	}
}

func TestSplitLaces(t *testing.T) {
	a, b, c := []byte("aaaa"), bytes.Repeat([]byte("b"), 300), []byte("cc")
	all := append(append(append([]byte{}, a...), b...), c...)

	xiph := []byte{2, 4, 255, 45}
	xiph = append(xiph, all...)

	// EBML lacing: first size 4 as a vint, then +296 as a signed two byte
	// vint (bias 8191), the last size is implied
	diff := uint64(296 + 8191)
	ebml := []byte{2, 0x84}
	ebml = append(ebml, byte(0x40|diff>>8), byte(diff))
	ebml = append(ebml, all...)

	for name, tc := range map[string]struct {
		buf    []byte
		lacing byte
		want   [][]byte
	}{
		"xiph":  {xiph, 1, [][]byte{a, b, c}},
		"ebml":  {ebml, 3, [][]byte{a, b, c}},
		"fixed": {append([]byte{2}, "xxyyzz"...), 2, [][]byte{[]byte("xx"), []byte("yy"), []byte("zz")}},
	} {
		got, err := splitLaces(tc.buf, tc.lacing)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %d frames", name, len(got))
		}
		for i := range got {
			if !bytes.Equal(got[i], tc.want[i]) {
				t.Errorf("%s: frame %d = %q", name, i, got[i])
			}
		}
	}
	if _, err := splitLaces([]byte{2, 255, 255}, 1); err == nil {
		t.Error("truncated xiph lace accepted")
	}
	if _, err := splitLaces([]byte{1, 0x90, 1, 2}, 3); err == nil {
		t.Error("ebml lace larger than the block accepted")
	}
}

// A hand built file with the features the muxer never writes: an unknown
// size cluster ended by the next cluster, a BlockGroup, Xiph lacing and
// header stripping.
func TestDemuxBlockGroupsLacingAndHeaderStripping(t *testing.T) {
	head := master(idEBML, stringElement(idDocType, "matroska"))
	opusHead := codec.WriteDefaultOpusExtraData()[:19]
	opusHead[9] = 2
	tracks := master(idTracks,
		master(idTrackEntry,
			uintElement(idTrackNumber, 1),
			uintElement(idTrackType, trackTypeAudio),
			stringElement(idCodecID, CodecIDOpus),
			element(idCodecPrivate, opusHead),
			master(idContentEncodings,
				master(idContentEncoding,
					master(idContentCompression,
						uintElement(idContentCompAlgo, 3),
						element(idContentCompSetting, []byte{0xfc}))))))

	block := func(rel int16, flags byte, payload ...byte) []byte {
		b := []byte{0x81, 0, 0, flags}
		binary.BigEndian.PutUint16(b[1:], uint16(rel))
		return append(b, payload...)
	}
	// three 20 ms frames in one Xiph laced block: sizes 1, 2 and the rest
	laced := block(0, 0x80|0x02, 2, 1, 2, 0xa1, 0xb1, 0xb2, 0xc1)
	cluster1 := appendSize(appendID(nil, idCluster), unknownSize, 8)
	cluster1 = append(cluster1, uintElement(idClusterTime, 100)...)
	cluster1 = append(cluster1, element(idSimpleBlock, laced)...)
	cluster2 := master(idCluster,
		uintElement(idClusterTime, 200),
		master(idBlockGroup, element(idBlock, block(-10, 0, 0xd1))))

	file := append(head, appendSize(appendID(nil, idSegment), unknownSize, 8)...)
	file = append(file, tracks...)
	file = append(file, cluster1...)
	file = append(file, cluster2...)

	infos, got, _ := demuxAll(t, file)
	if infos[0].SampleRate != 48000 || infos[0].ChannelCount != 0 && infos[0].ChannelCount != 2 {
		t.Fatalf("track %+v", infos[0])
	}
	want := []struct {
		data []byte
		pts  uint64
	}{
		{[]byte{0xfc, 0xa1}, 100},
		{[]byte{0xfc, 0xb1, 0xb2}, 120},
		{[]byte{0xfc, 0xc1}, 140},
		{[]byte{0xfc, 0xd1}, 190},
	}
	if len(got[1]) != len(want) {
		t.Fatalf("%d packets, want %d", len(got[1]), len(want))
	}
	for i, p := range got[1] {
		if !bytes.Equal(p.Data, want[i].data) || p.Pts != want[i].pts {
			t.Errorf("packet %d: %x at %d, want %x at %d", i, p.Data, p.Pts, want[i].data, want[i].pts)
		}
	}
}

func TestDtsReorder(t *testing.T) {
	for name, pts := range map[string][]uint64{
		"no b frames":      {0, 40, 80, 120, 160},
		"one b frame":      {0, 80, 40, 160, 120, 240, 200},
		"two b frames":     {0, 120, 40, 80, 240, 160, 200, 360, 280, 320},
		"pyramid, offset":  {80, 240, 160, 120, 200, 400, 320, 280, 360},
		"one frame":        {500},
		"longer than 16 f": {0, 120, 40, 80, 240, 160, 200, 360, 280, 320, 480, 400, 440, 600, 520, 560, 720, 640, 680},
	} {
		r := newDtsReorder()
		var qs []*queued
		for _, p := range pts {
			q := &queued{pkt: &Packet{Pts: p}, dts: int64(p)}
			qs = append(qs, q)
			r.push(q)
		}
		r.flush()
		for i, q := range qs {
			if !q.resolved {
				t.Fatalf("%s: frame %d unresolved", name, i)
			}
			if q.dts > int64(q.pkt.Pts) {
				t.Errorf("%s: frame %d dts %d > pts %d", name, i, q.dts, q.pkt.Pts)
			}
			if i > 0 && q.dts <= qs[i-1].dts {
				t.Errorf("%s: frame %d dts %d not after %d", name, i, q.dts, qs[i-1].dts)
			}
			if q.dts+int64(r.shift()) < 0 {
				t.Errorf("%s: frame %d dts %d below zero after a shift of %d", name, i, q.dts, r.shift())
			}
		}
	}
}

func TestDemuxRejectsGarbage(t *testing.T) {
	for _, data := range [][]byte{
		nil,
		[]byte("not a matroska file at all"),
		master(idEBML, stringElement(idDocType, "other")),
		master(idEBML, stringElement(idDocType, "webm")),
	} {
		if _, err := NewDemuxer(bytes.NewReader(data)).ReadHead(); err == nil {
			t.Errorf("%q accepted", data)
		}
	}
}

func TestSignedIntRoundTrip(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 127, 128, -128, -129, 13500000, -13500000, 1<<62 + 5, -1 << 63} {
		e := intElement(idDiscardPadding, v)
		got, err := readInt(e[3:])
		if err != nil || got != v {
			t.Fatalf("%d: read back %d (%v) from %x", v, got, err, e)
		}
	}
}

// The padding of the last opus frame survives a round trip, so a player
// trims the same samples the encoder padded.
func TestDiscardPaddingRoundTrip(t *testing.T) {
	ws := &memWriteSeeker{}
	m, _ := NewMuxer(ws, WithDocType(DocTypeWebM))
	a, _ := m.AddAudioTrack(codec.CODECID_AUDIO_OPUS)
	for i := 0; i < 3; i++ {
		var padding int64
		if i == 2 {
			padding = 13500000
		}
		if err := m.WritePadded(a, []byte{0xfc, byte(i)}, uint64(i*20), uint64(i*20), padding); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
	_, got, _ := demuxAll(t, ws.buf)
	if len(got[a]) != 3 {
		t.Fatalf("%d packets", len(got[a]))
	}
	for i, p := range got[a] {
		want := int64(0)
		if i == 2 {
			want = 13500000
		}
		if p.DiscardPadding != want || !p.KeyFrame || p.Pts != uint64(i*20) || p.Data[1] != byte(i) {
			t.Errorf("packet %d: %+v", i, p)
		}
	}
}

// G.711 written a few samples at a time comes out in 20 ms blocks; a gap in
// the timestamps starts a new run and the tail is flushed at the end.
func TestG711RegroupedInto20msBlocks(t *testing.T) {
	ws := &memWriteSeeker{}
	m, _ := NewMuxer(ws)
	a, _ := m.AddAudioTrack(codec.CODECID_AUDIO_G711A)
	var sent []byte
	write := func(from, bytes int, startMs uint64) {
		for i := from; i < from+bytes; i += 5 {
			chunk := make([]byte, 5)
			for j := range chunk {
				chunk[j] = byte(i + j)
			}
			sent = append(sent, chunk...)
			// 5 samples at 8 kHz is 0.625 ms: the ms timestamps repeat
			if err := m.Write(a, chunk, startMs+uint64(i-from)/8, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(0, 400, 0)     // 50 ms: two 20 ms blocks, 10 ms carried over
	write(400, 200, 500) // a jump to 500 ms flushes the 10 ms tail first
	if err := m.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
	_, got, _ := demuxAll(t, ws.buf)
	wantPts := []uint64{0, 20, 40, 500, 520}
	wantLen := []int{160, 160, 80, 160, 40}
	if len(got[a]) != len(wantPts) {
		t.Fatalf("%d blocks, want %d", len(got[a]), len(wantPts))
	}
	var back []byte
	for i, p := range got[a] {
		if p.Pts != wantPts[i] || len(p.Data) != wantLen[i] {
			t.Errorf("block %d: %d bytes at %d ms, want %d at %d", i, len(p.Data), p.Pts, wantLen[i], wantPts[i])
		}
		back = append(back, p.Data...)
	}
	if !bytes.Equal(back, sent) {
		t.Error("samples changed on the way through")
	}
}
