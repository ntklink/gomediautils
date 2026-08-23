package mp4

func makeMdiaBox(track *mp4track) ([]byte, error) {
	handlerType, err := getHandlerType(track.cid)
	if err != nil {
		return nil, err
	}
	mdhdbox := makeMdhdBox(track.mediaDuration())
	hdlrbox := makeHdlrBox(handlerType)
	minfbox, err := makeMinfBox(track)
	if err != nil {
		return nil, err
	}
	mdia := BasicBox{Type: [4]byte{'m', 'd', 'i', 'a'}}
	mdia.Size = 8 + uint64(len(mdhdbox)+len(hdlrbox)+len(minfbox))
	offset, mdiabox := mdia.Encode()
	copy(mdiabox[offset:], mdhdbox)
	offset += len(mdhdbox)
	copy(mdiabox[offset:], hdlrbox)
	offset += len(hdlrbox)
	copy(mdiabox[offset:], minfbox)
	offset += len(minfbox)
	return mdiabox, nil
}
