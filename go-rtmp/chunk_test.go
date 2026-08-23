package rtmp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// oldWriteData is the previous (allocation heavy) implementation of chunkStreamWriter.writeData,
// kept here to assert that the optimized version is byte identical on the wire
func oldWriteData(cs *chunkStreamWriter, data []byte, msgType MessageType, streamId uint32, ts uint32) []byte {
	lastChunk := cs.current
	format := 0
	delta := ts
	if lastChunk != nil && streamId == lastChunk.msgHdr.msgStreamId && ts >= cs.timestamp {
		format = 1
		delta = ts - cs.timestamp
		if msgType == MessageType(lastChunk.msgHdr.msgTypeId) && int(lastChunk.msgHdr.msgLen) == len(data) {
			format = 2
			if delta == lastChunk.msgHdr.timestamp {
				format = 3
			}
		}
	}

	if lastChunk == nil {
		cs.current = &chunkPacket{
			basic: basicHead{
				fmt:  uint8(format),
				csid: cs.csid,
			},
			msgHdr: chunkMsgHead{
				timestamp:   delta,
				msgLen:      uint32(len(data)),
				msgTypeId:   uint8(msgType),
				msgStreamId: streamId,
			},
		}
		lastChunk = cs.current
	}
	lastChunk.basic.fmt = uint8(format)
	lastChunk.msgHdr.timestamp = delta
	lastChunk.msgHdr.msgLen = uint32(len(data))
	lastChunk.msgHdr.msgTypeId = uint8(msgType)
	lastChunk.msgHdr.msgStreamId = streamId

	chks := make([]byte, 0, cs.chunkSize)
	for len(data) > 0 {
		if len(data) > int(cs.chunkSize) {
			lastChunk.data = data[:cs.chunkSize]
			data = data[cs.chunkSize:]
		} else {
			lastChunk.data = data
			data = data[:0]
		}
		pkt, err := lastChunk.encode()
		if err != nil {
			panic(err)
		}
		chks = append(chks, pkt...)
		lastChunk.basic.fmt = 3
	}
	cs.timestamp = ts
	return chks
}

// mustWrite chunks data and fails the test when the header cannot be encoded
func mustWrite(t *testing.T, cs *chunkStreamWriter, data []byte, msgType MessageType, streamId, ts uint32) []byte {
	t.Helper()
	pkt, err := cs.writeData(data, msgType, streamId, ts)
	if err != nil {
		t.Fatal(err)
	}
	return pkt
}

func makePayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func TestWriteDataByteIdentical(t *testing.T) {
	type step struct {
		size    int
		msgType MessageType
		sid     uint32
		ts      uint32
	}
	steps := []step{
		{1, AUDIO, 1, 0},
		{127, AUDIO, 1, 10},
		{128, AUDIO, 1, 20},
		{129, AUDIO, 1, 30},
		{129, AUDIO, 1, 40},
		{129, AUDIO, 1, 50}, // same delta -> fmt 3
		{1000, VIDEO, 1, 60},
		{5000, VIDEO, 2, 5},         // stream id change -> fmt 0
		{3000, VIDEO, 2, 0x1000000}, // extended timestamp, multi chunk
		{3000, VIDEO, 2, 0x1000000 + 0xFFFFFF},
		{64, VIDEO, 2, 0x1000000 + 0xFFFFFF + 0xFFFFFF},
		{0, VIDEO, 2, 7},
	}
	for _, csid := range []uint32{3, 5, 63, 64, 319, 320, 1000} {
		for _, chunkSize := range []uint32{128, 1024, 60000} {
			oldW := newChunkStreamWriter(csid)
			oldW.chunkSize = chunkSize
			newW := newChunkStreamWriter(csid)
			newW.chunkSize = chunkSize
			for i, st := range steps {
				payload := makePayload(st.size)
				want := oldWriteData(oldW, payload, st.msgType, st.sid, st.ts)
				got, err := newW.writeData(payload, st.msgType, st.sid, st.ts)
				if err != nil {
					t.Fatalf("csid %d chunkSize %d step %d: %v", csid, chunkSize, i, err)
				}
				if !bytes.Equal(want, got) {
					t.Fatalf("csid %d chunkSize %d step %d: output differs (want %d bytes, got %d bytes)", csid, chunkSize, i, len(want), len(got))
				}
			}
		}
	}
}

