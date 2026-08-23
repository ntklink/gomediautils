package flv

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestEncodeOnMetaData(t *testing.T) {
	got, err := EncodeOnMetaData(map[string]interface{}{
		"videocodecid": float64(7),
		"audiocall":    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x02, 0x00, 0x0a, 'o', 'n', 'M', 'e', 't', 'a', 'D', 'a', 't', 'a',
		0x08, 0x00, 0x00, 0x00, 0x02,
		0x00, 0x09, 'a', 'u', 'd', 'i', 'o', 'c', 'a', 'l', 'l', 0x01, 0x01,
		0x00, 0x0c, 'v', 'i', 'd', 'e', 'o', 'c', 'o', 'd', 'e', 'c', 'i', 'd', 0x00, 0x40, 0x1c, 0, 0, 0, 0, 0, 0,
		0x00, 0x00, 0x09,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got\n% x\nwant\n% x", got, want)
	}
}

func TestAmf0LongStringRoundTrip(t *testing.T) {
	long := strings.Repeat("x", 70000)
	enc := EncodeAmf0String(nil, long)
	if enc[0] != AMF0_TYPE_LONG_STRING {
		t.Fatalf("marker %#x, want long string", enc[0])
	}
	if len(enc) != 5+len(long) {
		t.Fatalf("encoded len %d", len(enc))
	}
	got, n, err := DecodeAmf0String(enc)
	if err != nil || n != len(enc) || got != long {
		t.Fatalf("round trip failed err=%v n=%d eq=%v", err, n, got == long)
	}

	short := "hello"
	enc = EncodeAmf0String(nil, short)
	if enc[0] != AMF0_TYPE_STRING {
		t.Fatalf("marker %#x, want short string", enc[0])
	}
	got, n, err = DecodeAmf0String(enc)
	if err != nil || n != len(enc) || got != short {
		t.Fatalf("short round trip failed err=%v n=%d got=%q", err, n, got)
	}

	if _, _, err = DecodeAmf0String(enc[:3]); err == nil {
		t.Fatal("truncated string must fail")
	}
}

func TestAmf0LongStringValueInArray(t *testing.T) {
	long := strings.Repeat("y", 65536)
	body, err := EncodeOnMetaData(map[string]interface{}{"comment": long})
	if err != nil {
		t.Fatal(err)
	}
	// "onMetaData" string + array marker + count + key
	off := 3 + len("onMetaData") + 1 + 4 + 2 + len("comment")
	got, _, err := DecodeAmf0String(body[off:])
	if err != nil || got != long {
		t.Fatalf("long value not decodable: %v", err)
	}
}

func TestAmf0Errors(t *testing.T) {
	longKey := strings.Repeat("k", 65536)
	if _, err := EncodeAmf0EcmaArray(nil, map[string]interface{}{longKey: 1}); !errors.Is(err, ErrAmf0KeyTooLong) {
		t.Fatalf("want ErrAmf0KeyTooLong, got %v", err)
	}
	if _, err := EncodeAmf0EcmaArray(nil, map[string]interface{}{"nested": map[string]interface{}{}}); !errors.Is(err, ErrAmf0UnsupportedValue) {
		t.Fatalf("want ErrAmf0UnsupportedValue, got %v", err)
	}
	if _, err := EncodeAmf0EcmaArray(nil, map[string]interface{}{"list": []int{1}}); !errors.Is(err, ErrAmf0UnsupportedValue) {
		t.Fatalf("want ErrAmf0UnsupportedValue, got %v", err)
	}

	// a bad entry fails the whole metadata object instead of being dropped
	if _, err := EncodeOnMetaData(map[string]interface{}{
		"videocodecid": float64(7),
		"nested":       []int{1},
	}); !errors.Is(err, ErrAmf0UnsupportedValue) {
		t.Fatalf("want ErrAmf0UnsupportedValue, got %v", err)
	}
	if _, err := EncodeOnMetaData(map[string]interface{}{longKey: 2}); !errors.Is(err, ErrAmf0KeyTooLong) {
		t.Fatalf("want ErrAmf0KeyTooLong, got %v", err)
	}
}
