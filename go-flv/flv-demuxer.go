package flv

import (
	"errors"
	"fmt"

	"github.com/yapingcat/gomedia/go-codec"
)

type OnVideoFrameCallBack func(codecid codec.CodecID, frame []byte, cts int)
type VideoTagDemuxer interface {
	Decode(data []byte) error
	OnFrame(onframe OnVideoFrameCallBack)
}

var (
	errNaluTruncated   = errors.New("flv: nalu length exceeds tag payload")
	errNaluZeroLength  = errors.New("flv: zero length nalu")
	errBadNaluLenSize  = errors.New("flv: unsupported nalu length size")
	errEnhancedTooSmal = errors.New("flv: enhanced video tag size < 8")

	// ErrUnsupportedVideoCodec is returned when no demuxer/muxer exists for a
	// video codec id.
	ErrUnsupportedVideoCodec = errors.New("flv: unsupported video codec id")
	// ErrUnsupportedSoundFormat is returned when no demuxer/muxer exists for a
	// sound format.
	ErrUnsupportedSoundFormat = errors.New("flv: unsupported sound format")
)

// convertAVCCFrameToAnnexB walks a frame made of length-prefixed NALUs (AVCC /
// HVCC form) and converts it to Annex-B form. Every NALU length is validated
// against the remaining payload before it is used.
//
// When lengthSize is 4 the conversion is done in place (the returned slice
// aliases data, no allocation). For 1..3 byte length prefixes a new buffer is
// allocated. onNalu is invoked with every NALU payload (without start code).
func convertAVCCFrameToAnnexB(data []byte, lengthSize int, onNalu func(nalu []byte)) ([]byte, error) {
	if lengthSize < 1 || lengthSize > 4 {
		return nil, errBadNaluLenSize
	}
	var out []byte
	if lengthSize != 4 {
		out = make([]byte, 0, len(data)+16)
	}
	tmp := data
	for len(tmp) > 0 {
		if len(tmp) < lengthSize {
			return nil, errNaluTruncated
		}
		var naluSize uint64
		for i := 0; i < lengthSize; i++ {
			naluSize = naluSize<<8 | uint64(tmp[i])
		}
		if naluSize == 0 {
			return nil, errNaluZeroLength
		}
		if naluSize+uint64(lengthSize) > uint64(len(tmp)) {
			return nil, errNaluTruncated
		}
		end := lengthSize + int(naluSize)
		nalu := tmp[lengthSize:end]
		if lengthSize == 4 {
			codec.CovertAVCCToAnnexB(tmp)
		} else {
			out = append(out, 0x00, 0x00, 0x00, 0x01)
			out = append(out, nalu...)
		}
		if onNalu != nil {
			onNalu(nalu)
		}
		tmp = tmp[end:]
	}
	if lengthSize == 4 {
		return data, nil
	}
	return out, nil
}

// decodeHVCC parses an HEVCDecoderConfigurationRecord and returns it together
// with the NALU length size it declares. The record is validated by the codec
// parser, which reports truncated and malformed records.
func decodeHVCC(data []byte) (*codec.HEVCRecordConfiguration, int, error) {
	hvcc := codec.NewHEVCRecordConfiguration()
	if err := hvcc.Decode(data); err != nil {
		return nil, 0, err
	}
	return hvcc, int(hvcc.LengthSizeMinusOne&0x03) + 1, nil
}

type AVCTagDemuxer struct {
	spss           map[uint64][]byte
	ppss           map[uint64][]byte
	naluLengthSize int
	onframe        OnVideoFrameCallBack
}

func NewAVCTagDemuxer() *AVCTagDemuxer {
	return &AVCTagDemuxer{
		spss:           make(map[uint64][]byte),
		ppss:           make(map[uint64][]byte),
		naluLengthSize: 4,
		onframe:        nil,
	}
}

func (demuxer *AVCTagDemuxer) OnFrame(onframe OnVideoFrameCallBack) {
	demuxer.onframe = onframe
}

