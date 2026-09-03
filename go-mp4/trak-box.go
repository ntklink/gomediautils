package mp4

func makeTrak(track *mp4track, movflag MP4_FLAG) ([]byte, error) {

	// a fragmented moov describes no samples: its sample tables are empty and
	// so are its durations. The length of the presentation lives in mehd,
	// which is the only box a player may read without adding the fragments to
	// it a second time
	duration := track.mediaDuration()
	edts := []byte{}
	if movflag.isDash() || movflag.isFragment() {
		duration = 0
		track.makeEmptyStblTable()
	} else {
		if len(track.samplelist) > 0 {
			track.makeStblTable()
			edts = makeEdtsBox(track)
		}
	}

	tkhd := makeTkhdBox(track, duration)
	mdia, err := makeMdiaBox(track, duration)
	if err != nil {
		return nil, err
	}

	trak := BasicBox{Type: [4]byte{'t', 'r', 'a', 'k'}}
	trak.Size = 8 + uint64(len(tkhd)+len(edts)+len(mdia))
	offset, trakBox := trak.Encode()
	copy(trakBox[offset:], tkhd)
	offset += len(tkhd)
	copy(trakBox[offset:], edts)
	offset += len(edts)
	copy(trakBox[offset:], mdia)
	return trakBox, nil
}
