package rtmp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

type AMF0_DATA_TYPE int

const (
	AMF0_NUMBER AMF0_DATA_TYPE = iota
	AMF0_BOOLEAN
	AMF0_STRING
	AMF0_OBJECT
	AMF0_MOVIECLIP
	AMF0_NULL
	AMF0_UNDEFINED
	AMF0_REFERENCE
	AMF0_ECMA_ARRAY
	AMF0_OBJECT_END
	AMF0_STRICT_ARRAY
	AMF0_DATE
	AMF0_LONG_STRING
	AMF0_UNSUPPORTED
	AMF0_RECORDSET
	AMF0_XML_DOCUMENT
	AMF0_TYPED_OBJECT
	AMF0_AVMPLUS_OBJECT
)

var NullItem []byte = []byte{byte(AMF0_NULL)}
var EndObj []byte = []byte{0, 0, byte(AMF0_OBJECT_END)}

const amf0MaxStringLen = 0xFFFF

var (
	errAmf0Truncated     = errors.New("amf0 data truncated")
	errAmf0StringTooLong = errors.New("amf0: string longer than 65535 bytes")
	errAmf0BadValue      = errors.New("amf0: value does not match its type marker")
)

type amf0Item struct {
	amfType AMF0_DATA_TYPE
	length  int
	value   interface{}
}

// appendTo encodes the item and appends it to dst. It reports an error for
// values that amf0 cannot represent (an over long string) and for items whose
// value does not match their type marker, instead of truncating or panicking.
func (amf *amf0Item) appendTo(dst []byte) ([]byte, error) {
	switch amf.amfType {
	case AMF0_NUMBER:
		v, ok := amf.value.(float64)
		if !ok {
			return dst, fmt.Errorf("%w: number", errAmf0BadValue)
		}
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(v))
		return append(append(dst, byte(AMF0_NUMBER)), b[:]...), nil
	case AMF0_BOOLEAN:
		v, ok := amf.value.(bool)
		if !ok {
			return dst, fmt.Errorf("%w: boolean", errAmf0BadValue)
		}
		if v {
			return append(dst, byte(AMF0_BOOLEAN), 1), nil
		}
		return append(dst, byte(AMF0_BOOLEAN), 0), nil
	case AMF0_STRING:
		v, ok := amf.value.(string)
		if !ok {
			return dst, fmt.Errorf("%w: string", errAmf0BadValue)
		}
		if len(v) > amf0MaxStringLen {
			return dst, fmt.Errorf("%w: %d bytes", errAmf0StringTooLong, len(v))
		}
		dst = append(dst, byte(AMF0_STRING), byte(len(v)>>8), byte(len(v)))
		return append(dst, v...), nil
	case AMF0_NULL:
		return append(dst, byte(AMF0_NULL)), nil
	default:
		return dst, fmt.Errorf("amf0: cannot encode type %d", amf.amfType)
	}
}

// amf0Buf accumulates encoded amf0 values, keeping the first encoding error so
// that a whole message can be built without checking every single value
type amf0Buf struct {
	buf []byte
	err error
}

func (b *amf0Buf) item(it amf0Item) *amf0Buf {
	if b.err == nil {
		b.buf, b.err = it.appendTo(b.buf)
	}
	return b
}

func (b *amf0Buf) object(obj *amfObject) *amf0Buf {
	if b.err == nil {
		b.buf, b.err = obj.appendTo(b.buf)
	}
	return b
}

func (b *amf0Buf) null() *amf0Buf {
	if b.err == nil {
		b.buf = append(b.buf, NullItem...)
	}
	return b
}

func (b *amf0Buf) bytes() ([]byte, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.buf, nil
}

// amf0SkipObjectBody skips the (name, value) pairs of an object/ecma array body up to and
// including the end marker, returns the number of bytes consumed
func amf0SkipObjectBody(data []byte) (int, error) {
	total := 0
	for {
		if len(data) < 3 {
			return 0, errAmf0Truncated
		}
		if data[0] == 0 && data[1] == 0 && data[2] == byte(AMF0_OBJECT_END) {
			return total + 3, nil
		}
		nameLen := int(binary.BigEndian.Uint16(data))
		if len(data) < 2+nameLen {
			return 0, errAmf0Truncated
		}
		l, err := amf0Skip(data[2+nameLen:])
		if err != nil {
			return 0, err
		}
		data = data[2+nameLen+l:]
		total += 2 + nameLen + l
	}
}

