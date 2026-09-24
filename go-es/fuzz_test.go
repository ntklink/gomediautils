package es

import (
	"bytes"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

func FuzzReaders(f *testing.F) {
	f.Add(append(append(append([]byte{}, h264SPS...), h264PPS...), 0, 0, 1, 0x65, 0x88, 0x84, 0, 0, 1, 0x41, 0x9a))
	f.Add(append(append([]byte{}, h265VPS...), 0, 0, 1, 0x26, 0x01, 0xaf, 0, 0, 1, 0x02, 0x01, 0xd0))
	f.Add(adtsFrame(1, 2, 3))
	f.Add([]byte("ID3\x04\x00\x00\x00\x00\x00\x00\xff\xfb\x90\x64"))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, r := range []Reader{
			NewH264Reader(bytes.NewReader(data)),
			NewH265Reader(bytes.NewReader(data)),
			NewAACReader(bytes.NewReader(data)),
			NewMP3Reader(bytes.NewReader(data)),
			NewG711Reader(bytes.NewReader(data), codec.CODECID_AUDIO_G711A),
		} {
			for i := 0; i < 10000; i++ {
				fr, err := r.ReadFrame()
				if err != nil {
					break
				}
				if fr.Pts < fr.Dts {
					t.Fatalf("%s: pts %d before dts %d", codec.CodecString(r.Codec()), fr.Pts, fr.Dts)
				}
			}
		}
		Probe(data)
	})
}
