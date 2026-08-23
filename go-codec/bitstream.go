package codec

import (
	"encoding/binary"
	"errors"
)

var bitMask = [8]byte{0x01, 0x03, 0x07, 0x0F, 0x1F, 0x3F, 0x7F, 0xFF}

// ErrOutOfRange is reported by BitStream.Err after a read that ran past the
// end of the buffer.
var ErrOutOfRange = errors.New("codec: read past the end of the bit stream")

// BitStream reads a bit oriented buffer.
//
// Reads past the end of the buffer do not panic. They yield a zero value and
// put the stream into a sticky error state: every later read is a no-op and
// Err reports ErrOutOfRange. A parser can therefore decode a whole syntax
// structure and check for truncation once, instead of bounds checking every
// field.
type BitStream struct {
	bits        []byte
	bytesOffset int
	bitsOffset  int
	bitsmark    int
	bytemark    int
	err         error
}

func NewBitStream(buf []byte) *BitStream {
	return &BitStream{
		bits:        buf,
		bytesOffset: 0,
		bitsOffset:  0,
		bitsmark:    0,
		bytemark:    0,
	}
}

// Err reports whether the stream ran out of data. Once set it stays set.
func (bs *BitStream) Err() error {
	return bs.err
}

func (bs *BitStream) setErr() {
	if bs.err == nil {
		bs.err = ErrOutOfRange
	}
}

// Fail puts the stream into the error state with a specific reason. Parsers
// use it to reject a value that is well formed but outside the limits the
// specification allows.
func (bs *BitStream) Fail(err error) {
	if bs.err == nil {
		bs.err = err
	}
}

func (bs *BitStream) Uint8(n int) uint8 {
	return uint8(bs.GetBits(n))
}

func (bs *BitStream) Uint16(n int) uint16 {
	return uint16(bs.GetBits(n))
}

func (bs *BitStream) Uint32(n int) uint32 {
	return uint32(bs.GetBits(n))
}

// GetBytes returns the next n bytes. It must be called on a byte boundary; a
// misaligned or truncated read yields nil and sets the error state.
func (bs *BitStream) GetBytes(n int) []byte {
	if bs.err != nil {
		return nil
	}
	if n < 0 || bs.bitsOffset != 0 || bs.bytesOffset+n > len(bs.bits) {
		bs.setErr()
		return nil
	}
	data := make([]byte, n)
	copy(data, bs.bits[bs.bytesOffset:bs.bytesOffset+n])
	bs.bytesOffset += n
	return data
}

// GetBits reads the next n bits, n <= 64. A read that does not fit in the
// remaining data returns 0 and sets the error state without consuming
// anything.
func (bs *BitStream) GetBits(n int) uint64 {
	if bs.err != nil {
		return 0
	}
	if n < 0 || n > 64 {
		bs.setErr()
		return 0
	}
	if n == 0 {
		return 0
	}
	if n > bs.RemainBits() {
		bs.setErr()
		return 0
	}
	var ret uint64 = 0
	if 8-bs.bitsOffset >= n {
		ret = uint64((bs.bits[bs.bytesOffset] >> (8 - bs.bitsOffset - n)) & bitMask[n-1])
		bs.bitsOffset += n
		if bs.bitsOffset == 8 {
			bs.bytesOffset++
			bs.bitsOffset = 0
		}
	} else {
		ret = uint64(bs.bits[bs.bytesOffset] & bitMask[8-bs.bitsOffset-1])
		bs.bytesOffset++
		n -= 8 - bs.bitsOffset
		bs.bitsOffset = 0
		for n > 0 {
			if n >= 8 {
				ret = ret<<8 | uint64(bs.bits[bs.bytesOffset])
				bs.bytesOffset++
				n -= 8
			} else {
				ret = (ret << n) | uint64((bs.bits[bs.bytesOffset]>>(8-n))&bitMask[n-1])
				bs.bitsOffset = n
				break
			}
		}
	}
	return ret
}

func (bs *BitStream) GetBit() uint8 {
	if bs.err != nil {
		return 0
	}
	if bs.bytesOffset >= len(bs.bits) {
		bs.setErr()
		return 0
	}
	ret := bs.bits[bs.bytesOffset] >> (7 - bs.bitsOffset) & 0x01
	bs.bitsOffset++
	if bs.bitsOffset >= 8 {
		bs.bytesOffset++
		bs.bitsOffset = 0
	}
	return ret
}

func (bs *BitStream) SkipBits(n int) {
	if bs.err != nil {
		return
	}
	if n < 0 || n > bs.RemainBits() {
		bs.setErr()
		return
	}
	bytecount := n / 8
	bitscount := n % 8
	bs.bytesOffset += bytecount
	if bs.bitsOffset+bitscount < 8 {
		bs.bitsOffset += bitscount
	} else {
		bs.bytesOffset += 1
		bs.bitsOffset += bitscount - 8
	}
}

