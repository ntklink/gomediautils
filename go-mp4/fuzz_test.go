package mp4

import (
	"bytes"
	"io"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// maxFuzzPackets bounds the demuxing loops so that a crafted file that keeps
// producing samples can never turn a fuzz target into a hang.
const maxFuzzPackets = 4096

// buildSeedMp4 muxes a tiny file so the corpus starts from something the
// demuxer accepts. Any error simply means "no seed", a fuzz seed is not worth
// failing the build over.
func buildSeedMp4(flags ...MP4_FLAG) []byte {
	ws := newMemWriteSeeker()
	opts := make([]MuxerOption, 0, len(flags))
	for _, f := range flags {
		opts = append(opts, WithMp4Flag(f))
	}
	muxer, err := CreateMp4Muxer(ws, opts...)
	if err != nil {
		return nil
	}
	tid, err := muxer.AddAudioTrack(MP4_CODEC_G711A, WithAudioChannelCount(1), WithAudioSampleRate(8000), WithAudioSampleBits(16))
	if err != nil {
		return nil
	}
	frame := make([]byte, 160)
	for i := 0; i < 8; i++ {
		if err := muxer.Write(tid, frame, uint64(i*20), uint64(i*20)); err != nil {
			return nil
		}
	}
	if err := muxer.WriteTrailer(); err != nil {
		return nil
	}
	return ws.buf
}

// addMp4Seeds feeds a handful of well formed and deliberately broken files.
func addMp4Seeds(f *testing.F) {
	if seed := buildSeedMp4(); len(seed) > 0 {
		f.Add(seed)
	}
	if seed := buildSeedMp4(MP4_FLAG_FRAGMENT); len(seed) > 0 {
		f.Add(seed)
	}
	f.Add([]byte{})
	f.Add([]byte("\x00\x00\x00\x08ftyp"))
	f.Add([]byte("\x00\x00\x00\x10ftypisom\x00\x00\x02\x00"))
	// a box that claims to be bigger than the file
	f.Add([]byte("\xff\xff\xff\xffmoov"))
	// largesize form
	f.Add([]byte("\x00\x00\x00\x01mdat\x00\x00\x00\x00\x00\x00\x00\x00"))
	// a uuid box
	f.Add([]byte("\x00\x00\x00\x18uuid0123456789abcdef"))
	// moov > trak > mdia > minf > stbl > stsz with a huge sample count
	f.Add([]byte("\x00\x00\x00\x40moov\x00\x00\x00\x38trak\x00\x00\x00\x30mdia" +
		"\x00\x00\x00\x28minf\x00\x00\x00\x20stbl" +
		"\x00\x00\x00\x14stsz\x00\x00\x00\x00\x00\x00\x00\x01\xff\xff\xff\xff"))
}

// exerciseDemuxer drives a demuxer over a fuzzed file and asserts the cheap
// invariants of the public API.
func exerciseDemuxer(t *testing.T, demuxer *MovDemuxer) {
	infos, err := demuxer.ReadHead()
	if err != nil {
		if infos != nil {
			t.Fatalf("ReadHead returned %d track infos together with error %v", len(infos), err)
		}
		return
	}
	_ = demuxer.GetMp4Info()
	for _, info := range infos {
		if info.Timescale == 0 {
			t.Fatalf("track %d was accepted with a zero timescale", info.TrackId)
		}
		// GetSyncTable must never index outside the sample list
		syncs, err := demuxer.GetSyncTable(uint32(info.TrackId))
		if err == nil {
			for _, s := range syncs {
				if uint64(s.Size) > uint64(maxBoxPayloadSize) {
					t.Fatalf("sync sample of %d bytes", s.Size)
				}
			}
		}
	}
	if err := demuxer.SeekTime(0); err != nil {
		t.Fatalf("SeekTime(0): %v", err)
	}
	for i := 0; i < maxFuzzPackets; i++ {
		pkt, err := demuxer.ReadPacket()
		if err != nil {
			if pkt != nil {
				t.Fatalf("ReadPacket returned a packet together with error %v", err)
			}
			break
		}
		if pkt == nil {
			t.Fatal("ReadPacket returned a nil packet and a nil error")
		}
	}
	if err := demuxer.SeekTime(1 << 40); err != nil {
		t.Fatalf("SeekTime(large): %v", err)
	}
	for i := 0; i < 16; i++ {
		if _, err := demuxer.ReadPacket(); err != nil {
			break
		}
	}
}

// ---------------------------------------------------------------------------
// demuxer
// ---------------------------------------------------------------------------

// FuzzMovDemuxer drives ReadHead/ReadPacket/SeekTime/GetSyncTable with every
// callback installed.
func FuzzMovDemuxer(f *testing.F) {
	addMp4Seeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		demuxer := CreateMp4Demuxer(bytes.NewReader(data))
		demuxer.OnRawSample = func(cid MP4_CODEC_TYPE, sample []byte, subSample *SubSample) error {
			if subSample != nil {
				_ = subSample.Patterns
				_ = subSample.PsshBoxes
			}
			_ = len(sample)
			return nil
		}
		exerciseDemuxer(t, demuxer)
	})
}

