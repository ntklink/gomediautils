package mp4

import (
	"encoding/binary"
	"errors"
	"io"
)

// fmp4Header remembers what was written at the head of a MP4_FLAG_FRAGMENT
// file so WriteTrailer can finish it.
//
// The moov goes out before the first fragment, when neither the length of the
// presentation nor the layout of the fragments is known. A player that trusts
// the moov (android's MediaPlayer, ExoPlayer) then sees the length of the
// first fragment as the length of the whole file, and without a segment index
// (sidx) in front of the fragments android cannot seek at all; apple players
// walk every fragment instead and are not affected. So the muxer keeps a copy
// of the moov, reserves room for a sidx behind it and, once the last fragment
// is out, patches the durations and fills in the index.
type fmp4Header struct {
	writer     io.WriteSeeker // where the head went; a rebound writer skips the fix up
	moovOffset int64
	moov       []byte
	sidxOffset int64 // start of the placeholder reserved for the sidx
	sidxSpace  int   // its size in bytes, 0 when no room was reserved
	refTrack   uint32
	fragments  []fmp4FragmentRef
}

// fmp4FragmentRef is one moof+mdat pair: where it starts and how long the
// samples of the reference track in it last.
type fmp4FragmentRef struct {
	offset   uint64
	duration uint32
}

// sidxBoxSize is the encoded size of a version 1 sidx with refs references.
func sidxBoxSize(refs int) int {
	return FullBoxLen + 28 + 12*refs
}

// addFragment records the fragment about to be written at moofOffset. Only a
// file whose head this muxer wrote gets an index, so dash output, which
// writes its own sidx per segment, records nothing.
func (h *fmp4Header) addFragment(moofOffset uint64, tracks map[uint32]*mp4track) {
	if h.moov == nil {
		return
	}
	var duration uint32
	if track, ok := tracks[h.refTrack]; ok {
		duration = track.runDuration
	}
	h.fragments = append(h.fragments, fmp4FragmentRef{offset: moofOffset, duration: duration})
}

// indexTrack picks the track the sidx describes: the first video track, or
// the first track of an audio only file.
func (muxer *Movmuxer) indexTrack() uint32 {
	for i := uint32(1); i < muxer.nextTrackId; i++ {
		if isVideo(muxer.tracks[i].cid) {
			return i
		}
	}
	return 1
}

// writeFileHeader writes ftyp and moov ahead of the first fragment of a
// MP4_FLAG_FRAGMENT file and reserves the room for the sidx as a free box.
func (muxer *Movmuxer) writeFileHeader() error {
	ftypBox := makeFtypBox(mov_tag(iso5), 0x200, []uint32{mov_tag(iso5), mov_tag(iso6), mov_tag(mp41)})
	if _, err := muxer.writer.Write(ftypBox); err != nil {
		return err
	}
	moovOffset, err := muxer.writer.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	moov, err := muxer.makeMoov()
	if err != nil {
		return err
	}
	if _, err := muxer.writer.Write(moov); err != nil {
		return err
	}
	muxer.header = fmp4Header{
		writer:     muxer.writer,
		moovOffset: moovOffset,
		moov:       moov,
		refTrack:   muxer.indexTrack(),
	}
	if muxer.sidxReserve == 0 {
		return nil
	}
	space := sidxBoxSize(int(muxer.sidxReserve))
	free := NewFreeBox()
	free.Data = make([]byte, space-BasicBoxLen)
	_, freeBox := free.Encode()
	if _, err := muxer.writer.Write(freeBox); err != nil {
		return err
	}
	muxer.header.sidxOffset = moovOffset + int64(len(moov))
	muxer.header.sidxSpace = space
	return nil
}

