package ogg

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

type rawPage struct {
	flags    byte
	granule  uint64
	serial   uint32
	seq      uint32
	segments []byte
	body     []byte
}

// splitPages walks a file page by page, checking every checksum.
func splitPages(t *testing.T, data []byte) []rawPage {
	t.Helper()
	var pages []rawPage
	for len(data) > 0 {
		if len(data) < 27 || string(data[:4]) != "OggS" {
			t.Fatalf("no page header at %d bytes from the end", len(data))
		}
		n := int(data[26])
		segs := data[27 : 27+n]
		size := 27 + n
		for _, s := range segs {
			size += int(s)
		}
		page := append([]byte(nil), data[:size]...)
		crc := binary.LittleEndian.Uint32(page[22:])
		binary.LittleEndian.PutUint32(page[22:], 0)
		if makeChecksum(0, page) != crc {
			t.Fatalf("page %d has a bad checksum", len(pages))
		}
		pages = append(pages, rawPage{
			flags:    data[5],
			granule:  binary.LittleEndian.Uint64(data[6:]),
			serial:   binary.LittleEndian.Uint32(data[14:]),
			seq:      binary.LittleEndian.Uint32(data[18:]),
			segments: segs,
			body:     data[27+n : size],
		})
		data = data[size:]
	}
	return pages
}

func opusHead(channels byte, preSkip uint16) []byte {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1
	head[9] = channels
	binary.LittleEndian.PutUint16(head[10:], preSkip)
	binary.LittleEndian.PutUint32(head[12:], 48000)
	return head
}

func TestMuxerRoundTrip(t *testing.T) {
	var out bytes.Buffer
	m := NewMuxer(&out)
	serial, err := m.AddOpusStream(opusHead(2, 312))
	if err != nil {
		t.Fatal(err)
	}
	// 3 s of 20 ms packets (TOC 0xfc: 20 ms, stereo), the last padded by
	// 13.5 ms
	var sent [][]byte
	for i := 0; i < 150; i++ {
		pkt := append([]byte{0xfc}, bytes.Repeat([]byte{byte(i)}, 60+i%7)...)
		sent = append(sent, pkt)
		var padding int64
		if i == 149 {
			padding = 13500000
		}
		if err := m.WritePadded(serial, pkt, uint64(i*20), uint64(i*20), padding); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.WriteTrailer(); err != nil {
		t.Fatal(err)
	}

	pages := splitPages(t, out.Bytes())
	if pages[0].flags != flagBOS || !bytes.Equal(pages[0].body, opusHead(2, 312)) || len(pages[0].segments) != 1 {
		t.Fatalf("first page %+v must hold the OpusHead alone", pages[0])
	}
	if !bytes.HasPrefix(pages[1].body, []byte("OpusTags")) || pages[1].granule != 0 {
		t.Fatal("second page must hold the OpusTags")
	}
	last := pages[len(pages)-1]
	if last.flags&flagEOS == 0 {
		t.Fatal("last page without the end of stream flag")
	}
	// 150 * 960 samples, less 648 samples of padding
	if want := uint64(150*960 - 648); last.granule != want {
		t.Errorf("final granule %d, want %d", last.granule, want)
	}
	for i, p := range pages {
		if p.serial != serial || p.seq != uint32(i) {
			t.Fatalf("page %d: serial %d seq %d", i, p.serial, p.seq)
		}
		if i > 0 && i < len(pages)-1 && p.granule < pages[i-1].granule {
			t.Fatalf("page %d granule goes backwards", i)
		}
	}
	if len(pages) < 5 {
		t.Errorf("%d pages: 3 s of audio should be spread over one second pages", len(pages))
	}

	var got [][]byte
	d := NewDemuxer()
	d.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts, dts uint64, lost int) {
		got = append(got, append([]byte(nil), frame...))
	}
	if err := d.Input(out.Bytes()); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(sent) {
		t.Fatalf("%d packets back, want %d", len(got), len(sent))
	}
	for i := range got {
		if !bytes.Equal(got[i], sent[i]) {
			t.Fatalf("packet %d changed", i)
		}
	}
	if p := d.GetAudioParam(); p == nil || p.ChannelCount != 2 {
		t.Fatalf("audio param %+v", p)
	}
}

// A packet longer than a page (255 segments of 255 bytes) continues on the
// next page, which is flagged as a continuation and carries no granule of
// its own until the packet ends.
func TestMuxerPacketSpanningPages(t *testing.T) {
	var out bytes.Buffer
	m := NewMuxer(&out)
	serial, _ := m.AddOpusStream(opusHead(1, 0))
	big := append([]byte{0x78}, bytes.Repeat([]byte{7}, 70000)...)
	if err := m.Write(serial, []byte{0x78, 1}, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Write(serial, big, 20, 20); err != nil {
		t.Fatal(err)
	}
	m.WriteTrailer()
	pages := splitPages(t, out.Bytes())[2:]
	if len(pages) != 2 {
		t.Fatalf("%d data pages, want 2", len(pages))
	}
	if pages[0].granule != 960 || pages[1].flags != flagContinued|flagEOS || pages[1].granule != 1920 {
		t.Fatalf("pages %d/%x %d/%x", pages[0].granule, pages[0].flags, pages[1].granule, pages[1].flags)
	}

	var got [][]byte
	d := NewDemuxer()
	d.OnFrame = func(_ uint32, _ codec.CodecID, frame []byte, _, _ uint64, _ int) {
		got = append(got, append([]byte(nil), frame...))
	}
	d.Input(out.Bytes())
	if len(got) != 2 || !bytes.Equal(got[1], big) {
		t.Fatalf("%d packets back", len(got))
	}
}

// Without an OpusHead the channel count comes from the first packet.
func TestMuxerBuildsOpusHead(t *testing.T) {
	var out bytes.Buffer
	m := NewMuxer(&out)
	serial, _ := m.AddOpusStream(nil)
	m.Write(serial, []byte{0xfc, 1}, 0, 0) // stereo bit set
	m.WriteTrailer()
	pages := splitPages(t, out.Bytes())
	ctx := codec.OpusContext{}
	if err := ctx.ParseExtranData(pages[0].body); err != nil || ctx.ChannelCount != 2 {
		t.Fatalf("head %x: %v", pages[0].body, err)
	}
}
