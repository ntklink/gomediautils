package flv

import "testing"

// CompositionTime is an SI24. Reading it as unsigned turns the small negative
// offset that reordered streams carry into a ~16.7 million ms one.
func TestCompositionTimeIsSigned(t *testing.T) {
	for _, cts := range []int32{-40, -1, 0, 1, 40, 0x7FFFFF, -0x800000} {
		var enc VideoTag
		enc.CodecId = uint8(FLV_AVC)
		enc.FrameType = uint8(INTER_FRAME)
		enc.AVCPacketType = uint8(AVC_NALU)
		enc.CompositionTime = cts

		var dec VideoTag
		dec.Decode(enc.Encode())
		if dec.CompositionTime != cts {
			t.Errorf("cts %d round tripped to %d", cts, dec.CompositionTime)
		}
	}
}

// Codecs other than AVC/HEVC have a one byte video tag header, so a one byte
// buffer is a complete tag and has to decode.
func TestVideoTagDecodeShortNonAvcHeader(t *testing.T) {
	var vtag VideoTag
	vtag.Decode([]byte{0x14}) // key frame, codec id 4 (VP6)
	if vtag.FrameType != uint8(KEY_FRAME) || vtag.CodecId != 4 {
		t.Errorf("frameType=%d codecId=%d, want %d and 4", vtag.FrameType, vtag.CodecId, KEY_FRAME)
	}
}
