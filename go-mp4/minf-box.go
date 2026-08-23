package mp4

func makeMinfBox(track *mp4track) ([]byte, error) {
	var mhdbox []byte
	switch {
	case isVideo(track.cid):
		mhdbox = makeVmhdBox()
	case isAudio(track.cid):
		mhdbox = makeSmhdBox()
	default:
		return nil, unsupportedCodec(track.cid)
	}
	dinfbox := makeDefaultDinfBox()
	stblbox, err := makeStblBox(track)
	if err != nil {
		return nil, err
	}

	minf := BasicBox{Type: [4]byte{'m', 'i', 'n', 'f'}}
	minf.Size = 8 + uint64(len(mhdbox)+len(dinfbox)+len(stblbox))
	offset, minfbox := minf.Encode()
	copy(minfbox[offset:], mhdbox)
	offset += len(mhdbox)
	copy(minfbox[offset:], dinfbox)
	offset += len(dinfbox)
	copy(minfbox[offset:], stblbox)
	offset += len(stblbox)
	return minfbox, nil
}
