package rtmp

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestChunkReaderReleasesHighWaterBuffer(t *testing.T) {
	reader := newChunkStreamReader(4096)
	writer := newChunkStreamWriter(6)
	writer.chunkSize = 4096

	large := make([]byte, 128*1024)
	for i := range large {
		large[i] = byte(i)
	}
	wire, err := writer.writeData(large, VIDEO, 1, 100)
	if err != nil {
		t.Fatalf("writeData: %v", err)
	}

	var got []byte
	if err := reader.readRtmpMessage(wire, func(msg *rtmpMessage) error {
		got = msg.msg
		return nil
	}); err != nil {
		t.Fatalf("readRtmpMessage: %v", err)
	}
	if len(got) != len(large) || got[0] != large[0] || got[len(got)-1] != large[len(large)-1] {
		t.Fatalf("payload mismatch: len=%d", len(got))
	}
	cs := reader.cks[6]
	if cs == nil {
		t.Fatal("missing chunk stream")
	}
	if cap(cs.message) > 64*1024 {
		t.Fatalf("expected high-water buffer to be released, got cap=%d", cap(cs.message))
	}
	if reader.buffered != 0 {
		t.Fatalf("expected buffered=0, got %d", reader.buffered)
	}
}

func TestAmf0MaxDepthRejected(t *testing.T) {
	// Deeply nested AMF0 objects: { "a": { "a": ... } }
	var obj []byte
	for i := 0; i < maxAmf0Depth+10; i++ {
		obj = append(obj, byte(AMF0_OBJECT), 0x00, 0x01, 'a')
	}
	obj = append(obj, byte(AMF0_NULL))
	for i := 0; i < maxAmf0Depth+10; i++ {
		obj = append(obj, 0x00, 0x00, byte(AMF0_OBJECT_END))
	}
	if _, _, err := decodeAmf0(obj); !errors.Is(err, errAmf0MaxDepth) {
		t.Fatalf("nested object: expected errAmf0MaxDepth, got %v", err)
	}

	// Deeply nested AMF0 strict arrays
	var arr []byte
	for i := 0; i < maxAmf0Depth+10; i++ {
		arr = append(arr, byte(AMF0_STRICT_ARRAY), 0, 0, 0, 1)
	}
	arr = append(arr, byte(AMF0_NULL))
	if _, _, err := decodeAmf0(arr); !errors.Is(err, errAmf0MaxDepth) {
		t.Fatalf("nested strict array: expected errAmf0MaxDepth, got %v", err)
	}
}

func TestChunkStreamWriterNoMutationOnInvalidOrEmpty(t *testing.T) {
	w := newChunkStreamWriter(4)
	if pkt, err := w.writeData(nil, VIDEO, 1, 500); err != nil || pkt != nil {
		t.Fatalf("empty writeData: pkt=%v err=%v", pkt, err)
	}
	if w.current != nil || w.timestamp != 0 {
		t.Fatalf("empty writeData mutated state: current=%v ts=%d", w.current, w.timestamp)
	}

	oversized := make([]byte, maxRtmpMessageLen+1)
	if _, err := w.writeData(oversized, VIDEO, 1, 900); err == nil {
		t.Fatal("expected error for oversized message")
	}
	if w.current != nil || w.timestamp != 0 {
		t.Fatalf("oversized writeData mutated state: current=%v ts=%d", w.current, w.timestamp)
	}
}

func TestAbortMessageResetsChunkStream(t *testing.T) {
	check := func(name string, reader *chunkStreamReader, handle func(*rtmpMessage) error) {
		t.Helper()
		w := newChunkStreamWriter(6)
		w.chunkSize = 128
		wire, err := w.writeData(make([]byte, 300), VIDEO, 1, 10)
		if err != nil {
			t.Fatalf("%s: writeData: %v", name, err)
		}
		// Feed only the first chunk (partial message)
		if err := reader.readRtmpMessage(wire[:140], handle); err != nil {
			t.Fatalf("%s: partial read: %v", name, err)
		}
		if reader.buffered == 0 || len(reader.cks[6].message) == 0 {
			t.Fatalf("%s: expected buffered partial message", name)
		}
		var abortPayload [4]byte
		binary.BigEndian.PutUint32(abortPayload[:], 6)
		if err := handle(&rtmpMessage{msgtype: ABORT_MESSAGE, msg: abortPayload[:]}); err != nil {
			t.Fatalf("%s: abort failed: %v", name, err)
		}
		if reader.buffered != 0 {
			t.Fatalf("%s: expected buffered=0 after abort, got %d", name, reader.buffered)
		}
		if reader.cks[6].message != nil || reader.cks[6].chunkRead != 0 {
			t.Fatalf("%s: chunk stream not reset after abort", name)
		}
	}

	cli := NewRtmpClient()
	check("client", cli.reader, cli.handleMessage)

	srv := NewRtmpServerHandle()
	check("server", srv.reader, srv.handleMessage)
}
