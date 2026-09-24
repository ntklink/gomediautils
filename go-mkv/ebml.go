package mkv

import (
	"encoding/binary"
	"errors"
	"math"
)

// Element ids used by the muxer and the demuxer. The ids keep their length
// marker bits, which is how they appear on the wire (RFC 8794 section 5 and
// RFC 9559 section 5.1).
const (
	idEBML               = 0x1A45DFA3
	idEBMLVersion        = 0x4286
	idEBMLReadVersion    = 0x42F7
	idEBMLMaxIDLength    = 0x42F2
	idEBMLMaxSizeLength  = 0x42F3
	idDocType            = 0x4282
	idDocTypeVersion     = 0x4287
	idDocTypeReadVersion = 0x4285
	idVoid               = 0xEC
	idCRC32              = 0xBF

	idSegment = 0x18538067

	idSeekHead     = 0x114D9B74
	idSeek         = 0x4DBB
	idSeekID       = 0x53AB
	idSeekPosition = 0x53AC

	idInfo           = 0x1549A966
	idTimestampScale = 0x2AD7B1
	idDuration       = 0x4489
	idMuxingApp      = 0x4D80
	idWritingApp     = 0x5741

	idTracks             = 0x1654AE6B
	idTrackEntry         = 0xAE
	idTrackNumber        = 0xD7
	idTrackUID           = 0x73C5
	idTrackType          = 0x83
	idFlagLacing         = 0x9C
	idFlagDefault        = 0x88
	idLanguage           = 0x22B59C
	idCodecID            = 0x86
	idCodecPrivate       = 0x63A2
	idCodecDelay         = 0x56AA
	idSeekPreRoll        = 0x56BB
	idDefaultDuration    = 0x23E383
	idVideo              = 0xE0
	idPixelWidth         = 0xB0
	idPixelHeight        = 0xBA
	idAudio              = 0xE1
	idSamplingFrequency  = 0xB5
	idChannels           = 0x9F
	idBitDepth           = 0x6264
	idContentEncodings   = 0x6D80
	idContentEncoding    = 0x6240
	idContentEncodingTyp = 0x5033
	idContentCompression = 0x5034
	idContentCompAlgo    = 0x4254
	idContentCompSetting = 0x4255

	idCluster        = 0x1F43B675
	idClusterTime    = 0xE7
	idSimpleBlock    = 0xA3
	idBlockGroup     = 0xA0
	idBlock          = 0xA1
	idReferenceBlock = 0xFB
	idDiscardPadding = 0x75A2

	idCues               = 0x1C53BB6B
	idCuePoint           = 0xBB
	idCueTime            = 0xB3
	idCueTrackPositions  = 0xB7
	idCueTrack           = 0xF7
	idCueClusterPosition = 0xF1

	idChapters    = 0x1043A770
	idTags        = 0x1254C367
	idAttachments = 0x1941A469
)

// unknownSize is the reserved all ones value of an 8 byte data size: the
// element runs until something that cannot be its child shows up. Live
// writers use it for the Segment and the Cluster.
const unknownSize = uint64(1<<56 - 1)

var (
	errBadVint   = errors.New("mkv: invalid variable size integer")
	errTooLarge  = errors.New("mkv: element size out of range")
	errBadHeader = errors.New("mkv: not an EBML stream")
)

