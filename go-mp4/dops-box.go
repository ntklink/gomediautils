package mp4

import (
	"encoding/binary"
	"io"

	"github.com/yapingcat/gomedia/go-codec"
)

// class ChannelMappingTable (unsigned int(8) OutputChannelCount){
//     unsigned int(8) StreamCount;
//     unsigned int(8) CoupledCount;
//     unsigned int(8 * OutputChannelCount) ChannelMapping;
// }
// aligned(8) class OpusSpecificBox extends Box('dOps'){
//     unsigned int(8) Version;
//     unsigned int(8) OutputChannelCount;
//     unsigned int(16) PreSkip;
//     unsigned int(32) InputSampleRate;
//     signed int(16) OutputGain;
//     unsigned int(8) ChannelMappingFamily;
//     if (ChannelMappingFamily != 0) {
//         ChannelMappingTable(OutputChannelCount);
//     }
// }

type ChannelMappingTable struct {
	StreamCount    uint8
	CoupledCount   uint8
	ChannelMapping []byte
}

type OpusSpecificBox struct {
	Box                  *BasicBox
	Version              uint8
	OutputChannelCount   uint8
	PreSkip              uint16
	InputSampleRate      uint32
	OutputGain           int16
	ChannelMappingFamily uint8
	ChanMapTable         *ChannelMappingTable
}

func NewdOpsBox() *OpusSpecificBox {
	return &OpusSpecificBox{
		Box: NewBasicBox([4]byte{'d', 'O', 'p', 's'}),
	}
}

// dOpsFixedLen is Version, OutputChannelCount, PreSkip, InputSampleRate,
// OutputGain and ChannelMappingFamily.
const dOpsFixedLen = 1 + 1 + 2 + 4 + 2 + 1

func (dops *OpusSpecificBox) Size() uint64 {
	size := uint64(BasicBoxLen + dOpsFixedLen)
	if dops.ChanMapTable != nil {
		size += 2 + uint64(len(dops.ChanMapTable.ChannelMapping))
	}
	return size
}

// Encode writes the box. Every multi byte field of dOps is big endian, unlike
// the little endian OpusHead the values usually come from: writing PreSkip in
// the source byte order turns the 312 sample priming of a typical encoder
// into 14337, and a decoder then throws away the first third of a second of
// audio.
func (dops *OpusSpecificBox) Encode() (int, []byte) {
	dops.Box.Size = dops.Size()
	offset, buf := dops.Box.Encode()
	buf[offset] = dops.Version
	offset++
	buf[offset] = dops.OutputChannelCount
	offset++
	binary.BigEndian.PutUint16(buf[offset:], dops.PreSkip)
	offset += 2
	binary.BigEndian.PutUint32(buf[offset:], dops.InputSampleRate)
	offset += 4
	binary.BigEndian.PutUint16(buf[offset:], uint16(dops.OutputGain))
	offset += 2
	buf[offset] = dops.ChannelMappingFamily
	offset++
	if dops.ChanMapTable != nil {
		buf[offset] = dops.ChanMapTable.StreamCount
		offset++
		buf[offset] = dops.ChanMapTable.CoupledCount
		offset++
		copy(buf[offset:], dops.ChanMapTable.ChannelMapping)
		offset += len(dops.ChanMapTable.ChannelMapping)
	}
	return offset, buf
}

func (dops *OpusSpecificBox) Decode(r io.Reader, size uint32) (offset int, err error) {

	dopsBuf, err := readBoxPayload(r, uint64(size), BasicBoxLen)
	if err != nil {
		return
	}
	if err = checkRemain(dopsBuf, 0, dOpsFixedLen); err != nil {
		return
	}

	dops.Version = dopsBuf[0]
	dops.OutputChannelCount = dopsBuf[1]
	dops.PreSkip = binary.BigEndian.Uint16(dopsBuf[2:])
	dops.InputSampleRate = binary.BigEndian.Uint32(dopsBuf[4:])
	dops.OutputGain = int16(binary.BigEndian.Uint16(dopsBuf[8:]))
	dops.ChannelMappingFamily = dopsBuf[10]
	dops.ChanMapTable = nil
	if dops.ChannelMappingFamily != 0 {
		// the mapping table holds one byte per output channel
		need := dOpsFixedLen + 2 + int(dops.OutputChannelCount)
		if err = checkRemain(dopsBuf, 0, need); err != nil {
			return
		}
		dops.ChanMapTable = &ChannelMappingTable{
			StreamCount:    dopsBuf[11],
			CoupledCount:   dopsBuf[12],
			ChannelMapping: make([]byte, dops.OutputChannelCount),
		}
		copy(dops.ChanMapTable.ChannelMapping, dopsBuf[13:])
	}

	return int(size - BasicBoxLen), nil
}

func makeOpusSpecificBox(extraData []byte) []byte {
	ctx := &codec.OpusContext{}
	if err := ctx.ParseExtranData(extraData); err != nil {
		// nothing describable: an empty box is still better than one built
		// from a half parsed header
		_, boxdata := NewdOpsBox().Encode()
		return boxdata
	}
	dops := NewdOpsBox()
	dops.Version = 0
	dops.OutputChannelCount = uint8(ctx.ChannelCount)
	dops.PreSkip = uint16(ctx.Preskip)
	dops.InputSampleRate = uint32(ctx.SampleRate)
	dops.OutputGain = int16(ctx.OutputGain)
	dops.ChannelMappingFamily = ctx.MapType
	if ctx.MapType > 0 {
		dops.ChanMapTable = &ChannelMappingTable{
			StreamCount:    uint8(ctx.StreamCount),
			CoupledCount:   uint8(ctx.StereoStreamCount),
			ChannelMapping: make([]byte, len(ctx.Channel)),
		}
		copy(dops.ChanMapTable.ChannelMapping, ctx.Channel)
	}
	_, dopsbox := dops.Encode()
	return dopsbox
}