func (demuxer *AVCTagDemuxer) Decode(data []byte) error {

	if len(data) < 5 {
		return errors.New("avc tag size < 5")
	}

	vtag := VideoTag{}
	vtag.Decode(data[0:5])
	data = data[5:]
	switch vtag.AVCPacketType {
	case AVC_SEQUENCE_HEADER:
		// CovertExtradata validates the whole record, so no separate bounds
		// check is needed here; after it succeeded data[4] is guaranteed.
		spss, ppss, err := codec.CovertExtradata(data)
		if err != nil {
			return err
		}
		demuxer.naluLengthSize = int(data[4]&0x03) + 1
		for _, sps := range spss {
			demuxer.spss[codec.GetSPSId(sps[4:])] = sps
		}
		for _, pps := range ppss {
			demuxer.ppss[codec.GetPPSId(pps[4:])] = pps
		}
	case AVC_NALU:
		var hassps bool
		var haspps bool
		var idr bool
		frame, err := convertAVCCFrameToAnnexB(data, demuxer.naluLengthSize, func(nalu []byte) {
			switch codec.H264NaluTypeWithoutStartCode(nalu) {
			case codec.H264_NAL_I_SLICE:
				idr = true
			case codec.H264_NAL_SPS:
				hassps = true
			case codec.H264_NAL_PPS:
				haspps = true
			}
		})
		if err != nil {
			return err
		}
		if len(frame) == 0 {
			return nil
		}
		if idr && (!hassps || !haspps) {
			total := len(frame)
			for _, sps := range demuxer.spss {
				total += len(sps)
			}
			for _, pps := range demuxer.ppss {
				total += len(pps)
			}
			nalus := make([]byte, 0, total)
			for _, sps := range demuxer.spss {
				nalus = append(nalus, sps...)
			}
			for _, pps := range demuxer.ppss {
				nalus = append(nalus, pps...)
			}
			nalus = append(nalus, frame...)
			if demuxer.onframe != nil {
				demuxer.onframe(codec.CODECID_VIDEO_H264, nalus, int(vtag.CompositionTime))
			}
		} else if demuxer.onframe != nil {
			demuxer.onframe(codec.CODECID_VIDEO_H264, frame, int(vtag.CompositionTime))
		}
	default:
		// AVC end of sequence or unknown packet type: nothing to emit
	}
	return nil
}

type HevcTagDemuxer struct {
	SpsPpsVps      []byte
	naluLengthSize int
	onframe        OnVideoFrameCallBack
}

func NewHevcTagDemuxer() *HevcTagDemuxer {
	return &HevcTagDemuxer{
		SpsPpsVps:      make([]byte, 0),
		naluLengthSize: 4,
		onframe:        nil,
	}
}

func (demuxer *HevcTagDemuxer) OnFrame(onframe OnVideoFrameCallBack) {
	demuxer.onframe = onframe
}

func (demuxer *HevcTagDemuxer) Decode(data []byte) error {

	if len(data) < 5 {
		return errors.New("hevc tag size < 5")
	}

	vtag := VideoTag{}

	isExHeader := data[0] & 0x80
	if isExHeader != 0 {
		// enhanced flv
		packetType := data[0] & 0x0F
		if packetType == PacketTypeCodedFrames && len(data) < 8 {
			return errEnhancedTooSmal
		}
		vtag.Decode(data)
		switch vtag.AVCPacketType {
		case PacketTypeSequenceStart:
			return demuxer.decodeSequenceHeader(data[5:])
		case PacketTypeCodedFrames:
			return demuxer.decodeNalus(data[8:], vtag.CompositionTime)
		case PacketTypeCodedFramesX:
			return demuxer.decodeNalus(data[5:], 0)
		}
		return nil
	}

	vtag.Decode(data[0:5])
	data = data[5:]
	switch vtag.AVCPacketType {
	case AVC_SEQUENCE_HEADER:
		return demuxer.decodeSequenceHeader(data)
	case AVC_NALU:
		return demuxer.decodeNalus(data, vtag.CompositionTime)
	}
	return nil
}

func (demuxer *HevcTagDemuxer) decodeSequenceHeader(data []byte) error {
	hvcc, lengthSize, err := decodeHVCC(data)
	if err != nil {
		return err
	}
	demuxer.naluLengthSize = lengthSize
	demuxer.SpsPpsVps = hvcc.ToNalus()
	return nil
}