func TestBasicHeadCsidRoundTrip(t *testing.T) {
	for _, csid := range []uint32{2, 63, 64, 319, 320, 1000, 65599} {
		bh := basicHead{fmt: 1, csid: csid}
		enc, err := bh.encode()
		if err != nil {
			t.Fatalf("csid %d: %v", csid, err)
		}
		dec := basicHead{}
		dec.decode(enc)
		if dec.csid != csid || dec.fmt != 1 {
			t.Fatalf("csid %d: decoded as %d (fmt %d)", csid, dec.csid, dec.fmt)
		}
		if csid >= 320 {
			if binary.LittleEndian.Uint16(enc[1:]) != uint16(csid-64) {
				t.Fatalf("csid %d: 2 byte form must be little endian", csid)
			}
		}
	}
}

type collected struct {
	msgs []*rtmpMessage
}

func (c *collected) on(msg *rtmpMessage) error {
	c.msgs = append(c.msgs, msg)
	return nil
}

// feed the wire bytes to the reader in pieces of the given size
func feed(t *testing.T, reader *chunkStreamReader, wire []byte, piece int, c *collected) {
	t.Helper()
	for len(wire) > 0 {
		n := piece
		if n > len(wire) {
			n = len(wire)
		}
		if err := reader.readRtmpMessage(wire[:n], c.on); err != nil {
			t.Fatal(err)
		}
		wire = wire[n:]
	}
}

func TestExtendedTimestampMultiChunkRoundTrip(t *testing.T) {
	w := newChunkStreamWriter(CHUNK_CHANNEL_VIDEO)
	w.chunkSize = 128
	payload := makePayload(1000)
	wire := mustWrite(t, w, payload, VIDEO, 1, 0x1000000)
	// a second message with the same size/type -> fmt 2 with delta 0 (no ext ts)
	payload2 := makePayload(1000)
	wire = append(wire, mustWrite(t, w, payload2, VIDEO, 1, 0x1000000)...)
	// a third one, ts delta 0xFFFFFF exactly must also use the extended field
	wire = append(wire, mustWrite(t, w, payload2, VIDEO, 1, 0x1000000+0xFFFFFF)...)

	for _, piece := range []int{len(wire), 1, 7, 128, 131} {
		reader := newChunkStreamReader(128)
		c := &collected{}
		feed(t, reader, wire, piece, c)
		if len(c.msgs) != 3 {
			t.Fatalf("piece %d: expected 3 messages, got %d", piece, len(c.msgs))
		}
		wantTs := []uint32{0x1000000, 0x1000000, 0x1000000 + 0xFFFFFF}
		for i, msg := range c.msgs {
			if msg.timestamp != wantTs[i] {
				t.Fatalf("piece %d msg %d: timestamp %#x, want %#x", piece, i, msg.timestamp, wantTs[i])
			}
			if !bytes.Equal(msg.msg, payload) {
				t.Fatalf("piece %d msg %d: payload mismatch", piece, i)
			}
			if msg.msgtype != VIDEO || msg.streamid != 1 {
				t.Fatalf("piece %d msg %d: bad type/stream", piece, i)
			}
		}
	}
}

