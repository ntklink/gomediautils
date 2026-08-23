package mp4

func decodeFrmaBox(demuxer *MovDemuxer, size uint32) (err error) {
	track := demuxer.lastTrack()
	if track == nil {
		return errNoTrack
	}
	buf, err := readBoxPayload(demuxer.reader, uint64(size), BasicBoxLen)
	if err != nil {
		return
	}
	if err = checkRemain(buf, 0, 4); err != nil {
		return
	}
	var format [4]byte
	copy(format[:], buf)

	switch mov_tag(format) {
	case mov_tag([4]byte{'a', 'v', 'c', '1'}):
		track.cid = MP4_CODEC_H264
		if track.extra == nil {
			track.extra = new(h264ExtraData)
		}
	case mov_tag([4]byte{'h', 'v', 'c', '1'}), mov_tag([4]byte{'h', 'e', 'v', '1'}):
		track.cid = MP4_CODEC_H265
		if track.extra == nil {
			track.extra = newh265ExtraData()
		}
	case mov_tag([4]byte{'m', 'p', '4', 'a'}):
		track.cid = MP4_CODEC_AAC
		if track.extra == nil {
			track.extra = new(aacExtraData)
		}
	}

	return
}
