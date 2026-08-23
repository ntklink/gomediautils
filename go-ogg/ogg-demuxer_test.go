package ogg

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/yapingcat/gomedia/go-codec"
)

func TestDemuxer_Input(t *testing.T) {

	t.Run("ogg demux", func(t *testing.T) {
		demuxer := NewDemuxer()
		demuxer.OnPacket = func(streamId uint32, granule uint64, packet []byte, lost int) {
			//fmt.Printf("onpacket sid:%d granule:%d package len:%d lost:%d\n", streamId, granule, len(packet), lost)
		}
		getAudioParam := false
		getVideoParam := false
		demuxer.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts, dts uint64, lost int) {
			if cid == codec.CODECID_AUDIO_OPUS {
				param := demuxer.GetAudioParam()
				if param != nil && !getAudioParam {
					fmt.Println(param)
					getAudioParam = true
				}
				fmt.Printf("opus frame:sid[%d] frame len:[%d] pts:[%d] dts:[%d] lost:%d\n", streamId, len(frame), pts, dts, lost)
			} else if cid == codec.CODECID_VIDEO_VP8 {
				param := demuxer.GetVideoParam()
				if param != nil && !getVideoParam {
					fmt.Println(param)
					getVideoParam = true
				}
				fmt.Printf("vp8 frame:sid[%d] frame len:[%d] pts:[%d] dts:[%d] lost:%d\n", streamId, len(frame), pts, dts, lost)
			}
		}

		demuxer.OnPage = func(page *oggPage) {
			//	PrintPage(page)
		}
		oggfile, err := os.Open("test.ogg")
		if err != nil {
			if os.IsNotExist(err) {
				t.Skip("test.ogg not present")
			}
			t.Fatal(err)
		}
		defer oggfile.Close()
		buf := make([]byte, 4096)
		for {
			n, err := oggfile.Read(buf)
			if err != nil {
				fmt.Println(err)
				break
			}
			//fmt.Printf("read buf %d\n", n)
			err = demuxer.Input(buf[0:n])
			if err != nil {
				fmt.Println(err)
			}
		}
	})
}

func makeOggPage(flags byte, granule uint64, sid uint32, seq uint32, segments []byte, payload []byte) []byte {
	hdr := make([]byte, 27, 27+len(segments)+len(payload))
	copy(hdr, "OggS")
	hdr[4] = 0
	hdr[5] = flags
	binary.LittleEndian.PutUint64(hdr[6:], granule)
	binary.LittleEndian.PutUint32(hdr[14:], sid)
	binary.LittleEndian.PutUint32(hdr[18:], seq)
	hdr[26] = byte(len(segments))
	hdr = append(hdr, segments...)
	return append(hdr, payload...)
}

func TestDemuxer_ContinuedPacket(t *testing.T) {
	opusHead := []byte("OpusHead")
	opusHead = append(opusHead, 0x01, 0x02, 0x00, 0x00, 0x80, 0xBB, 0x00, 0x00, 0x00, 0x00, 0x00)
	opusTags := append([]byte("OpusTags"), 0, 0, 0, 0)
	first := make([]byte, 255) // TOC byte 0x00 (code 0, config 0)
	second := make([]byte, 50)
	third := make([]byte, 30)
	for i := range second {
		second[i] = 0x02
	}
	for i := range third {
		third[i] = 0x03
	}
	second[0], third[0] = 0x00, 0x00

	stream := append([]byte{}, makeOggPage(0x02, 0, 1, 0, []byte{byte(len(opusHead))}, opusHead)...)
	stream = append(stream, makeOggPage(0x00, 0, 1, 1, []byte{byte(len(opusTags))}, opusTags)...)
	stream = append(stream, makeOggPage(0x00, 5000, 1, 2, []byte{255}, first)...)
	cont := append(append(append([]byte{}, make([]byte, 10)...), second...), third...)
	stream = append(stream, makeOggPage(0x01, 6000, 1, 3, []byte{10, 50, 30}, cont)...)

	run := func(chunk int) {
		demuxer := NewDemuxer()
		var packets []int
		demuxer.OnPacket = func(streamId uint32, granule uint64, packet []byte, lost int) {
			packets = append(packets, len(packet))
		}
		var frames [][]byte
		demuxer.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts, dts uint64, lost int) {
			frames = append(frames, append([]byte{}, frame...))
		}
		for i := 0; i < len(stream); i += chunk {
			end := i + chunk
			if end > len(stream) {
				end = len(stream)
			}
			if err := demuxer.Input(stream[i:end]); err != nil {
				t.Fatalf("chunk %d: unexpected error %v", chunk, err)
			}
		}
		want := []int{19, 12, 265, 50, 30}
		if len(packets) != len(want) {
			t.Fatalf("chunk %d: packets %v, want %v", chunk, packets, want)
		}
		for i := range want {
			if packets[i] != want[i] {
				t.Fatalf("chunk %d: packets %v, want %v", chunk, packets, want)
			}
		}
		if len(frames) != 3 || len(frames[0]) != 265 || len(frames[1]) != 50 || len(frames[2]) != 30 {
			t.Fatalf("chunk %d: got %d frames", chunk, len(frames))
		}
		if frames[1][1] != 0x02 || frames[2][1] != 0x03 {
			t.Fatalf("chunk %d: packet content mis-sliced", chunk)
		}
	}
	run(len(stream))
	run(7)
}

