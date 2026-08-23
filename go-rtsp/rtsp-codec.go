package rtsp

import (
	"strings"
)

type RTSP_CODEC_ID int

const (
	RTSP_CODEC_H264 RTSP_CODEC_ID = iota
	RTSP_CODEC_H265
	RTSP_CODEC_AAC
	RTSP_CODEC_G711A
	RTSP_CODEC_G711U
	RTSP_CODEC_PS
	RTSP_CODEC_TS
	// RTSP_CODEC_UNKNOWN is returned for encoding names this package can not handle
	// (e.g. JPEG, opus, application tracks). Tracks with this codec id are skipped.
	RTSP_CODEC_UNKNOWN RTSP_CODEC_ID = -1
)

type RtspCodec struct {
	Cid          RTSP_CODEC_ID //H264,H265,PCMU,PCMA...
	PayloadType  uint8
	SampleRate   uint32
	ChannelCount uint8
}

// GetCodecIdByEncodeName maps an SDP rtpmap encoding name to a codec id.
// The second return value is false (and the id is RTSP_CODEC_UNKNOWN) for
// unsupported names; it never panics.
func GetCodecIdByEncodeName(name string) (RTSP_CODEC_ID, bool) {
	lowName := strings.ToLower(strings.TrimSpace(name))
	switch lowName {
	case "h264":
		return RTSP_CODEC_H264, true
	case "h265", "hevc":
		return RTSP_CODEC_H265, true
	case "mpeg4-generic", "mpeg4-latm":
		return RTSP_CODEC_AAC, true
	case "pcmu":
		return RTSP_CODEC_G711U, true
	case "pcma":
		return RTSP_CODEC_G711A, true
	case "mp2p":
		return RTSP_CODEC_PS, true
	case "mp2t":
		return RTSP_CODEC_TS, true
	}
	return RTSP_CODEC_UNKNOWN, false
}

// GetEncodeNameByCodecId returns the SDP encoding name for a codec id, or an
// empty string for unknown ids. It never panics.
func GetEncodeNameByCodecId(cid RTSP_CODEC_ID) string {
	switch cid {
	case RTSP_CODEC_H264:
		return "H264"
	case RTSP_CODEC_H265:
		return "H265"
	case RTSP_CODEC_AAC:
		return "mpeg4-generic"
	case RTSP_CODEC_G711A:
		return "PCMA"
	case RTSP_CODEC_G711U:
		return "PCMU"
	case RTSP_CODEC_PS:
		return "MP2P"
	case RTSP_CODEC_TS:
		return "MP2T"
	default:
		return ""
	}
}

func codecIdOrUnknown(name string) RTSP_CODEC_ID {
	cid, _ := GetCodecIdByEncodeName(name)
	return cid
}

func NewCodec(name string, pt uint8, sampleRate uint32, channel uint8) RtspCodec {
	return RtspCodec{Cid: codecIdOrUnknown(name), PayloadType: pt, SampleRate: sampleRate, ChannelCount: channel}
}

func NewVideoCodec(name string, pt uint8, sampleRate uint32) RtspCodec {
	return RtspCodec{Cid: codecIdOrUnknown(name), PayloadType: pt, SampleRate: sampleRate}
}

func NewAudioCodec(name string, pt uint8, sampleRate uint32, channelCount int) RtspCodec {
	return RtspCodec{Cid: codecIdOrUnknown(name), PayloadType: pt, SampleRate: sampleRate, ChannelCount: uint8(channelCount)}
}

func NewApplicatioCodec(name string, pt uint8) RtspCodec {
	return RtspCodec{Cid: codecIdOrUnknown(name), PayloadType: pt}
}
