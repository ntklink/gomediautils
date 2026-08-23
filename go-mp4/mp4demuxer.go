package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/yapingcat/gomedia/go-codec"
)

type AVPacket struct {
	Cid     MP4_CODEC_TYPE
	Data    []byte
	TrackId int
	Pts     uint64
	Dts     uint64
}

type SyncSample struct {
	Pts    uint64
	Dts    uint64
	Size   uint32
	Offset uint32
}

type SubSample struct {
	KID            [16]byte
	IV             [16]byte
	Patterns       []SubSamplePattern
	Number         uint32
	CryptByteBlock uint8
	SkipByteBlock  uint8
	PsshBoxes      []PsshBox
}

type SubSamplePattern struct {
	BytesClear     uint16
	BytesProtected uint32
}

type TrackInfo struct {
	Duration     uint32
	TrackId      int
	Cid          MP4_CODEC_TYPE
	Height       uint32
	Width        uint32
	SampleRate   uint32
	SampleSize   uint16
	SampleCount  uint32
	ChannelCount uint8
	Timescale    uint32
	StartDts     uint64
	EndDts       uint64
}

type Mp4Info struct {
	MajorBrand       uint32
	MinorVersion     uint32
	CompatibleBrands []uint32
	Duration         uint32
	Timescale        uint32
	CreateTime       uint64
	ModifyTime       uint64
}

type MovDemuxer struct {
	reader        io.ReadSeeker
	mdatOffset    []uint64 //一个mp4文件可能存在多个mdatbox
	tracks        []*mp4track
	readSampleIdx []uint32
	mp4out        []byte
	mp4Info       Mp4Info

	//for demux fmp4
	isFragement  bool
	currentTrack *mp4track
	pssh         []PsshBox
	moofOffset   int64
	dataOffset   uint32

	OnRawSample func(cid MP4_CODEC_TYPE, sample []byte, subSample *SubSample) error
}

// how to demux mp4 file
// 1. CreateMovDemuxer
// 2. ReadHead()
// 3. ReadPacket

func CreateMp4Demuxer(r io.ReadSeeker) *MovDemuxer {
	return &MovDemuxer{
		reader: r,
	}
}

// lastTrack returns the track currently being parsed (the last trak box
// seen), or nil when no trak box has been encountered yet.
func (demuxer *MovDemuxer) lastTrack() *mp4track {
	if len(demuxer.tracks) == 0 {
		return nil
	}
	return demuxer.tracks[len(demuxer.tracks)-1]
}

// lastStbl returns the current track, ensuring that its sample table
// container has been created (i.e. an stbl box was seen).
func (demuxer *MovDemuxer) lastStbl() (*mp4track, error) {
	track := demuxer.lastTrack()
	if track == nil {
		return nil, errNoTrack
	}
	if track.stbltable == nil {
		return nil, errors.New("mp4: sample table box found outside of an stbl box")
	}
	return track, nil
}

// remainingFileSize returns the number of bytes between the current read
// position and the end of the stream without changing the position.
func (demuxer *MovDemuxer) remainingFileSize() (int64, int64, error) {
	cur, err := demuxer.reader.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, 0, err
	}
	end, err := demuxer.reader.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, 0, err
	}
	if _, err = demuxer.reader.Seek(cur, io.SeekStart); err != nil {
		return 0, 0, err
	}
	return cur, end - cur, nil
}