func TestReadPageRejectsMalformedHeader(t *testing.T) {
	if _, err := readPage([]byte("OggS")); err == nil {
		t.Fatalf("short page header must be rejected")
	}
	page := makeOggPage(0x02, 0, 1, 0, []byte{4}, []byte{1, 2, 3, 4})
	if _, err := readPage(page); err != nil {
		t.Fatalf("valid page rejected: %v", err)
	}
	bad := append([]byte{}, page...)
	bad[4] = 1 // version
	if _, err := readPage(bad); err == nil {
		t.Fatalf("unsupported page version must be rejected")
	}
	bad = append([]byte{}, page...)
	bad[0] = 'X'
	if _, err := readPage(bad); err == nil {
		t.Fatalf("missing capture pattern must be rejected")
	}
	// segment count announces more entries than the header holds
	truncated := append([]byte{}, page[:27]...)
	truncated[26] = 4
	if _, err := readPage(truncated); err == nil {
		t.Fatalf("truncated segment table must be rejected")
	}
}

func TestDemuxerInputErrors(t *testing.T) {
	demuxer := NewDemuxer()
	if err := demuxer.Input(nil); err != nil {
		t.Fatalf("empty input: %v", err)
	}
	// 27 bytes that are not a page
	if err := demuxer.Input(make([]byte, 64)); err == nil {
		t.Fatalf("garbage input must be reported")
	}

	// a logical stream whose first packet matches no codec magic
	demuxer2 := NewDemuxer()
	unknown := makeOggPage(0x02, 0, 7, 0, []byte{4}, []byte{'X', 'X', 'X', 'X'})
	if err := demuxer2.Input(unknown); err == nil {
		t.Fatalf("unknown codec must be reported")
	}
}

// A media packet on a page with granule 0 used to abort the demuxer with
// "unsupported opus header"; it must be decoded as a frame instead.
func TestDemuxerDataPacketOnGranuleZeroPage(t *testing.T) {
	opusHead := []byte("OpusHead")
	opusHead = append(opusHead, 0x01, 0x02, 0x00, 0x00, 0x80, 0xBB, 0x00, 0x00, 0x00, 0x00, 0x00)
	opusTags := append([]byte("OpusTags"), 0, 0, 0, 0)
	data := make([]byte, 20) // TOC byte 0x00

	stream := makeOggPage(0x02, 0, 1, 0, []byte{byte(len(opusHead))}, opusHead)
	stream = append(stream, makeOggPage(0x00, 0, 1, 1, []byte{byte(len(opusTags))}, opusTags)...)
	stream = append(stream, makeOggPage(0x00, 0, 1, 2, []byte{byte(len(data))}, data)...)

	demuxer := NewDemuxer()
	frames := 0
	demuxer.OnFrame = func(streamId uint32, cid codec.CodecID, frame []byte, pts, dts uint64, lost int) {
		frames++
	}
	if err := demuxer.Input(stream); err != nil {
		t.Fatalf("input: %v", err)
	}
	if frames != 1 {
		t.Fatalf("got %d frames, want 1", frames)
	}
	param := demuxer.GetAudioParam()
	if param == nil || param.ChannelCount != 2 || param.SampleRate != 48000 {
		t.Fatalf("audio param %+v", param)
	}
}

func TestDemuxerEmptyOpusPacket(t *testing.T) {
	opusHead := []byte("OpusHead")
	opusHead = append(opusHead, 0x01, 0x02, 0x00, 0x00, 0x80, 0xBB, 0x00, 0x00, 0x00, 0x00, 0x00)
	stream := makeOggPage(0x02, 0, 1, 0, []byte{byte(len(opusHead))}, opusHead)
	stream = append(stream, makeOggPage(0x00, 960, 1, 1, []byte{0}, nil)...)

	demuxer := NewDemuxer()
	if err := demuxer.Input(stream); err == nil {
		t.Fatalf("empty opus packet must be reported")
	}
}
