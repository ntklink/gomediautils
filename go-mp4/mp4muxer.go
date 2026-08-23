package mp4

import (
	"encoding/binary"
	"errors"
	"io"
)

type MP4_FLAG uint32

// ffmpeg movenc.h
const (
	MP4_FLAG_FRAGMENT MP4_FLAG = (1 << 1)
	MP4_FLAG_KEYFRAME MP4_FLAG = (1 << 3)
	MP4_FLAG_CUSTOM   MP4_FLAG = (1 << 5)
	MP4_FLAG_DASH     MP4_FLAG = (1 << 11)
)

// defaultFragmentDuration is the fragment length used for streams without a
// video track, in track timescale units (tracks default to a 1000 timescale).
const (
	defaultFragmentDuration uint32 = 1000
)

func (f MP4_FLAG) has(ff MP4_FLAG) bool {
	return (f & ff) != 0
}

func (f MP4_FLAG) isFragment() bool {
	return (f & MP4_FLAG_FRAGMENT) != 0
}

func (f MP4_FLAG) isDash() bool {
	return (f & MP4_FLAG_DASH) != 0
}

type OnFragment func(duration uint32, firstPts, firstDts uint64)
type Movmuxer struct {
	writer         io.WriteSeeker
	nextTrackId    uint32
	nextFragmentId uint32
	mdatOffset     uint32
	tracks         map[uint32]*mp4track
	movFlag        MP4_FLAG
	onNewFragment  OnFragment
	fragDuration   uint32
}

type MuxerOption func(muxer *Movmuxer)

func WithMp4Flag(f MP4_FLAG) MuxerOption {
	return func(muxer *Movmuxer) {
		muxer.movFlag |= f
	}
}

// WithFragmentDuration sets the target duration, in track timescale units, of
// a fragment for streams that carry no video track. Video streams keep
// fragmenting on key frames.
func WithFragmentDuration(duration uint32) MuxerOption {
	return func(muxer *Movmuxer) {
		muxer.fragDuration = duration
	}
}

func CreateMp4Muxer(w io.WriteSeeker, options ...MuxerOption) (*Movmuxer, error) {
	muxer := &Movmuxer{
		writer:         w,
		nextTrackId:    1,
		nextFragmentId: 0,
		tracks:         make(map[uint32]*mp4track),
		movFlag:        MP4_FLAG_KEYFRAME,
		fragDuration:   defaultFragmentDuration,
	}

	for _, opt := range options {
		opt(muxer)
	}

	if !muxer.movFlag.isFragment() && !muxer.movFlag.isDash() {
		ftyp := NewFileTypeBox()
		ftyp.Major_brand = mov_tag(isom)
		ftyp.Minor_version = 0x200
		ftyp.Compatible_brands = make([]uint32, 4)
		ftyp.Compatible_brands[0] = mov_tag(isom)
		ftyp.Compatible_brands[1] = mov_tag(iso2)
		ftyp.Compatible_brands[2] = mov_tag(avc1)
		ftyp.Compatible_brands[3] = mov_tag(mp41)
		length, boxdata := ftyp.Encode()
		_, err := muxer.writer.Write(boxdata[0:length])
		if err != nil {
			return nil, err
		}
		free := NewFreeBox()
		freelen, freeboxdata := free.Encode()
		_, err = muxer.writer.Write(freeboxdata[0:freelen])
		if err != nil {
			return nil, err
		}
		currentOffset, err := muxer.writer.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		muxer.mdatOffset = uint32(currentOffset)
		mdat := BasicBox{Type: [4]byte{'m', 'd', 'a', 't'}}
		mdat.Size = 8
		mdatlen, mdatBox := mdat.Encode()
		_, err = muxer.writer.Write(mdatBox[0:mdatlen])
		if err != nil {
			return nil, err
		}
	}
	return muxer, nil
}

type TrackOption func(track *mp4track)

func WithVideoWidth(width uint32) TrackOption {
	return func(track *mp4track) {
		track.width = width
	}
}

func WithVideoHeight(height uint32) TrackOption {
	return func(track *mp4track) {
		track.height = height
	}
}

func WithAudioChannelCount(channelCount uint8) TrackOption {
	return func(track *mp4track) {
		track.chanelCount = channelCount
	}
}

func WithAudioSampleRate(sampleRate uint32) TrackOption {
	return func(track *mp4track) {
		track.sampleRate = sampleRate
	}
}

func WithAudioSampleBits(sampleBits uint8) TrackOption {
	return func(track *mp4track) {
		track.sampleBits = sampleBits
	}
}

