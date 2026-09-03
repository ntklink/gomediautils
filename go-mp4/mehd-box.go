package mp4

import (
	"encoding/binary"
	"io"
)

// aligned(8) class MovieExtendsHeaderBox extends FullBox(‘mehd’, version, 0) {
// 	if (version==1) {
// 		unsigned int(64)  fragment_duration;
// 	} else { // version==0
// 		unsigned int(32)  fragment_duration;
// 	}
// }

// MovieExtendsHeaderBox carries the length of a fragmented presentation,
// fragments included.
//
// It is the only box that says so. The durations in mvhd, tkhd and mdhd cover
// what the moov itself describes, and the sample tables of a fragmented file
// are empty, so those are zero and the fragments extend the movie from there.
// A player that adds the two up, apple's AVFoundation among them, reads a
// length written into mvhd as a stretch of movie that comes before the
// fragments and reports twice the real length, playing the first half and
// sitting on a frozen frame through the second.
//
// This muxer always writes version 1, so the box keeps its size whatever the
// duration turns out to be and WriteTrailer can patch the real value over a
// placeholder.
type MovieExtendsHeaderBox struct {
	Box              *FullBox
	FragmentDuration uint64
}

func NewMovieExtendsHeaderBox(duration uint64) *MovieExtendsHeaderBox {
	return &MovieExtendsHeaderBox{
		Box:              NewFullBox([4]byte{'m', 'e', 'h', 'd'}, 1),
		FragmentDuration: duration,
	}
}

func (mehd *MovieExtendsHeaderBox) Size() uint64 {
	if mehd.Box.Version == 1 {
		return mehd.Box.Size() + 8
	}
	return mehd.Box.Size() + 4
}

func (mehd *MovieExtendsHeaderBox) Decode(r io.Reader) (offset int, err error) {
	if offset, err = mehd.Box.Decode(r); err != nil {
		return
	}
	if mehd.Box.Version == 1 {
		buf := make([]byte, 8)
		if _, err = io.ReadFull(r, buf); err != nil {
			return 0, err
		}
		mehd.FragmentDuration = binary.BigEndian.Uint64(buf)
		return offset + 8, nil
	}
	buf := make([]byte, 4)
	if _, err = io.ReadFull(r, buf); err != nil {
		return 0, err
	}
	mehd.FragmentDuration = uint64(binary.BigEndian.Uint32(buf))
	return offset + 4, nil
}

func (mehd *MovieExtendsHeaderBox) Encode() (int, []byte) {
	mehd.Box.Box.Size = mehd.Size()
	offset, boxdata := mehd.Box.Encode()
	if mehd.Box.Version == 1 {
		binary.BigEndian.PutUint64(boxdata[offset:], mehd.FragmentDuration)
		return offset + 8, boxdata
	}
	binary.BigEndian.PutUint32(boxdata[offset:], uint32(mehd.FragmentDuration))
	return offset + 4, boxdata
}

// mehdBoxSize is the encoded size of the version 1 box this muxer writes.
const mehdBoxSize = FullBoxLen + 8

func makeMehdBox(duration uint64) []byte {
	mehd := NewMovieExtendsHeaderBox(duration)
	_, boxData := mehd.Encode()
	return boxData
}