func (bs *BitStream) Markdot() {
	bs.bitsmark = bs.bitsOffset
	bs.bytemark = bs.bytesOffset
}

func (bs *BitStream) DistanceFromMarkDot() int {
	bytecount := bs.bytesOffset - bs.bytemark - 1
	bitscount := bs.bitsOffset + (8 - bs.bitsmark)
	return bytecount*8 + bitscount
}

func (bs *BitStream) RemainBytes() int {
	if bs.bitsOffset > 0 {
		return len(bs.bits) - bs.bytesOffset - 1
	} else {
		return len(bs.bits) - bs.bytesOffset
	}
}

func (bs *BitStream) RemainBits() int {
	if bs.bitsOffset > 0 {
		return bs.RemainBytes()*8 + 8 - bs.bitsOffset
	} else {
		return bs.RemainBytes() * 8
	}

}

func (bs *BitStream) Bits() []byte {
	return bs.bits
}

func (bs *BitStream) RemainData() []byte {
	if bs.bytesOffset >= len(bs.bits) {
		return nil
	}
	return bs.bits[bs.bytesOffset:]
}

// ReadUE reads an unsigned Exp-Golomb coded value. A prefix longer than 63
// zero bits cannot be represented and sets the error state.
func (bs *BitStream) ReadUE() uint64 {
	leadingZeroBits := 0
	for {
		if bs.err != nil {
			return 0
		}
		if bs.GetBit() != 0 {
			break
		}
		leadingZeroBits++
		if leadingZeroBits > 63 {
			bs.setErr()
			return 0
		}
	}
	if leadingZeroBits == 0 {
		return 0
	}
	info := bs.GetBits(leadingZeroBits)
	return uint64(1)<<leadingZeroBits - 1 + info
}

// 有符号哥伦布熵编码
func (bs *BitStream) ReadSE() int64 {
	v := bs.ReadUE()
	if v%2 == 0 {
		return -1 * int64(v/2)
	} else {
		return int64(v+1) / 2
	}
}

func (bs *BitStream) ByteOffset() int {
	return bs.bytesOffset
}

// UnRead rewinds the stream by n bits. Rewinding before the start of the
// buffer clamps to the start and sets the error state.
func (bs *BitStream) UnRead(n int) {
	if n < 0 {
		bs.setErr()
		return
	}
	pos := bs.bytesOffset*8 + bs.bitsOffset
	if n > pos {
		bs.setErr()
		n = pos
	}
	pos -= n
	bs.bytesOffset = pos / 8
	bs.bitsOffset = pos % 8
}

// NextBits returns the next n bits without consuming them.
func (bs *BitStream) NextBits(n int) uint64 {
	if bs.err != nil {
		return 0
	}
	if n < 0 || n > bs.RemainBits() {
		bs.setErr()
		return 0
	}
	r := bs.GetBits(n)
	bs.UnRead(n)
	return r
}

// EOS reports whether the stream is exhausted. A stream in the error state is
// always at EOS so that `for !bs.EOS()` loops terminate on truncated input.
func (bs *BitStream) EOS() bool {
	return bs.err != nil || (bs.bytesOffset == len(bs.bits) && bs.bitsOffset == 0)
}

// ErrNotByteAligned is reported by BitStreamWriter.Err after a byte oriented
// write that was issued while the writer sat in the middle of a byte.
var ErrNotByteAligned = errors.New("codec: bit stream writer is not byte aligned")

type BitStreamWriter struct {
	bits       []byte
	byteoffset int
	bitsoffset int
	bitsmark   int
	bytemark   int
	err        error
}

// Err reports whether a write was rejected. Once set it stays set.
func (bsw *BitStreamWriter) Err() error {
	return bsw.err
}

func NewBitStreamWriter(n int) *BitStreamWriter {
	return &BitStreamWriter{
		bits:       make([]byte, n),
		byteoffset: 0,
		bitsoffset: 0,
		bitsmark:   0,
		bytemark:   0,
	}
}

func (bsw *BitStreamWriter) expandSpace(n int) {
	if (len(bsw.bits)-bsw.byteoffset-1)*8+8-bsw.bitsoffset < n {
		newlen := 0
		if len(bsw.bits)*8 < n {
			newlen = len(bsw.bits) + n/8 + 1
		} else {
			newlen = len(bsw.bits) * 2
		}
		tmp := make([]byte, newlen)
		copy(tmp, bsw.bits)
		bsw.bits = tmp
	}
}

func (bsw *BitStreamWriter) ByteOffset() int {
	return bsw.byteoffset
}

