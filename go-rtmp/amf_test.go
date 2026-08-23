package rtmp

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
)

func amfNumber(v float64) []byte {
	b := make([]byte, 9)
	b[0] = byte(AMF0_NUMBER)
	binary.BigEndian.PutUint64(b[1:], math.Float64bits(v))
	return b
}

func amfString(s string) []byte {
	b := make([]byte, 3+len(s))
	b[0] = byte(AMF0_STRING)
	binary.BigEndian.PutUint16(b[1:], uint16(len(s)))
	copy(b[3:], s)
	return b
}

func amfKey(s string) []byte {
	b := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(b, uint16(len(s)))
	copy(b[2:], s)
	return b
}

func TestDecodeAmf0EcmaArrayAndNested(t *testing.T) {
	// number, ecma array { a: 1, nested: {x: "y"}, arr: [1, "s"], d: date, ls: longstring }, object {code: "ok"}
	var data []byte
	data = append(data, amfNumber(3)...)
	ecma := []byte{byte(AMF0_ECMA_ARRAY), 0, 0, 0, 5}
	ecma = append(ecma, amfKey("a")...)
	ecma = append(ecma, amfNumber(1)...)
	ecma = append(ecma, amfKey("nested")...)
	ecma = append(ecma, byte(AMF0_OBJECT))
	ecma = append(ecma, amfKey("x")...)
	ecma = append(ecma, amfString("y")...)
	ecma = append(ecma, EndObj...)
	ecma = append(ecma, amfKey("arr")...)
	ecma = append(ecma, byte(AMF0_STRICT_ARRAY), 0, 0, 0, 2)
	ecma = append(ecma, amfNumber(1)...)
	ecma = append(ecma, amfString("s")...)
	ecma = append(ecma, amfKey("d")...)
	ecma = append(ecma, byte(AMF0_DATE), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	ecma = append(ecma, amfKey("ls")...)
	ecma = append(ecma, byte(AMF0_LONG_STRING), 0, 0, 0, 3, 'a', 'b', 'c')
	ecma = append(ecma, amfKey("typed")...)
	ecma = append(ecma, byte(AMF0_TYPED_OBJECT))
	ecma = append(ecma, amfKey("cls")...)
	ecma = append(ecma, amfKey("k")...)
	ecma = append(ecma, amfNumber(2)...)
	ecma = append(ecma, EndObj...)
	ecma = append(ecma, EndObj...)
	data = append(data, ecma...)
	obj := []byte{byte(AMF0_OBJECT)}
	obj = append(obj, amfKey("code")...)
	obj = append(obj, amfString("ok")...)
	obj = append(obj, EndObj...)
	data = append(data, obj...)

	items, objs, err := decodeAmf0(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].value.(float64) != 3 {
		t.Fatalf("unexpected items %v", items)
	}
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objs))
	}
	if len(objs[0].items) != 6 {
		t.Fatalf("expected 6 ecma array entries, got %d", len(objs[0].items))
	}
	if objs[0].items[0].name != "a" || objs[0].items[0].value.value.(float64) != 1 {
		t.Fatalf("bad first entry")
	}
	if objs[0].items[1].amfTypeOf() != AMF0_OBJECT || objs[0].items[2].amfTypeOf() != AMF0_STRICT_ARRAY {
		t.Fatalf("nested types not preserved")
	}
	if string(objs[0].items[4].value.value.([]byte)) != "abc" {
		t.Fatalf("long string value not set")
	}
	if len(objs[1].items) != 1 || objs[1].items[0].name != "code" || amf0String(objs[1].items[0].value) != "ok" {
		t.Fatalf("bad object")
	}

	// every truncation of the input must return an error, never panic
	for i := 0; i < len(data); i++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on truncated input of %d bytes: %v", i, r)
				}
			}()
			decodeAmf0(data[:i])
		}()
	}
}

func (it *amfObjectItem) amfTypeOf() AMF0_DATA_TYPE {
	return it.value.amfType
}