func WithExtraData(extraData []byte) TrackOption {
	return func(track *mp4track) {
		track.extraData = make([]byte, len(extraData))
		copy(track.extraData, extraData)
	}
}

// AddAudioTrack registers a new audio track. It reports an error, wrapping
// ErrUnsupportedCodec, for a codec id this muxer cannot write as audio; the
// codec id is validated here so that no unwritable track can ever reach the
// box writers.
func (muxer *Movmuxer) AddAudioTrack(cid MP4_CODEC_TYPE, options ...TrackOption) (uint32, error) {
	if !isAudio(cid) {
		return 0, unsupportedCodec(cid)
	}
	return muxer.addTrack(cid, options...), nil
}

// AddVideoTrack registers a new video track. It reports an error, wrapping
// ErrUnsupportedCodec, for a codec id this muxer cannot write as video.
func (muxer *Movmuxer) AddVideoTrack(cid MP4_CODEC_TYPE, options ...TrackOption) (uint32, error) {
	if !isVideo(cid) {
		return 0, unsupportedCodec(cid)
	}
	return muxer.addTrack(cid, options...), nil
}

func (muxer *Movmuxer) addTrack(cid MP4_CODEC_TYPE, options ...TrackOption) uint32 {
	var track *mp4track
	if muxer.movFlag.isDash() || muxer.movFlag.isFragment() {
		track = newmp4track(cid, newFmp4WriterSeeker(1024*1024))
	} else {
		track = newmp4track(cid, muxer.writer)
	}
	track.trackId = muxer.nextTrackId
	muxer.tracks[muxer.nextTrackId] = track
	muxer.nextTrackId++

	for _, opt := range options {
		opt(track)
	}

	return track.trackId
}

func (muxer *Movmuxer) Write(track uint32, data []byte, pts uint64, dts uint64) error {
	mp4track, found := muxer.tracks[track]
	if !found {
		return errors.New("mp4: unknown track id")
	}
	err := mp4track.writeSample(data, pts, dts)
	if err != nil {
		return err
	}

	if !muxer.movFlag.isFragment() && !muxer.movFlag.isDash() {
		return err
	}

	if isAudio(mp4track.cid) {
		if muxer.hasVideoTrack() {
			// fragments are cut on video key frames
			return nil
		}
		// audio only: without this the whole stream would be buffered until
		// WriteTrailer
		if muxer.fragDuration == 0 || mp4track.duration < muxer.fragDuration {
			return nil
		}
		if err = muxer.flushFragment(); err != nil {
			return err
		}
		if muxer.onNewFragment != nil {
			muxer.onNewFragment(mp4track.duration, mp4track.startPts, mp4track.startDts)
		}
		return nil
	}

	// isCustion := muxer.movFlag.has(MP4_FLAG_CUSTOM)
	isKeyFrag := muxer.movFlag.has(MP4_FLAG_KEYFRAME)
	if isKeyFrag {
		if mp4track.lastSample.isKey && mp4track.duration > 0 {
			err = muxer.flushFragment()
			if err != nil {
				return err
			}
			if muxer.onNewFragment != nil {
				muxer.onNewFragment(mp4track.duration, mp4track.startPts, mp4track.startDts)
			}
		}
	}

	return nil
}

func (muxer *Movmuxer) WriteTrailer() (err error) {

	for _, track := range muxer.tracks {
		if err = track.flush(); err != nil {
			return
		}
	}

	switch {
	case muxer.movFlag.isDash():
	case muxer.movFlag.isFragment():
		err = muxer.flushFragment()
		if err != nil {
			return err
		}
		hasVideo := muxer.hasVideoTrack()
		for _, track := range muxer.tracks {
			if hasVideo && isAudio(track.cid) {
				continue
			}
			if muxer.onNewFragment != nil {
				muxer.onNewFragment(track.duration, track.startPts, track.startDts)
			}
		}
		return muxer.writeMfra()
	default:
		if err = muxer.reWriteMdatSize(); err != nil {
			return err
		}
		return muxer.writeMoov(muxer.writer)
	}
	return
}

func (muxer *Movmuxer) hasVideoTrack() bool {
	for _, track := range muxer.tracks {
		if isVideo(track.cid) {
			return true
		}
	}
	return false
}

func (muxer *Movmuxer) ReBindWriter(w io.WriteSeeker) {
	muxer.writer = w
}

func (muxer *Movmuxer) OnNewFragment(onFragment OnFragment) {
	muxer.onNewFragment = onFragment
}

