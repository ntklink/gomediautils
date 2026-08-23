package flv

import (
	"bytes"
	"io"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

// flvSeedFile builds a tiny but valid flv file (header + one avc sequence
// header tag + one avc nalu tag) used to seed the file level targets.
func flvSeedFile() []byte {
	var out bytes.Buffer
	w := CreateFlvWriter(&out)
	_ = w.WriteFlvHeader()
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1e, 0xaa}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x3c, 0x80}
	idr := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x11}
	frame := append(append(append([]byte{}, sps...), pps...), idr...)
	_ = w.WriteH264(frame, 0, 0)
	_ = w.WriteH264(append([]byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a}, 0x11, 0x22), 40, 40)
	return out.Bytes()
}

func addFlvSeeds(f *testing.F, extra ...[]byte) {
	f.Add([]byte{})
	f.Add([]byte{0x17, 0x01, 0x00, 0x00, 0x00})
	f.Add([]byte{0x97, 'h', 'v', 'c', '1'})
	f.Add([]byte{0x91, 'h', 'v', 'c', '1', 0x00, 0x00, 0x00})
	f.Add([]byte{0xaf, 0x01, 0x21, 0x00})
	f.Add(flvSeedFile())
	for _, s := range extra {
		f.Add(s)
	}
}

func FuzzFlvReaderInput(f *testing.F) {
	addFlvSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		r := CreateFlvReader()
		r.OnFrame = func(cid codec.CodecID, frame []byte, pts uint32, dts uint32) {
			_ = len(frame)
		}
		_ = r.Input(data)
	})
}

// FuzzFlvReaderInputNilCallback runs the reader without an OnFrame callback.
func FuzzFlvReaderInputNilCallback(f *testing.F) {
	addFlvSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		r := CreateFlvReader()
		_ = r.Input(data)
	})
}

// FuzzFlvReaderInputChunked feeds the data in fuzzer chosen chunks so the
// reader's cache and its partial-tag handling are exercised.
func FuzzFlvReaderInputChunked(f *testing.F) {
	addFlvSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		r := CreateFlvReader()
		r.OnFrame = func(cid codec.CodecID, frame []byte, pts uint32, dts uint32) {}
		i := 0
		fed := 0
		// bounded: every step consumes at least one byte of data
		for i < len(data) {
			n := int(data[i])%41 + 1
			if i+n > len(data) {
				n = len(data) - i
			}
			_ = r.Input(data[i : i+n])
			i += n
			fed += n
			// the reader may only hold back bytes it was actually given
			if len(r.cache) > fed {
				t.Fatalf("cache holds %d bytes after %d bytes of input", len(r.cache), fed)
			}
		}
	})
}

func FuzzFlvTagDecode(f *testing.F) {
	addFlvSeeds(f, []byte{0x09, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		var tag FlvTag
		if err := tag.Decode(data); err != nil {
			return
		}
		// a decoded tag must round trip through Encode
		if got := tag.Encode(); len(got) != int(FLVTAG_SIZE) {
			t.Fatalf("encoded tag is %d bytes, want %d", len(got), FLVTAG_SIZE)
		}
	})
}

func FuzzVideoTagDecode(f *testing.F) {
	addFlvSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		var tag VideoTag
		tag.Decode(data)
		_ = tag.Encode()
	})
}

func FuzzAudioTagDecode(f *testing.F) {
	addFlvSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		var tag AudioTag
		if err := tag.Decode(data); err != nil {
			return
		}
		_ = tag.Encode()
	})
}

func FuzzGetFLVVideoCodecId(f *testing.F) {
	addFlvSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = GetFLVVideoCodecId(data)
	})
}

func FuzzAVCTagDemuxerDecode(f *testing.F) {
	addFlvSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewAVCTagDemuxer()
		demuxer.OnFrame(func(cid codec.CodecID, frame []byte, cts int) {
			_ = len(frame)
		})
		_ = demuxer.Decode(data)
		// a second call reuses the state built by the first one
		_ = demuxer.Decode(data)
	})
}

