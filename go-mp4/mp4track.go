package mp4

import (
	"errors"
	"io"

	"github.com/ntklink/gomediautils/go-codec"
)

type sampleCache struct {
	pts    uint64
	dts    uint64
	hasVcl bool
	isKey  bool
	cache  []byte
}

type sampleEntry struct {
	pts                    uint64
	dts                    uint64
	offset                 uint64
	size                   uint64
	isKeyFrame             bool
	SampleDescriptionIndex uint32 //always should be 1
}

type movchunk struct {
	chunknum    uint32
	samplenum   uint32
	chunkoffset uint64
}

// extraData holds the codec configuration of a track (the avcC/hvcC record or
// the AAC AudioSpecificConfig). Both directions can fail on malformed input,
// so both report an error.
type extraData interface {
	export() ([]byte, error)
	load(data []byte) error
}

type h264ExtraData struct {
	spss [][]byte
	ppss [][]byte
}

func (extra *h264ExtraData) export() ([]byte, error) {
	return codec.CreateH264AVCCExtradata(extra.spss, extra.ppss)
}

func (extra *h264ExtraData) load(data []byte) error {
	spss, ppss, err := codec.CovertExtradata(data)
	if err != nil {
		return err
	}
	extra.spss, extra.ppss = spss, ppss
	return nil
}

type h265ExtraData struct {
	hvccExtra *codec.HEVCRecordConfiguration
}

func newh265ExtraData() *h265ExtraData {
	return &h265ExtraData{
		hvccExtra: codec.NewHEVCRecordConfiguration(),
	}
}

func (extra *h265ExtraData) export() ([]byte, error) {
	if extra.hvccExtra == nil {
		return nil, errors.New("mp4: hevc record configuration is not initialised")
	}
	return extra.hvccExtra.Encode()
}

func (extra *h265ExtraData) load(data []byte) error {
	if extra.hvccExtra == nil {
		return errors.New("mp4: hevc record configuration is not initialised")
	}
	return extra.hvccExtra.Decode(data)
}

type aacExtraData struct {
	asc []byte
}

func (extra *aacExtraData) export() ([]byte, error) {
	return extra.asc, nil
}

func (extra *aacExtraData) load(data []byte) error {
	extra.asc = make([]byte, len(data))
	copy(extra.asc, data)
	return nil
}

type movFragment struct {
	offset   uint64
	duration uint32
	firstDts uint64
	firstPts uint64
	lastPts  uint64
	lastDts  uint64
}

type mp4track struct {
	cid         MP4_CODEC_TYPE
	trackId     uint32
	stbltable   *movstbl
	duration    uint32
	timescale   uint32
	width       uint32
	height      uint32
	sampleRate  uint32
	sampleBits  uint8
	chanelCount uint8
	samplelist  []sampleEntry
	elst        *movelst
	extra       extraData
	lastSample  *sampleCache
	writer      io.WriteSeeker
	fragments   []movFragment
	// paramSetCache holds sps/pps/vps seen in a sample that carried no slice,
	// so they can be prepended to the next key frame while demuxing
	paramSetCache []byte

	//for fmp4
	extraData          []byte
	startDts           uint64
	startPts           uint64
	defaultSize        uint32
	defaultDuration    uint32
	defaultSampleFlags uint32
	baseDataOffset     uint64

	//for subsample
	defaultIsProtected     uint8
	defaultPerSampleIVSize uint8
	defaultCryptByteBlock  uint8
	defaultSkipByteBlock   uint8
	defaultConstantIV      []byte
	defaultKID             [16]byte
	lastSeig               *SeigSampleGroupEntry
	lastSaiz               *SaizBox
	subSamples             []sencEntry
}

func newmp4track(cid MP4_CODEC_TYPE, writer io.WriteSeeker) *mp4track {
	track := &mp4track{
		cid:        cid,
		timescale:  1000,
		stbltable:  nil,
		samplelist: make([]sampleEntry, 0),
		lastSample: &sampleCache{
			hasVcl: false,
			cache:  make([]byte, 0, 128),
		},
		writer:    writer,
		fragments: make([]movFragment, 0, 32),
		startDts:  0,
	}

	if cid == MP4_CODEC_H264 {
		track.extra = new(h264ExtraData)
	} else if cid == MP4_CODEC_H265 {
		track.extra = newh265ExtraData()
	} else if cid == MP4_CODEC_AAC {
		track.extra = new(aacExtraData)
	}
	return track
}