func (demuxer *HevcTagDemuxer) decodeNalus(data []byte, CompositionTime int32) error {
	var hassps bool
	var haspps bool
	var hasvps bool
	var idr bool
	frame, err := convertAVCCFrameToAnnexB(data, demuxer.naluLengthSize, func(nalu []byte) {
		naluType := codec.H265NaluTypeWithoutStartCode(nalu)
		switch {
		case naluType >= 16 && naluType <= 21:
			idr = true
		case naluType == codec.H265_NAL_SPS:
			hassps = true
		case naluType == codec.H265_NAL_PPS:
			haspps = true
		case naluType == codec.H265_NAL_VPS:
			hasvps = true
		}
	})
	if err != nil {
		return err
	}
	if len(frame) == 0 {
		return nil
	}

	if idr && (!hassps || !haspps || !hasvps) {
		nalus := make([]byte, 0, len(demuxer.SpsPpsVps)+len(frame))
		nalus = append(nalus, demuxer.SpsPpsVps...)
		nalus = append(nalus, frame...)
		if demuxer.onframe != nil {
			demuxer.onframe(codec.CODECID_VIDEO_H265, nalus, int(CompositionTime))
		}
	} else if demuxer.onframe != nil {
		demuxer.onframe(codec.CODECID_VIDEO_H265, frame, int(CompositionTime))
	}

	return nil
}

type OnAudioFrameCallBack func(codecid codec.CodecID, frame []byte)

type AudioTagDemuxer interface {
	Decode(data []byte) error
	OnFrame(onframe OnAudioFrameCallBack)
}

type AACTagDemuxer struct {
	asc     []byte
	onframe OnAudioFrameCallBack
}

func NewAACTagDemuxer() *AACTagDemuxer {
	return &AACTagDemuxer{
		asc:     make([]byte, 0, 2),
		onframe: nil,
	}
}

func (demuxer *AACTagDemuxer) OnFrame(onframe OnAudioFrameCallBack) {
	demuxer.onframe = onframe
}

func (demuxer *AACTagDemuxer) Decode(data []byte) error {

	if len(data) < 2 {
		return errors.New("aac tag size < 2")
	}

	atag := AudioTag{}
	err := atag.Decode(data[0:2])
	if err != nil {
		return err
	}
	data = data[2:]
	if atag.AACPacketType == AAC_SEQUENCE_HEADER {
		demuxer.asc = make([]byte, len(data))
		copy(demuxer.asc, data)
	} else {
		adts, err := codec.ConvertASCToADTS(demuxer.asc, len(data)+7)
		if err != nil {
			return err
		}
		adts_frame := append(adts.Encode(), data...)
		if demuxer.onframe != nil {
			demuxer.onframe(codec.CODECID_AUDIO_AAC, adts_frame)
		}
	}
	return nil
}

type G711Demuxer struct {
	format  FLV_SOUND_FORMAT
	onframe OnAudioFrameCallBack
}

func NewG711Demuxer(format FLV_SOUND_FORMAT) *G711Demuxer {
	return &G711Demuxer{
		format:  format,
		onframe: nil,
	}
}

func (demuxer *G711Demuxer) OnFrame(onframe OnAudioFrameCallBack) {
	demuxer.onframe = onframe
}

func (demuxer *G711Demuxer) Decode(data []byte) error {

	if len(data) < 1 {
		return errors.New("audio tag size < 1")
	}

	atag := AudioTag{}
	err := atag.Decode(data[0:1])
	if err != nil {
		return err
	}
	data = data[1:]

	if demuxer.onframe != nil {
		demuxer.onframe(demuxer.format.ToMpegCodecId(), data)
	}
	return nil
}

// CreateAudioTagDemuxer returns a demuxer for the given sound format, or
// ErrUnsupportedSoundFormat when the format has no demuxer.
func CreateAudioTagDemuxer(formats FLV_SOUND_FORMAT) (AudioTagDemuxer, error) {
	switch formats {
	case FLV_G711A, FLV_G711U, FLV_MP3:
		return NewG711Demuxer(formats), nil
	case FLV_AAC:
		return NewAACTagDemuxer(), nil
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedSoundFormat, formats)
	}
}

// CreateFlvVideoTagHandle returns a demuxer for the given video codec id, or
// ErrUnsupportedVideoCodec when the codec has no demuxer.
func CreateFlvVideoTagHandle(cid FLV_VIDEO_CODEC_ID) (VideoTagDemuxer, error) {
	switch cid {
	case FLV_AVC:
		return NewAVCTagDemuxer(), nil
	case FLV_HEVC:
		return NewHevcTagDemuxer(), nil
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedVideoCodec, cid)
	}
}