// FuzzMovDemuxerNoCallback runs the same paths with no callback installed.
func FuzzMovDemuxerNoCallback(f *testing.F) {
	addMp4Seeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		exerciseDemuxer(t, CreateMp4Demuxer(bytes.NewReader(data)))
	})
}

// ---------------------------------------------------------------------------
// individual boxes
// ---------------------------------------------------------------------------

// FuzzMovBoxDecode feeds a raw buffer, and a size field chosen by the fuzzer,
// to every box decoder that parses a buffer of its own.
func FuzzMovBoxDecode(f *testing.F) {
	f.Add(uint8(0), uint32(12), []byte{})
	f.Add(uint8(1), uint32(0xFFFFFFFF), []byte{0, 0, 0, 0})
	f.Add(uint8(2), uint32(16), []byte{0, 0, 0, 1, 0, 0, 0, 0})
	f.Add(uint8(3), uint32(20), []byte{1, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add(uint8(7), uint32(0x7FFFFFFF), []byte{0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF})
	for sel := uint8(0); sel < 24; sel++ {
		f.Add(sel, uint32(12), []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	}

	f.Fuzz(func(t *testing.T, sel uint8, size uint32, data []byte) {
		r := bytes.NewReader(data)
		// A decoder may only ever report an error; a panic fails the target.
		switch sel % 22 {
		case 0:
			trun := TrackRunBox{Box: new(FullBox)}
			if _, err := trun.Decode(r, size, uint32(len(data))); err == nil && trun.EntryList != nil {
				if len(trun.EntryList.entrys) != int(trun.SampleCount) {
					t.Fatalf("trun: %d entries for sample count %d", len(trun.EntryList.entrys), trun.SampleCount)
				}
			}
		case 1:
			tfhd := TrackFragmentHeaderBox{Box: new(FullBox)}
			_, _ = tfhd.Decode(r, size, uint64(len(data)))
		case 2:
			senc := SencBox{Box: new(FullBox)}
			ivSize := uint8(0)
			if len(data) > 0 {
				ivSize = data[0]
			}
			if _, err := senc.Decode(r, size, ivSize); err == nil && senc.EntryList != nil {
				if len(senc.EntryList.entrys) != int(senc.SampleCount) {
					t.Fatalf("senc: %d entries for sample count %d", len(senc.EntryList.entrys), senc.SampleCount)
				}
			}
		case 3:
			saio := SaioBox{Box: new(FullBox)}
			_ = saio.Decode(r, size)
		case 4:
			saiz := SaizBox{Box: new(FullBox)}
			if err := saiz.Decode(r, size); err == nil {
				if saiz.DefaultSampleInfoSize == 0 && len(saiz.SampleInfo) != int(saiz.SampleCount) {
					t.Fatalf("saiz: %d sample infos for sample count %d", len(saiz.SampleInfo), saiz.SampleCount)
				}
			}
		case 5:
			pssh := PsshBox{Box: new(FullBox)}
			_, _ = pssh.Decode(r, size)
		case 6:
			demuxer := &MovDemuxer{reader: r, tracks: []*mp4track{{}}}
			demuxer.currentTrack = demuxer.tracks[0]
			_ = decodeSgpdBox(demuxer, size)
		case 7:
			sidx := SegmentIndexBox{Box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			if _, err := sidx.Decode(r); err == nil && len(sidx.Entrys) != int(sidx.ReferenceCount) {
				t.Fatalf("sidx: %d entries for reference count %d", len(sidx.Entrys), sidx.ReferenceCount)
			}
		case 8:
			tfra := TrackFragmentRandomAccessBox{Box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			if _, err := tfra.Decode(r); err == nil && tfra.FragEntrys != nil {
				if len(tfra.FragEntrys.frags) != int(tfra.NumberOfEntry) {
					t.Fatalf("tfra: %d entries for number of entry %d", len(tfra.FragEntrys.frags), tfra.NumberOfEntry)
				}
			}
		case 9:
			stts := TimeToSampleBox{box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			if _, err := stts.Decode(r); err == nil && stts.entryList != nil {
				if len(stts.entryList.entrys) != int(stts.entryList.entryCount) {
					t.Fatalf("stts: %d entries for entry count %d", len(stts.entryList.entrys), stts.entryList.entryCount)
				}
			}
		case 10:
			stsc := SampleToChunkBox{box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			if _, err := stsc.Decode(r); err == nil && stsc.stscentrys != nil {
				if len(stsc.stscentrys.entrys) != int(stsc.stscentrys.entryCount) {
					t.Fatalf("stsc: %d entries for entry count %d", len(stsc.stscentrys.entrys), stsc.stscentrys.entryCount)
				}
			}
		case 11:
			stco := ChunkOffsetBox{box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			_, _ = stco.Decode(r)
		case 12:
			co64 := ChunkLargeOffsetBox{box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			_, _ = co64.Decode(r)
		case 13:
			stsz := SampleSizeBox{box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			if _, err := stsz.Decode(r); err == nil && stsz.stsz != nil && stsz.stsz.sampleSize == 0 {
				if len(stsz.stsz.entrySizelist) != int(stsz.stsz.sampleCount) {
					t.Fatalf("stsz: %d entries for sample count %d", len(stsz.stsz.entrySizelist), stsz.stsz.sampleCount)
				}
			}
		case 14:
			ctts := CompositionOffsetBox{box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			if _, err := ctts.Decode(r); err == nil && ctts.ctts != nil {
				for i := range ctts.ctts.entrys {
					_ = ctts.ctts.sampleOffset(i)
				}
			}
		case 15:
			stss := SyncSampleBox{box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			_, _ = stss.Decode(r)
		case 16:
			base := BaseDescriptor{}
			_, _ = base.Decode(data)
			_, _ = decodeESDescriptor(data, &mp4track{})
		case 17:
			demuxer := &MovDemuxer{reader: r, tracks: []*mp4track{{stbltable: new(movstbl)}}}
			_ = decodeStsdBox(demuxer)
		case 18:
			hdlr := HandlerBox{Box: new(FullBox)}
			_, _ = hdlr.Decode(r, uint64(size))
		case 19:
			ftyp := FileTypeBox{}
			_, _ = ftyp.decode(r, size)
		case 20:
			mdhd := MediaHeaderBox{Box: new(FullBox)}
			_, _ = mdhd.Decode(r)
			tkhd := TrackHeaderBox{Box: new(FullBox)}
			_, _ = tkhd.Decode(bytes.NewReader(data))
			mvhd := MovieHeaderBox{Box: new(FullBox)}
			_, _ = mvhd.Decode(bytes.NewReader(data))
		case 21:
			elst := EditListBox{box: &FullBox{Box: &BasicBox{Size: uint64(size)}}}
			if _, err := elst.Decode(r); err == nil && elst.entrys != nil {
				if len(elst.entrys.entrys) != int(elst.entrys.entryCount) {
					t.Fatalf("elst: %d entries for entry count %d", len(elst.entrys.entrys), elst.entrys.entryCount)
				}
			}
		}
	})
}

// ---------------------------------------------------------------------------
// muxer
// ---------------------------------------------------------------------------

var fuzzMuxCodecs = []MP4_CODEC_TYPE{
	MP4_CODEC_H264, MP4_CODEC_H265, MP4_CODEC_AAC,
	MP4_CODEC_G711A, MP4_CODEC_G711U, MP4_CODEC_MP3, MP4_CODEC_OPUS,
}

// FuzzMovMuxer writes arbitrary frame bytes to a track of every codec and then
// closes the file. Every step must report an error rather than crash.
func FuzzMovMuxer(f *testing.F) {
	f.Add(uint8(0), uint8(0), []byte{})
	f.Add(uint8(0), uint8(0), []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1e})
	f.Add(uint8(1), uint8(0), []byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c, 0x01})
	f.Add(uint8(2), uint8(0), []byte{0xff, 0xf1, 0x50, 0x80, 0x00, 0x1f, 0xfc})
	f.Add(uint8(3), uint8(2), make([]byte, 160))
	f.Add(uint8(6), uint8(0), []byte{0x4f, 0x70, 0x75, 0x73})
	f.Fuzz(func(t *testing.T, codecSel uint8, flagSel uint8, frame []byte) {
		cid := fuzzMuxCodecs[int(codecSel)%len(fuzzMuxCodecs)]
		var opts []MuxerOption
		switch flagSel % 3 {
		case 1:
			opts = append(opts, WithMp4Flag(MP4_FLAG_FRAGMENT))
		case 2:
			opts = append(opts, WithMp4Flag(MP4_FLAG_DASH))
		}
		muxer, err := CreateMp4Muxer(newMemWriteSeeker(), opts...)
		if err != nil {
			return
		}
		muxer.OnNewFragment(func(duration uint32, firstPts, firstDts uint64) {})
		var tid uint32
		if isVideo(cid) {
			tid, err = muxer.AddVideoTrack(cid, WithVideoWidth(320), WithVideoHeight(240))
		} else {
			tid, err = muxer.AddAudioTrack(cid, WithAudioChannelCount(1), WithAudioSampleRate(8000), WithAudioSampleBits(16))
		}
		if err != nil {
			return
		}
		// splitting the fuzz input into a few frames exercises the sample
		// aggregation paths as well
		chunk := len(frame)/3 + 1
		for i, pts := 0, uint64(0); i < len(frame); i, pts = i+chunk, pts+40 {
			end := i + chunk
			if end > len(frame) {
				end = len(frame)
			}
			if err := muxer.Write(tid, frame[i:end], pts, pts); err != nil {
				return
			}
		}
		if err := muxer.Write(tid, frame, 0, 0); err != nil {
			return
		}
		if err := muxer.WriteTrailer(); err != nil {
			return
		}
		_ = muxer.WriteInitSegment(io.Discard)
	})
}
