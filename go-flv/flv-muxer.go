package flv

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/yapingcat/gomedia/go-codec"
)

// errShortSPS is reported when an SPS is too short to build an
// AVCDecoderConfigurationRecord from (it needs the nal header plus the
// profile, compatibility and level bytes).
var errShortSPS = codec.ErrShortSPS

func WriteAudioTag(data []byte, cid FLV_SOUND_FORMAT, sampleRate int, channelCount int, isSequenceHeader bool) []byte {
	var atag AudioTag
	atag.SoundFormat = uint8(cid)
	if cid == FLV_AAC {
		atag.SoundRate = uint8(FLV_SAMPLE_44000)
		atag.SoundSize = 1
		atag.SoundType = 1
	} else {
		switch sampleRate {
		case 5500:
			atag.SoundRate = uint8(FLV_SAMPLE_5500)
		case 11025:
			atag.SoundRate = uint8(FLV_SAMPLE_11000)
		case 22050:
			atag.SoundRate = uint8(FLV_SAMPLE_22000)
		case 44100:
			atag.SoundRate = uint8(FLV_SAMPLE_44000)
		default:
			atag.SoundRate = uint8(FLV_SAMPLE_44000)
		}
		atag.SoundSize = 1
		if channelCount > 1 {
			atag.SoundType = 1
		} else {
			atag.SoundType = 0
		}
	}

	if isSequenceHeader {
		atag.AACPacketType = 0
	} else {
		atag.AACPacketType = 1
	}
	hdr := atag.Encode()
	tagData := make([]byte, 0, len(hdr)+len(data))
	tagData = append(tagData, hdr...)
	tagData = append(tagData, data...)
	return tagData
}

func WriteVideoTag(data []byte, isKey bool, cid FLV_VIDEO_CODEC_ID, cts int32, isSequenceHeader bool) []byte {
	var vtag VideoTag
	vtag.CodecId = uint8(cid)
	vtag.CompositionTime = cts
	if isKey {
		vtag.FrameType = uint8(KEY_FRAME)
	} else {
		vtag.FrameType = uint8(INTER_FRAME)
	}

	var hdr []byte
	if cid == FLV_HEVC {
		// hevc has no legacy codec id, so it goes out as an enhanced tag
		if isSequenceHeader {
			vtag.AVCPacketType = PacketTypeSequenceStart
		} else {
			vtag.AVCPacketType = PacketTypeCodedFrames
		}
		hdr = vtag.EncodeEx(hevcFourCC)
	} else {
		if isSequenceHeader {
			vtag.AVCPacketType = uint8(AVC_SEQUENCE_HEADER)
		} else {
			vtag.AVCPacketType = uint8(AVC_NALU)
		}
		hdr = vtag.Encode()
	}

	tagData := make([]byte, 0, len(hdr)+len(data))
	tagData = append(tagData, hdr...)
	tagData = append(tagData, data...)
	return tagData
}

// naluBody returns nalu without its leading start code. The input slice is
// never modified.
func naluBody(nalu []byte) []byte {
	if start, sc := codec.FindStartCode(nalu, 0); start >= 0 {
		return nalu[start+int(sc):]
	}
	return nalu
}

