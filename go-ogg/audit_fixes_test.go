package ogg

import (
	"bytes"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

func TestDemuxerPacketLossContinuedSegments(t *testing.T) {
	opusHead := append([]byte("OpusHead"), 0x01, 0x02, 0x00, 0x00, 0x80, 0xBB, 0x00, 0x00, 0x00, 0x00, 0x00)
	opusTags := append([]byte("OpusTags"), 0, 0, 0, 0)

	// Skip seq=2 to simulate page loss; seq=3 starts with a continued packet
	// spanning [255, 10] followed by a fresh 20-byte packet.
	lostPart := make([]byte, 255+10)
	fresh := make([]byte, 20)
	fresh[0] = 0x08 // valid opus TOC
	for i := 1; i < len(fresh); i++ {
		fresh[i] = byte(i)
	}

	var stream []byte
	stream = append(stream, makeOggPage(0x02, 0, 1, 0, []byte{byte(len(opusHead))}, opusHead)...)
	stream = append(stream, makeOggPage(0x00, 0, 1, 1, []byte{byte(len(opusTags))}, opusTags)...)
	stream = append(stream, makeOggPage(0x01, 4800, 1, 3, []byte{255, 10, 20}, append(lostPart, fresh...))...)

	d := NewDemuxer()
	var got [][]byte
	d.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts, dts uint64, lost int) {
		got = append(got, append([]byte(nil), frame...))
	}
	if err := d.Input(stream); err != nil {
		t.Fatalf("Input: %v", err)
	}
	if len(got) != 1 || !bytes.Equal(got[0], fresh) {
		t.Fatalf("got %d frames (%x), want 1 fresh frame (%x)", len(got), got, fresh)
	}
}

func TestOpusDemuxerPageStartPTSAndNoGranule(t *testing.T) {
	opusHead := append([]byte("OpusHead"), 0x01, 0x01, 0x00, 0x00, 0x80, 0xBB, 0x00, 0x00, 0x00, 0x00, 0x00)
	opusTags := append([]byte("OpusTags"), 0, 0, 0, 0)
	// Config 1 (20ms = 960 samples), code 0 (1 frame) -> TOC = 1<<3 = 0x08
	pkt := []byte{0x08, 0x01, 0x02}

	var stream []byte
	stream = append(stream, makeOggPage(0x02, 0, 1, 0, []byte{byte(len(opusHead))}, opusHead)...)
	stream = append(stream, makeOggPage(0x00, 0, 1, 1, []byte{byte(len(opusTags))}, opusTags)...)
	// Page 2 starts at 1000ms (48000 samples); 2 packets of 960 -> granule = 48000 + 1920 = 49920
	stream = append(stream, makeOggPage(0x00, 49920, 1, 2, []byte{3, 3}, append(append([]byte{}, pkt...), pkt...))...)
	// Page 3 has granulePos == ^uint64(0) (no-granule sentinel)
	stream = append(stream, makeOggPage(0x00, ^uint64(0), 1, 3, []byte{3}, pkt)...)

	d := NewDemuxer()
	var ptsList []uint64
	d.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts, dts uint64, lost int) {
		ptsList = append(ptsList, pts)
	}
	if err := d.Input(stream); err != nil {
		t.Fatalf("Input: %v", err)
	}
	want := []uint64{1000, 1020, 1040}
	if len(ptsList) != len(want) {
		t.Fatalf("got pts=%v, want %v", ptsList, want)
	}
	for i := range want {
		if ptsList[i] != want[i] {
			t.Fatalf("pts[%d] = %d, want %d (all=%v)", i, ptsList[i], want[i], ptsList)
		}
	}
}

func TestMuxerFullSegmentTableNotContinued(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf)
	sid, err := m.AddOpusStream(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Write 256 tiny packets (each takes 1 segment, < pageTargetBytes & < pageTargetSamples)
	// Config 16 (CELT 2.5ms = 120 samples): 255 packets = 30600 samples < 48000, 255*2 = 510 bytes < 4096.
	// Packet 256 sees room == 0 and flushes the full page; the new page must NOT have flagContinued.
	pkt := []byte{16 << 3, 0x01}
	for i := 0; i < 256; i++ {
		if err := m.Write(sid, pkt, uint64(i)*3, uint64(i)*3); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := m.WriteTrailer(); err != nil {
		t.Fatal(err)
	}

	d := NewDemuxer()
	var continuedPages int
	d.OnPage = func(p *oggPage) {
		if p.isContinuePacket {
			continuedPages++
		}
	}
	var frames int
	d.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts, dts uint64, lost int) {
		frames++
	}
	if err := d.Input(buf.Bytes()); err != nil {
		t.Fatalf("Input: %v", err)
	}
	if continuedPages != 0 {
		t.Fatalf("got %d continued pages, want 0", continuedPages)
	}
	if frames != 256 {
		t.Fatalf("got %d frames, want 256", frames)
	}
}

func TestVP8GptoptsGuards(t *testing.T) {
	v := &vp8Demuxer{}
	if got := v.gptopts(0); got != 0 {
		t.Fatalf("gptopts(0) = %d, want 0", got)
	}
	if got := v.gptopts(^uint64(0)); got != 0 {
		t.Fatalf("gptopts(^uint64(0)) = %d, want 0", got)
	}
}

func TestMakeChecksumInitialCRC(t *testing.T) {
	a := []byte("OggS\x00\x02")
	b := []byte("hello world")
	combined := append(append([]byte(nil), a...), b...)
	want := makeChecksum(0, combined)
	got := makeChecksum(makeChecksum(0, a), b)
	if got != want {
		t.Fatalf("incremental checksum %#x != combined %#x", got, want)
	}
}