// amf0Skip returns the encoded size of the amf0 value at the start of data, without decoding it
func amf0Skip(data []byte) (int, error) {
	if len(data) < 1 {
		return 0, errAmf0Truncated
	}
	need := func(n int) error {
		if len(data) < n {
			return errAmf0Truncated
		}
		return nil
	}
	switch AMF0_DATA_TYPE(data[0]) {
	case AMF0_NUMBER:
		return 9, need(9)
	case AMF0_BOOLEAN:
		return 2, need(2)
	case AMF0_STRING:
		if err := need(3); err != nil {
			return 0, err
		}
		l := 3 + int(binary.BigEndian.Uint16(data[1:]))
		return l, need(l)
	case AMF0_OBJECT:
		l, err := amf0SkipObjectBody(data[1:])
		return 1 + l, err
	case AMF0_NULL, AMF0_UNDEFINED, AMF0_UNSUPPORTED:
		return 1, nil
	case AMF0_REFERENCE:
		return 3, need(3)
	case AMF0_ECMA_ARRAY:
		if err := need(5); err != nil {
			return 0, err
		}
		l, err := amf0SkipObjectBody(data[5:])
		return 5 + l, err
	case AMF0_OBJECT_END:
		return 3, need(3)
	case AMF0_STRICT_ARRAY:
		if err := need(5); err != nil {
			return 0, err
		}
		count := int(binary.BigEndian.Uint32(data[1:]))
		total := 5
		for i := 0; i < count; i++ {
			l, err := amf0Skip(data[total:])
			if err != nil {
				return 0, err
			}
			total += l
		}
		return total, nil
	case AMF0_DATE:
		return 11, need(11)
	case AMF0_LONG_STRING, AMF0_XML_DOCUMENT:
		if err := need(5); err != nil {
			return 0, err
		}
		l := 5 + int(binary.BigEndian.Uint32(data[1:]))
		return l, need(l)
	case AMF0_TYPED_OBJECT:
		if err := need(3); err != nil {
			return 0, err
		}
		nameLen := int(binary.BigEndian.Uint16(data[1:]))
		if err := need(3 + nameLen); err != nil {
			return 0, err
		}
		l, err := amf0SkipObjectBody(data[3+nameLen:])
		return 3 + nameLen + l, err
	default:
		return 0, fmt.Errorf("unsupport amf type %d", data[0])
	}
}

// decode parses one amf0 value, returns the number of bytes consumed. Nested/complex types are
// skipped (value is nil), truncated input returns an error instead of panicking
func (amf *amf0Item) decode(data []byte) (int, error) {
	if len(data) < 1 {
		return 0, errAmf0Truncated
	}
	amf.amfType = AMF0_DATA_TYPE(data[0])
	amf.value = nil
	amf.length = 0
	switch amf.amfType {
	case AMF0_NUMBER:
		if len(data) < 9 {
			return 0, errAmf0Truncated
		}
		amf.length = 8
		amf.value = math.Float64frombits(binary.BigEndian.Uint64(data[1:]))
		return 9, nil
	case AMF0_BOOLEAN:
		if len(data) < 2 {
			return 0, errAmf0Truncated
		}
		amf.length = 1
		amf.value = data[1] == 1
		return 2, nil
	case AMF0_STRING:
		if len(data) < 3 {
			return 0, errAmf0Truncated
		}
		amf.length = int(binary.BigEndian.Uint16(data[1:]))
		if len(data) < 3+amf.length {
			return 0, errAmf0Truncated
		}
		str := make([]byte, amf.length)
		copy(str, data[3:3+amf.length])
		amf.value = str
		return 3 + amf.length, nil
	case AMF0_LONG_STRING:
		if len(data) < 5 {
			return 0, errAmf0Truncated
		}
		amf.length = int(binary.BigEndian.Uint32(data[1:]))
		if len(data) < 5+amf.length {
			return 0, errAmf0Truncated
		}
		str := make([]byte, amf.length)
		copy(str, data[5:5+amf.length])
		amf.value = str
		return 5 + amf.length, nil
	case AMF0_NULL, AMF0_UNDEFINED, AMF0_UNSUPPORTED:
		return 1, nil
	default:
		l, err := amf0Skip(data)
		if err != nil {
			return 0, err
		}
		amf.length = l
		return l, nil
	}
}