// appendAVCC appends nalu (Annex-B, with or without start code) to dst in
// 4-byte length-prefixed form. The input slice is never modified.
func appendAVCC(dst []byte, nalu []byte) []byte {
	nalu = naluBody(nalu)
	n := uint32(len(nalu))
	dst = append(dst, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	return append(dst, nalu...)
}

// cloneNalu returns a private copy of nalu so stored parameter sets never
// alias the caller's buffer.
func cloneNalu(nalu []byte) []byte {
	c := make([]byte, len(nalu))
	copy(c, nalu)
	return c
}

// AVTagMuxer turns elementary stream frames into flv tag bodies. Write
// reports the frames it could not encode instead of dropping them silently.
type AVTagMuxer interface {
	Write(frames []byte, pts uint32, dts uint32) ([][]byte, error)
}

type AVCMuxer struct {
	spsset map[uint64][]byte // Annex-B form (start code included), private copies
	ppsset map[uint64][]byte // Annex-B form (start code included), private copies
	cache  []byte
	first  bool
}

func NewAVCMuxer() *AVCMuxer {
	return &AVCMuxer{
		spsset: make(map[uint64][]byte),
		ppsset: make(map[uint64][]byte),
		cache:  make([]byte, 0, 1024),
		first:  true,
	}
}

func (muxer *AVCMuxer) Write(frames []byte, pts uint32, dts uint32) ([][]byte, error) {
	var vcl bool = false
	var isKey bool = false
	var err error
	codec.SplitFrameWithStartCode(frames, func(nalu []byte) bool {
		naltype := codec.H264NaluType(nalu)
		switch naltype {
		case codec.H264_NAL_SPS:
			// CreateH264AVCCExtradata reads 4 bytes of the first SPS
			if len(naluBody(nalu)) < 4 {
				err = errShortSPS
				return false
			}
			spsid := codec.GetSPSIdWithStartCode(nalu)
			s, found := muxer.spsset[spsid]
			if !found || !bytes.Equal(s, nalu) {
				muxer.spsset[spsid] = cloneNalu(nalu)
				muxer.cache = appendAVCC(muxer.cache, nalu)
			}
		case codec.H264_NAL_PPS:
			ppsid := codec.GetPPSIdWithStartCode(nalu)
			s, found := muxer.ppsset[ppsid]
			if !found || !bytes.Equal(s, nalu) {
				muxer.ppsset[ppsid] = cloneNalu(nalu)
				muxer.cache = appendAVCC(muxer.cache, nalu)
			}
		default:
			if naltype <= codec.H264_NAL_I_SLICE {
				vcl = true
				if naltype == codec.H264_NAL_I_SLICE {
					isKey = true
				}
			}
			muxer.cache = appendAVCC(muxer.cache, nalu)
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	var tags [][]byte
	if muxer.first && len(muxer.ppsset) > 0 && len(muxer.spsset) > 0 {
		// CreateH264AVCCExtradata re-slices its inputs, so hand it fresh slices
		spss := make([][]byte, 0, len(muxer.spsset))
		for _, sps := range muxer.spsset {
			spss = append(spss, sps)
		}
		ppss := make([][]byte, 0, len(muxer.ppsset))
		for _, pps := range muxer.ppsset {
			ppss = append(ppss, pps)
		}
		extraData, err := codec.CreateH264AVCCExtradata(spss, ppss)
		if err != nil {
			return nil, err
		}
		tags = append(tags, WriteVideoTag(extraData, true, FLV_AVC, 0, true))
		muxer.first = false
	}

	if vcl {
		tags = append(tags, WriteVideoTag(muxer.cache, isKey, FLV_AVC, int32(pts-dts), false))
		muxer.cache = muxer.cache[:0]
	}
	return tags, nil
}

type HevcMuxer struct {
	hvcc  *codec.HEVCRecordConfiguration
	cache []byte
	first bool
}

func NewHevcMuxer() *HevcMuxer {
	return &HevcMuxer{
		hvcc:  codec.NewHEVCRecordConfiguration(),
		cache: make([]byte, 0, 1024),
		first: true,
	}
}

func (muxer *HevcMuxer) Write(frames []byte, pts uint32, dts uint32) ([][]byte, error) {
	var vcl bool = false
	var isKey bool = false
	var err error
	codec.SplitFrameWithStartCode(frames, func(nalu []byte) bool {
		naltype := codec.H265NaluType(nalu)
		switch naltype {
		case codec.H265_NAL_SPS:
			err = muxer.hvcc.UpdateSPS(nalu)
		case codec.H265_NAL_PPS:
			err = muxer.hvcc.UpdatePPS(nalu)
		case codec.H265_NAL_VPS:
			err = muxer.hvcc.UpdateVPS(nalu)
		default:
			if naltype >= 16 && naltype <= 21 {
				isKey = true
			}
			vcl = codec.IsH265VCLNaluType(naltype)
		}
		if err != nil {
			return false
		}
		muxer.cache = appendAVCC(muxer.cache, nalu)
		return true
	})
	if err != nil {
		return nil, err
	}

	var tags [][]byte
	if muxer.first && len(muxer.hvcc.Arrays) > 0 {
		extraData, err := muxer.hvcc.Encode()
		if err != nil {
			return nil, err
		}
		tags = append(tags, WriteVideoTag(extraData, true, FLV_HEVC, 0, true))
		muxer.first = false
	}
	if vcl {
		tags = append(tags, WriteVideoTag(muxer.cache, isKey, FLV_HEVC, int32(pts-dts), false))
		muxer.cache = muxer.cache[:0]
	}
	return tags, nil
}

// CreateVideoMuxer returns a muxer for the given video codec id, or
// ErrUnsupportedVideoCodec when the codec has no muxer.
func CreateVideoMuxer(cid FLV_VIDEO_CODEC_ID) (AVTagMuxer, error) {
	switch cid {
	case FLV_AVC:
		return NewAVCMuxer(), nil
	case FLV_HEVC:
		return NewHevcMuxer(), nil
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedVideoCodec, cid)
	}
}

type AACMuxer struct {
	updateSequence bool
}

func NewAACMuxer() *AACMuxer {
	return &AACMuxer{updateSequence: true}
}

func (muxer *AACMuxer) Write(frames []byte, pts uint32, dts uint32) ([][]byte, error) {
	var tags [][]byte
	var ascErr error
	err := codec.SplitAACFrame(frames, func(aac []byte) {
		if ascErr != nil {
			return
		}
		if muxer.updateSequence {
			asc, err := codec.ConvertADTSToASC(aac)
			if err != nil {
				ascErr = err
				return
			}
			tags = append(tags, WriteAudioTag(asc.Encode(), FLV_AAC, 0, 0, true))
			muxer.updateSequence = false
		}
		tags = append(tags, WriteAudioTag(aac[7:], FLV_AAC, 0, 0, false))
	})
	if err != nil {
		return nil, err
	}
	if ascErr != nil {
		return nil, ascErr
	}
	return tags, nil
}

// g711SampleRate is the sample rate handed to the G.711 muxers created by
// CreateAudioMuxer. G.711 in FLV is 8 kHz mono, which the 2-bit SoundRate
// field cannot express; players ignore the field for G.711, so index 0
// (5.5 kHz) is written on purpose, matching what other FLV muxers emit.
const g711SampleRate = 5500

type G711AMuxer struct {
	channelCount int
	sampleRate   int
}

func NewG711AMuxer(channelCount int, sampleRate int) *G711AMuxer {
	return &G711AMuxer{
		channelCount: channelCount,
		sampleRate:   sampleRate,
	}
}

func (muxer *G711AMuxer) Write(frames []byte, pts uint32, dts uint32) ([][]byte, error) {
	return [][]byte{WriteAudioTag(frames, FLV_G711A, muxer.sampleRate, muxer.channelCount, false)}, nil
}

type G711UMuxer struct {
	channelCount int
	sampleRate   int
}

func NewG711UMuxer(channelCount int, sampleRate int) *G711UMuxer {
	return &G711UMuxer{
		channelCount: channelCount,
		sampleRate:   sampleRate,
	}
}

func (muxer *G711UMuxer) Write(frames []byte, pts uint32, dts uint32) ([][]byte, error) {
	return [][]byte{WriteAudioTag(frames, FLV_G711U, muxer.sampleRate, muxer.channelCount, false)}, nil
}

type Mp3Muxer struct {
}

func (muxer *Mp3Muxer) Write(frames []byte, pts uint32, dts uint32) ([][]byte, error) {
	tags := make([][]byte, 0, 1)
	err := codec.SplitMp3Frames(frames, func(head *codec.MP3FrameHead, frame []byte) {
		tags = append(tags, WriteAudioTag(frame, FLV_MP3, head.GetSampleRate(), head.GetChannelCount(), false))
	})
	if err != nil {
		return nil, err
	}
	return tags, nil
}

// CreateAudioMuxer returns a muxer for the given sound format, or
// ErrUnsupportedSoundFormat when the format has no muxer.
func CreateAudioMuxer(cid FLV_SOUND_FORMAT) (AVTagMuxer, error) {
	switch cid {
	case FLV_AAC:
		return NewAACMuxer(), nil
	case FLV_G711A:
		return NewG711AMuxer(1, g711SampleRate), nil
	case FLV_G711U:
		return NewG711UMuxer(1, g711SampleRate), nil
	case FLV_MP3:
		return new(Mp3Muxer), nil
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedSoundFormat, cid)
	}
}

type FlvMuxer struct {
	videoMuxer AVTagMuxer
	audioMuxer AVTagMuxer
}

// NewFlvMuxer creates a muxer for the given codecs. It fails when either
// codec is not supported by flv.
func NewFlvMuxer(vid FLV_VIDEO_CODEC_ID, aid FLV_SOUND_FORMAT) (*FlvMuxer, error) {
	videoMuxer, err := CreateVideoMuxer(vid)
	if err != nil {
		return nil, err
	}
	audioMuxer, err := CreateAudioMuxer(aid)
	if err != nil {
		return nil, err
	}
	return &FlvMuxer{videoMuxer: videoMuxer, audioMuxer: audioMuxer}, nil
}

func (muxer *FlvMuxer) SetVideoCodeId(cid FLV_VIDEO_CODEC_ID) error {
	videoMuxer, err := CreateVideoMuxer(cid)
	if err != nil {
		return err
	}
	muxer.videoMuxer = videoMuxer
	return nil
}

func (muxer *FlvMuxer) SetAudioCodeId(cid FLV_SOUND_FORMAT) error {
	audioMuxer, err := CreateAudioMuxer(cid)
	if err != nil {
		return err
	}
	muxer.audioMuxer = audioMuxer
	return nil
}

func (muxer *FlvMuxer) WriteVideo(frames []byte, pts uint32, dts uint32) ([][]byte, error) {
	return muxer.WriteFrames(VIDEO_TAG, frames, pts, dts)
}

func (muxer *FlvMuxer) WriteAudio(frames []byte, pts uint32, dts uint32) ([][]byte, error) {
	return muxer.WriteFrames(AUDIO_TAG, frames, pts, dts)
}

// writeTagBodies runs the codec muxer and returns the tag bodies together
// with the FlvTag header template (DataSize left unset).
func (muxer *FlvMuxer) writeTagBodies(frameType TagType, frames []byte, pts uint32, dts uint32) (FlvTag, [][]byte, error) {
	var ftag FlvTag
	var tags [][]byte
	var err error
	switch frameType {
	case AUDIO_TAG:
		if muxer.audioMuxer == nil {
			return ftag, nil, errors.New("audio Muxer is Nil")
		}
		ftag.TagType = uint8(AUDIO_TAG)
		tags, err = muxer.audioMuxer.Write(frames, pts, dts)
	case VIDEO_TAG:
		if muxer.videoMuxer == nil {
			return ftag, nil, errors.New("video Muxer is Nil")
		}
		ftag.TagType = uint8(VIDEO_TAG)
		tags, err = muxer.videoMuxer.Write(frames, pts, dts)
	default:
		return ftag, nil, errors.New("unsupport Frame Type")
	}
	if err != nil {
		return ftag, nil, err
	}
	ftag.Timestamp = dts & 0x00FFFFFF
	ftag.TimestampExtended = uint8(dts >> 24 & 0xFF)
	return ftag, tags, nil
}

func (muxer *FlvMuxer) WriteFrames(frameType TagType, frames []byte, pts uint32, dts uint32) ([][]byte, error) {
	ftag, tags, err := muxer.writeTagBodies(frameType, frames, pts, dts)
	if err != nil {
		return nil, err
	}
	tmptags := make([][]byte, 0, len(tags))
	for _, tag := range tags {
		ftag.DataSize = uint32(len(tag))
		full := make([]byte, 0, int(FLVTAG_SIZE)+len(tag))
		full = ftag.appendTo(full)
		full = append(full, tag...)
		tmptags = append(tmptags, full)
	}
	return tmptags, nil
}