// addSampleEntry appends a sample and keeps track.duration in step with it.
// duration is the span from the first sample of the current run to this one,
// so every interval including the very first has to be accumulated.
func (track *mp4track) addSampleEntry(entry sampleEntry) {
	if len(track.samplelist) == 0 {
		track.duration = 0
	} else {
		delta := int64(entry.dts) - int64(track.samplelist[len(track.samplelist)-1].dts)
		if delta < 0 {
			track.duration += 1
		} else {
			track.duration += uint32(delta)
		}
	}
	track.samplelist = append(track.samplelist, entry)
}

func (track *mp4track) makeStblTable() {
	if len(track.samplelist) == 0 {
		track.makeEmptyStblTable()
		return
	}
	if track.stbltable == nil {
		track.stbltable = new(movstbl)
	}
	sameSize := true
	stts := new(movstts)
	stts.entrys = make([]sttsEntry, 0)
	movchunks := make([]movchunk, 0)
	ctts := new(movctts)
	ctts.entrys = make([]cttsEntry, 0)
	ckn := uint32(0)
	for i, sample := range track.samplelist {
		sttsEntry := sttsEntry{sampleCount: 1, sampleDelta: track.lastSampleDelta()}
		// the composition offset is signed; a stream whose pts is reordered
		// ahead of its dts needs the version 1 box to express it
		offset := int64(sample.pts) - int64(sample.dts)
		if offset < 0 {
			ctts.version = 1
		}
		cttsEntry := cttsEntry{sampleCount: 1, sampleOffset: uint32(int32(offset))}
		if i == len(track.samplelist)-1 {
			stts.entrys = append(stts.entrys, sttsEntry)
			stts.entryCount++
		} else {
			var delta uint64 = 1
			if track.samplelist[i+1].dts >= sample.dts {
				delta = track.samplelist[i+1].dts - sample.dts
			}

			if len(stts.entrys) > 0 && delta == uint64(stts.entrys[len(stts.entrys)-1].sampleDelta) {
				stts.entrys[len(stts.entrys)-1].sampleCount++
			} else {
				sttsEntry.sampleDelta = uint32(delta)
				stts.entrys = append(stts.entrys, sttsEntry)
				stts.entryCount++
			}
		}

		if len(ctts.entrys) == 0 {
			ctts.entrys = append(ctts.entrys, cttsEntry)
			ctts.entryCount++
		} else {
			if ctts.entrys[len(ctts.entrys)-1].sampleOffset == cttsEntry.sampleOffset {
				ctts.entrys[len(ctts.entrys)-1].sampleCount++
			} else {
				ctts.entrys = append(ctts.entrys, cttsEntry)
				ctts.entryCount++
			}
		}
		if sameSize && i < len(track.samplelist)-1 && track.samplelist[i+1].size != track.samplelist[i].size {
			sameSize = false
		}
		if i > 0 && sample.offset == track.samplelist[i-1].offset+track.samplelist[i-1].size {
			movchunks[ckn-1].samplenum++
		} else {
			ck := movchunk{chunknum: ckn, samplenum: 1, chunkoffset: sample.offset}
			movchunks = append(movchunks, ck)
			ckn++
		}
	}
	stsz := &movstsz{
		sampleSize:  0,
		sampleCount: uint32(len(track.samplelist)),
	}
	// sample_size == 0 is the sentinel that says the per sample sizes follow
	// in a table, so a run of samples that are all zero bytes long cannot use
	// the uniform form
	if sameSize && track.samplelist[0].size == 0 {
		sameSize = false
	}
	if sameSize {
		stsz.sampleSize = uint32(track.samplelist[0].size)
	} else {
		stsz.entrySizelist = make([]uint32, stsz.sampleCount)
		for i := 0; i < len(stsz.entrySizelist); i++ {
			stsz.entrySizelist[i] = uint32(track.samplelist[i].size)
		}
	}

	stsc := &movstsc{
		entrys:     make([]stscEntry, len(movchunks)),
		entryCount: 0,
	}
	for i, chunk := range movchunks {
		if i == 0 || chunk.samplenum != movchunks[i-1].samplenum {
			stsc.entrys[stsc.entryCount].firstChunk = chunk.chunknum + 1
			stsc.entrys[stsc.entryCount].sampleDescriptionIndex = 1
			stsc.entrys[stsc.entryCount].samplesPerChunk = chunk.samplenum
			stsc.entryCount++
		}
	}
	stco := &movstco{entryCount: ckn, chunkOffsetlist: make([]uint64, ckn)}
	for i := 0; i < int(stco.entryCount); i++ {
		stco.chunkOffsetlist[i] = movchunks[i].chunkoffset
	}
	track.stbltable.stts = stts
	track.stbltable.stsc = stsc
	track.stbltable.stco = stco
	track.stbltable.stsz = stsz
	if track.cid == MP4_CODEC_H264 || track.cid == MP4_CODEC_H265 {
		track.stbltable.ctts = ctts
	}
}