func (bsw *BitStreamWriter) BitOffset() int {
	return bsw.bitsoffset
}

func (bsw *BitStreamWriter) Markdot() {
	bsw.bitsmark = bsw.bitsoffset
	bsw.bytemark = bsw.byteoffset
}

func (bsw *BitStreamWriter) DistanceFromMarkDot() int {
	bytecount := bsw.byteoffset - bsw.bytemark - 1
	bitscount := bsw.bitsoffset + (8 - bsw.bitsmark)
	return bytecount*8 + bitscount
}

func (bsw *BitStreamWriter) PutByte(v byte) {
	bsw.expandSpace(8)
	if bsw.bitsoffset == 0 {
		bsw.bits[bsw.byteoffset] = v
		bsw.byteoffset++
	} else {
		bsw.bits[bsw.byteoffset] |= v >> byte(bsw.bitsoffset)
		bsw.byteoffset++
		bsw.bits[bsw.byteoffset] = v & bitMask[bsw.bitsoffset-1]
	}
}

// PutBytes appends v. It must be called on a byte boundary; otherwise the
// writer is put into the error state and the write is dropped.
func (bsw *BitStreamWriter) PutBytes(v []byte) {
	if bsw.err != nil {
		return
	}
	if bsw.bitsoffset != 0 {
		bsw.err = ErrNotByteAligned
		return
	}
	bsw.expandSpace(8 * len(v))
	copy(bsw.bits[bsw.byteoffset:], v)
	bsw.byteoffset += len(v)
}

func (bsw *BitStreamWriter) PutRepetValue(v byte, n int) {
	if bsw.err != nil {
		return
	}
	if bsw.bitsoffset != 0 {
		bsw.err = ErrNotByteAligned
		return
	}
	bsw.expandSpace(8 * n)
	for i := 0; i < n; i++ {
		bsw.bits[bsw.byteoffset] = v
		bsw.byteoffset++
	}
}

func (bsw *BitStreamWriter) PutUint8(v uint8, n int) {
	bsw.PutUint64(uint64(v), n)
}

func (bsw *BitStreamWriter) PutUint16(v uint16, n int) {
	bsw.PutUint64(uint64(v), n)
}

func (bsw *BitStreamWriter) PutUint32(v uint32, n int) {
	bsw.PutUint64(uint64(v), n)
}

func (bsw *BitStreamWriter) PutUint64(v uint64, n int) {
	bsw.expandSpace(n)
	if 8-bsw.bitsoffset >= n {
		bsw.bits[bsw.byteoffset] |= uint8(v) & bitMask[n-1] << (8 - bsw.bitsoffset - n)
		bsw.bitsoffset += n
		if bsw.bitsoffset == 8 {
			bsw.bitsoffset = 0
			bsw.byteoffset++
		}
	} else {
		bsw.bits[bsw.byteoffset] |= uint8(v>>(n-int(8-bsw.bitsoffset))) & bitMask[8-bsw.bitsoffset-1]
		bsw.byteoffset++
		n -= 8 - bsw.bitsoffset
		for n-8 >= 0 {
			bsw.bits[bsw.byteoffset] = uint8(v>>(n-8)) & 0xFF
			bsw.byteoffset++
			n -= 8
		}
		bsw.bitsoffset = n
		if n > 0 {
			bsw.bits[bsw.byteoffset] |= (uint8(v) & bitMask[n-1]) << (8 - n)
		}
	}
}

func (bsw *BitStreamWriter) SetByte(v byte, where int) {
	bsw.bits[where] = v
}

func (bsw *BitStreamWriter) SetUint16(v uint16, where int) {
	binary.BigEndian.PutUint16(bsw.bits[where:where+2], v)
}

func (bsw *BitStreamWriter) Bits() []byte {
	if bsw.byteoffset == len(bsw.bits) {
		return bsw.bits
	}
	if bsw.bitsoffset > 0 {
		return bsw.bits[0 : bsw.byteoffset+1]
	} else {
		return bsw.bits[0:bsw.byteoffset]
	}
}

// 用v 填充剩余字节
func (bsw *BitStreamWriter) FillRemainData(v byte) {
	for i := bsw.byteoffset; i < len(bsw.bits); i++ {
		bsw.bits[i] = v
	}
	bsw.byteoffset = len(bsw.bits)
	bsw.bitsoffset = 0
}

func (bsw *BitStreamWriter) Reset() {
	bsw.err = nil
	for i := 0; i < len(bsw.bits); i++ {
		bsw.bits[i] = 0
	}
	bsw.bitsmark = 0
	bsw.bytemark = 0
	bsw.bitsoffset = 0
	bsw.byteoffset = 0
}
