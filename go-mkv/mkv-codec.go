package mkv

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ntklink/gomediautils/go-codec"
)

// Matroska codec ids (https://www.matroska.org/technical/codec_specs.html).
const (
	CodecIDH264  = "V_MPEG4/ISO/AVC"
	CodecIDH265  = "V_MPEGH/ISO/HEVC"
	CodecIDVP8   = "V_VP8"
	CodecIDAAC   = "A_AAC"
	CodecIDOpus  = "A_OPUS"
	CodecIDMP3   = "A_MPEG/L3"
	CodecIDMSACM = "A_MS/ACM"
)

// WAVEFORMATEX format tags that A_MS/ACM carries G.711 with.
const (
	waveFormatALaw  = 0x0006
	waveFormatMuLaw = 0x0007
)

// ErrUnsupportedCodec is reported for a codec this package cannot carry, or,
// in a WebM file, one the WebM subset does not allow.
var ErrUnsupportedCodec = errors.New("mkv: unsupported codec")

const (
	trackTypeVideo = 1
	trackTypeAudio = 2
)

func isVideo(cid codec.CodecID) bool {
	return cid == codec.CODECID_VIDEO_H264 || cid == codec.CODECID_VIDEO_H265 || cid == codec.CODECID_VIDEO_VP8
}

func isAudio(cid codec.CodecID) bool {
	switch cid {
	case codec.CODECID_AUDIO_AAC, codec.CODECID_AUDIO_OPUS, codec.CODECID_AUDIO_MP3,
		codec.CODECID_AUDIO_G711A, codec.CODECID_AUDIO_G711U:
		return true
	}
	return false
}

// allowedInWebM reports whether the WebM subset of Matroska may carry cid.
func allowedInWebM(cid codec.CodecID) bool {
	return cid == codec.CODECID_VIDEO_VP8 || cid == codec.CODECID_AUDIO_OPUS
}

func codecIDString(cid codec.CodecID) (string, error) {
	switch cid {
	case codec.CODECID_VIDEO_H264:
		return CodecIDH264, nil
	case codec.CODECID_VIDEO_H265:
		return CodecIDH265, nil
	case codec.CODECID_VIDEO_VP8:
		return CodecIDVP8, nil
	case codec.CODECID_AUDIO_AAC:
		return CodecIDAAC, nil
	case codec.CODECID_AUDIO_OPUS:
		return CodecIDOpus, nil
	case codec.CODECID_AUDIO_MP3:
		return CodecIDMP3, nil
	case codec.CODECID_AUDIO_G711A, codec.CODECID_AUDIO_G711U:
		return CodecIDMSACM, nil
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupportedCodec, codec.CodecString(cid))
}

// codecFromTrack maps a track entry's codec id back to a codec. A_MS/ACM
// needs the format tag of the WAVEFORMATEX in CodecPrivate; the old
// A_AAC/MPEG4/... ids predate A_AAC and still turn up in files.
func codecFromTrack(id string, private []byte) codec.CodecID {
	switch {
	case id == CodecIDH264:
		return codec.CODECID_VIDEO_H264
	case id == CodecIDH265:
		return codec.CODECID_VIDEO_H265
	case id == CodecIDVP8:
		return codec.CODECID_VIDEO_VP8
	case id == CodecIDAAC || len(id) > 6 && id[:6] == "A_AAC/":
		return codec.CODECID_AUDIO_AAC
	case id == CodecIDOpus:
		return codec.CODECID_AUDIO_OPUS
	case id == CodecIDMP3:
		return codec.CODECID_AUDIO_MP3
	case id == CodecIDMSACM && len(private) >= 2:
		switch binary.LittleEndian.Uint16(private) {
		case waveFormatALaw:
			return codec.CODECID_AUDIO_G711A
		case waveFormatMuLaw:
			return codec.CODECID_AUDIO_G711U
		}
	}
	return codec.CODECID_UNRECOGNIZED
}

// waveFormatEx builds the 18 byte WAVEFORMATEX that A_MS/ACM tracks keep in
// CodecPrivate, all fields little endian.
func waveFormatEx(tag uint16, channels uint16, sampleRate uint32, bits uint16) []byte {
	buf := make([]byte, 18)
	blockAlign := channels * bits / 8
	binary.LittleEndian.PutUint16(buf[0:], tag)
	binary.LittleEndian.PutUint16(buf[2:], channels)
	binary.LittleEndian.PutUint32(buf[4:], sampleRate)
	binary.LittleEndian.PutUint32(buf[8:], sampleRate*uint32(blockAlign))
	binary.LittleEndian.PutUint16(buf[12:], blockAlign)
	binary.LittleEndian.PutUint16(buf[14:], bits)
	// buf[16:18] cbSize = 0, no extra format bytes
	return buf
}