func (track *mp4track) makeEmptyStblTable() {
	track.stbltable = new(movstbl)
	track.stbltable.stts = &movstts{}
	track.stbltable.stsc = &movstsc{}
	track.stbltable.stco = &movstco{}
	track.stbltable.stsz = &movstsz{}
	track.stbltable.stss = &movstss{}
}

func (track *mp4track) writeSample(sample []byte, pts, dts uint64) (err error) {
	switch track.cid {
	case MP4_CODEC_H264:
		err = track.writeH264(sample, pts, dts)
	case MP4_CODEC_H265:
		err = track.writeH265(sample, pts, dts)
	case MP4_CODEC_AAC:
		err = track.writeAAC(sample, pts, dts)
	case MP4_CODEC_G711A, MP4_CODEC_G711U:
		err = track.writeG711(sample, pts, dts)
	case MP4_CODEC_MP2, MP4_CODEC_MP3:
		err = track.writeMP3(sample, pts, dts)
	case MP4_CODEC_OPUS:
		err = track.writeOPUS(sample, pts, dts)
	}
	return err
}

func (track *mp4track) writeH264(h264 []byte, pts, dts uint64) (err error) {
	h264extra, ok := track.extra.(*h264ExtraData)
	if !ok {
		return errors.New("mp4: h264 track has no h264 codec configuration")
	}
	codec.SplitFrameWithStartCode(h264, func(nalu []byte) bool {
		nalu_type := codec.H264NaluType(nalu)
		switch nalu_type {
		case codec.H264_NAL_SPS:
			spsid := codec.GetSPSIdWithStartCode(nalu)
			for _, sps := range h264extra.spss {
				if spsid == codec.GetSPSIdWithStartCode(sps) {
					return true
				}
			}
			tmp := make([]byte, len(nalu))
			copy(tmp, nalu)
			h264extra.spss = append(h264extra.spss, tmp)
			if track.width == 0 || track.height == 0 {
				width, height, e := codec.GetH264Resolution(h264extra.spss[0])
				if e != nil {
					err = e
					return false
				}
				if track.width == 0 {
					track.width = width
				}
				if track.height == 0 {
					track.height = height
				}
			}
		case codec.H264_NAL_PPS:
			ppsid := codec.GetPPSIdWithStartCode(nalu)
			for _, pps := range h264extra.ppss {
				if ppsid == codec.GetPPSIdWithStartCode(pps) {
					return true
				}
			}
			tmp := make([]byte, len(nalu))
			copy(tmp, nalu)
			h264extra.ppss = append(h264extra.ppss, tmp)
		}
		//aud/sps/pps/sei 为帧间隔
		//通过first_slice_in_mb来判断，改nalu是否为一帧的开头
		if track.lastSample.hasVcl && isH264NewAccessUnit(nalu) {
			var currentOffset int64
			if currentOffset, err = track.writer.Seek(0, io.SeekCurrent); err != nil {
				return false
			}
			entry := sampleEntry{
				pts:                    track.lastSample.pts,
				dts:                    track.lastSample.dts,
				size:                   0,
				isKeyFrame:             track.lastSample.isKey,
				SampleDescriptionIndex: 1,
				offset:                 uint64(currentOffset),
			}
			n := 0
			if n, err = track.writer.Write(track.lastSample.cache); err != nil {
				return false
			}
			entry.size = uint64(n)
			track.addSampleEntry(entry)
			track.lastSample.cache = track.lastSample.cache[:0]
			track.lastSample.hasVcl = false
		}
		if codec.IsH264VCLNaluType(nalu_type) {
			track.lastSample.pts = pts
			track.lastSample.dts = dts
			track.lastSample.hasVcl = true
			track.lastSample.isKey = false
			if nalu_type == codec.H264_NAL_I_SLICE {
				track.lastSample.isKey = true
			}
		}
		track.lastSample.cache = append(track.lastSample.cache, codec.ConvertAnnexBToAVCC(nalu)...)
		return true
	})
	return
}

