package codec

import (
	"testing"
)

// The parsers in this package are fed bytes that come straight off the wire or
// out of a file. None of them may panic on malformed input: BitStream reports
// truncation through its sticky error instead.

func FuzzSPSDecode(f *testing.F) {
	f.Add([]byte{0x67, 0x42, 0x00, 0x1e, 0xe9, 0x02, 0x83, 0xf4, 0x00})
	f.Add([]byte{0x67})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var s SPS
		s.Decode(NewBitStream(CovertRbspToSodb(data)))
		_, _, _ = GetH264Resolution(data)
		_ = GetSPSIdWithStartCode(data)
		_ = GetPPSIdWithStartCode(data)
		_, _, _ = CovertExtradata(data)
	})
}

func FuzzH265SPSDecode(f *testing.F) {
	f.Add([]byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90})
	f.Add([]byte{0x42})
	f.Fuzz(func(t *testing.T, data []byte) {
		var s H265RawSPS
		_ = s.Decode(data)
		_, _, _ = GetH265Resolution(data)
		_ = GetH265SPSIdWithStartCode(data)
		_ = GetH265PPSIdWithStartCode(data)
		_ = GetVPSIdWithStartCode(data)
	})
}

func FuzzHEVCRecordConfiguration(f *testing.F) {
	f.Add([]byte{0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x78, 0xf0, 0x00, 0xfc, 0xfd, 0xf8, 0xf8, 0x00, 0x00, 0x0f, 0x03})
	f.Add([]byte{0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		hvcc := NewHEVCRecordConfiguration()
		_ = hvcc.Decode(data)
		_ = hvcc.UpdateSPS(data)
		_ = hvcc.UpdatePPS(data)
		_ = hvcc.UpdateVPS(data)
		_, _ = hvcc.Encode()
	})
}

func FuzzADTS(f *testing.F) {
	f.Add([]byte{0xFF, 0xF1, 0x50, 0x80, 0x01, 0x3F, 0xFC, 0x01, 0x02})
	f.Add([]byte{0xFF, 0xF1})
	f.Fuzz(func(t *testing.T, data []byte) {
		hdr := NewAdtsFrameHeader()
		if err := hdr.Decode(data); err == nil {
			_ = hdr.Encode()
		}
		_, _ = ConvertADTSToASC(data)
		_ = SplitAACFrame(data, func(aac []byte) {})
		_, _ = ConvertASCToADTS(data, len(data))
		var asc AudioSpecificConfiguration
		_ = asc.Decode(data)
	})
}

func FuzzMp3(f *testing.F) {
	f.Add([]byte{0xFF, 0xFB, 0x90, 0xC0, 0x00, 0x00})
	f.Add([]byte{'I', 'D', '3', 3, 0, 0, 0, 0, 0, 10})
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = SplitMp3Frames(data, func(head *MP3FrameHead, frame []byte) {})
	})
}

func FuzzOpus(f *testing.F) {
	f.Add([]byte{0x00, 0x01, 0x02})
	f.Add([]byte("OpusHead\x01\x02\x00\x00\x80\xBB\x00\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = OpusPacketDuration(data)
		_ = DecodeOpusPacket(data)
		ctx := &OpusContext{}
		if ctx.ParseExtranData(data) == nil {
			_ = ctx.WriteOpusExtraData()
		}
	})
}

func FuzzSplitFrame(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0x67, 0, 0, 1, 0x68, 0, 0, 0, 1, 0x65})
	f.Add([]byte{0, 0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		SplitFrame(data, func(nalu []byte) bool { return true })
		SplitFrameWithStartCode(data, func(nalu []byte) bool { return true })
		_ = IsH264IDRFrame(data)
		_ = IsH265IDRFrame(data)
		_ = H264NaluType(data)
		_ = H265NaluType(data)
		_ = GetH264FirstMbInSlice(data)
		_ = GetH265FirstMbInSlice(data)
		_ = CovertRbspToSodb(data)
		_ = ConvertAnnexBToAVCC(data)
	})
}

// A BitStream must survive any sequence of reads on any buffer without
// panicking, and must never advance past the end of its data.
func FuzzBitStream(f *testing.F) {
	f.Add([]byte{0x01, 0x02, 0x03}, []byte{1, 8, 64, 3})
	f.Fuzz(func(t *testing.T, data []byte, ops []byte) {
		bs := NewBitStream(data)
		for i := 0; i+1 < len(ops); i += 2 {
			n := int(ops[i+1])
			switch ops[i] % 8 {
			case 0:
				bs.GetBits(n % 65)
			case 1:
				bs.GetBit()
			case 2:
				bs.GetBytes(n)
			case 3:
				bs.SkipBits(n)
			case 4:
				bs.UnRead(n)
			case 5:
				bs.NextBits(n % 65)
			case 6:
				bs.ReadUE()
			case 7:
				bs.ReadSE()
			}
			if bs.ByteOffset() > len(data) {
				t.Fatalf("offset %d past the end of a %d byte buffer", bs.ByteOffset(), len(data))
			}
			_ = bs.RemainData()
			_ = bs.RemainBytes()
			_ = bs.RemainBits()
			_ = bs.EOS()
		}
	})
}
