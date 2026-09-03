package mp4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type boxRef struct {
	typ  string
	off  int
	size int
	data []byte
}

// topLevelBoxes lists the boxes laid end to end in buf.
func topLevelBoxes(t *testing.T, buf []byte) []boxRef {
	t.Helper()
	var boxes []boxRef
	off := 0
	err := walkBoxes(buf, func(typ string, box []byte) error {
		boxes = append(boxes, boxRef{typ: typ, off: off, size: len(box), data: box})
		off += len(box)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return boxes
}

func findBox(boxes []boxRef, typ string) *boxRef {
	for i := range boxes {
		if boxes[i].typ == typ {
			return &boxes[i]
		}
	}
	return nil
}

// moovDurations reads the mvhd duration and the tkhd/mdhd durations of every
// track from an encoded moov (version 0 headers, which is what the muxer
// writes).
func moovDurations(t *testing.T, moov []byte) (movie uint32, tkhd, mdhd map[uint32]uint32) {
	t.Helper()
	tkhd = make(map[uint32]uint32)
	mdhd = make(map[uint32]uint32)
	err := walkBoxes(moov[BasicBoxLen:], func(typ string, box []byte) error {
		switch typ {
		case "mvhd":
			movie = binary.BigEndian.Uint32(box[24:])
		case "trak":
			var id uint32
			return walkBoxes(box[BasicBoxLen:], func(typ string, box []byte) error {
				switch typ {
				case "tkhd":
					id = binary.BigEndian.Uint32(box[20:])
					tkhd[id] = binary.BigEndian.Uint32(box[28:])
				case "mdia":
					return walkBoxes(box[BasicBoxLen:], func(typ string, box []byte) error {
						if typ == "mdhd" {
							mdhd[id] = binary.BigEndian.Uint32(box[24:])
						}
						return nil
					})
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return
}

type sidxRef struct {
	size, duration uint32
}

func decodeSidx(t *testing.T, box []byte) (timescale uint32, earliest, firstOffset uint64, refs []sidxRef) {
	t.Helper()
	if box[8] != 1 {
		t.Fatalf("sidx version %d, want 1", box[8])
	}
	timescale = binary.BigEndian.Uint32(box[16:])
	earliest = binary.BigEndian.Uint64(box[20:])
	firstOffset = binary.BigEndian.Uint64(box[28:])
	count := int(binary.BigEndian.Uint16(box[38:]))
	for i := 0; i < count; i++ {
		e := box[40+12*i:]
		if e[0]&0x80 != 0 {
			t.Fatalf("sidx entry %d references another sidx", i)
		}
		refs = append(refs, sidxRef{
			size:     binary.BigEndian.Uint32(e) & 0x7FFFFFFF,
			duration: binary.BigEndian.Uint32(e[4:]),
		})
	}
	return
}

// muxVideoFragments writes frames video frames 40 ms apart, a key frame every
// gop frames, into a MP4_FLAG_FRAGMENT file.
func muxVideoFragments(t *testing.T, frames, gop int, options ...MuxerOption) []byte {
	t.Helper()
	w := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(w, append([]MuxerOption{WithMp4Flag(MP4_FLAG_FRAGMENT)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	vtid, err := muxer.AddVideoTrack(MP4_CODEC_H264)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < frames; i++ {
		frame := testP
		if i%gop == 0 {
			frame = append(append(append([]byte{}, testSPS...), testPPS...), testIDR...)
		}
		if err := muxer.Write(vtid, frame, uint64(i*40), uint64(i*40)); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
	return w.buf
}

func TestFragmentedFileHeaderCarriesWholeDuration(t *testing.T) {
	buf := muxVideoFragments(t, 10, 5)
	boxes := topLevelBoxes(t, buf)

	moov := findBox(boxes, "moov")
	if moov == nil {
		t.Fatal("no moov box")
	}
	// 10 frames of 40 ms; the last one is given the duration of the one before
	const want = 400
	got, ok := mehdDuration(t, moov.data)
	if !ok {
		t.Fatal("no mehd box: nothing states the length of the presentation")
	}
	if got != want {
		t.Fatalf("mehd duration %d, want %d", got, want)
	}
	// the fragments extend whatever the moov describes, so the durations in
	// the moov itself stay zero; a non zero one is added to the fragments and
	// doubles the length the player reports
	movie, tkhd, mdhd := moovDurations(t, moov.data)
	if movie != 0 || tkhd[1] != 0 || mdhd[1] != 0 {
		t.Fatalf("moov durations mvhd=%d tkhd=%d mdhd=%d, want 0 for all", movie, tkhd[1], mdhd[1])
	}

	// the head of the file is ftyp, moov, sidx, then the fragments
	if len(boxes) < 4 || boxes[0].typ != "ftyp" || boxes[1].typ != "moov" || boxes[2].typ != "sidx" {
		types := make([]string, 0, len(boxes))
		for _, b := range boxes {
			types = append(types, b.typ)
		}
		t.Fatalf("unexpected box layout %v", types)
	}
	sidx := boxes[2]
	timescale, earliest, firstOffset, refs := decodeSidx(t, sidx.data)
	if timescale != 1000 || earliest != 0 {
		t.Fatalf("sidx timescale=%d earliest=%d, want 1000 and 0", timescale, earliest)
	}

	var moofs []boxRef
	for _, b := range boxes {
		if b.typ == "moof" {
			moofs = append(moofs, b)
		}
	}
	mfra := findBox(boxes, "mfra")
	if len(moofs) != 2 || mfra == nil {
		t.Fatalf("got %d fragments, want 2 (key frames at 0 and 5) and an mfra", len(moofs))
	}
	if int(firstOffset) != moofs[0].off-(sidx.off+sidx.size) {
		t.Fatalf("sidx first_offset %d does not point at the first moof at %d", firstOffset, moofs[0].off)
	}
	if len(refs) != 2 {
		t.Fatalf("sidx has %d references, want 2", len(refs))
	}
	if int(refs[0].size) != moofs[1].off-moofs[0].off || int(refs[1].size) != mfra.off-moofs[1].off {
		t.Fatalf("sidx sizes %v do not match the fragments at %d, %d, mfra at %d", refs, moofs[0].off, moofs[1].off, mfra.off)
	}
	if refs[0].duration != 200 || refs[1].duration != 200 {
		t.Fatalf("sidx durations %v, want 200 each", refs)
	}
	if uint64(refs[0].duration+refs[1].duration) != got {
		t.Fatalf("sidx covers %d, mehd says %d", refs[0].duration+refs[1].duration, got)
	}

	// the demuxer must still read the file
	demuxer := CreateMp4Demuxer(bytes.NewReader(buf))
	infos, err := demuxer.ReadHead()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("demuxed %d tracks, want 1", len(infos))
	}
	n := 0
	for {
		pkg, err := demuxer.ReadPacket()
		if err != nil {
			break
		}
		if pkg != nil {
			n++
		}
	}
	if n != 10 {
		t.Fatalf("demuxed %d samples, want 10", n)
	}
}

func TestGlobalSidxMergesFragmentsBeyondReserve(t *testing.T) {
	buf := muxVideoFragments(t, 20, 5, WithSidxReserve(2))
	boxes := topLevelBoxes(t, buf)
	sidx := findBox(boxes, "sidx")
	if sidx == nil {
		t.Fatal("no sidx box")
	}
	var moofs []boxRef
	for _, b := range boxes {
		if b.typ == "moof" {
			moofs = append(moofs, b)
		}
	}
	mfra := findBox(boxes, "mfra")
	if len(moofs) != 4 || mfra == nil {
		t.Fatalf("got %d fragments, want 4", len(moofs))
	}
	_, _, firstOffset, refs := decodeSidx(t, sidx.data)
	if int(firstOffset) != moofs[0].off-(sidx.off+sidx.size) {
		t.Fatalf("sidx first_offset %d does not point at the first moof at %d", firstOffset, moofs[0].off)
	}
	if len(refs) != 2 {
		t.Fatalf("sidx has %d references, want 2 (two fragments each)", len(refs))
	}
	if int(refs[0].size) != moofs[2].off-moofs[0].off || int(refs[1].size) != mfra.off-moofs[2].off {
		t.Fatalf("merged sidx sizes %v wrong for fragments at %d %d %d %d", refs, moofs[0].off, moofs[1].off, moofs[2].off, moofs[3].off)
	}
	if refs[0].duration != 400 || refs[1].duration != 400 {
		t.Fatalf("merged sidx durations %v, want 400 each", refs)
	}
	// two entries fill the room reserved for two exactly: no free box is left
	if boxes[2].typ != "sidx" || boxes[3].typ != "moof" {
		t.Fatalf("layout after moov is %s %s, want sidx moof", boxes[2].typ, boxes[3].typ)
	}
	if boxes[2].size != sidxBoxSize(2) {
		t.Fatalf("sidx takes %d bytes, want the reserved %d", boxes[2].size, sidxBoxSize(2))
	}
}

func TestGlobalSidxLeavesFreeBoxForUnusedRoom(t *testing.T) {
	buf := muxVideoFragments(t, 10, 5, WithSidxReserve(8))
	boxes := topLevelBoxes(t, buf)
	if boxes[2].typ != "sidx" || boxes[3].typ != "free" || boxes[4].typ != "moof" {
		t.Fatalf("layout after moov is %s %s %s, want sidx free moof", boxes[2].typ, boxes[3].typ, boxes[4].typ)
	}
	if boxes[2].size+boxes[3].size != sidxBoxSize(8) {
		t.Fatalf("sidx+free take %d bytes, want the reserved %d", boxes[2].size+boxes[3].size, sidxBoxSize(8))
	}
	_, _, firstOffset, refs := decodeSidx(t, boxes[2].data)
	if len(refs) != 2 || int(firstOffset) != boxes[3].size {
		t.Fatalf("sidx has %d refs and first_offset %d, want 2 refs skipping the %d byte free box", len(refs), firstOffset, boxes[3].size)
	}
}

func TestSidxReserveZeroWritesNoIndexButFixesDurations(t *testing.T) {
	buf := muxVideoFragments(t, 10, 5, WithSidxReserve(0))
	boxes := topLevelBoxes(t, buf)
	if findBox(boxes, "sidx") != nil || findBox(boxes, "free") != nil {
		t.Fatal("sidx or free box written although the index is disabled")
	}
	if boxes[1].typ != "moov" || boxes[2].typ != "moof" {
		t.Fatalf("layout %s %s, want moov moof", boxes[1].typ, boxes[2].typ)
	}
	got, ok := mehdDuration(t, boxes[1].data)
	if !ok {
		t.Fatal("no mehd box")
	}
	if got != 400 {
		t.Fatalf("mehd duration %d, want 400", got)
	}
	movie, _, mdhd := moovDurations(t, boxes[1].data)
	if movie != 0 || mdhd[1] != 0 {
		t.Fatalf("moov durations mvhd=%d mdhd=%d, want 0", movie, mdhd[1])
	}
}

func TestReboundWriterLeavesHeadAlone(t *testing.T) {
	first := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(first, WithMp4Flag(MP4_FLAG_FRAGMENT))
	if err != nil {
		t.Fatal(err)
	}
	vtid, err := muxer.AddVideoTrack(MP4_CODEC_H264)
	if err != nil {
		t.Fatal(err)
	}
	key := append(append(append([]byte{}, testSPS...), testPPS...), testIDR...)
	for i := 0; i < 10; i++ {
		frame := testP
		if i%5 == 0 {
			frame = key
		}
		if err := muxer.Write(vtid, frame, uint64(i*40), uint64(i*40)); err != nil {
			t.Fatal(err)
		}
		if i == 5 {
			// the first fragment is out, move the rest to another writer
			muxer.ReBindWriter(newMemWriteSeeker())
		}
	}
	snapshot := append([]byte{}, first.buf...)
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot, first.buf) {
		t.Fatal("WriteTrailer wrote into the writer the moov went to after it was rebound")
	}
}

func TestLoneSampleFragmentIsNotMarkedEmpty(t *testing.T) {
	w := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(w, WithMp4Flag(MP4_FLAG_FRAGMENT))
	if err != nil {
		t.Fatal(err)
	}
	vtid, err := muxer.AddVideoTrack(MP4_CODEC_H264)
	if err != nil {
		t.Fatal(err)
	}
	key := append(append(append([]byte{}, testSPS...), testPPS...), testIDR...)
	if err := muxer.Write(vtid, key, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
	moof := findBox(topLevelBoxes(t, w.buf), "moof")
	if moof == nil {
		t.Fatal("no moof box")
	}
	var flags uint32
	err = walkBoxes(moof.data[BasicBoxLen:], func(typ string, box []byte) error {
		if typ != "traf" {
			return nil
		}
		return walkBoxes(box[BasicBoxLen:], func(typ string, box []byte) error {
			if typ == "tfhd" {
				flags = uint32(box[9])<<16 | uint32(box[10])<<8 | uint32(box[11])
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if flags&TF_FLAG_DURATION_IS_EMPTY != 0 {
		t.Fatalf("tfhd flags %#x mark a fragment with a sample as duration_is_empty", flags)
	}
}

func TestPatchMoovDurationsRewritesEveryHeader(t *testing.T) {
	w := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(w, WithMp4Flag(MP4_FLAG_FRAGMENT))
	if err != nil {
		t.Fatal(err)
	}
	vtid, err := muxer.AddVideoTrack(MP4_CODEC_H264)
	if err != nil {
		t.Fatal(err)
	}
	atid, err := muxer.AddAudioTrack(MP4_CODEC_AAC)
	if err != nil {
		t.Fatal(err)
	}
	key := append(append(append([]byte{}, testSPS...), testPPS...), testIDR...)
	if err := muxer.Write(vtid, key, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := muxer.Write(atid, adtsFrame(100), 0, 0); err != nil {
		t.Fatal(err)
	}
	moov, err := muxer.makeMoov()
	if err != nil {
		t.Fatal(err)
	}
	patched := append([]byte{}, moov...)
	if err := patchMoovDurations(patched, 12345, map[uint32]uint64{1: 12345, 2: 6789}); err != nil {
		t.Fatal(err)
	}
	if len(patched) != len(moov) {
		t.Fatal("patching changed the moov size")
	}
	movie, tkhd, mdhd := moovDurations(t, patched)
	if movie != 12345 || tkhd[1] != 12345 || mdhd[1] != 12345 || tkhd[2] != 6789 || mdhd[2] != 6789 {
		t.Fatalf("patched durations mvhd=%d tkhd=%v mdhd=%v", movie, tkhd, mdhd)
	}
}

func TestDashSegmentSidxDescribesItsFragment(t *testing.T) {
	w := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(w, WithMp4Flag(MP4_FLAG_DASH))
	if err != nil {
		t.Fatal(err)
	}
	vtid, err := muxer.AddVideoTrack(MP4_CODEC_H264)
	if err != nil {
		t.Fatal(err)
	}
	atid, err := muxer.AddAudioTrack(MP4_CODEC_AAC)
	if err != nil {
		t.Fatal(err)
	}
	key := append(append(append([]byte{}, testSPS...), testPPS...), testIDR...)
	for i := 0; i < 10; i++ {
		frame := testP
		if i%5 == 0 {
			frame = key
		}
		if err := muxer.Write(vtid, frame, uint64(i*40), uint64(i*40)); err != nil {
			t.Fatal(err)
		}
		if err := muxer.Write(atid, adtsFrame(50), uint64(i*40), uint64(i*40)); err != nil {
			t.Fatal(err)
		}
	}
	// dash output leaves the last segment to the caller, as the hls examples do
	if err := muxer.FlushFragment(); err != nil {
		t.Fatal(err)
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}

	boxes := topLevelBoxes(t, w.buf)
	segments := 0
	for i := 0; i < len(boxes); i++ {
		if boxes[i].typ != "styp" {
			continue
		}
		segments++
		// styp, one sidx per track, moof, mdat
		if i+4 >= len(boxes) || boxes[i+1].typ != "sidx" || boxes[i+2].typ != "sidx" || boxes[i+3].typ != "moof" || boxes[i+4].typ != "mdat" {
			t.Fatalf("segment %d: unexpected layout after styp at %d", segments, boxes[i].off)
		}
		moof, mdat := boxes[i+3], boxes[i+4]
		for k, sidx := range boxes[i+1 : i+3] {
			if sidx.size != sidxBoxSize(1) {
				t.Fatalf("segment %d sidx %d is %d bytes, want %d", segments, k, sidx.size, sidxBoxSize(1))
			}
			_, _, firstOffset, refs := decodeSidx(t, sidx.data)
			if sidx.off+sidx.size+int(firstOffset) != moof.off {
				t.Fatalf("segment %d sidx %d: first_offset %d does not land on the moof at %d", segments, k, firstOffset, moof.off)
			}
			if len(refs) != 1 || int(refs[0].size) != moof.size+mdat.size {
				t.Fatalf("segment %d sidx %d: refs %v, want one of %d bytes", segments, k, refs, moof.size+mdat.size)
			}
			// five frames of 40 ms per fragment, the last one given the
			// duration of the one before
			if refs[0].duration != 200 {
				t.Fatalf("segment %d sidx %d: duration %d, want 200", segments, k, refs[0].duration)
			}
			if sidx.data[48]>>4 != 0x9 { // starts_with_SAP=1, SAP_type=1
				t.Fatalf("segment %d sidx %d: SAP byte %#x, want starts_with_SAP=1 SAP_type=1", segments, k, sidx.data[48])
			}
		}
	}
	if segments != 2 {
		t.Fatalf("got %d segments, want 2", segments)
	}
}

// streamWriteSeeker is a write once sink, an HTTP response say: it answers
// where the next write lands and a seek that goes nowhere, and refuses every
// seek that would move backwards.
type streamWriteSeeker struct{ buf []byte }

func (s *streamWriteSeeker) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *streamWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	pos := int64(len(s.buf))
	if (whence == io.SeekCurrent && offset == 0) || (whence == io.SeekStart && offset == pos) {
		return pos, nil
	}
	return pos, errors.New("stream is not seekable")
}

func muxVideoFragmentsTo(t *testing.T, w io.WriteSeeker, frames, gop int, options ...MuxerOption) {
	t.Helper()
	muxer, err := CreateMp4Muxer(w, append([]MuxerOption{WithMp4Flag(MP4_FLAG_FRAGMENT)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	vtid, err := muxer.AddVideoTrack(MP4_CODEC_H264)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < frames; i++ {
		frame := testP
		if i%gop == 0 {
			frame = append(append(append([]byte{}, testSPS...), testPPS...), testIDR...)
		}
		if err := muxer.Write(vtid, frame, uint64(i*40), uint64(i*40)); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
}

func TestDurationHintReachesHeadOfUnseekableFile(t *testing.T) {
	w := &streamWriteSeeker{}
	// 10 frames of 40 ms really last 400 ms, so a head carrying the hint
	// cannot be one the muxer filled in from the samples
	const hint = 60000
	muxVideoFragmentsTo(t, w, 10, 5, WithSidxReserve(0), WithDurationHint(hint))

	boxes := topLevelBoxes(t, w.buf)
	moovs := 0
	for _, b := range boxes {
		if b.typ == "moov" {
			moovs++
		}
	}
	if moovs != 1 {
		t.Fatalf("got %d moov boxes, want 1: a head that cannot be patched must not be appended again", moovs)
	}
	moov := findBox(boxes, "moov").data
	got, ok := mehdDuration(t, moov)
	if !ok {
		t.Fatal("no mehd box: nothing states the length of the presentation")
	}
	if got != hint {
		t.Fatalf("mehd duration %d, want %d", got, hint)
	}
	// a player adds the fragments to whatever the moov says it already
	// describes, so every duration in the moov itself has to be zero
	movie, tkhd, mdhd := moovDurations(t, moov)
	if movie != 0 || tkhd[1] != 0 || mdhd[1] != 0 {
		t.Fatalf("moov durations mvhd=%d tkhd=%d mdhd=%d, want 0 for all: a non zero one is added to the fragments and doubles the reported length", movie, tkhd[1], mdhd[1])
	}
}

func TestDurationHintYieldsToRealDurationWhenSeekable(t *testing.T) {
	buf := muxVideoFragments(t, 10, 5, WithDurationHint(60000))
	moov := findBox(topLevelBoxes(t, buf), "moov").data
	// a writer that seeks gets the length WriteTrailer measured, not the hint
	const want = 400
	got, ok := mehdDuration(t, moov)
	if !ok {
		t.Fatal("no mehd box")
	}
	if got != want {
		t.Fatalf("mehd duration %d, want the measured %d", got, want)
	}
}

// mehdDuration reads the length the moov states for the whole presentation,
// fragments included. It reports false when the box is absent.
func mehdDuration(t *testing.T, moov []byte) (uint64, bool) {
	t.Helper()
	var got uint64
	var found bool
	err := walkBoxes(moov[BasicBoxLen:], func(typ string, box []byte) error {
		if typ != "mvex" {
			return nil
		}
		return walkBoxes(box[BasicBoxLen:], func(typ string, box []byte) error {
			if typ == "mehd" {
				got = binary.BigEndian.Uint64(box[FullBoxLen:])
				found = true
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return got, found
}

func TestUnhintedUnseekableFileLeavesLengthUnstated(t *testing.T) {
	w := &streamWriteSeeker{}
	muxVideoFragmentsTo(t, w, 10, 5, WithSidxReserve(0))
	moov := findBox(topLevelBoxes(t, w.buf), "moov").data

	// nothing knew the length while the head went out, so nothing may claim
	// one: a player walks the fragments and gets the right answer only if
	// every duration in the moov reads zero
	movie, tkhd, mdhd := moovDurations(t, moov)
	if movie != 0 || tkhd[1] != 0 || mdhd[1] != 0 {
		t.Fatalf("moov durations mvhd=%d tkhd=%d mdhd=%d, want 0 for all", movie, tkhd[1], mdhd[1])
	}
	if got, ok := mehdDuration(t, moov); ok && got != 0 {
		t.Fatalf("mehd duration %d, want 0 for a length nobody could know", got)
	}
}
