package rtmp

import (
	"errors"
	"testing"
)

// chunkHdr builds a fmt 0 chunk header on chunk stream csid announcing a
// message of msgLen bytes.
func chunkHdr(csid int, msgLen int) []byte {
	w := csid - 64
	return []byte{
		0x01, byte(w & 0xff), byte(w >> 8), // 3 byte basic header, fmt 0
		0, 0, 0, // timestamp
		byte(msgLen >> 16), byte(msgLen >> 8), byte(msgLen), // message length
		9,          // message type id (video)
		0, 0, 0, 0, // message stream id
	}
}

// A peer picks its own chunk stream ids and message lengths, and an
// incomplete message stays buffered. Both have to be bounded or a peer can
// make the process allocate without limit.
func TestChunkStreamLimits(t *testing.T) {
	t.Run("chunk stream count is bounded", func(t *testing.T) {
		r := newChunkStreamReader(FIX_CHUNK_SIZE)
		payload := make([]byte, FIX_CHUNK_SIZE)
		var err error
		// each chunk is complete, so the parser is left at a chunk boundary,
		// but no message ever is: every chunk stream stays open
		for i := 0; i < 4096 && err == nil; i++ {
			pkt := append(chunkHdr(64+i, 0xFFFFFF), payload...)
			err = r.readRtmpMessage(pkt, func(*rtmpMessage) error { return nil })
		}
		if !errors.Is(err, errTooManyChunkStreams) {
			t.Fatalf("want errTooManyChunkStreams, got %v after %d streams", err, len(r.cks))
		}
		if len(r.cks) > defaultMaxChunkStreams {
			t.Errorf("%d chunk streams open, limit is %d", len(r.cks), defaultMaxChunkStreams)
		}
	})

	t.Run("buffered bytes are bounded", func(t *testing.T) {
		r := newChunkStreamReader(FIX_CHUNK_SIZE)
		r.maxBufferedMsgLen = 4 * FIX_CHUNK_SIZE
		payload := make([]byte, FIX_CHUNK_SIZE)
		var err error
		// every chunk stream announces a message far bigger than what is fed,
		// so nothing ever completes and the buffer only grows
		for i := 0; i < 64 && err == nil; i++ {
			pkt := append(chunkHdr(64+i, 0xFFFFFF), payload...)
			err = r.readRtmpMessage(pkt, func(*rtmpMessage) error { return nil })
		}
		if !errors.Is(err, errBufferedTooMuch) {
			t.Fatalf("want errBufferedTooMuch, got %v (buffered %d)", err, r.buffered)
		}
		if r.buffered > r.maxBufferedMsgLen {
			t.Errorf("buffered %d bytes, limit is %d", r.buffered, r.maxBufferedMsgLen)
		}
	})

	t.Run("a completed message releases its buffer", func(t *testing.T) {
		r := newChunkStreamReader(FIX_CHUNK_SIZE)
		got := 0
		pkt := append(chunkHdr(64, 10), make([]byte, 10)...)
		for i := 0; i < 1000; i++ {
			if err := r.readRtmpMessage(pkt, func(*rtmpMessage) error { got++; return nil }); err != nil {
				t.Fatal(err)
			}
		}
		if got != 1000 {
			t.Errorf("got %d messages, want 1000", got)
		}
		if r.buffered != 0 {
			t.Errorf("buffered=%d after every message completed, want 0", r.buffered)
		}
	})
}
