package mp4

import (
	"encoding/binary"
	"errors"
	"io"
)

var _ = io.EOF

// SaizBox - Sample Auxiliary Information Sizes Box (saiz)  (in stbl or traf box)
type SaizBox struct {
	Box                   *FullBox
	AuxInfoType           string // Used for Common Encryption Scheme (4-bytes uint32 according to spec)
	AuxInfoTypeParameter  uint32
	SampleCount           uint32
	SampleInfo            []byte
	DefaultSampleInfoSize uint8
}

func (s *SaizBox) Decode(r io.Reader, size uint32) error {
	if _, err := s.Box.Decode(r); err != nil {
		return err
	}
	buf, err := readBoxPayload(r, uint64(size), FullBoxLen)
	if err != nil {
		return err
	}
	var n int
	flags := uint32(s.Box.Flags[0])<<16 | uint32(s.Box.Flags[1])<<8 | uint32(s.Box.Flags[2])
	if flags&0x01 != 0 {
		if err = checkRemain(buf, n, 8); err != nil {
			return err
		}
		s.AuxInfoType = string(buf[n : n+4])
		n += 4
		s.AuxInfoTypeParameter = binary.BigEndian.Uint32(buf[n:])
		n += 4
	}
	if err = checkRemain(buf, n, 5); err != nil {
		return err
	}
	s.DefaultSampleInfoSize = buf[n]
	n += 1

	s.SampleCount = binary.BigEndian.Uint32(buf[n:])
	n += 4

	if s.DefaultSampleInfoSize == 0 {
		if err = checkTableSize(s.SampleCount, 1, int64(len(buf)-n)); err != nil {
			return err
		}
		for i := 0; i < int(s.SampleCount); i++ {
			s.SampleInfo = append(s.SampleInfo, buf[n])
			n += 1
		}
	}
	return nil
}

func decodeSaizBox(demuxer *MovDemuxer, size uint32) error {
	saiz := SaizBox{Box: new(FullBox)}
	err := saiz.Decode(demuxer.reader, size)
	if err != nil {
		return err
	}
	if demuxer.currentTrack == nil {
		return errors.New("current track is nil")
	}
	demuxer.currentTrack.lastSaiz = &saiz
	return nil
}
