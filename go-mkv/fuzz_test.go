package mkv

import (
	"bytes"
	"io"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

// seedFile muxes a short file with every codec the muxer writes, so the
// fuzzer starts from track entries and blocks of each kind.
func seedFile(seekable bool) []byte {
	var out bytes.Buffer
	ws := &memWriteSeeker{}
	var w io.Writer = &out
	if seekable {
		w = ws
	}
	m, _ := NewMuxer(w)
	v, _ := m.AddVideoTrack(codec.CODECID_VIDEO_H264)
	h, _ := m.AddVideoTrack(codec.CODECID_VIDEO_H265)
	a, _ := m.AddAudioTrack(codec.CODECID_AUDIO_AAC)
	o, _ := m.AddAudioTrack(codec.CODECID_AUDIO_OPUS)
	g, _ := m.AddAudioTrack(codec.CODECID_AUDIO_G711U)
	for i := 0; i < 4; i++ {
		ts := uint64(i * 40)
		m.Write(v, h264Frame(i == 0), ts, ts)
		m.Write(h, h265Frame(i == 0), ts, ts)
		m.Write(a, adtsFrame([]byte{byte(i), 1}), ts, ts)
		m.WritePadded(o, []byte{0xfc, byte(i)}, ts, ts, int64(i))
		m.Write(g, bytes.Repeat([]byte{byte(i)}, 160), ts, ts)
	}
	m.WriteTrailer()
	if seekable {
		return ws.buf
	}
	return out.Bytes()
}

func FuzzDemuxer(f *testing.F) {
	f.Add([]byte{})
	f.Add(master(idEBML, stringElement(idDocType, "matroska")))
	f.Add(seedFile(true))
	f.Add(seedFile(false))
	f.Fuzz(func(t *testing.T, data []byte) {
		d := NewDemuxer(bytes.NewReader(data))
		if _, err := d.ReadHead(); err != nil {
			return
		}
		d.Duration()
		for i := 0; i < 10000; i++ {
			p, err := d.ReadPacket()
			if err != nil {
				return
			}
			if p.Dts > p.Pts {
				t.Fatalf("dts %d after pts %d", p.Dts, p.Pts)
			}
		}
	})
}

func FuzzSplitLaces(f *testing.F) {
	f.Add([]byte{2, 4, 255, 45, 1, 2, 3}, byte(1))
	f.Add([]byte{2, 0x84, 0x60, 0x00, 1, 2, 3, 4, 5}, byte(3))
	f.Add([]byte{2, 1, 2, 3}, byte(2))
	f.Fuzz(func(t *testing.T, data []byte, lacing byte) {
		frames, err := splitLaces(data, lacing&3)
		if err != nil {
			return
		}
		n := 0
		for _, fr := range frames {
			n += len(fr)
		}
		if n > len(data) {
			t.Fatalf("%d bytes of frames out of %d", n, len(data))
		}
	})
}
