package mpeg2

import (
	"bytes"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

// tsSeedStream builds a small but valid transport stream that is used as a
// seed for the ts oriented fuzz targets.
func tsSeedStream() []byte {
	muxer := NewTSMuxer()
	var out bytes.Buffer
	muxer.OnPacket = func(pkg []byte) { out.Write(pkg) }
	vpid := muxer.AddStream(TS_STREAM_H264)
	apid := muxer.AddStream(TS_STREAM_AAC)
	frame := append(h264Frame(0x67, 0x42, 0x00, 0x1e), append(h264Frame(0x68, 0xce), h264Frame(0x65, bytes.Repeat([]byte{0xAA}, 400)...)...)...)
	_ = muxer.Write(vpid, frame, 0, 0)
	_ = muxer.Write(apid, bytes.Repeat([]byte{0x5A}, 64), 40, 40)
	_ = muxer.Write(vpid, h264Frame(0x41, bytes.Repeat([]byte{0xBB}, 300)...), 80, 80)
	return out.Bytes()
}

// psSeedStream builds a small but valid program stream.
func psSeedStream() []byte {
	muxer := NewPsMuxer()
	var out bytes.Buffer
	muxer.OnPacket = func(pkg []byte) { out.Write(pkg) }
	vsid := muxer.AddStream(PS_STREAM_H264)
	asid := muxer.AddStream(PS_STREAM_AAC)
	frame := append(h264Frame(0x67, 0x42, 0x00, 0x1e), append(h264Frame(0x68, 0xce), h264Frame(0x65, bytes.Repeat([]byte{0xAA}, 400)...)...)...)
	_ = muxer.Write(vsid, frame, 0, 0)
	_ = muxer.Write(asid, bytes.Repeat([]byte{0x5A}, 64), 40, 40)
	return out.Bytes()
}

// tsSeeds adds the seed corpus shared by the ts targets.
func addByteSeeds(f *testing.F, extra ...[]byte) {
	f.Add([]byte{})
	f.Add([]byte{0x47})
	f.Add([]byte{0x00, 0x00, 0x01, 0xBA})
	f.Add(bytes.Repeat([]byte{0xff}, 188))
	for _, s := range extra {
		f.Add(s)
	}
}

// chunk splits data into pieces whose sizes are taken from the data itself, so
// the fuzzer can steer where the chunk boundaries fall.
func chunk(data []byte) [][]byte {
	var out [][]byte
	i := 0
	for i < len(data) {
		n := int(data[i])%64 + 1
		if i+n > len(data) {
			n = len(data) - i
		}
		out = append(out, data[i:i+n])
		i += n
	}
	return out
}

func FuzzTSDemuxerInput(f *testing.F) {
	addByteSeeds(f, tsSeedStream())
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewTSDemuxer()
		frames := 0
		demuxer.OnFrame = func(cid TS_STREAM_TYPE, frame []byte, pts uint64, dts uint64) {
			frames++
			_ = len(frame)
		}
		demuxer.OnTSPacket = func(pkg *TSPacket) {
			if pkg == nil {
				t.Fatal("OnTSPacket called with a nil packet")
			}
		}
		_ = demuxer.Input(bytes.NewReader(data))
	})
}

// FuzzTSDemuxerInputNilCallbacks runs the demuxer with every callback left nil.
func FuzzTSDemuxerInputNilCallbacks(f *testing.F) {
	addByteSeeds(f, tsSeedStream())
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewTSDemuxer()
		_ = demuxer.Input(bytes.NewReader(data))
	})
}

func FuzzPSDemuxerInput(f *testing.F) {
	addByteSeeds(f, psSeedStream(), ps5, ps6, ps7)
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewPSDemuxer()
		demuxer.OnFrame = func(frame []byte, cid PS_STREAM_TYPE, pts uint64, dts uint64) {
			_ = len(frame)
		}
		demuxer.OnPacket = func(pkg Display, decodeResult error) {
			if pkg == nil {
				t.Fatal("OnPacket called with a nil packet")
			}
		}
		_ = demuxer.Input(data)
		demuxer.Flush()
	})
}

// FuzzPSDemuxerInputChunked feeds the same bytes in fuzzer chosen chunks, which
// exercises the demuxer's cache and its need-more handling.
func FuzzPSDemuxerInputChunked(f *testing.F) {
	addByteSeeds(f, psSeedStream(), ps5, ps6, ps7)
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := NewPSDemuxer()
		demuxer.OnFrame = func(frame []byte, cid PS_STREAM_TYPE, pts uint64, dts uint64) {}
		demuxer.OnPacket = func(pkg Display, decodeResult error) {}
		chunks := chunk(data)
		if len(chunks) > 4096 {
			t.Skip("too many chunks")
		}
		fed := 0
		for _, c := range chunks {
			fed += len(c)
			_ = demuxer.Input(c)
			// the demuxer may only buffer bytes it has actually been given;
			// a cache larger than that means it kept asking for more without
			// ever consuming anything.
			if len(demuxer.cache) > fed {
				t.Fatalf("cache holds %d bytes after %d bytes of input", len(demuxer.cache), fed)
			}
		}
		demuxer.Flush()
	})
}