// finishFileHeader goes back to the head of the file, writes the real
// durations into the moov and the segment index into the room reserved for
// it, then returns to the end so the mfra can follow.
//
// The head is left alone when the moov went to another writer (ReBindWriter
// moved on to a new segment) or the writer refuses to seek backwards, which
// is what a streaming sink behind a WriteSeeker shim does; the file is then
// no worse than before.
func (muxer *Movmuxer) finishFileHeader() error {
	h := &muxer.header
	if h.moov == nil || h.writer != muxer.writer {
		return nil
	}
	end, err := muxer.writer.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := muxer.writer.Seek(h.moovOffset, io.SeekStart); err != nil {
		return nil
	}

	var movieDuration uint64
	for _, track := range muxer.tracks {
		// mehd counts in the movie timescale, which makeMvhdBox fixes at 1000
		if d := track.totalDuration * 1000 / uint64(track.timescale); d > movieDuration {
			movieDuration = d
		}
	}
	// only mehd: the durations in mvhd, tkhd and mdhd describe what the moov
	// itself holds, which for a fragmented file is nothing, and a player that
	// adds them to the fragments reports twice the real length
	moov := make([]byte, len(h.moov))
	copy(moov, h.moov)
	if err := patchMoovMehd(moov, movieDuration); err != nil {
		return err
	}
	if _, err := muxer.writer.Write(moov); err != nil {
		return err
	}

	if h.sidxSpace > 0 {
		if sidx := muxer.makeGlobalSidx(uint64(end)); sidx != nil {
			if _, err := muxer.writer.Seek(h.sidxOffset, io.SeekStart); err != nil {
				return err
			}
			if _, err := muxer.writer.Write(sidx); err != nil {
				return err
			}
			if rest := h.sidxSpace - len(sidx); rest > 0 {
				free := NewFreeBox()
				free.Data = make([]byte, rest-BasicBoxLen)
				_, freeBox := free.Encode()
				if _, err := muxer.writer.Write(freeBox); err != nil {
					return err
				}
			}
		}
	}
	_, err = muxer.writer.Seek(end, io.SeekStart)
	return err
}

// makeGlobalSidx builds the sidx covering every fragment written so far; end
// is the offset just past the last one. It fits the reserved room by letting
// one entry span several fragments when there are more fragments than
// entries. It returns nil when there is nothing sensible to write, and the
// placeholder then stays a free box.
func (muxer *Movmuxer) makeGlobalSidx(end uint64) []byte {
	h := &muxer.header
	track, ok := muxer.tracks[h.refTrack]
	if !ok || len(h.fragments) == 0 || muxer.sidxReserve == 0 {
		return nil
	}
	reserve := int(muxer.sidxReserve)
	group := (len(h.fragments) + reserve - 1) / reserve

	sidx := NewSegmentIndexBox()
	sidx.ReferenceID = track.trackId
	sidx.TimeScale = track.timescale
	if len(track.fragments) > 0 {
		sidx.EarliestPresentationTime = track.fragments[0].firstPts
	}
	for i := 0; i < len(h.fragments); i += group {
		j := i + group
		if j > len(h.fragments) {
			j = len(h.fragments)
		}
		next := end
		if j < len(h.fragments) {
			next = h.fragments[j].offset
		}
		if next < h.fragments[i].offset {
			return nil
		}
		size := next - h.fragments[i].offset
		if size > 0x7FFFFFFF { // referenced_size is 31 bits
			return nil
		}
		var duration uint64
		for _, frag := range h.fragments[i:j] {
			duration += uint64(frag.duration)
		}
		if duration > 0xFFFFFFFF {
			duration = 0xFFFFFFFF
		}
		sidx.Entrys = append(sidx.Entrys, sidxEntry{
			ReferencedSize:     uint32(size),
			SubsegmentDuration: uint32(duration),
			StartsWithSAP:      1,
			SAPType:            1,
		})
	}
	sidx.ReferenceCount = uint16(len(sidx.Entrys))
	// first_offset is measured from the end of the sidx to the first moof,
	// which is right behind the free box that takes up the rest of the room
	sidx.FirstOffset = uint64(h.sidxSpace - sidxBoxSize(len(sidx.Entrys)))
	_, boxData := sidx.Encode()
	return boxData
}