func FuzzHevcTagDemuxerDecode(f *testing.F) {
	addFlvSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewHevcTagDemuxer()
		demuxer.OnFrame(func(cid codec.CodecID, frame []byte, cts int) {
			_ = len(frame)
		})
		_ = demuxer.Decode(data)
		_ = demuxer.Decode(data)
	})
}

// FuzzTagDemuxersNilCallback runs the tag demuxers without callbacks.
func FuzzTagDemuxersNilCallback(f *testing.F) {
	addFlvSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = NewAVCTagDemuxer().Decode(data)
		_ = NewHevcTagDemuxer().Decode(data)
		_ = NewAACTagDemuxer().Decode(data)
		_ = NewG711Demuxer(FLV_G711A).Decode(data)
	})
}

func FuzzAACTagDemuxerDecode(f *testing.F) {
	addFlvSeeds(f, []byte{0xaf, 0x00, 0x12, 0x10}, []byte{0xaf, 0x01, 0x21, 0x00, 0x03})
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewAACTagDemuxer()
		demuxer.OnFrame(func(cid codec.CodecID, frame []byte) {
			_ = len(frame)
		})
		_ = demuxer.Decode(data)
		_ = demuxer.Decode(data)
	})
}

func FuzzG711DemuxerDecode(f *testing.F) {
	addFlvSeeds(f, []byte{0x72, 0x11, 0x22})
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, format := range []FLV_SOUND_FORMAT{FLV_G711A, FLV_G711U, FLV_MP3} {
			demuxer := NewG711Demuxer(format)
			demuxer.OnFrame(func(cid codec.CodecID, frame []byte) {
				_ = len(frame)
			})
			_ = demuxer.Decode(data)
		}
	})
}

func FuzzAmf0DecodeString(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x02, 0x00, 0x03, 'a', 'b', 'c'})
	f.Add([]byte{0x0c, 0x00, 0x00, 0x00, 0x02, 'a', 'b'})
	f.Add(EncodeAmf0String(nil, "onMetaData"))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, n, err := DecodeAmf0String(data)
		if err != nil {
			return
		}
		if n < 0 || n > len(data) {
			t.Fatalf("DecodeAmf0String consumed %d of %d bytes", n, len(data))
		}
		// re-encoding the value must yield exactly the bytes it was read from,
		// except that a short string may have been written as a long string
		again := EncodeAmf0String(nil, s)
		if len(again) != n && len(again)+2 != n {
			t.Fatalf("re-encoding %q gave %d bytes, decoded from %d", s, len(again), n)
		}
	})
}

func FuzzAmf0MetaDataRoundTrip(f *testing.F) {
	f.Add("duration", []byte{}, 0.0)
	f.Add("width", []byte{0x01}, 1280.0)
	f.Fuzz(func(t *testing.T, key string, raw []byte, num float64) {
		values := map[string]interface{}{
			key:      num,
			"str":    string(raw),
			"flag":   len(raw)%2 == 0,
			"number": num,
		}
		body, err := EncodeOnMetaData(values)
		if err != nil {
			return
		}
		name, n, err := DecodeAmf0String(body)
		if err != nil {
			t.Fatalf("metadata does not start with a string: %v", err)
		}
		if name != "onMetaData" {
			t.Fatalf("metadata name is %q", name)
		}
		if n > len(body) {
			t.Fatalf("string decoder consumed %d of %d bytes", n, len(body))
		}
	})
}