func FuzzTSPacketDecodeHeader(f *testing.F) {
	addByteSeeds(f, tsSeedStream()[:188])
	f.Fuzz(func(t *testing.T, data []byte) {
		var pkg TSPacket
		_ = pkg.DecodeHeader(codec.NewBitStream(data))
	})
}

func FuzzAdaptationFieldDecode(f *testing.F) {
	addByteSeeds(f, []byte{0x07, 0x50, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		var field Adaptation_field
		_ = field.Decode(codec.NewBitStream(data))
	})
}

func FuzzPatDecode(f *testing.F) {
	addByteSeeds(f, []byte{0x00, 0xb0, 0x0d, 0x00, 0x01, 0xc1, 0x00, 0x00, 0x00, 0x01, 0xe1, 0x00, 0x00, 0x00, 0x00, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		pat := NewPat()
		_ = pat.Decode(codec.NewBitStream(data))
	})
}

func FuzzPmtDecode(f *testing.F) {
	addByteSeeds(f, []byte{0x02, 0xb0, 0x17, 0x00, 0x01, 0xc1, 0x00, 0x00, 0xe1, 0x00, 0xf0, 0x00, 0x1b, 0xe1, 0x00, 0xf0, 0x00, 0x0f, 0xe1, 0x01, 0xf0, 0x00, 0x00, 0x00, 0x00, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		pmt := NewPmt()
		_ = pmt.Decode(codec.NewBitStream(data))
	})
}

func FuzzReadSection(f *testing.F) {
	f.Add(0, []byte{0x00, 0xb0, 0x0d, 0x00, 0x01, 0xc1, 0x00, 0x00, 0x00, 0x01, 0xe1, 0x00, 0x00, 0x00, 0x00, 0x00})
	f.Add(2, []byte{0x02, 0xb0, 0x17, 0x00, 0x01, 0xc1, 0x00, 0x00, 0xe1, 0x00, 0xf0, 0x00, 0x1b, 0xe1, 0x00, 0xf0, 0x00, 0x00, 0x00, 0x00, 0x00})
	f.Add(1, []byte{})
	f.Add(0, bytes.Repeat([]byte{0xff}, 184))
	f.Fuzz(func(t *testing.T, tid int, data []byte) {
		_, _ = ReadSection(PAT_TID(tid), codec.NewBitStream(data))
	})
}

func FuzzPesPacketDecode(f *testing.F) {
	addByteSeeds(f, []byte{0x00, 0x00, 0x01, 0xe0, 0x00, 0x0f, 0x80, 0x80, 0x05, 0x21, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x65, 0xaa})
	f.Fuzz(func(t *testing.T, data []byte) {
		pkg := NewPesPacket()
		_ = pkg.Decode(codec.NewBitStream(data))
	})
}

func FuzzPesPacketDecodeMpeg1(f *testing.F) {
	addByteSeeds(f, []byte{0x00, 0x00, 0x01, 0xe0, 0x00, 0x0a, 0xff, 0xff, 0x21, 0x00, 0x01, 0x00, 0x01, 0xaa, 0xbb})
	f.Fuzz(func(t *testing.T, data []byte) {
		pkg := NewPesPacket()
		_ = pkg.DecodeMpeg1(codec.NewBitStream(data))
	})
}

func FuzzPSPackHeaderDecode(f *testing.F) {
	addByteSeeds(f, ps2, ps3, ps7)
	f.Fuzz(func(t *testing.T, data []byte) {
		var hdr PSPackHeader
		_ = hdr.Decode(codec.NewBitStream(data))
	})
}

func FuzzSystemHeaderDecode(f *testing.F) {
	addByteSeeds(f, []byte{0x00, 0x00, 0x01, 0xBB, 0x00, 0x09, 0x80, 0x66, 0x7a, 0x04, 0xe1, 0x7f, 0xe0, 0xe0, 0x80})
	f.Fuzz(func(t *testing.T, data []byte) {
		var sh System_header
		_ = sh.Decode(codec.NewBitStream(data))
	})
}

func FuzzProgramStreamMapDecode(f *testing.F) {
	addByteSeeds(f, ps6)
	f.Fuzz(func(t *testing.T, data []byte) {
		var psm Program_stream_map
		_ = psm.Decode(codec.NewBitStream(data))
	})
}