// walkBoxes calls fn for every box laid end to end in data with the whole box,
// header included.
func walkBoxes(data []byte, fn func(boxType string, box []byte) error) error {
	for off := 0; off < len(data); {
		if off+BasicBoxLen > len(data) {
			return errors.New("mp4: truncated box header")
		}
		size := uint64(binary.BigEndian.Uint32(data[off:]))
		hdr := BasicBoxLen
		switch size {
		case 0:
			size = uint64(len(data) - off)
		case 1:
			if off+16 > len(data) {
				return errors.New("mp4: truncated box header")
			}
			size = binary.BigEndian.Uint64(data[off+8:])
			hdr = 16
		}
		if size < uint64(hdr) || size > uint64(len(data)-off) {
			return errors.New("mp4: box size out of range")
		}
		if err := fn(string(data[off+4:off+8]), data[off:off+int(size)]); err != nil {
			return err
		}
		off += int(size)
	}
	return nil
}

// patchHeaderDuration overwrites the duration field of an mvhd, tkhd or mdhd
// box; v0Off and v1Off locate it for the 32 and 64 bit layouts.
func patchHeaderDuration(box []byte, v0Off, v1Off int, duration uint64) error {
	if len(box) < FullBoxLen {
		return errors.New("mp4: header box too short")
	}
	if box[8] == 1 {
		if len(box) < v1Off+8 {
			return errors.New("mp4: header box too short")
		}
		binary.BigEndian.PutUint64(box[v1Off:], duration)
		return nil
	}
	if len(box) < v0Off+4 {
		return errors.New("mp4: header box too short")
	}
	if duration > 0xFFFFFFFF {
		duration = 0xFFFFFFFF
	}
	binary.BigEndian.PutUint32(box[v0Off:], uint32(duration))
	return nil
}

// patchMoovDurations rewrites, in place, the movie duration in mvhd and the
// duration of every track in its tkhd and mdhd. Nothing else in the moov
// moves, so the patched box is a drop in replacement for the one on disk.
func patchMoovDurations(moov []byte, movieDuration uint64, trackDurations map[uint32]uint64) error {
	return walkBoxes(moov, func(boxType string, box []byte) error {
		if boxType != "moov" {
			return nil
		}
		return walkBoxes(box[BasicBoxLen:], func(boxType string, box []byte) error {
			switch boxType {
			case "mvhd":
				return patchHeaderDuration(box, 24, 32, movieDuration)
			case "trak":
				return patchTrakDurations(box, trackDurations)
			case "mvex":
				return patchMvexMehd(box, movieDuration)
			}
			return nil
		})
	})
}

// patchMoovMehd rewrites, in place, the length of the whole presentation in
// the mehd box, leaving every other duration in the moov alone. A moov with
// no mehd is left untouched: a player then works the length out by walking
// the fragments, which is correct as long as the moov's own durations are
// zero.
func patchMoovMehd(moov []byte, movieDuration uint64) error {
	return walkBoxes(moov, func(boxType string, box []byte) error {
		if boxType != "moov" {
			return nil
		}
		return walkBoxes(box[BasicBoxLen:], func(boxType string, box []byte) error {
			if boxType != "mvex" {
				return nil
			}
			return patchMvexMehd(box, movieDuration)
		})
	})
}

func patchMvexMehd(mvex []byte, movieDuration uint64) error {
	return walkBoxes(mvex[BasicBoxLen:], func(boxType string, box []byte) error {
		if boxType != "mehd" {
			return nil
		}
		if len(box) < mehdBoxSize {
			return errors.New("mp4: mehd box too short")
		}
		binary.BigEndian.PutUint64(box[FullBoxLen:], movieDuration)
		return nil
	})
}

func patchTrakDurations(trak []byte, trackDurations map[uint32]uint64) error {
	var trackID uint32
	return walkBoxes(trak[BasicBoxLen:], func(boxType string, box []byte) error {
		switch boxType {
		case "tkhd":
			idOff := 20
			if len(box) > 8 && box[8] == 1 {
				idOff = 28
			}
			if len(box) < idOff+4 {
				return errors.New("mp4: tkhd box too short")
			}
			trackID = binary.BigEndian.Uint32(box[idOff:])
			return patchHeaderDuration(box, 28, 36, trackDurations[trackID])
		case "mdia":
			return walkBoxes(box[BasicBoxLen:], func(boxType string, box []byte) error {
				if boxType != "mdhd" {
					return nil
				}
				return patchHeaderDuration(box, 24, 32, trackDurations[trackID])
			})
		}
		return nil
	})
}