func TestDecodeAmf0Garbage(t *testing.T) {
	inputs := [][]byte{
		{},
		{byte(AMF0_STRING)},
		{byte(AMF0_STRING), 0xff, 0xff, 'a'},
		{byte(AMF0_NUMBER), 1, 2},
		{byte(AMF0_OBJECT)},
		{byte(AMF0_OBJECT), 0, 1, 'a'},
		{byte(AMF0_ECMA_ARRAY), 0, 0},
		{byte(AMF0_STRICT_ARRAY), 0xff, 0xff, 0xff, 0xff, 0},
		{byte(AMF0_LONG_STRING), 0xff, 0xff, 0xff, 0xff},
		{0x42},
		{byte(AMF0_AVMPLUS_OBJECT), 1, 2, 3},
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %v: %v", in, r)
				}
			}()
			if _, _, err := decodeAmf0(in); err == nil && len(in) > 0 {
				t.Fatalf("expected error for %v", in)
			}
		}()
	}
}

func TestDecodeUserControlMsg(t *testing.T) {
	if _, err := decodeUserControlMsg([]byte{0, 6, 0}); err == nil {
		t.Fatal("short ping must fail")
	}
	ev, err := decodeUserControlMsg(makeUserControlMessage(PingRequest, 42))
	if err != nil || ev.code != PingRequest || ev.data[0] != 42 {
		t.Fatalf("bad ping decode %v %v", ev, err)
	}
	if _, err := decodeUserControlMsg(makeUserControlMessage(SetBufferLength, 1)); err == nil {
		t.Fatal("6 byte set buffer length must fail")
	}
	msg := append(makeUserControlMessage(SetBufferLength, 1), 0, 0, 0, 9)
	ev, err = decodeUserControlMsg(msg)
	if err != nil || len(ev.data) != 2 || ev.data[1] != 9 {
		t.Fatalf("bad set buffer length decode %v %v", ev, err)
	}
}

func TestAmf0EncodeErrors(t *testing.T) {
	long := strings.Repeat("x", 65536)

	// a string value that does not fit the 16 bit length field must be reported
	longStr := makeStringItem(long)
	if _, err := longStr.appendTo(nil); !errors.Is(err, errAmf0StringTooLong) {
		t.Fatalf("want errAmf0StringTooLong, got %v", err)
	}
	// so must an over long object key
	obj := amfObject{items: []*amfObjectItem{{name: long, value: makeNumberItem(1)}}}
	if _, err := obj.appendTo(nil); !errors.Is(err, errAmf0StringTooLong) {
		t.Fatalf("want errAmf0StringTooLong for the key, got %v", err)
	}
	// an item whose value does not match its marker is rejected, not asserted on
	bad := amf0Item{amfType: AMF0_STRING, value: []byte("decoded")}
	if _, err := bad.appendTo(nil); !errors.Is(err, errAmf0BadValue) {
		t.Fatalf("want errAmf0BadValue, got %v", err)
	}
	// types the encoder does not implement are an error instead of a panic
	if _, err := (&amf0Item{amfType: AMF0_ECMA_ARRAY}).appendTo(nil); err == nil {
		t.Fatal("encoding an ecma array must fail")
	}
	if _, err := (&amf0Item{amfType: AMF0_DATA_TYPE(0x42)}).appendTo(nil); err == nil {
		t.Fatal("encoding an unknown type must fail")
	}

	// the command builders propagate it
	if _, err := makePublish(long, PUBLISHING_LIVE); !errors.Is(err, errAmf0StringTooLong) {
		t.Fatalf("makePublish: %v", err)
	}
	if _, err := makeStatusRes(1, NETSTREAM_PLAY_START, LEVEL_STATUS, long); !errors.Is(err, errAmf0StringTooLong) {
		t.Fatalf("makeStatusRes: %v", err)
	}
	if _, err := makeConnect(long, "rtmp://host/live"); !errors.Is(err, errAmf0StringTooLong) {
		t.Fatalf("makeConnect: %v", err)
	}
	// a message that fits is still built
	if b, err := makeConnect("live", "rtmp://host/live"); err != nil || len(b) == 0 {
		t.Fatalf("makeConnect: %v", err)
	}
}

func TestDecodeAmf0ReturnsNothingOnError(t *testing.T) {
	// a valid number followed by a truncated string
	data := append(amfNumber(1), byte(AMF0_STRING), 0xFF, 0xFF, 'a')
	items, objs, err := decodeAmf0(data)
	if err == nil {
		t.Fatal("expected an error")
	}
	if items != nil || objs != nil {
		t.Fatalf("partially decoded data leaked: %d items %d objects", len(items), len(objs))
	}
}
