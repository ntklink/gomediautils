package mp4

import (
	"encoding/binary"
	"errors"
	"io"
)

// SaioBox - Sample Auxiliary Information Offsets Box (saiz) (in stbl or traf box)
type SaioBox struct {
	Box                  *FullBox
	AuxInfoType          string // Used for Common Encryption Scheme (4-bytes uint32 according to spec)
	AuxInfoTypeParameter uint32
	Offset               []int64
}

func (s *SaioBox) Decode(r io.Reader, size uint32) error {
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
	if err = checkRemain(buf, n, 4); err != nil {
		return err
	}
	entryCount := binary.BigEndian.Uint32(buf[n:])
	n += 4
	entrySize := 4
	if s.Box.Version != 0 {
		entrySize = 8
	}
	if err = checkTableSize(entryCount, entrySize, int64(len(buf)-n)); err != nil {
		return err
	}
	if s.Box.Version == 0 {
		for i := uint32(0); i < entryCount; i++ {
			s.Offset = append(s.Offset, int64(binary.BigEndian.Uint32(buf[n:])))
			n += 4
		}
	} else {
		for i := uint32(0); i < entryCount; i++ {
			s.Offset = append(s.Offset, int64(binary.BigEndian.Uint64(buf[n:])))
			n += 8
		}
	}
	return nil
}

func decodeSaioBox(demuxer *MovDemuxer, size uint32) error {
	saio := SaioBox{Box: new(FullBox)}
	err := saio.Decode(demuxer.reader, size)
	if err != nil {
		return err
	}
	if demuxer.currentTrack == nil {
		return errors.New("current track is nil")
	}
	if len(saio.Offset) > 0 && len(demuxer.currentTrack.subSamples) == 0 {
		saiz := demuxer.currentTrack.lastSaiz
		if saiz == nil {
			return errors.New("mp4: saio box without a preceding saiz box")
		}
		if saiz.DefaultSampleInfoSize == 0 && len(saiz.SampleInfo) < int(saiz.SampleCount) {
			return errors.New("mp4: saiz sample info shorter than sample count")
		}
		var currentOffset int64
		currentOffset, err = demuxer.reader.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if _, err = demuxer.reader.Seek(demuxer.moofOffset+saio.Offset[0], io.SeekStart); err != nil {
			return err
		}
		for i := uint32(0); i < saiz.SampleCount; i++ {
			sampleSize := saiz.DefaultSampleInfoSize
			if saiz.DefaultSampleInfoSize == 0 {
				sampleSize = saiz.SampleInfo[i]
			}
			buf := make([]byte, sampleSize)
			if _, err = io.ReadFull(demuxer.reader, buf); err != nil {
				return err
			}
			var se sencEntry
			se.iv = make([]byte, 16)
			if err = checkRemain(buf, 0, 8); err != nil {
				return err
			}
			copy(se.iv, buf[:8])
			if sampleSize == 8 {
				demuxer.currentTrack.subSamples = append(demuxer.currentTrack.subSamples, se)
				continue
			}
			n := 8
			if err = checkRemain(buf, n, 2); err != nil {
				return err
			}
			sampleCount := binary.BigEndian.Uint16(buf[n:])
			n += 2
			if err = checkRemain(buf, n, int(sampleCount)*6); err != nil {
				return err
			}

			se.subSamples = make([]subSampleEntry, sampleCount)
			for j := 0; j < int(sampleCount); j++ {
				se.subSamples[j].bytesOfClearData = binary.BigEndian.Uint16(buf[n:])
				n += 2
				se.subSamples[j].bytesOfProtectedData = binary.BigEndian.Uint32(buf[n:])
				n += 4
			}
			demuxer.currentTrack.subSamples = append(demuxer.currentTrack.subSamples, se)
		}
		if _, err = demuxer.reader.Seek(currentOffset, io.SeekStart); err != nil {
			return err
		}
	}
	return nil
}
