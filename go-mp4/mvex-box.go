package mp4

func makeMvex(muxer *Movmuxer) []byte {
	// mehd comes first and is the only box that states the length of the
	// whole presentation, fragments included. A plain fragmented file always
	// carries one: the hint fills it in, and WriteTrailer writes the measured
	// length over it when the writer seeks. It stays zero only for a file
	// that is neither hinted nor seekable, a live stream, whose length is
	// genuinely unknown while the head goes out. Dash keeps it out, a segment
	// carries its own sidx.
	var mehd []byte
	if muxer.movFlag.isFragment() && !muxer.movFlag.isDash() {
		mehd = makeMehdBox(muxer.durationHint, muxer.mehdVersion0)
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