// adtsHeaderLen is 7, or 9 when the header carries a crc.
func adtsHeaderLen(frame []byte) int {
	if len(frame) > 1 && frame[1]&0x01 == 0 {
		return 9
	}
	return 7
}

// splitADTS cuts a buffer of ADTS frames into raw AAC payloads, returning
// the AudioSpecificConfig of the first frame. A buffer that does not start
// with a sync word is taken to be one raw frame already.
func splitADTS(data []byte) (frames [][]byte, asc []byte, err error) {
	if len(data) < 2 || data[0] != 0xFF || data[1]&0xF0 != 0xF0 {
		return [][]byte{data}, nil, nil
	}
	for len(data) > 0 {
		if len(data) < 7 || data[0] != 0xFF || data[1]&0xF0 != 0xF0 {
			return nil, nil, errors.New("mkv: truncated adts frame")
		}
		hdr := codec.NewAdtsFrameHeader()
		if err := hdr.Decode(data); err != nil {
			return nil, nil, err
		}
		size := int(hdr.Variable_Header.Frame_length)
		hl := adtsHeaderLen(data)
		if size < hl || size > len(data) {
			return nil, nil, errors.New("mkv: adts frame length out of range")
		}
		if asc == nil {
			c, err := codec.ConvertADTSToASC(data)
			if err != nil {
				return nil, nil, err
			}
			asc = c.Encode()
		}
		frames = append(frames, data[hl:size])
		data = data[size:]
	}
	return frames, asc, nil
}

// annexBToLengthPrefixed rewrites an access unit into the 4 byte length
// prefixed form avcC and hvcC tracks store, leaving out access unit
// delimiters and the parameter sets that CodecPrivate already carries.
func annexBToLengthPrefixed(au []byte, h265 bool) (out []byte, key bool) {
	out = make([]byte, 0, len(au)+16)
	codec.SplitFrame(au, func(nalu []byte) bool {
		if h265 {
			t := codec.H265NaluTypeWithoutStartCode(nalu)
			switch t {
			case codec.H265_NAL_AUD, codec.H265_NAL_VPS, codec.H265_NAL_SPS, codec.H265_NAL_PPS:
				return true
			}
			if t >= codec.H265_NAL_SLICE_BLA_W_LP && t <= codec.H265_NAL_SLICE_CRA {
				key = true
			}
		} else {
			t := codec.H264NaluTypeWithoutStartCode(nalu)
			switch t {
			case codec.H264_NAL_AUD, codec.H264_NAL_SPS, codec.H264_NAL_PPS:
				return true
			}
			if t == codec.H264_NAL_I_SLICE {
				key = true
			}
		}
		out = binary.BigEndian.AppendUint32(out, uint32(len(nalu)))
		out = append(out, nalu...)
		return true
	})
	return out, key
}

// lengthPrefixedToAnnexB turns a block of length prefixed nal units back
// into Annex-B. nalLen is the prefix size from the avcC/hvcC record.
func lengthPrefixedToAnnexB(block []byte, nalLen int) ([]byte, error) {
	out := make([]byte, 0, len(block)+16)
	for len(block) > 0 {
		if len(block) < nalLen {
			return nil, errors.New("mkv: truncated nal unit length")
		}
		var n uint64
		for i := 0; i < nalLen; i++ {
			n = n<<8 | uint64(block[i])
		}
		block = block[nalLen:]
		if n > uint64(len(block)) {
			return nil, errors.New("mkv: nal unit longer than its block")
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, block[:n]...)
		block = block[n:]
	}
	return out, nil
}

// isKeyAccessUnit reports whether a length prefixed access unit holds an
// IDR/IRAP slice, for Block elements that carry no key frame flag.
func isKeyAccessUnit(block []byte, nalLen int, h265 bool) bool {
	for len(block) > nalLen {
		var n uint64
		for i := 0; i < nalLen; i++ {
			n = n<<8 | uint64(block[i])
		}
		block = block[nalLen:]
		if n == 0 || n > uint64(len(block)) {
			return false
		}
		if h265 {
			t := codec.H265NaluTypeWithoutStartCode(block)
			if t >= codec.H265_NAL_SLICE_BLA_W_LP && t <= codec.H265_NAL_SLICE_CRA {
				return true
			}
		} else if codec.H264NaluTypeWithoutStartCode(block) == codec.H264_NAL_I_SLICE {
			return true
		}
		block = block[n:]
	}
	return false
}