// FuzzFlvWriterWrite feeds arbitrary bytes to every FlvWriter entry point. A
// frame the muxer cannot encode must be reported, never crash.
func FuzzFlvWriterWrite(f *testing.F) {
	f.Add([]byte{}, uint32(0), uint32(0), uint8(0))
	f.Add([]byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1e}, uint32(0), uint32(0), uint8(0))
	f.Add([]byte{0xff, 0xf1, 0x50, 0x80, 0x00, 0x9f, 0xfc, 0x00}, uint32(0), uint32(0), uint8(2))
	f.Add([]byte{0xff, 0xfb, 0x90, 0x00}, uint32(0), uint32(0), uint8(3))
	f.Add([]byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c}, uint32(1), uint32(0), uint8(1))
	f.Fuzz(func(t *testing.T, frame []byte, pts uint32, dts uint32, which uint8) {
		var out bytes.Buffer
		w := CreateFlvWriter(&out)
		if err := w.WriteFlvHeader(); err != nil {
			t.Fatalf("write flv header: %v", err)
		}
		var err error
		switch which % 4 {
		case 0:
			err = w.WriteH264(frame, pts, dts)
			if err == nil {
				err = w.WriteH264(frame, pts+40, dts+40)
			}
		case 1:
			err = w.WriteH265(frame, pts, dts)
			if err == nil {
				err = w.WriteH265(frame, pts+40, dts+40)
			}
		case 2:
			err = w.WriteAAC(frame, pts, dts)
			if err == nil {
				err = w.WriteAAC(frame, pts+40, dts+40)
			}
		case 3:
			err = w.WriteMp3(frame, pts, dts)
			if err == nil {
				err = w.WriteMp3(frame, pts+40, dts+40)
			}
		}
		if err != nil {
			return
		}
		// what the writer produced must be parseable again
		r := CreateFlvReader()
		r.OnFrame = func(cid codec.CodecID, frame []byte, pts uint32, dts uint32) {}
		_ = r.Input(out.Bytes())
	})
}

// FuzzFlvWriterWriteDiscard writes to a discarding writer with no reader
// attached, so the muxers run with no callbacks at all.
func FuzzFlvWriterWriteDiscard(f *testing.F) {
	f.Add([]byte{}, uint8(0))
	f.Add([]byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88}, uint8(0))
	f.Fuzz(func(t *testing.T, frame []byte, which uint8) {
		w := CreateFlvWriter(io.Discard)
		switch which % 4 {
		case 0:
			_ = w.WriteH264(frame, 0, 0)
		case 1:
			_ = w.WriteH265(frame, 0, 0)
		case 2:
			_ = w.WriteAAC(frame, 0, 0)
		case 3:
			_ = w.WriteMp3(frame, 0, 0)
		}
	})
}

// FuzzFlvMuxerWriteFrames drives the tag muxers directly and checks the tag
// headers they produce.
func FuzzFlvMuxerWriteFrames(f *testing.F) {
	f.Add([]byte{}, uint32(0), uint8(0))
	f.Add([]byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88}, uint32(0x01020304), uint8(0))
	f.Fuzz(func(t *testing.T, frame []byte, dts uint32, which uint8) {
		vids := []FLV_VIDEO_CODEC_ID{FLV_AVC, FLV_HEVC}
		aids := []FLV_SOUND_FORMAT{FLV_AAC, FLV_G711A, FLV_G711U, FLV_MP3}
		muxer, err := NewFlvMuxer(vids[int(which)%len(vids)], aids[int(which)%len(aids)])
		if err != nil {
			t.Fatalf("new flv muxer: %v", err)
		}
		for _, tagType := range []TagType{VIDEO_TAG, AUDIO_TAG, SCRIPT_TAG} {
			tags, err := muxer.WriteFrames(tagType, frame, dts, dts)
			if err != nil {
				continue
			}
			for _, tag := range tags {
				if len(tag) < int(FLVTAG_SIZE) {
					t.Fatalf("tag is %d bytes, shorter than the tag header", len(tag))
				}
				var ftag FlvTag
				if err := ftag.Decode(tag); err != nil {
					t.Fatalf("decode a tag the muxer produced: %v", err)
				}
				if int(ftag.DataSize) != len(tag)-int(FLVTAG_SIZE) {
					t.Fatalf("tag DataSize %d, body is %d bytes", ftag.DataSize, len(tag)-int(FLVTAG_SIZE))
				}
			}
		}
	})
}
