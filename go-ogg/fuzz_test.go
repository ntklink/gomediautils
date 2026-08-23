package ogg

import (
	"bytes"
	"testing"

	"github.com/yapingcat/gomedia/go-codec"
)

// oggSeedStream builds a tiny but structurally valid ogg stream: an OpusHead
// page, an OpusTags page and two data pages, the second of which continues a
// packet started by the first.
func oggSeedStream() []byte {
	opusHead := make([]byte, 19)
	copy(opusHead, "OpusHead")
	opusHead[8] = 1     // version
	opusHead[9] = 2     // channels
	opusHead[10] = 0x38 // pre-skip
	opusHead[12] = 0x80 // sample rate 48000
	opusHead[13] = 0xBB
	opusHead[14] = 0x00
	opusHead[15] = 0x00

	tags := append([]byte("OpusTags"), bytes.Repeat([]byte{0x00}, 8)...)

	var out []byte
	out = append(out, makeOggPage(0x02, 0, 1, 0, []byte{byte(len(opusHead))}, opusHead)...)
	out = append(out, makeOggPage(0x00, 0, 1, 1, []byte{byte(len(tags))}, tags)...)
	// a packet split across two pages: 255 + 10 bytes
	first := bytes.Repeat([]byte{0x78}, 255)
	out = append(out, makeOggPage(0x00, 960, 1, 2, []byte{255}, first)...)
	rest := append(bytes.Repeat([]byte{0x78}, 10), 0x78, 0x00, 0x00)
	out = append(out, makeOggPage(0x01, 1920, 1, 3, []byte{10, 3}, rest)...)
	return out
}

func addOggSeeds(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("OggS"))
	f.Add(append([]byte("OggS"), bytes.Repeat([]byte{0x00}, 23)...))
	f.Add(makeOggPage(0x02, 0, 1, 0, []byte{0}, nil))
	f.Add(makeOggPage(0x01, 0, 1, 0, bytes.Repeat([]byte{255}, 255), bytes.Repeat([]byte{0x41}, 255*255)))
	f.Add(oggSeedStream())
}

func FuzzOggDemuxerInput(f *testing.F) {
	addOggSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewDemuxer()
		demuxer.OnPage = func(page *oggPage) {
			if page == nil {
				t.Fatal("OnPage called with a nil page")
			}
		}
		demuxer.OnPacket = func(streamId uint32, granule uint64, packet []byte, lost int) {
			_ = len(packet)
		}
		demuxer.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts uint64, dts uint64, lost int) {
			_ = len(frame)
		}
		_ = demuxer.Input(data)
		_ = demuxer.GetAudioParam()
		_ = demuxer.GetVideoParam()
	})
}

// FuzzOggDemuxerInputNilCallbacks exercises the demuxer with no callbacks set.
func FuzzOggDemuxerInputNilCallbacks(f *testing.F) {
	addOggSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewDemuxer()
		_ = demuxer.Input(data)
	})
}

// FuzzOggDemuxerInputChunked feeds the same bytes in chunks whose sizes are
// picked by the fuzzer, which exercises the page head cache and the payload
// continuation logic.
func FuzzOggDemuxerInputChunked(f *testing.F) {
	addOggSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewDemuxer()
		demuxer.OnPage = func(page *oggPage) {}
		demuxer.OnPacket = func(streamId uint32, granule uint64, packet []byte, lost int) {}
		demuxer.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts uint64, dts uint64, lost int) {}
		i := 0
		// bounded by len(data): every step consumes at least one byte
		for i < len(data) {
			n := int(data[i])%37 + 1
			if i+n > len(data) {
				n = len(data) - i
			}
			if err := demuxer.Input(data[i : i+n]); err != nil {
				return
			}
			i += n
			// a page header is at most 27 + 255 bytes, the cache can never
			// hold a complete one
			if len(demuxer.headCache) >= PAGE_HEAD_LENGTH {
				t.Fatalf("page head cache holds %d bytes", len(demuxer.headCache))
			}
		}
	})
}

func FuzzOggReadPage(f *testing.F) {
	addOggSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		page, err := readPage(data)
		if err != nil {
			return
		}
		if page == nil {
			t.Fatal("readPage returned no page and no error")
		}
		if int(page.payloadLen) > 255*255 {
			t.Fatalf("payload length %d out of range", page.payloadLen)
		}
	})
}