func makeStringItem(str string) amf0Item {
	item := amf0Item{
		amfType: AMF0_STRING,
		length:  len(str),
		value:   str,
	}
	return item
}

func makeNumberItem(num float64) amf0Item {
	item := amf0Item{
		amfType: AMF0_NUMBER,
		value:   num,
	}
	return item
}

func makeBoolItem(v bool) amf0Item {
	item := amf0Item{
		amfType: AMF0_BOOLEAN,
		value:   v,
	}
	return item
}

type amfObjectItem struct {
	name  string
	value amf0Item
}

type amfObject struct {
	items []*amfObjectItem
}

// appendTo encodes the object (marker, (name, value) pairs, end marker) and
// appends it to dst
func (object *amfObject) appendTo(dst []byte) ([]byte, error) {
	dst = append(dst, byte(AMF0_OBJECT))
	for _, item := range object.items {
		if len(item.name) > amf0MaxStringLen {
			return dst, fmt.Errorf("%w: key %d bytes", errAmf0StringTooLong, len(item.name))
		}
		dst = append(dst, byte(len(item.name)>>8), byte(len(item.name)))
		dst = append(dst, item.name...)
		var err error
		if dst, err = item.value.appendTo(dst); err != nil {
			return dst, fmt.Errorf("amf0: object key %q: %w", item.name, err)
		}
	}
	return append(dst, EndObj...), nil
}

// decodeBody parses (name, value) pairs up to and including the object end marker
func (object *amfObject) decodeBody(data []byte) (int, error) {
	total := 0
	for {
		if len(data) < 3 {
			return 0, errAmf0Truncated
		}
		if data[0] == 0x00 && data[1] == 0x00 && data[2] == byte(AMF0_OBJECT_END) {
			return total + 3, nil
		}
		length := int(binary.BigEndian.Uint16(data))
		if len(data) < 2+length {
			return 0, errAmf0Truncated
		}
		name := string(data[2 : 2+length])
		item := amf0Item{}
		l, err := item.decode(data[2+length:])
		if err != nil {
			return 0, err
		}
		object.items = append(object.items, &amfObjectItem{
			name:  name,
			value: item,
		})
		data = data[2+length+l:]
		total += 2 + length + l
	}
}

// decode parses an AMF0_OBJECT (1 byte marker + body)
func (object *amfObject) decode(data []byte) (int, error) {
	if len(data) < 1 {
		return 0, errAmf0Truncated
	}
	l, err := object.decodeBody(data[1:])
	if err != nil {
		return 0, err
	}
	return 1 + l, nil
}

// decodeAmf0 decodes a sequence of amf0 values. Scalars go into items (nested values are kept as
// items with nil value), top level objects and ecma arrays go into objs. A malformed value fails
// the whole sequence: nothing is returned besides the error, so a caller can never act on half a
// message
func decodeAmf0(data []byte) (items []amf0Item, objs []amfObject, err error) {
	for len(data) > 0 {
		var l int
		switch AMF0_DATA_TYPE(data[0]) {
		case AMF0_ECMA_ARRAY:
			// 1 byte marker + 4 byte associative count, then the same body as an object
			if len(data) < 5 {
				return nil, nil, errAmf0Truncated
			}
			obj := amfObject{}
			l, err = obj.decodeBody(data[5:])
			if err != nil {
				return nil, nil, err
			}
			l += 5
			objs = append(objs, obj)
		case AMF0_OBJECT:
			obj := amfObject{}
			l, err = obj.decode(data)
			if err != nil {
				return nil, nil, err
			}
			objs = append(objs, obj)
		default:
			item := amf0Item{}
			l, err = item.decode(data)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, item)
		}
		data = data[l:]
	}
	return items, objs, nil
}
