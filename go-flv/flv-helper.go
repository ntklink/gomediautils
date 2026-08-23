package flv

import (
	"github.com/ntklink/gomediautils/go-codec"
)

func PutUint24(b []byte, v uint32) {
	_ = b[2]
	b[0] = byte(v >> 16)
	b[1] = byte(v >> 8)
	b[2] = byte(v)
}

func GetUint24(b []byte) (v uint32) {
	_ = b[2]
	v = uint32(b[0])
	v = (v << 8) | uint32(b[1])
	v = (v << 8) | uint32(b[2])
	return v
}

// GetInt24 reads a big endian SI24, sign extending it to an int32. The flv
// CompositionTime field is signed: a stream whose first frames are reordered
// carries a negative offset there.
func GetInt24(b []byte) int32 {
	v := GetUint24(b)
	if v&0x800000 != 0 {
		v |= 0xFF000000
	}
	return int32(v)
}

func CovertFlvVideoCodecId2MpegCodecId(cid FLV_VIDEO_CODEC_ID) codec.CodecID {
	if cid == FLV_AVC {
		return codec.CODECID_VIDEO_H264
	} else if cid == FLV_HEVC {
		return codec.CODECID_VIDEO_H265
	}
	return codec.CODECID_UNRECOGNIZED
}

func CovertFlvAudioCodecId2MpegCodecId(cid FLV_SOUND_FORMAT) codec.CodecID {
	if cid == FLV_AAC {
		return codec.CODECID_AUDIO_AAC
	} else if cid == FLV_G711A {
		return codec.CODECID_AUDIO_G711A
	} else if cid == FLV_G711U {
		return codec.CODECID_AUDIO_G711U
	} else if cid == FLV_MP3 {
		return codec.CODECID_AUDIO_MP3
	}
	return codec.CODECID_UNRECOGNIZED
}

func CovertCodecId2FlvVideoCodecId(cid codec.CodecID) FLV_VIDEO_CODEC_ID {
	if cid == codec.CODECID_VIDEO_H264 {
		return FLV_AVC
	} else if cid == codec.CODECID_VIDEO_H265 {
		return FLV_HEVC
	} else {
		return FLV_VIDEO_CODEC_UNKNOWN
	}
}

func CovertCodecId2SoundFromat(cid codec.CodecID) FLV_SOUND_FORMAT {
	switch cid {
	case codec.CODECID_AUDIO_AAC:
		return FLV_AAC
	case codec.CODECID_AUDIO_G711A:
		return FLV_G711A
	case codec.CODECID_AUDIO_G711U:
		return FLV_G711U
	case codec.CODECID_AUDIO_MP3:
		return FLV_MP3
	default:
		return FLV_SOUND_FORMAT_UNKNOWN
	}
}

func GetTagLenByAudioCodec(cid FLV_SOUND_FORMAT) int {
	if cid == FLV_AAC {
		return 2
	} else {
		return 1
	}
}

func GetTagLenByVideoCodec(cid FLV_VIDEO_CODEC_ID) int {
	if cid == FLV_AVC || cid == FLV_HEVC {
		return 5
	} else {
		return 1
	}
}