func (track *mp4track) writeH265(h265 []byte, pts, dts uint64) (err error) {
	h265extra, ok := track.extra.(*h265ExtraData)
	if !ok {
		return errors.New("mp4: h265 track has no h265 codec configuration")
	}
	codec.SplitFrameWithStartCode(h265, func(nalu []byte) bool {
		nalu_type := codec.H265NaluType(nalu)
		switch nalu_type {
		case codec.H265_NAL_SPS:
			if err = h265extra.hvccExtra.UpdateSPS(nalu); err != nil {
				return false
			}
			if track.width == 0 || track.height == 0 {
				width, height, e := codec.GetH265Resolution(nalu)
				if e != nil {
					err = e
					return false
				}
				if track.width == 0 {
					track.width = width
				}
				if track.height == 0 {
					track.height = height
				}
			}
		case codec.H265_NAL_PPS:
			if err = h265extra.hvccExtra.UpdatePPS(nalu); err != nil {
				return false
			}
		case codec.H265_NAL_VPS:
			if err = h265extra.hvccExtra.UpdateVPS(nalu); err != nil {
				return false
			}
		}

		if track.lastSample.hasVcl && isH265NewAccessUnit(nalu) {
			var currentOffset int64
			if currentOffset, err = track.writer.Seek(0, io.SeekCurrent); err != nil {
				return false
			}
			entry := sampleEntry{
				pts:                    track.lastSample.pts,
				dts:                    track.lastSample.dts,
				size:                   0,
				isKeyFrame:             track.lastSample.isKey,
				SampleDescriptionIndex: 1,
				offset:                 uint64(currentOffset),
			}
			n := 0
			if n, err = track.writer.Write(track.lastSample.cache); err != nil {
				return false
			}
			entry.size = uint64(n)
			track.addSampleEntry(entry)
			track.lastSample.cache = track.lastSample.cache[:0]
			track.lastSample.hasVcl = false
		}
		if codec.IsH265VCLNaluType(nalu_type) {
			track.lastSample.pts = pts
			track.lastSample.dts = dts
			track.lastSample.hasVcl = true
			track.lastSample.isKey = false
			if nalu_type >= codec.H265_NAL_SLICE_BLA_W_LP && nalu_type <= codec.H265_NAL_SLICE_CRA {
				track.lastSample.isKey = true
			}
		}
		track.lastSample.cache = append(track.lastSample.cache, codec.ConvertAnnexBToAVCC(nalu)...)
		return true
	})
	return
}

func (track *mp4track) writeAAC(aacframes []byte, pts, dts uint64) (err error) {
	aacextra, ok := track.extra.(*aacExtraData)
	if !ok {
		return errors.New("must init aacExtraData first")
	}
	if len(aacextra.asc) <= 0 {
		asc, err := codec.ConvertADTSToASC(aacframes)
		if err != nil {
			return err
		}
		aacextra.asc = asc.Encode()

		if track.chanelCount == 0 {
			track.chanelCount = asc.Channel_configuration
		}
		if track.sampleRate == 0 {
			track.sampleRate = uint32(codec.AACSampleIdxToSample(int(asc.Sample_freq_index)))
		}
		if track.sampleBits == 0 {
			// aac has no fixed bit depth, so we just set it to the default of 16
			// see AudioSampleEntry (stsd-box) and https://superuser.com/a/1173507
			track.sampleBits = 16
		}
	}

	var currentOffset int64
	if currentOffset, err = track.writer.Seek(0, io.SeekCurrent); err != nil {
		return
	}
	// aacframes may hold several aac frames: a ts or ps demuxer hands over a
	// whole PES payload, which usually carries a handful of them under one
	// timestamp. mp4 needs one sample per frame, each with its own decode
	// time, so the timestamp is advanced by the duration of a frame. Giving
	// them all the same timestamp produces a file whose audio dts does not
	// increase, which players reject.
	var writeErr error
	frameIndex := 0
	splitErr := codec.SplitAACFrame(aacframes, func(aac []byte) {
		if writeErr != nil {
			return
		}
		offsetMs := track.aacFrameOffsetMs(frameIndex)
		frameIndex++
		n := 0
		if n, writeErr = track.writer.Write(aac[7:]); writeErr != nil {
			return
		}
		track.addSampleEntry(sampleEntry{
			pts:                    pts + offsetMs,
			dts:                    dts + offsetMs,
			size:                   uint64(n),
			SampleDescriptionIndex: 1,
			offset:                 uint64(currentOffset),
		})
		currentOffset += int64(n)
	})
	if writeErr != nil {
		return writeErr
	}
	return splitErr
}

// aacSamplesPerFrame is the number of samples an aac frame decodes to. Only
// the 1024 sample variants are muxed here.
const aacSamplesPerFrame = 1024

// aacFrameOffsetMs is how far frame i of a multi frame aac payload sits after
// the first one. It is computed from the index rather than accumulated so
// that millisecond rounding does not drift over a long file.
func (track *mp4track) aacFrameOffsetMs(i int) uint64 {
	if i == 0 || track.sampleRate == 0 {
		return 0
	}
	return uint64(i) * aacSamplesPerFrame * 1000 / uint64(track.sampleRate)
}

