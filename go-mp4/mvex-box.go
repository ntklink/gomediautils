package mp4

func makeMvex(muxer *Movmuxer) []byte {
	// mehd comes first and is the only box that states the length of the
	// whole presentation, fragments included; without a hint the length is
	// not known here and the box is left out, so a player works it out by
	// walking the fragments
	var mehd []byte
	if muxer.durationHint > 0 {
		mehd = makeMehdBox(muxer.durationHint)
	}
	trexs := make([]byte, 0, 64)
	for i := uint32(1); i < muxer.nextTrackId; i++ {
		trex := NewTrackExtendsBox(muxer.tracks[i].trackId)
		trex.DefaultSampleDescriptionIndex = 1
		_, boxData := trex.Encode()
		trexs = append(trexs, boxData...)
	}
	mvex := BasicBox{Type: [4]byte{'m', 'v', 'e', 'x'}}
	mvex.Size = 8 + uint64(len(mehd)) + uint64(len(trexs))
	offset, mvexBox := mvex.Encode()
	copy(mvexBox[offset:], mehd)
	offset += len(mehd)
	copy(mvexBox[offset:], trexs)
	return mvexBox
}