// FuzzTSMuxerWrite feeds arbitrary frame bytes and timestamps to the ts muxer.
// Every packet handed to OnPacket must be a well formed 188 byte packet, and a
// frame the muxer cannot encode must come back as an error.
func FuzzTSMuxerWrite(f *testing.F) {
	f.Add([]byte{}, uint64(0), uint64(0), uint8(0))
	f.Add(h264Frame(0x65, 0x11, 0x22), uint64(40), uint64(40), uint8(0))
	f.Add(bytes.Repeat([]byte{0xAA}, 500), uint64(1<<40), uint64(0), uint8(1))
	f.Add([]byte{0x00, 0x00, 0x01}, uint64(0), uint64(1<<40), uint8(2))
	f.Fuzz(func(t *testing.T, frame []byte, pts uint64, dts uint64, which uint8) {
		cids := []TS_STREAM_TYPE{TS_STREAM_H264, TS_STREAM_H265, TS_STREAM_AAC, TS_STREAM_AUDIO_MPEG1}
		cid := cids[int(which)%len(cids)]
		muxer := NewTSMuxer()
		muxer.OnPacket = func(pkg []byte) {
			if len(pkg) != TS_PAKCET_SIZE {
				t.Fatalf("ts muxer emitted a %d byte packet, want %d", len(pkg), TS_PAKCET_SIZE)
			}
			if pkg[0] != 0x47 {
				t.Fatalf("ts packet does not start with the sync byte: %#x", pkg[0])
			}
		}
		pid := muxer.AddStream(cid)
		if err := muxer.Write(pid+1000, frame, pts, dts); err == nil {
			t.Fatal("write to an unknown pid must fail")
		}
		_ = muxer.Write(pid, frame, pts, dts)
		_ = muxer.Write(pid, frame, pts+40, dts+40)
	})
}

// FuzzTSMuxerWriteNilCallback runs the muxer without an OnPacket callback.
func FuzzTSMuxerWriteNilCallback(f *testing.F) {
	f.Add([]byte{}, uint64(0), uint64(0))
	f.Add(h264Frame(0x65, 0x11, 0x22), uint64(40), uint64(40))
	f.Fuzz(func(t *testing.T, frame []byte, pts uint64, dts uint64) {
		muxer := NewTSMuxer()
		pid := muxer.AddStream(TS_STREAM_H264)
		_ = muxer.Write(pid, frame, pts, dts)
	})
}

// FuzzPSMuxerWrite feeds arbitrary frame bytes and timestamps to the ps muxer.
func FuzzPSMuxerWrite(f *testing.F) {
	f.Add([]byte{}, uint64(0), uint64(0), uint8(0))
	f.Add(h264Frame(0x65, 0x11, 0x22), uint64(40), uint64(40), uint8(0))
	f.Add(bytes.Repeat([]byte{0xAA}, 70000), uint64(1<<40), uint64(0), uint8(1))
	f.Add([]byte{0x00, 0x00, 0x01}, uint64(0), uint64(1<<40), uint8(2))
	f.Fuzz(func(t *testing.T, frame []byte, pts uint64, dts uint64, which uint8) {
		cids := []PS_STREAM_TYPE{PS_STREAM_H264, PS_STREAM_H265, PS_STREAM_AAC, PS_STREAM_G711A}
		cid := cids[int(which)%len(cids)]
		muxer := NewPsMuxer()
		muxer.OnPacket = func(pkg []byte) {
			if len(pkg) == 0 {
				t.Fatal("ps muxer emitted an empty pack")
			}
		}
		sid := muxer.AddStream(cid)
		if err := muxer.Write(sid+0x40, frame, pts, dts); err == nil {
			t.Fatal("write to an unknown stream id must fail")
		}
		_ = muxer.Write(sid, frame, pts, dts)
		_ = muxer.Write(sid, frame, pts+40, dts+40)
	})
}

// FuzzMuxDemuxRoundTripTS pushes what the ts muxer produced back through the
// demuxer, so the two halves are fuzzed against each other.
func FuzzMuxDemuxRoundTripTS(f *testing.F) {
	f.Add([]byte{}, uint8(0))
	f.Add(h264Frame(0x65, 0x11, 0x22), uint8(0))
	f.Add(bytes.Repeat([]byte{0xAA}, 400), uint8(2))
	f.Fuzz(func(t *testing.T, frame []byte, which uint8) {
		cids := []TS_STREAM_TYPE{TS_STREAM_H264, TS_STREAM_H265, TS_STREAM_AAC}
		cid := cids[int(which)%len(cids)]
		muxer := NewTSMuxer()
		var out bytes.Buffer
		muxer.OnPacket = func(pkg []byte) {
			if len(pkg) != TS_PAKCET_SIZE {
				t.Fatalf("ts muxer emitted a %d byte packet, want %d", len(pkg), TS_PAKCET_SIZE)
			}
			out.Write(pkg)
		}
		pid := muxer.AddStream(cid)
		if err := muxer.Write(pid, frame, 0, 0); err != nil {
			return
		}
		if err := muxer.Write(pid, frame, 40, 40); err != nil {
			return
		}
		demuxer := NewTSDemuxer()
		demuxer.OnFrame = func(cid TS_STREAM_TYPE, frame []byte, pts uint64, dts uint64) {}
		demuxer.OnTSPacket = func(pkg *TSPacket) {}
		_ = demuxer.Input(bytes.NewReader(out.Bytes()))
	})
}