func (track *mp4track) writeG711(g711 []byte, pts, dts uint64) (err error) {
	if len(g711) == 0 {
		// a zero byte sample carries nothing and cannot be described by stsz
		return nil
	}
	var currentOffset int64
	if currentOffset, err = track.writer.Seek(0, io.SeekCurrent); err != nil {
		return
	}
	n := 0
	if n, err = track.writer.Write(g711); err != nil {
		return err
	}
	track.addSampleEntry(sampleEntry{
		pts:                    pts,
		dts:                    dts,
		size:                   uint64(n),
		SampleDescriptionIndex: 1,
		offset:                 uint64(currentOffset),
	})
	return nil
}

func (track *mp4track) writeMP3(mp3 []byte, pts, dts uint64) (err error) {
	if track.sampleRate == 0 {
		if err = codec.SplitMp3Frames(mp3, func(head *codec.MP3FrameHead, frame []byte) {
			track.sampleRate = uint32(head.GetSampleRate())
			track.chanelCount = uint8(head.GetChannelCount())
			track.sampleBits = 16
		}); err != nil {
			return err
		}
		if track.sampleRate > 24000 {
			track.cid = MP4_CODEC_MP2
		} else {
			track.cid = MP4_CODEC_MP3
		}
	}
	return track.writeG711(mp3, pts, dts)
}

func (track *mp4track) writeOPUS(opus []byte, pts, dts uint64) (err error) {
	if track.sampleRate == 0 {
		opusPacket := codec.DecodeOpusPacket(opus)
		track.sampleRate = 48000 // TODO: fixed?
		if opusPacket.Stereo != 0 {
			track.chanelCount = 2
		} else {
			track.chanelCount = 1
		}
		track.sampleBits = 16 // TODO: fixed
	}

	return track.writeG711(opus, pts, dts)
}

func (track *mp4track) flush() (err error) {
	var currentOffset int64
	if track.lastSample != nil && len(track.lastSample.cache) > 0 {
		if currentOffset, err = track.writer.Seek(0, io.SeekCurrent); err != nil {
			return err
		}
		entry := sampleEntry{
			pts:                    track.lastSample.pts,
			dts:                    track.lastSample.dts,
			isKeyFrame:             track.lastSample.isKey,
			size:                   0,
			SampleDescriptionIndex: 1,
			offset:                 uint64(currentOffset),
		}
		n := 0
		if n, err = track.writer.Write(track.lastSample.cache); err != nil {
			return err
		}
		entry.size = uint64(n)
		track.addSampleEntry(entry)
		track.lastSample.cache = track.lastSample.cache[:0]
		track.lastSample.hasVcl = false
		track.lastSample.isKey = false
		track.lastSample.dts = 0
		track.lastSample.pts = 0
	}
	return nil
}

// lastSampleDelta is the duration to give the final sample, which has no
// successor to measure against. The one before it is the best estimate; a
// single tick, which is what a placeholder would give, makes the track end
// one frame early and players drop that frame.
func (track *mp4track) lastSampleDelta() uint32 {
	n := len(track.samplelist)
	if n < 2 {
		return 1
	}
	delta := int64(track.samplelist[n-1].dts) - int64(track.samplelist[n-2].dts)
	if delta <= 0 {
		return 1
	}
	return uint32(delta)
}

// mediaDuration is the whole length of the track in media timescale units:
// the span between the first and last decode time plus the duration of the
// last sample. track.duration only covers the span, so using it as the
// duration of the media cuts the last sample off.
func (track *mp4track) mediaDuration() uint32 {
	if len(track.samplelist) == 0 {
		return track.duration
	}
	return track.duration + track.lastSampleDelta()
}

// syncSampleAtOrBefore returns the index of the last sync sample at or before
// idx. Without a stss box every sample is a sync sample, so idx is returned
// unchanged.
func (track *mp4track) syncSampleAtOrBefore(idx int) int {
	if track.stbltable == nil || track.stbltable.stss == nil || len(track.stbltable.stss.sampleNumber) == 0 {
		return idx
	}
	best := -1
	for _, num := range track.stbltable.stss.sampleNumber {
		if num == 0 {
			continue
		}
		if int(num-1) <= idx && int(num-1) > best {
			best = int(num - 1)
		}
	}
	if best < 0 {
		return idx
	}
	return best
}

func (track *mp4track) clearSamples() {
	track.samplelist = track.samplelist[:0]
}