func (demuxer *MovDemuxer) ReadHead() ([]TrackInfo, error) {
	infos := make([]TrackInfo, 0, 2)
	var err error
	for {
		fullbox := FullBox{}
		basebox := BasicBox{}
		var hdrLen int
		hdrLen, err = basebox.Decode(demuxer.reader)
		if err != nil {
			break
		}
		if basebox.Size == 0 {
			// box extends to the end of the file
			var remain int64
			if _, remain, err = demuxer.remainingFileSize(); err != nil {
				break
			}
			basebox.Size = uint64(remain) + uint64(hdrLen)
		}
		if basebox.Size < uint64(hdrLen) {
			err = errors.New("mp4 Parser error")
			break
		}
		payloadLen := basebox.Size - uint64(hdrLen)
		boxType := mov_tag(basebox.Type)
		isMdat := boxType == mov_tag([4]byte{'m', 'd', 'a', 't'})
		isMoov := boxType == mov_tag([4]byte{'m', 'o', 'o', 'v'})
		if !isMdat && !isMoov && basebox.Size > 0xFFFFFFFF {
			err = errors.New("mp4: unexpected large box size")
			break
		}
		size32 := uint32(basebox.Size)

		// Boxes that live inside a trak box need a track to attach to.
		var track *mp4track
		needTrack := false
		switch boxType {
		case mov_tag([4]byte{'t', 'k', 'h', 'd'}), mov_tag([4]byte{'m', 'd', 'h', 'd'}),
			mov_tag([4]byte{'s', 't', 'b', 'l'}), mov_tag([4]byte{'a', 'v', 'c', '1'}),
			mov_tag([4]byte{'h', 'v', 'c', '1'}), mov_tag([4]byte{'h', 'e', 'v', '1'}),
			mov_tag([4]byte{'m', 'p', '4', 'a'}), mov_tag([4]byte{'u', 'l', 'a', 'w'}),
			mov_tag([4]byte{'a', 'l', 'a', 'w'}), mov_tag([4]byte{'o', 'p', 'u', 's'}):
			needTrack = true
		}
		if needTrack {
			if track = demuxer.lastTrack(); track == nil {
				err = errNoTrack
				break
			}
		}

		switch boxType {
		case mov_tag([4]byte{'f', 't', 'y', 'p'}):
			err = decodeFtypBox(demuxer, size32)
		case mov_tag([4]byte{'f', 'r', 'e', 'e'}):
			err = decodeFreeBox(demuxer, size32)
		case mov_tag([4]byte{'m', 'd', 'a', 't'}):
			var currentOffset int64
			if currentOffset, err = demuxer.reader.Seek(0, io.SeekCurrent); err != nil {
				break
			}
			demuxer.mdatOffset = append(demuxer.mdatOffset, uint64(currentOffset))
			_, err = demuxer.reader.Seek(int64(payloadLen), io.SeekCurrent)
		case mov_tag([4]byte{'m', 'o', 'o', 'v'}):
			var remain int64
			if _, remain, err = demuxer.remainingFileSize(); err != nil {
				break
			}
			if uint64(remain) < payloadLen {
				err = errors.New("incomplete mp4 file")
				break
			}
		case mov_tag([4]byte{'m', 'v', 'h', 'd'}):
			err = decodeMvhd(demuxer)
		case mov_tag([4]byte{'p', 's', 's', 'h'}):
			err = decodePsshBox(demuxer, size32)
		case mov_tag([4]byte{'t', 'r', 'a', 'k'}):
			track := &mp4track{}
			demuxer.tracks = append(demuxer.tracks, track)
		case mov_tag([4]byte{'t', 'k', 'h', 'd'}):
			err = decodeTkhdBox(demuxer)
		case mov_tag([4]byte{'m', 'd', 'h', 'd'}):
			err = decodeMdhdBox(demuxer)
		case mov_tag([4]byte{'h', 'd', 'l', 'r'}):
			err = decodeHdlrBox(demuxer, basebox.Size)
		case mov_tag([4]byte{'m', 'd', 'i', 'a'}):
		case mov_tag([4]byte{'m', 'i', 'n', 'f'}):
		case mov_tag([4]byte{'v', 'm', 'h', 'd'}):
			err = decodeVmhdBox(demuxer)
		case mov_tag([4]byte{'s', 'm', 'h', 'd'}):
			err = decodeSmhdBox(demuxer)
		case mov_tag([4]byte{'h', 'm', 'h', 'd'}):
			_, err = fullbox.Decode(demuxer.reader)
		case mov_tag([4]byte{'n', 'm', 'h', 'd'}):
			_, err = fullbox.Decode(demuxer.reader)
		case mov_tag([4]byte{'s', 't', 'b', 'l'}):
			track.stbltable = new(movstbl)
		case mov_tag([4]byte{'s', 't', 's', 'd'}):
			err = decodeStsdBox(demuxer)
		case mov_tag([4]byte{'s', 't', 't', 's'}):
			err = decodeSttsBox(demuxer, basebox.Size)
		case mov_tag([4]byte{'c', 't', 't', 's'}):
			err = decodeCttsBox(demuxer, basebox.Size)
		case mov_tag([4]byte{'s', 't', 's', 'c'}):
			err = decodeStscBox(demuxer, basebox.Size)
		case mov_tag([4]byte{'s', 't', 's', 'z'}):
			err = decodeStszBox(demuxer, basebox.Size)
		case mov_tag([4]byte{'s', 't', 'c', 'o'}):
			err = decodeStcoBox(demuxer, basebox.Size)
		case mov_tag([4]byte{'c', 'o', '6', '4'}):
			err = decodeCo64Box(demuxer, basebox.Size)
		case mov_tag([4]byte{'s', 't', 's', 's'}):
			err = decodeStssBox(demuxer, basebox.Size)
		case mov_tag([4]byte{'e', 'n', 'c', 'v'}):
			err = decodeVisualSampleEntry(demuxer)
		case mov_tag([4]byte{'s', 'i', 'n', 'f'}):
		case mov_tag([4]byte{'f', 'r', 'm', 'a'}):
			err = decodeFrmaBox(demuxer, size32)
		case mov_tag([4]byte{'s', 'c', 'h', 'i'}):
		case mov_tag([4]byte{'t', 'e', 'n', 'c'}):
			err = decodeTencBox(demuxer, size32)
		case mov_tag([4]byte{'a', 'v', 'c', '1'}):
			track.cid = MP4_CODEC_H264
			track.extra = new(h264ExtraData)
			err = decodeVisualSampleEntry(demuxer)
		case mov_tag([4]byte{'h', 'v', 'c', '1'}), mov_tag([4]byte{'h', 'e', 'v', '1'}):
			track.cid = MP4_CODEC_H265
			track.extra = newh265ExtraData()
			err = decodeVisualSampleEntry(demuxer)
		case mov_tag([4]byte{'e', 'n', 'c', 'a'}):
			err = decodeAudioSampleEntry(demuxer)
		case mov_tag([4]byte{'m', 'p', '4', 'a'}):
			track.cid = MP4_CODEC_AAC
			track.extra = new(aacExtraData)
			err = decodeAudioSampleEntry(demuxer)
		case mov_tag([4]byte{'u', 'l', 'a', 'w'}):
			track.cid = MP4_CODEC_G711U
			err = decodeAudioSampleEntry(demuxer)
		case mov_tag([4]byte{'a', 'l', 'a', 'w'}):
			track.cid = MP4_CODEC_G711A
			err = decodeAudioSampleEntry(demuxer)
		case mov_tag([4]byte{'o', 'p', 'u', 's'}):
			track.cid = MP4_CODEC_OPUS
		case mov_tag([4]byte{'a', 'v', 'c', 'C'}):
			err = decodeAvccBox(demuxer, size32)
		case mov_tag([4]byte{'h', 'v', 'c', 'C'}):
			err = decodeHvccBox(demuxer, size32)
		case mov_tag([4]byte{'e', 's', 'd', 's'}):
			err = decodeEsdsBox(demuxer, size32)
		case mov_tag([4]byte{'e', 'd', 't', 's'}):
		case mov_tag([4]byte{'e', 'l', 's', 't'}):
			err = decodeElstBox(demuxer, basebox.Size)
		case mov_tag([4]byte{'m', 'v', 'e', 'x'}):
			demuxer.isFragement = true
		case mov_tag([4]byte{'m', 'o', 'o', 'f'}):
			if demuxer.moofOffset, err = demuxer.reader.Seek(0, io.SeekCurrent); err != nil {
				break
			}
			demuxer.moofOffset -= int64(hdrLen)
			demuxer.dataOffset = size32 + 8
		case mov_tag([4]byte{'m', 'f', 'h', 'd'}):
			err = decodeMfhdBox(demuxer)
		case mov_tag([4]byte{'t', 'r', 'a', 'f'}):
		case mov_tag([4]byte{'t', 'f', 'h', 'd'}):
			err = decodeTfhdBox(demuxer, size32)
		case mov_tag([4]byte{'t', 'f', 'd', 't'}):
			err = decodeTfdtBox(demuxer, size32)
		case mov_tag([4]byte{'t', 'r', 'u', 'n'}):
			err = decodeTrunBox(demuxer, size32)
		case mov_tag([4]byte{'s', 'e', 'n', 'c'}):
			err = decodeSencBox(demuxer, size32)
		case mov_tag([4]byte{'s', 'a', 'i', 'z'}):
			err = decodeSaizBox(demuxer, size32)
		case mov_tag([4]byte{'s', 'a', 'i', 'o'}):
			err = decodeSaioBox(demuxer, size32)
		case mov_tag([4]byte{'s', 'g', 'p', 'd'}):
			err = decodeSgpdBox(demuxer, size32)
		case mov_tag([4]byte{'w', 'a', 'v', 'e'}):
			err = decodeWaveBox(demuxer)
		default:
			// also covers 'uuid' boxes: hdrLen already includes the 16-byte usertype
			_, err = demuxer.reader.Seek(int64(payloadLen), io.SeekCurrent)
		}
		if err != nil {
			break
		}
	}
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(demuxer.tracks) > 0 && demuxer.mp4Info.Timescale == 0 {
		return nil, errors.New("mp4: missing or invalid mvhd box (timescale is 0)")
	}
	for _, track := range demuxer.tracks {
		if track.timescale == 0 {
			return nil, fmt.Errorf("mp4: track %d has no mdhd box or a zero timescale", track.trackId)
		}
	}
	if !demuxer.isFragement {
		if err = demuxer.buildSampleList(); err != nil {
			return nil, err
		}
	}
	demuxer.readSampleIdx = make([]uint32, len(demuxer.tracks))
	for _, track := range demuxer.tracks {
		info := TrackInfo{}
		info.Cid = track.cid
		info.Duration = track.duration
		info.ChannelCount = track.chanelCount
		info.SampleRate = track.sampleRate
		info.SampleCount = uint32(len(track.samplelist))
		info.SampleSize = uint16(track.sampleBits)
		info.TrackId = int(track.trackId)
		info.Width = track.width
		info.Height = track.height
		info.Timescale = track.timescale
		if len(track.samplelist) > 0 {
			info.StartDts = track.samplelist[0].dts * 1000 / uint64(track.timescale)
			info.EndDts = track.samplelist[len(track.samplelist)-1].dts * 1000 / uint64(track.timescale)
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func (demuxer *MovDemuxer) GetMp4Info() Mp4Info {
	return demuxer.mp4Info
}

// buildSubSample builds the CENC sub-sample information for sample idx of
// the given track, or returns nil when the sample carries no such info.
func (demuxer *MovDemuxer) buildSubSample(track *mp4track, idx uint32) *SubSample {
	if int(idx) >= len(track.subSamples) {
		return nil
	}
	subSample := new(SubSample)
	subSample.Number = idx
	if len(track.subSamples[idx].iv) > 0 {
		copy(subSample.IV[:], track.subSamples[idx].iv)
	} else {
		copy(subSample.IV[:], track.defaultConstantIV)
	}
	if track.lastSeig != nil {
		copy(subSample.KID[:], track.lastSeig.KID[:])
		subSample.CryptByteBlock = track.lastSeig.CryptByteBlock
		subSample.SkipByteBlock = track.lastSeig.SkipByteBlock
	} else {
		copy(subSample.KID[:], track.defaultKID[:])
		subSample.CryptByteBlock = track.defaultCryptByteBlock
		subSample.SkipByteBlock = track.defaultSkipByteBlock
	}
	subSample.PsshBoxes = append(subSample.PsshBoxes, demuxer.pssh...)
	if len(track.subSamples[idx].subSamples) > 0 {
		subSample.Patterns = make([]SubSamplePattern, len(track.subSamples[idx].subSamples))
		for ei, e := range track.subSamples[idx].subSamples {
			subSample.Patterns[ei].BytesClear = e.bytesOfClearData
			subSample.Patterns[ei].BytesProtected = e.bytesOfProtectedData
		}
	}
	return subSample
}

// /return error == io.EOF, means read mp4 file completed
func (demuxer *MovDemuxer) ReadPacket() (*AVPacket, error) {
	for {
		maxdts := int64(-1)
		minTsSample := sampleEntry{dts: uint64(maxdts)}
		var whichTrack *mp4track = nil
		whichTracki := 0
		whichIdx := uint32(0)
		for i, track := range demuxer.tracks {
			idx := demuxer.readSampleIdx[i]
			if int(idx) >= len(track.samplelist) {
				continue
			}
			if track.timescale == 0 {
				return nil, fmt.Errorf("mp4: track %d has zero timescale", track.trackId)
			}
			if whichTrack == nil {
				minTsSample = track.samplelist[idx]
				whichTrack = track
				whichTracki = i
				whichIdx = idx
			} else {
				dts1 := minTsSample.dts * uint64(demuxer.mp4Info.Timescale) / uint64(whichTrack.timescale)
				dts2 := track.samplelist[idx].dts * uint64(demuxer.mp4Info.Timescale) / uint64(track.timescale)
				if dts1 > dts2 {
					minTsSample = track.samplelist[idx]
					whichTrack = track
					whichTracki = i
					whichIdx = idx
				}
			}
		}

		if whichTrack == nil {
			return nil, io.EOF
		}
		if _, err := demuxer.reader.Seek(int64(minTsSample.offset), io.SeekStart); err != nil {
			return nil, err
		}
		if minTsSample.size > maxBoxPayloadSize {
			return nil, fmt.Errorf("mp4: sample size %d exceeds limit", minTsSample.size)
		}
		sample := make([]byte, minTsSample.size)
		if _, err := io.ReadFull(demuxer.reader, sample); err != nil {
			return nil, err
		}
		demuxer.readSampleIdx[whichTracki]++
		avpkg := &AVPacket{
			Cid:     whichTrack.cid,
			TrackId: int(whichTrack.trackId),
			Pts:     minTsSample.pts * 1000 / uint64(whichTrack.timescale),
			Dts:     minTsSample.dts * 1000 / uint64(whichTrack.timescale),
		}
		if demuxer.OnRawSample != nil {
			subSample := demuxer.buildSubSample(whichTrack, whichIdx)
			err := demuxer.OnRawSample(whichTrack.cid, sample, subSample)
			if err != nil {
				return nil, err
			}
		}
		if whichTrack.cid == MP4_CODEC_H264 {
			extra, ok := whichTrack.extra.(*h264ExtraData)
			if !ok {
				return nil, errors.New("mp4: h264 track has no avcC extra data")
			}
			avpkg.Data = demuxer.processH264(sample, extra)
		} else if whichTrack.cid == MP4_CODEC_H265 {
			extra, ok := whichTrack.extra.(*h265ExtraData)
			if !ok {
				return nil, errors.New("mp4: h265 track has no hvcC extra data")
			}
			avpkg.Data = demuxer.processH265(sample, extra)
		} else if whichTrack.cid == MP4_CODEC_AAC {
			aacExtra, ok := whichTrack.extra.(*aacExtraData)
			if !ok {
				return nil, errors.New("mp4: aac track has no esds extra data")
			}
			adts, err := codec.ConvertASCToADTS(aacExtra.asc, len(sample)+7)
			if err != nil {
				return nil, err
			}
			avpkg.Data = append(adts.Encode(), sample...)
		} else {
			avpkg.Data = sample
		}
		if len(avpkg.Data) > 0 {
			return avpkg, nil
		}
	}
}

func (demuxer *MovDemuxer) GetSyncTable(trackId uint32) ([]SyncSample, error) {
	var track *mp4track = nil
	for i := 0; i < len(demuxer.tracks); i++ {
		if demuxer.tracks[i].trackId != trackId {
			continue
		}
		track = demuxer.tracks[i]
	}
	if track == nil {
		return nil, errors.New("not found track")
	}

	if track.stbltable == nil || track.stbltable.stss == nil {
		return nil, errors.New("not found stss box")
	}
	if track.timescale == 0 {
		return nil, errors.New("mp4: track has zero timescale")
	}

	syncTable := make([]SyncSample, 0, len(track.stbltable.stss.sampleNumber))

	for i := 0; i < len(track.stbltable.stss.sampleNumber); i++ {
		num := track.stbltable.stss.sampleNumber[i]
		if num == 0 || uint64(num) > uint64(len(track.samplelist)) {
			return nil, fmt.Errorf("mp4: stss sample number %d out of range (1..%d)", num, len(track.samplelist))
		}
		idx := num - 1
		syncTable = append(syncTable, SyncSample{
			Pts:    track.samplelist[idx].pts * 1000 / uint64(track.timescale),
			Dts:    track.samplelist[idx].dts * 1000 / uint64(track.timescale),
			Offset: uint32(track.samplelist[idx].offset),
			Size:   uint32(track.samplelist[idx].size),
		})
	}
	return syncTable, nil
}

func (demuxer *MovDemuxer) SeekTime(dts uint64) error {
	for i, track := range demuxer.tracks {
		if track.timescale == 0 {
			continue
		}
		for j := 0; j < len(track.samplelist); j++ {
			if track.samplelist[j].dts*1000/uint64(track.timescale) < dts {
				continue
			}
			demuxer.readSampleIdx[i] = uint32(j)
			break
		}
	}
	return nil
}

func (demuxer *MovDemuxer) buildSampleList() error {
	for _, track := range demuxer.tracks {
		stbl := track.stbltable
		if stbl == nil || stbl.stsz == nil || stbl.stsz.sampleCount == 0 {
			// empty track (or a track without a sample table): nothing to build
			track.samplelist = nil
			continue
		}
		if stbl.stco == nil || stbl.stsc == nil || stbl.stts == nil {
			return fmt.Errorf("mp4: track %d is missing stco/stsc/stts box", track.trackId)
		}
		if stbl.stsc.entryCount == 0 || len(stbl.stsc.entrys) == 0 {
			return fmt.Errorf("mp4: track %d has an empty stsc box", track.trackId)
		}
		if stbl.stsz.sampleSize == 0 && len(stbl.stsz.entrySizelist) < int(stbl.stsz.sampleCount) {
			return fmt.Errorf("mp4: track %d stsz entry list is shorter than sample count", track.trackId)
		}
		chunkCount := len(stbl.stco.chunkOffsetlist)
		chunks := make([]movchunk, chunkCount)
		iterator := 0
		for i := 0; i < chunkCount; i++ {
			chunks[i].chunknum = uint32(i + 1)
			chunks[i].chunkoffset = stbl.stco.chunkOffsetlist[i]
			for iterator+1 < len(stbl.stsc.entrys) && stbl.stsc.entrys[iterator+1].firstChunk <= chunks[i].chunknum {
				iterator++
			}
			chunks[i].samplenum = stbl.stsc.entrys[iterator].samplesPerChunk
		}
		track.samplelist = make([]sampleEntry, stbl.stsz.sampleCount)
		for i := range track.samplelist {
			if stbl.stsz.sampleSize == 0 {
				track.samplelist[i].size = uint64(stbl.stsz.entrySizelist[i])
			} else {
				track.samplelist[i].size = uint64(stbl.stsz.sampleSize)
			}
		}
		iterator = 0
		for i := range chunks {
			for j := 0; j < int(chunks[i].samplenum); j++ {
				if iterator >= len(track.samplelist) {
					break
				}
				if j == 0 {
					track.samplelist[iterator].offset = chunks[i].chunkoffset
				} else {
					track.samplelist[iterator].offset = track.samplelist[iterator-1].offset + track.samplelist[iterator-1].size
				}
				iterator++
			}
		}
		iterator = 0
		track.samplelist[iterator].dts = 0
		if track.elst != nil {
			for _, entry := range track.elst.entrys {
				if entry.mediaTime == -1 {
					// an empty edit: segment_duration is expressed in the movie
					// timescale, convert it to the track (media) timescale.
					if demuxer.mp4Info.Timescale == 0 {
						return errors.New("mp4: movie timescale is 0, cannot apply edit list")
					}
					track.samplelist[iterator].dts = entry.segmentDuration * uint64(track.timescale) / uint64(demuxer.mp4Info.Timescale)
				}
			}
		}
		iterator++
		for i := range stbl.stts.entrys {
			for j := 0; j < int(stbl.stts.entrys[i].sampleCount); j++ {
				if iterator >= len(track.samplelist) {
					break
				}
				track.samplelist[iterator].dts = uint64(stbl.stts.entrys[i].sampleDelta) + track.samplelist[iterator-1].dts
				iterator++
			}
		}

		// no ctts table, so pts == dts
		if stbl.ctts == nil || stbl.ctts.entryCount == 0 {
			for i := range track.samplelist {
				track.samplelist[i].pts = track.samplelist[i].dts
			}
		} else {
			iterator = 0
			for i := range stbl.ctts.entrys {
				offset := stbl.ctts.sampleOffset(i)
				for j := 0; j < int(stbl.ctts.entrys[i].sampleCount); j++ {
					if iterator >= len(track.samplelist) {
						break
					}
					track.samplelist[iterator].pts = uint64(int64(track.samplelist[iterator].dts) + offset)
					iterator++
				}
			}
			// samples not covered by ctts keep pts == dts
			for ; iterator < len(track.samplelist); iterator++ {
				track.samplelist[iterator].pts = track.samplelist[iterator].dts
			}
		}
	}
	return nil
}

func (demuxer *MovDemuxer) processH264(avcc []byte, extra *h264ExtraData) []byte {
	idr := false
	vcl := false
	spspps := false
	h264 := avcc
	for len(h264) >= 4 {
		nalusize := binary.BigEndian.Uint32(h264)
		if uint64(nalusize)+4 > uint64(len(h264)) {
			break
		}
		codec.CovertAVCCToAnnexB(h264)
		nalType := codec.H264NaluType(h264)
		switch {
		case nalType == codec.H264_NAL_PPS:
			fallthrough
		case nalType == codec.H264_NAL_SPS:
			spspps = true
		case nalType == codec.H264_NAL_I_SLICE:
			idr = true
			fallthrough
		case nalType >= codec.H264_NAL_P_SLICE && nalType <= codec.H264_NAL_SLICE_C:
			vcl = true
		}
		h264 = h264[4+nalusize:]
	}

	if !vcl {
		if !spspps {
			return avcc
		} else {
			demuxer.mp4out = append(demuxer.mp4out, avcc...)
		}
		return nil
	}

	if spspps {
		demuxer.mp4out = demuxer.mp4out[:0]
		return avcc
	}
	if !idr {
		return avcc
	}
	if len(demuxer.mp4out) > 0 {
		out := make([]byte, len(demuxer.mp4out)+len(avcc))
		copy(out, demuxer.mp4out)
		copy(out[len(demuxer.mp4out):], avcc)
		demuxer.mp4out = demuxer.mp4out[:0]
		return out
	}

	out := make([]byte, 0)
	for _, sps := range extra.spss {
		out = append(out, sps...)
	}
	for _, pps := range extra.ppss {
		out = append(out, pps...)
	}
	out = append(out, avcc...)
	return out
}

func (demuxer *MovDemuxer) processH265(hvcc []byte, extra *h265ExtraData) []byte {
	idr := false
	vcl := false
	spsppsvps := false
	h265 := hvcc
	for len(h265) >= 4 {
		nalusize := binary.BigEndian.Uint32(h265)
		if uint64(nalusize)+4 > uint64(len(h265)) {
			break
		}
		codec.CovertAVCCToAnnexB(h265)
		nalType := codec.H265NaluType(h265)
		switch {
		case nalType == codec.H265_NAL_VPS:
			fallthrough
		case nalType == codec.H265_NAL_PPS:
			fallthrough
		case nalType == codec.H265_NAL_SPS:
			spsppsvps = true
		case nalType >= codec.H265_NAL_SLICE_BLA_W_LP && nalType <= codec.H265_NAL_SLICE_CRA:
			idr = true
			fallthrough
		case nalType >= codec.H265_NAL_Slice_TRAIL_N && nalType <= codec.H265_NAL_SLICE_RASL_R:
			vcl = true
		}
		h265 = h265[4+nalusize:]
	}
	if !vcl {
		if !spsppsvps {
			return hvcc
		} else {
			demuxer.mp4out = append(demuxer.mp4out, hvcc...)
		}
		return nil
	}

	if spsppsvps {
		demuxer.mp4out = demuxer.mp4out[:0]
		return hvcc
	}
	if !idr {
		return hvcc
	}
	if len(demuxer.mp4out) > 0 {
		out := make([]byte, len(demuxer.mp4out)+len(hvcc))
		copy(out, demuxer.mp4out)
		copy(out[len(demuxer.mp4out):], hvcc)
		demuxer.mp4out = demuxer.mp4out[:0]
		return out
	}

	out := extra.hvccExtra.ToNalus()
	out = append(out, hvcc...)
	return out
}