func (muxer *Movmuxer) WriteInitSegment(w io.Writer) error {
	ftypBox := makeFtypBox(mov_tag(iso5), 0x200, []uint32{mov_tag(iso5), mov_tag(iso6), mov_tag(mp41)})
	_, err := w.Write(ftypBox)
	if err != nil {
		return err
	}
	return muxer.writeMoov(w)
}

func (muxer *Movmuxer) reWriteMdatSize() (err error) {
	var currentOffset int64
	if currentOffset, err = muxer.writer.Seek(0, io.SeekCurrent); err != nil {
		return err
	}
	datalen := currentOffset - int64(muxer.mdatOffset)
	if datalen > 0xFFFFFFFF {
		mdat := BasicBox{Type: [4]byte{'m', 'd', 'a', 't'}}
		mdat.Size = uint64(datalen + 8)
		mdatBoxLen, mdatBox := mdat.Encode()
		if _, err = muxer.writer.Seek(int64(muxer.mdatOffset)-8, io.SeekStart); err != nil {
			return
		}
		if _, err = muxer.writer.Write(mdatBox[0:mdatBoxLen]); err != nil {
			return
		}
		if _, err = muxer.writer.Seek(currentOffset, io.SeekStart); err != nil {
			return
		}
	} else {
		if _, err = muxer.writer.Seek(int64(muxer.mdatOffset), io.SeekStart); err != nil {
			return
		}
		tmpdata := make([]byte, 4)
		binary.BigEndian.PutUint32(tmpdata, uint32(datalen))
		if _, err = muxer.writer.Write(tmpdata); err != nil {
			return
		}
		if _, err = muxer.writer.Seek(currentOffset, io.SeekStart); err != nil {
			return
		}
	}
	return
}

func (muxer *Movmuxer) writeMoov(w io.Writer) (err error) {
	var mvhd []byte
	var mvex []byte
	if muxer.movFlag.isDash() || muxer.movFlag.isFragment() {
		mvhd = makeMvhdBox(muxer.nextTrackId, 0)
		mvex = makeMvex(muxer)
	} else {
		maxdurtaion := uint32(0)
		for _, track := range muxer.tracks {
			if maxdurtaion < track.duration {
				maxdurtaion = track.duration
			}
		}
		mvhd = makeMvhdBox(muxer.nextTrackId, maxdurtaion)
	}
	moovsize := len(mvhd) + len(mvex)
	traks := make([][]byte, len(muxer.tracks))
	for i := uint32(1); i < muxer.nextTrackId; i++ {
		trak, err := makeTrak(muxer.tracks[i], muxer.movFlag)
		if err != nil {
			return err
		}
		traks[i-1] = trak
		moovsize += len(trak)
	}

	moov := BasicBox{Type: [4]byte{'m', 'o', 'o', 'v'}}
	moov.Size = 8 + uint64(moovsize)
	offset, moovBox := moov.Encode()
	copy(moovBox[offset:], mvhd)
	offset += len(mvhd)
	for _, trak := range traks {
		copy(moovBox[offset:], trak)
		offset += len(trak)
	}
	copy(moovBox[offset:], mvex)
	_, err = w.Write(moovBox)
	return
}

func (muxer *Movmuxer) writeMfra() (err error) {
	mfraSize := 0
	tfras := make([][]byte, len(muxer.tracks))
	for i := uint32(1); i < muxer.nextTrackId; i++ {
		tfras[i-1] = makeTfraBox(muxer.tracks[i])
		mfraSize += len(tfras[i-1])
	}

	mfro := makeMfroBox(uint32(mfraSize) + 16)
	mfraSize += len(mfro)
	mfra := BasicBox{Type: [4]byte{'m', 'f', 'r', 'a'}}
	mfra.Size = 8 + uint64(mfraSize)
	offset, mfraBox := mfra.Encode()
	for _, tfra := range tfras {
		copy(mfraBox[offset:], tfra)
		offset += len(tfra)
	}
	copy(mfraBox[offset:], mfro)
	_, err = muxer.writer.Write(mfraBox)
	return
}

func (muxer *Movmuxer) FlushFragment() (err error) {
	for _, track := range muxer.tracks {
		if err = track.flush(); err != nil {
			return err
		}
	}
	return muxer.flushFragment()
}