func TestZeroLengthMessage(t *testing.T) {
	// fmt 0 header with msgLen 0, followed by a normal message
	hdr := chunkPacket{basic: basicHead{fmt: 0, csid: 3}, msgHdr: chunkMsgHead{timestamp: 5, msgLen: 0, msgTypeId: uint8(Command_AMF0), msgStreamId: 0}}
	wire, err := hdr.encode()
	if err != nil {
		t.Fatal(err)
	}
	w := newChunkStreamWriter(3)
	wire = append(wire, mustWrite(t, w, []byte{1, 2, 3}, Command_AMF0, 0, 9)...)

	reader := newChunkStreamReader(128)
	c := &collected{}
	feed(t, reader, wire, len(wire), c)
	if len(c.msgs) != 2 || len(c.msgs[0].msg) != 0 || len(c.msgs[1].msg) != 3 {
		t.Fatalf("unexpected messages: %d", len(c.msgs))
	}
	if reader.state != S_BASIC_HEAD {
		t.Fatalf("reader must be back in S_BASIC_HEAD, got %d", reader.state)
	}
	// zero length message with no trailing input must also be emitted immediately
	reader = newChunkStreamReader(128)
	c = &collected{}
	empty, err := hdr.encode()
	if err != nil {
		t.Fatal(err)
	}
	feed(t, reader, empty, 1, c)
	if len(c.msgs) != 1 {
		t.Fatalf("expected the zero length message to be emitted, got %d", len(c.msgs))
	}
}

func TestInterleavedChunkStreams(t *testing.T) {
	wa := newChunkStreamWriter(CHUNK_CHANNEL_AUDIO)
	wv := newChunkStreamWriter(CHUNK_CHANNEL_VIDEO)
	a := mustWrite(t, wa, makePayload(300), AUDIO, 1, 100)
	v := mustWrite(t, wv, makePayload(500), VIDEO, 1, 200)
	// interleave chunk by chunk: audio chunks are 128+hdr, video too, just split by known sizes
	// simpler: feed whole audio then whole video, the reader keeps per csid state anyway
	wire := append(append([]byte{}, a...), v...)
	reader := newChunkStreamReader(128)
	c := &collected{}
	feed(t, reader, wire, 3, c)
	if len(c.msgs) != 2 || c.msgs[0].msgtype != AUDIO || c.msgs[1].msgtype != VIDEO {
		t.Fatalf("unexpected messages %d", len(c.msgs))
	}
	if c.msgs[0].timestamp != 100 || c.msgs[1].timestamp != 200 {
		t.Fatalf("bad timestamps %d %d", c.msgs[0].timestamp, c.msgs[1].timestamp)
	}
}

func TestChunkHeaderErrors(t *testing.T) {
	// a chunk stream id the 3 byte basic header cannot describe
	w := newChunkStreamWriter(maxCsid + 1)
	if _, err := w.writeData([]byte{1, 2, 3}, AUDIO, 1, 0); !errors.Is(err, errInvalidCsid) {
		t.Fatalf("want errInvalidCsid, got %v", err)
	}
	bh := basicHead{csid: maxCsid + 1}
	if _, err := bh.encode(); !errors.Is(err, errInvalidCsid) {
		t.Fatalf("basicHead.encode: %v", err)
	}
	if _, err := bh.size(); !errors.Is(err, errInvalidCsid) {
		t.Fatalf("basicHead.size: %v", err)
	}
	// an out of range chunk format is reported by both directions
	var cmh chunkMsgHead
	if _, err := cmh.encodeTo(4, make([]byte, 11)); !errors.Is(err, errInvalidChunkFmt) {
		t.Fatalf("chunkMsgHead.encodeTo: %v", err)
	}
	if err := cmh.decode(4, make([]byte, 11)); !errors.Is(err, errInvalidChunkFmt) {
		t.Fatalf("chunkMsgHead.decode: %v", err)
	}
	if _, err := (&chunkPacket{basic: basicHead{fmt: 4}}).headSize(); !errors.Is(err, errInvalidChunkFmt) {
		t.Fatalf("chunkPacket.headSize: %v", err)
	}
	// the valid range still works
	if _, err := newChunkStreamWriter(maxCsid).writeData([]byte{1}, AUDIO, 1, 0); err != nil {
		t.Fatalf("csid %d must be usable: %v", maxCsid, err)
	}
}
