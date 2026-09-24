package mp4

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func opusTestHead(channels uint8, preSkip uint16, rate uint32) []byte {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1
	head[9] = channels
	binary.LittleEndian.PutUint16(head[10:], preSkip)
	binary.LittleEndian.PutUint32(head[12:], rate)
	return head
}

// 5.1 layout with mapping family 1: four streams, two of them coupled
func opusTestHead51() []byte {
	head := opusTestHead(6, 312, 48000)
	head[18] = 1
	return append(head, 4, 2, 0, 4, 1, 2, 3, 5)
}

func TestOpusDemuxRoundTrip(t *testing.T) {
	for _, head := range [][]byte{opusTestHead(2, 312, 48000), opusTestHead51()} {
		for _, flag := range []MP4_FLAG{0, MP4_FLAG_FRAGMENT} {
			testOpusDemuxRoundTrip(t, head, flag)
		}
	}
}

func testOpusDemuxRoundTrip(t *testing.T, head []byte, flag MP4_FLAG) {
	{
		ws := newMemWriteSeeker()
		muxer, err := CreateMp4Muxer(ws, WithMp4Flag(flag))
		if err != nil {
			t.Fatal(err)
		}
		tid, err := muxer.AddAudioTrack(MP4_CODEC_OPUS, WithExtraData(head),
			WithAudioChannelCount(head[9]), WithAudioSampleRate(48000))
		if err != nil {
			t.Fatal(err)
		}
		// TOC 0xfc: config 31 (CELT FB 20ms), stereo, one frame
		pkt := []byte{0xfc, 1, 2, 3, 4, 5}
		for i := 0; i < 10; i++ {
			if err := muxer.Write(tid, pkt, uint64(i*20), uint64(i*20)); err != nil {
				t.Fatal(err)
			}
		}
		if err := muxer.WriteTrailer(); err != nil {
			t.Fatal(err)
		}

		demuxer := CreateMp4Demuxer(bytes.NewReader(ws.buf))
		infos, err := demuxer.ReadHead()
		if err != nil {
			t.Fatalf("flag %d: ReadHead: %v", flag, err)
		}
		if len(infos) != 1 || infos[0].Cid != MP4_CODEC_OPUS {
			t.Fatalf("flag %d: tracks %+v", flag, infos)
		}
		if infos[0].ChannelCount != head[9] || infos[0].SampleRate != 48000 {
			t.Fatalf("flag %d: channels %d rate %d", flag, infos[0].ChannelCount, infos[0].SampleRate)
		}
		got, err := demuxer.GetExtraData(uint32(infos[0].TrackId))
		if err != nil {
			t.Fatalf("flag %d: GetExtraData: %v", flag, err)
		}
		if !bytes.Equal(got, head) {
			t.Fatalf("flag %d: OpusHead %x want %x", flag, got, head)
		}
		n := 0
		for {
			p, err := demuxer.ReadPacket()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("flag %d: ReadPacket: %v", flag, err)
			}
			if !bytes.Equal(p.Data, pkt) {
				t.Fatalf("flag %d: packet %x", flag, p.Data)
			}
			n++
		}
		if n != 10 {
			t.Fatalf("flag %d: got %d packets", flag, n)
		}
	}
}

// The sample entry is registered as Opus. Files written by ffmpeg use that
// spelling, and files written by older versions of this package the lower
// case one; both have to be read.
func TestOpusSampleEntryName(t *testing.T) {
	ws := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(ws)
	if err != nil {
		t.Fatal(err)
	}
	head := opusTestHead(2, 312, 48000)
	tid, _ := muxer.AddAudioTrack(MP4_CODEC_OPUS, WithExtraData(head))
	if err := muxer.Write(tid, []byte{0xfc, 1}, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(ws.buf, []byte("Opus")) {
		t.Fatal("muxer did not write an Opus sample entry")
	}
	for _, name := range []string{"Opus", "opus"} {
		file := bytes.Replace(ws.buf, []byte("Opus"), []byte(name), 1)
		infos, err := CreateMp4Demuxer(bytes.NewReader(file)).ReadHead()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(infos) != 1 || infos[0].Cid != MP4_CODEC_OPUS || infos[0].ChannelCount != 2 {
			t.Fatalf("%s: %+v", name, infos)
		}
	}
}
