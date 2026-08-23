package mp4

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	UseSubsampleEncryption uint32 = 0x000002
)

// SencBox - Sample Encryption Box (senc) (in trak or traf box)
// See ISO/IEC 23001-7 Section 7.2 and CMAF specification
// Full Box + SampleCount
type SencBox struct {
	Box             *FullBox
	SampleCount     uint32
	PerSampleIVSize uint32
	EntryList       *movsenc
}

func (senc *SencBox) Decode(r io.Reader, size uint32, perSampleIVSize uint8) (offset int, err error) {
	if offset, err = senc.Box.Decode(r); err != nil {
		return
	}
	senc.PerSampleIVSize = uint32(perSampleIVSize)
	buf, err := readBoxPayload(r, uint64(size), FullBoxLen)
	if err != nil {
		return 0, err
	}
	n := 0
	if err = checkRemain(buf, n, 4); err != nil {
		return 0, err
	}
	senc.SampleCount = binary.BigEndian.Uint32(buf[n:])
	n += 4
	sencFlags := uint32(senc.Box.Flags[0])<<16 | uint32(senc.Box.Flags[1])<<8 | uint32(senc.Box.Flags[2])

	// each sample needs at least the IV (and a subsample count when present)
	minEntry := int(senc.PerSampleIVSize)
	if sencFlags&UseSubsampleEncryption > 0 {
		minEntry += 2
	}
	if minEntry > 0 {
		if err = checkTableSize(senc.SampleCount, minEntry, int64(len(buf)-n)); err != nil {
			return 0, err
		}
	} else if senc.SampleCount > maxTrunSampleCount {
		return 0, errors.New("mp4: senc sample_count too large")
	}
	senc.EntryList = new(movsenc)
	senc.EntryList.entrys = make([]sencEntry, senc.SampleCount)
	for i := 0; i < int(senc.SampleCount); i++ {
		if err = checkRemain(buf, n, int(senc.PerSampleIVSize)); err != nil {
			return 0, err
		}
		senc.EntryList.entrys[i].iv = buf[n : n+int(senc.PerSampleIVSize)]
		n += int(senc.PerSampleIVSize)

		if sencFlags&UseSubsampleEncryption <= 0 {
			continue
		}

		if err = checkRemain(buf, n, 2); err != nil {
			return 0, err
		}
		subsampleCount := binary.BigEndian.Uint16(buf[n:])
		n += 2
		if err = checkRemain(buf, n, int(subsampleCount)*6); err != nil {
			return 0, err
		}

		senc.EntryList.entrys[i].subSamples = make([]subSampleEntry, subsampleCount)
		for j := uint16(0); j < subsampleCount; j++ {
			senc.EntryList.entrys[i].subSamples[j].bytesOfClearData = binary.BigEndian.Uint16(buf[n:])
			n += 2
			senc.EntryList.entrys[i].subSamples[j].bytesOfProtectedData = binary.BigEndian.Uint32(buf[n:])
			n += 4
		}
	}

	offset += n
	return
}

func decodeSencBox(demuxer *MovDemuxer, size uint32) (err error) {
	if demuxer.currentTrack == nil {
		return errors.New("current track is nil")
	}
	perSampleIVSize := demuxer.currentTrack.defaultPerSampleIVSize
	senc := SencBox{Box: new(FullBox)}
	if _, err = senc.Decode(demuxer.reader, size, perSampleIVSize); err != nil {
		return err
	}
	demuxer.currentTrack.subSamples = append(demuxer.currentTrack.subSamples, senc.EntryList.entrys...)
	return
}