// vintLen is how many bytes a variable size integer takes, read from the
// position of the first set bit of its first byte. 0 means the byte cannot
// start one.
func vintLen(first byte) int {
	for i := 0; i < 8; i++ {
		if first&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 0
}

// sizeLen is the shortest data size encoding that can hold v. The all ones
// value of each length is reserved for unknown size, so it is skipped.
func sizeLen(v uint64) int {
	n := 1
	for n < 8 && v >= (uint64(1)<<(7*n))-1 {
		n++
	}
	return n
}

func appendSize(buf []byte, v uint64, n int) []byte {
	v |= uint64(1) << (7 * n)
	for i := n - 1; i >= 0; i-- {
		buf = append(buf, byte(v>>(8*i)))
	}
	return buf
}

func appendID(buf []byte, id uint32) []byte {
	switch {
	case id >= 1<<24:
		return append(buf, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id >= 1<<16:
		return append(buf, byte(id>>16), byte(id>>8), byte(id))
	case id >= 1<<8:
		return append(buf, byte(id>>8), byte(id))
	default:
		return append(buf, byte(id))
	}
}

// element builds a complete element around an already encoded payload.
func element(id uint32, payload []byte) []byte {
	buf := make([]byte, 0, 4+8+len(payload))
	buf = appendID(buf, id)
	buf = appendSize(buf, uint64(len(payload)), sizeLen(uint64(len(payload))))
	return append(buf, payload...)
}

// master concatenates child elements into a master element.
func master(id uint32, children ...[]byte) []byte {
	n := 0
	for _, c := range children {
		n += len(c)
	}
	payload := make([]byte, 0, n)
	for _, c := range children {
		payload = append(payload, c...)
	}
	return element(id, payload)
}

func uintElement(id uint32, v uint64) []byte {
	n := 1
	for n < 8 && v>>(8*n) != 0 {
		n++
	}
	payload := make([]byte, n)
	for i := 0; i < n; i++ {
		payload[n-1-i] = byte(v >> (8 * i))
	}
	return element(id, payload)
}

func intElement(id uint32, v int64) []byte {
	n := 1
	for n < 8 && (v>>(8*n-1) != 0 && v>>(8*n-1) != -1) {
		n++
	}
	payload := make([]byte, n)
	for i := 0; i < n; i++ {
		payload[n-1-i] = byte(v >> (8 * i))
	}
	return element(id, payload)
}

func floatElement(id uint32, v float64) []byte {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, math.Float64bits(v))
	return element(id, payload)
}

func stringElement(id uint32, s string) []byte {
	return element(id, []byte(s))
}

// voidElement builds a Void element occupying exactly total bytes, which
// must be at least 2 (one byte id, one byte size) and at most 2^56.
func voidElement(total int) []byte {
	// a one byte size holds up to 126 bytes of payload; longer voids take an
	// 8 byte size so the element length never depends on rounding
	if total-2 < 127 {
		buf := appendSize([]byte{idVoid}, uint64(total-2), 1)
		return append(buf, make([]byte, total-2)...)
	}
	buf := appendSize([]byte{idVoid}, uint64(total-9), 8)
	return append(buf, make([]byte, total-9)...)
}

// readVint decodes a data size or a block's track number from the head of
// buf, reporting the value and how many bytes it took. The length marker is
// removed. unknown is set when every value bit is 1.
func readVint(buf []byte) (v uint64, n int, unknown bool, err error) {
	if len(buf) == 0 {
		return 0, 0, false, errBadVint
	}
	n = vintLen(buf[0])
	if n == 0 || len(buf) < n {
		return 0, 0, false, errBadVint
	}
	v = uint64(buf[0]) & (0xFF >> n)
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(buf[i])
	}
	return v, n, v == (uint64(1)<<(7*n))-1, nil
}

func readUint(payload []byte) (uint64, error) {
	if len(payload) > 8 {
		return 0, errTooLarge
	}
	var v uint64
	for _, b := range payload {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

func readInt(payload []byte) (int64, error) {
	if len(payload) > 8 {
		return 0, errTooLarge
	}
	var v int64
	for i, b := range payload {
		if i == 0 {
			v = int64(int8(b))
		} else {
			v = v<<8 | int64(b)
		}
	}
	return v, nil
}

func readFloat(payload []byte) (float64, error) {
	switch len(payload) {
	case 0:
		return 0, nil
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(payload))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(payload)), nil
	default:
		return 0, errors.New("mkv: float element must be 0, 4 or 8 bytes")
	}
}

// readString drops the zero padding a string element may carry.
func readString(payload []byte) string {
	for i, b := range payload {
		if b == 0 {
			return string(payload[:i])
		}
	}
	return string(payload)
}
