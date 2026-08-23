package mp4

import (
	"encoding/binary"
	"errors"
)

func makeTraf(track *mp4track, moofOffset uint64, moofSize uint64) []byte {
	tfhd := makeTfhdBox(track, moofOffset)
	tfdt := makeTfdtBox(track)
	trun := makeTrunBoxes(track, moofSize)

	traf := BasicBox{Type: [4]byte{'t', 'r', 'a', 'f'}}
	traf.Size = 8 + uint64(len(tfhd)+len(tfdt)+len(trun))
	offset, boxData := traf.Encode()
	copy(boxData[offset:], tfhd)
	offset += len(tfhd)
	copy(boxData[offset:], tfdt)
	offset += len(tfdt)
	copy(boxData[offset:], trun)
	offset += len(trun)
	return boxData
}

// patchTrafDataOffset adds delta to the data_offset field of every trun box
// inside traf. A traf is first built with a zero moof size so that the size of
// the moof box can be computed; this then fixes up the sample offsets without
// re-encoding the whole traf.
func patchTrafDataOffset(traf []byte, delta int32) error {
	if len(traf) < BasicBoxLen {
		return errors.New("mp4: traf box too short")
	}
	for p := BasicBoxLen; p+BasicBoxLen <= len(traf); {
		size := int(binary.BigEndian.Uint32(traf[p:]))
		if size < BasicBoxLen || p+size > len(traf) {
			return errors.New("mp4: malformed traf box")
		}
		if string(traf[p+4:p+8]) == "trun" {
			// FullBox header (12) + sample_count (4), data_offset follows
			if size < FullBoxLen+8 {
				return errors.New("mp4: malformed trun box")
			}
			flags := uint32(traf[p+9])<<16 | uint32(traf[p+10])<<8 | uint32(traf[p+11])
			if flags&TR_FLAG_DATA_OFFSET == 0 {
				return errors.New("mp4: trun box without data_offset")
			}
			off := p + FullBoxLen + 4
			v := int32(binary.BigEndian.Uint32(traf[off:]))
			binary.BigEndian.PutUint32(traf[off:], uint32(v+delta))
		}
		p += size
	}
	return nil
}