func (muxer *Movmuxer) flushFragment() (err error) {

	if muxer.movFlag.isFragment() {
		if muxer.nextFragmentId == 0 { //first fragment ,write moov
			ftypBox := makeFtypBox(mov_tag(iso5), 0x200, []uint32{mov_tag(iso5), mov_tag(iso6), mov_tag(mp41)})
			_, err := muxer.writer.Write(ftypBox)
			if err != nil {
				return err
			}
			if err := muxer.writeMoov(muxer.writer); err != nil {
				return err
			}
		}
	}

	var moofOffset int64
	if moofOffset, err = muxer.writer.Seek(0, io.SeekCurrent); err != nil {
		return err
	}
	var mdatlen uint64 = 0
	for i := uint32(1); i < muxer.nextTrackId; i++ {
		if len(muxer.tracks[i].samplelist) == 0 {
			continue
		}
		for j := 0; j < len(muxer.tracks[i].samplelist); j++ {
			muxer.tracks[i].samplelist[j].offset += mdatlen
		}
		ws := muxer.tracks[i].writer.(*fmp4WriterSeeker)
		mdatlen += uint64(len(ws.buffer))
	}
	mdatlen += 8

	// the first sample of this fragment defines the fragment start; sidx and
	// the onNewFragment callback report it, so record it before the samples
	// are handed over and cleared
	for i := uint32(1); i < muxer.nextTrackId; i++ {
		if len(muxer.tracks[i].samplelist) > 0 {
			muxer.tracks[i].startPts = muxer.tracks[i].samplelist[0].pts
			muxer.tracks[i].startDts = muxer.tracks[i].samplelist[0].dts
		}
	}

	// the size of a traf does not depend on the moof size, so build each traf
	// once with a zero base and patch the sample offsets afterwards
	moofSize := 0
	mfhd := makeMfhdBox(muxer.nextFragmentId + 1)
	moofSize += len(mfhd)
	trafs := make([][]byte, len(muxer.tracks))
	for i := uint32(1); i < muxer.nextTrackId; i++ {
		traf := makeTraf(muxer.tracks[i], uint64(moofOffset), uint64(0))
		moofSize += len(traf)
		trafs[i-1] = traf
	}

	moofSize += 8 //moof box
	for i := range trafs {
		if trafs[i] == nil {
			continue
		}
		if err = patchTrafDataOffset(trafs[i], int32(moofSize+8)); err != nil { //moofSize + 8(mdat box)
			return err
		}
	}
	muxer.nextFragmentId++

	moof := BasicBox{Type: [4]byte{'m', 'o', 'o', 'f'}}
	moof.Size = uint64(moofSize)
	offset, moofBox := moof.Encode()
	copy(moofBox[offset:], mfhd)
	offset += len(mfhd)
	for i := range trafs {
		copy(moofBox[offset:], trafs[i])
		offset += len(trafs[i])
	}

	mdat := BasicBox{Type: [4]byte{'m', 'd', 'a', 't'}}
	mdat.Size = 8
	_, mdatBox := mdat.Encode()

	if muxer.movFlag.isDash() {
		stypBox := makeStypBox(mov_tag(msdh), 0, []uint32{mov_tag(msdh), mov_tag(msix)})
		_, err := muxer.writer.Write(stypBox)
		if err != nil {
			return err
		}

		for i := uint32(1); i < muxer.nextTrackId; i++ {
			sidx := makeSidxBox(muxer.tracks[i], 52*(muxer.nextTrackId-1-i), uint32(mdatlen)+uint32(len(moofBox))+52*(muxer.nextTrackId-i-1))
			_, err := muxer.writer.Write(sidx)
			if err != nil {
				return err
			}
		}
	}

	_, err = muxer.writer.Write(moofBox)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint32(mdatBox, uint32(mdatlen))
	_, err = muxer.writer.Write(mdatBox)
	if err != nil {
		return err
	}

	for i := uint32(1); i < muxer.nextTrackId; i++ {
		if len(muxer.tracks[i].samplelist) > 0 {
			firstPts := muxer.tracks[i].samplelist[0].pts
			firstDts := muxer.tracks[i].samplelist[0].dts
			lastPts := muxer.tracks[i].samplelist[len(muxer.tracks[i].samplelist)-1].pts
			lastDts := muxer.tracks[i].samplelist[len(muxer.tracks[i].samplelist)-1].dts
			frag := movFragment{
				offset:   uint64(moofOffset),
				duration: muxer.tracks[i].duration,
				firstDts: firstDts,
				firstPts: firstPts,
				lastPts:  lastPts,
				lastDts:  lastDts,
			}
			muxer.tracks[i].fragments = append(muxer.tracks[i].fragments, frag)
		}
		ws := muxer.tracks[i].writer.(*fmp4WriterSeeker)
		_, err = muxer.writer.Write(ws.buffer)
		if err != nil {
			return err
		}
		ws.buffer = ws.buffer[:0]
		ws.offset = 0
		muxer.tracks[i].clearSamples()
	}
	return nil
}
