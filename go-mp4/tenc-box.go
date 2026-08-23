package mp4

import (
	"encoding/binary"
)

func decodeTencBox(demuxer *MovDemuxer, size uint32) (err error) {
	track := demuxer.lastTrack()
	if track == nil {
		return errNoTrack
	}
	buf, err := readBoxPayload(demuxer.reader, uint64(size), BasicBoxLen)
	if err != nil {
		return
	}
	// version/flags(4) reserved(1) pattern(1) isProtected(1) perSampleIVSize(1) KID(16)
	if err = checkRemain(buf, 0, 24); err != nil {
		return
	}
	n := 0
	versionAndFlags := binary.BigEndian.Uint32(buf[n:])
	n += 5
	version := byte(versionAndFlags >> 24)
	if version != 0 {
		infoByte := buf[n]
		track.defaultCryptByteBlock = infoByte >> 4
		track.defaultSkipByteBlock = infoByte & 0x0f
	}
	n += 1
	track.defaultIsProtected = buf[n]
	n += 1
	track.defaultPerSampleIVSize = buf[n]
	n += 1
	copy(track.defaultKID[:], buf[n:n+16])
	n += 16
	if track.defaultIsProtected == 1 && track.defaultPerSampleIVSize == 0 {
		if err = checkRemain(buf, n, 1); err != nil {
			return
		}
		defaultConstantIVSize := int(buf[n])
		n += 1
		if err = checkRemain(buf, n, defaultConstantIVSize); err != nil {
			return
		}
		track.defaultConstantIV = make([]byte, defaultConstantIVSize)
		copy(track.defaultConstantIV, buf[n:n+defaultConstantIVSize])
	}
	return nil
}
