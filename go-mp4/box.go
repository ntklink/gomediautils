package mp4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	BasicBoxLen = 8
	FullBoxLen  = 12

	// maxBoxPayloadSize caps the payload of any non-mdat box that is read
	// into memory in one piece, so a bogus box size cannot trigger a huge
	// allocation.
	maxBoxPayloadSize = 256 << 20
)

var (
	errBoxTruncated = errors.New("mp4: box payload truncated")
	errNoTrack      = errors.New("mp4: box found outside of a trak box")
)

// boxPayloadLen validates a box size against its header length and the
// global payload cap and returns the payload length.
func boxPayloadLen(size uint64, headerLen int) (int, error) {
	if size < uint64(headerLen) {
		return 0, fmt.Errorf("mp4: box size %d smaller than header length %d", size, headerLen)
	}
	payload := size - uint64(headerLen)
	if payload > maxBoxPayloadSize {
		return 0, fmt.Errorf("mp4: box payload %d exceeds limit %d", payload, maxBoxPayloadSize)
	}
	return int(payload), nil
}

// readBoxPayload reads the remaining payload of a box whose size field is
// `size` after `headerLen` bytes of header have already been consumed.
func readBoxPayload(r io.Reader, size uint64, headerLen int) ([]byte, error) {
	n, err := boxPayloadLen(size, headerLen)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err = io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// checkRemain reports an error if fewer than `need` bytes remain in buf at n.
func checkRemain(buf []byte, n int, need int) error {
	if n < 0 || need < 0 || n+need > len(buf) {
		return errBoxTruncated
	}
	return nil
}

// checkTableSize validates that `count` entries of `entrySize` bytes fit in
// `remain` bytes (when remain >= 0) and under the global cap.
func checkTableSize(count uint32, entrySize int, remain int64) error {
	need := uint64(count) * uint64(entrySize)
	if need > maxBoxPayloadSize {
		return fmt.Errorf("mp4: table of %d entries exceeds size limit", count)
	}
	if remain >= 0 && need > uint64(remain) {
		return fmt.Errorf("mp4: table of %d entries (%d bytes) exceeds enclosing box (%d bytes)", count, need, remain)
	}
	return nil
}

type BoxEncoder interface {
	Encode(buf []byte) (int, []byte)
}

type BoxDecoder interface {
	Decode(buf []byte) (int, error)
}

type BoxSize interface {
	Size() uint64
}

// aligned(8) class Box (unsigned int(32) boxtype, optional unsigned int(8)[16] extended_type) {
//     unsigned int(32) size;
//     unsigned int(32) type = boxtype;
//     if (size==1) {
//        unsigned int(64) largesize;
//     } else if (size==0) {
//        // box extends to end of file
//     }
//     if (boxtype==‘uuid’) {
//     unsigned int(8)[16] usertype = extended_type;
//  }
// }

type BasicBox struct {
	Size     uint64
	Type     [4]byte
	UserType [16]byte
}

func NewBasicBox(boxtype [4]byte) *BasicBox {
	return &BasicBox{
		Type: boxtype,
	}
}

func (box *BasicBox) Decode(r io.Reader) (int, error) {
	buf := make([]byte, 16)
	if n, err := io.ReadFull(r, buf[:8]); err != nil {
		return n, err
	}
	boxsize := binary.BigEndian.Uint32(buf)
	copy(box.Type[:], buf[4:8])
	nn := 8
	if boxsize == 1 {
		if n, err := io.ReadFull(r, buf[8:]); err != nil {
			return n, err
		}
		box.Size = binary.BigEndian.Uint64(buf[nn:])
		nn += 8
	} else {
		box.Size = uint64(boxsize)
	}
	if bytes.Equal(box.Type[:], []byte("uuid")) {
		uuid := make([]byte, 16)
		if n, err := io.ReadFull(r, uuid); err != nil {
			return n + nn, err
		}
		copy(box.UserType[:], uuid[:])
		nn += 16
	}

	return nn, nil
}

func (box *BasicBox) Encode() (int, []byte) {
	nn := 8
	var buf []byte
	if box.Size > 0xFFFFFFFF { //just for mdat box
		buf = make([]byte, 16)
		binary.BigEndian.PutUint32(buf, 1)
		copy(buf[4:], box.Type[:])
		nn += 8
		binary.BigEndian.PutUint64(buf[8:], box.Size)
	} else {
		buf = make([]byte, box.Size)
		binary.BigEndian.PutUint32(buf, uint32(box.Size))
		copy(buf[4:], box.Type[:])
		if bytes.Equal(box.Type[:], []byte("uuid")) {
			copy(buf[nn:nn+16], box.UserType[:])
			nn += 16
		}
	}
	return nn, buf
}

// aligned(8) class FullBox(unsigned int(32) boxtype, unsigned int(8) v, bit(24) f) extends Box(boxtype) {
//     unsigned int(8) version = v;
//     bit(24) flags = f;
// }

type FullBox struct {
	Box     *BasicBox
	Version uint8
	Flags   [3]byte
}

func NewFullBox(boxtype [4]byte, version uint8) *FullBox {
	return &FullBox{
		Box:     NewBasicBox(boxtype),
		Version: version,
	}
}

func (box *FullBox) Size() uint64 {
	if box.Box.Size > 0 {
		return box.Box.Size
	} else {
		return 12
	}
}

// tableRemain returns the number of bytes available for table entries after
// the full box header and `fixed` extra header bytes, or -1 when the box
// size is unknown (Box.Size == 0).
func (box *FullBox) tableRemain(fixed int) (int64, error) {
	if box.Box == nil || box.Box.Size == 0 {
		return -1, nil
	}
	if box.Box.Size < uint64(FullBoxLen+fixed) {
		return 0, fmt.Errorf("mp4: box size %d too small", box.Box.Size)
	}
	return int64(box.Box.Size) - int64(FullBoxLen+fixed), nil
}

func (box *FullBox) Decode(r io.Reader) (int, error) {
	buf := make([]byte, 4)
	if n, err := io.ReadFull(r, buf); err != nil {
		return n, err
	}
	box.Version = buf[0]
	copy(box.Flags[:], buf[1:])
	return 4, nil
}

func (box *FullBox) Encode() (int, []byte) {
	box.Box.Size = box.Size()
	offset, buf := box.Box.Encode()
	buf[offset] = box.Version
	copy(buf[offset+1:], box.Flags[:])
	return offset + 4, buf
}
