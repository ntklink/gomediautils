package flv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
)

// AMF0 type markers used by script data
const (
	AMF0_TYPE_NUMBER      = 0x00
	AMF0_TYPE_BOOLEAN     = 0x01
	AMF0_TYPE_STRING      = 0x02
	AMF0_TYPE_ECMA_ARRAY  = 0x08
	AMF0_TYPE_OBJECT_END  = 0x09
	AMF0_TYPE_LONG_STRING = 0x0C
)

const amf0MaxShortString = 0xFFFF

var (
	ErrAmf0KeyTooLong       = errors.New("amf0: object key longer than 65535 bytes")
	ErrAmf0UnsupportedValue = errors.New("amf0: unsupported value type")
	ErrAmf0Truncated        = errors.New("amf0: truncated data")
	ErrAmf0NotString        = errors.New("amf0: not a string marker")
)

// EncodeAmf0String appends s as an AMF0 string. Strings longer than 65535
// bytes are written with the long-string marker (0x0C, 32-bit length).
func EncodeAmf0String(dst []byte, s string) []byte {
	if len(s) > amf0MaxShortString {
		dst = append(dst, AMF0_TYPE_LONG_STRING)
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(s)))
		dst = append(dst, l[:]...)
		return append(dst, s...)
	}
	dst = append(dst, AMF0_TYPE_STRING)
	return appendAmf0Utf8(dst, s)
}

// DecodeAmf0String decodes an AMF0 string or long string at the start of data
// and returns the value and the number of bytes consumed.
func DecodeAmf0String(data []byte) (string, int, error) {
	if len(data) < 1 {
		return "", 0, ErrAmf0Truncated
	}
	switch data[0] {
	case AMF0_TYPE_STRING:
		if len(data) < 3 {
			return "", 0, ErrAmf0Truncated
		}
		n := int(binary.BigEndian.Uint16(data[1:]))
		if len(data) < 3+n {
			return "", 0, ErrAmf0Truncated
		}
		return string(data[3 : 3+n]), 3 + n, nil
	case AMF0_TYPE_LONG_STRING:
		if len(data) < 5 {
			return "", 0, ErrAmf0Truncated
		}
		n := binary.BigEndian.Uint32(data[1:])
		if uint64(len(data)) < 5+uint64(n) {
			return "", 0, ErrAmf0Truncated
		}
		return string(data[5 : 5+n]), 5 + int(n), nil
	default:
		return "", 0, ErrAmf0NotString
	}
}

func EncodeAmf0Number(dst []byte, v float64) []byte {
	dst = append(dst, AMF0_TYPE_NUMBER)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(v))
	return append(dst, b[:]...)
}

func EncodeAmf0Boolean(dst []byte, v bool) []byte {
	dst = append(dst, AMF0_TYPE_BOOLEAN)
	if v {
		return append(dst, 1)
	}
	return append(dst, 0)
}

// EncodeAmf0EcmaArray appends values as an AMF0 ECMA array. Keys are sorted
// so the output is deterministic. It fails on keys longer than 65535 bytes and
// on values of unsupported types (nested maps, slices, ...) instead of
// silently dropping them.
func EncodeAmf0EcmaArray(dst []byte, values map[string]interface{}) ([]byte, error) {
	keys := make([]string, 0, len(values))
	for k := range values {
		if len(k) > amf0MaxShortString {
			return dst, fmt.Errorf("%w: %q...", ErrAmf0KeyTooLong, k[:16])
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	dst = append(dst, AMF0_TYPE_ECMA_ARRAY)
	var cnt [4]byte
	binary.BigEndian.PutUint32(cnt[:], uint32(len(values)))
	dst = append(dst, cnt[:]...)

	var err error
	for _, k := range keys {
		dst = appendAmf0Utf8(dst, k)
		if dst, err = encodeAmf0Value(dst, values[k]); err != nil {
			return dst, fmt.Errorf("%w: key %q (%T)", ErrAmf0UnsupportedValue, k, values[k])
		}
	}
	return append(dst, 0, 0, AMF0_TYPE_OBJECT_END), nil
}

// EncodeOnMetaData builds the script tag body "onMetaData" + ECMA array. It
// fails on entries that cannot be represented in AMF0.
func EncodeOnMetaData(values map[string]interface{}) ([]byte, error) {
	dst := make([]byte, 0, 64+32*len(values))
	dst = EncodeAmf0String(dst, "onMetaData")
	return EncodeAmf0EcmaArray(dst, values)
}

func appendAmf0Utf8(dst []byte, s string) []byte {
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(s)))
	dst = append(dst, l[:]...)
	return append(dst, s...)
}

func encodeAmf0Value(dst []byte, v interface{}) ([]byte, error) {
	switch t := v.(type) {
	case float64:
		return EncodeAmf0Number(dst, t), nil
	case float32:
		return EncodeAmf0Number(dst, float64(t)), nil
	case int:
		return EncodeAmf0Number(dst, float64(t)), nil
	case int8:
		return EncodeAmf0Number(dst, float64(t)), nil
	case int16:
		return EncodeAmf0Number(dst, float64(t)), nil
	case int32:
		return EncodeAmf0Number(dst, float64(t)), nil
	case int64:
		return EncodeAmf0Number(dst, float64(t)), nil
	case uint:
		return EncodeAmf0Number(dst, float64(t)), nil
	case uint8:
		return EncodeAmf0Number(dst, float64(t)), nil
	case uint16:
		return EncodeAmf0Number(dst, float64(t)), nil
	case uint32:
		return EncodeAmf0Number(dst, float64(t)), nil
	case uint64:
		return EncodeAmf0Number(dst, float64(t)), nil
	case bool:
		return EncodeAmf0Boolean(dst, t), nil
	case string:
		return EncodeAmf0String(dst, t), nil
	default:
		return dst, ErrAmf0UnsupportedValue
	}
}
