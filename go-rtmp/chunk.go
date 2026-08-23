package rtmp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var ChunkType [4]byte = [4]byte{11, 7, 3, 0}

// maximum message length that a 24 bit length field can describe
const maxRtmpMessageLen = 0x00ffffff

// the 3 byte basic header can describe chunk stream ids up to 65599
const maxCsid = 65599

// A peer picks its own chunk stream ids and the length of every message it
// starts, and a message is buffered until it is complete. Without a bound a
// peer could interleave thousands of chunk streams, each with a 16 MiB
// message announced and never finished, and make the process allocate
// unbounded memory. Real clients use a handful of chunk streams.
const (
	defaultMaxChunkStreams   = 64
	defaultMaxBufferedMsgLen = 32 << 20
)

var (
	errTooManyChunkStreams = errors.New("rtmp: too many chunk streams")
	errBufferedTooMuch     = errors.New("rtmp: too many bytes buffered in incomplete messages")
)

var (
	errInvalidCsid     = errors.New("rtmp: chunk stream id out of range")
	errInvalidChunkFmt = errors.New("rtmp: unknown chunk format")
)

type basicHead struct {
	fmt  uint8
	csid uint32
}

// size returns the encoded length of the basic header
func (bh *basicHead) size() (int, error) {
	switch {
	case bh.csid < 64:
		return 1, nil
	case bh.csid < 320:
		return 2, nil
	case bh.csid <= maxCsid:
		return 3, nil
	}
	return 0, fmt.Errorf("%w: %d", errInvalidCsid, bh.csid)
}

// encodeTo writes the basic header into dst and returns the number of bytes written
func (bh *basicHead) encodeTo(dst []byte) (int, error) {
	dst[0] = bh.fmt << 6
	switch {
	case bh.csid < 64:
		dst[0] |= uint8(bh.csid)
		return 1, nil
	case bh.csid < 320:
		dst[1] = byte(bh.csid - 64)
		return 2, nil
	case bh.csid <= maxCsid:
		dst[0] |= 1
		// the 2-byte form of the chunk stream id is little-endian (csid - 64)
		binary.LittleEndian.PutUint16(dst[1:], uint16(bh.csid-64))
		return 3, nil
	}
	return 0, fmt.Errorf("%w: %d", errInvalidCsid, bh.csid)
}

func (bh *basicHead) encode() ([]byte, error) {
	hdr := make([]byte, 3)
	n, err := bh.encodeTo(hdr)
	if err != nil {
		return nil, err
	}
	return hdr[:n], nil
}

func (bh *basicHead) decode(data []byte) {
	bh.fmt = data[0] >> 6
	bh.csid = uint32(data[0] & 0x3F)
	if bh.csid == 0 {
		bh.csid = uint32(data[1]) + 64
	} else if bh.csid == 1 {
		bh.csid = uint32(data[2])*256 + uint32(data[1]) + 64
	}
}

type chunkMsgHead struct {
	timestamp   uint32
	msgLen      uint32
	msgTypeId   uint8
	msgStreamId uint32
}

// hasExtendedTs reports whether the timestamp needs the extended timestamp field
func (cmh *chunkMsgHead) hasExtendedTs() bool {
	return cmh.timestamp >= 0x00ffffff
}

// encodeTo writes the message header for the given fmt into dst and returns the number of bytes written
func (cmh *chunkMsgHead) encodeTo(format uint8, dst []byte) (int, error) {
	switch format {
	case 0:
		binary.LittleEndian.PutUint32(dst[7:], cmh.msgStreamId)
		fallthrough
	case 1:
		dst[3] = byte(cmh.msgLen >> 16)
		dst[4] = byte(cmh.msgLen >> 8)
		dst[5] = byte(cmh.msgLen)
		dst[6] = cmh.msgTypeId
		fallthrough
	case 2:
		if cmh.hasExtendedTs() {
			dst[0] = 0xff
			dst[1] = 0xff
			dst[2] = 0xff
		} else {
			dst[0] = byte(cmh.timestamp >> 16)
			dst[1] = byte(cmh.timestamp >> 8)
			dst[2] = byte(cmh.timestamp)
		}
	case 3:
	default:
		return 0, fmt.Errorf("%w: %d", errInvalidChunkFmt, format)
	}
	return int(ChunkType[format]), nil
}

func (cmh *chunkMsgHead) encode(format uint8) ([]byte, error) {
	hdr := make([]byte, 11)
	n, err := cmh.encodeTo(format, hdr)
	if err != nil {
		return nil, err
	}
	return hdr[:n], nil
}

func (cmh *chunkMsgHead) decode(format uint8, data []byte) error {
	switch format {
	case 0:
		// the message stream id is little-endian on the wire (matches encodeTo)
		cmh.msgStreamId = binary.LittleEndian.Uint32(data[7:])
		fallthrough
	case 1:
		cmh.msgLen = uint32(data[3])<<16 | uint32(data[4])<<8 | uint32(data[5])
		cmh.msgTypeId = data[6]
		fallthrough
	case 2:
		cmh.timestamp = uint32(data[0])<<16 | uint32(data[1])<<8 | uint32(data[2])
	case 3:
	default:
		return fmt.Errorf("%w: %d", errInvalidChunkFmt, format)
	}
	return nil
}

func clacBasicHeadLen(data []byte) int {

	length := 1
	csid := data[0] & 0x3F

	if csid == 0 {
		length += 1
	} else if csid == 1 {
		length += 2
	}

	return length
}

type chunkPacket struct {
	basic  basicHead
	msgHdr chunkMsgHead
	data   []byte
}

// headSize returns the encoded size of basic header + message header + extended timestamp
func (chk *chunkPacket) headSize() (int, error) {
	if chk.basic.fmt > 3 {
		return 0, fmt.Errorf("%w: %d", errInvalidChunkFmt, chk.basic.fmt)
	}
	basicSize, err := chk.basic.size()
	if err != nil {
		return 0, err
	}
	size := basicSize + int(ChunkType[chk.basic.fmt])
	if chk.msgHdr.hasExtendedTs() {
		size += 4
	}
	return size, nil
}

// encodeHeadTo writes the chunk header (without payload) into dst and returns the number of bytes written
func (chk *chunkPacket) encodeHeadTo(dst []byte) (int, error) {
	n, err := chk.basic.encodeTo(dst)
	if err != nil {
		return 0, err
	}
	m, err := chk.msgHdr.encodeTo(chk.basic.fmt, dst[n:])
	if err != nil {
		return 0, err
	}
	n += m
	if chk.msgHdr.hasExtendedTs() {
		binary.BigEndian.PutUint32(dst[n:], chk.msgHdr.timestamp)
		n += 4
	}
	return n, nil
}

func (chk *chunkPacket) encode() ([]byte, error) {
	size, err := chk.headSize()
	if err != nil {
		return nil, err
	}
	pkt := make([]byte, size+len(chk.data))
	n, err := chk.encodeHeadTo(pkt)
	if err != nil {
		return nil, err
	}
	copy(pkt[n:], chk.data)
	return pkt, nil
}

type ParserState int

const (
	S_BASIC_HEAD ParserState = iota
	S_MSG_HEAD
	S_EXTEND_TS
	S_PAYLOAD
)

type chunkStreamWriter struct {
	csid      uint32
	timestamp uint32
	current   *chunkPacket
	chunkSize uint32
}

func newChunkStreamWriter(csid uint32) *chunkStreamWriter {
	return &chunkStreamWriter{
		csid:      csid,
		chunkSize: FIX_CHUNK_SIZE,
	}
}

// writeData splits data into chunks and returns them ready for the wire. It
// reports an error instead of producing a header it cannot encode.
func (cs *chunkStreamWriter) writeData(data []byte, msgType MessageType, streamId uint32, ts uint32) ([]byte, error) {

	if cs.csid > maxCsid {
		return nil, fmt.Errorf("%w: %d", errInvalidCsid, cs.csid)
	}

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
				csid: cs.csid,
			},
		}
		lastChunk = cs.current
	}
	lastChunk.basic.fmt = uint8(format)
	lastChunk.msgHdr.timestamp = delta
	lastChunk.msgHdr.msgLen = uint32(len(data))
	lastChunk.msgHdr.msgTypeId = uint8(msgType)
	lastChunk.msgHdr.msgStreamId = streamId
	lastChunk.data = nil
	cs.timestamp = ts

	if len(data) == 0 {
		return nil, nil
	}

	chunkSize := int(cs.chunkSize)
	if chunkSize <= 0 {
		chunkSize = FIX_CHUNK_SIZE
	}

	// compute the total output size up front so that we allocate exactly once
	chunkCount := (len(data) + chunkSize - 1) / chunkSize
	firstHeadSize, err := lastChunk.headSize()
	if err != nil {
		return nil, err
	}
	lastChunk.basic.fmt = 3
	contHeadSize, err := lastChunk.headSize()
	if err != nil {
		return nil, err
	}
	lastChunk.basic.fmt = uint8(format)
	total := firstHeadSize + (chunkCount-1)*contHeadSize + len(data)

	chks := make([]byte, total)
	n := 0
	for len(data) > 0 {
		written, err := lastChunk.encodeHeadTo(chks[n:])
		if err != nil {
			return nil, err
		}
		n += written
		payload := data
		if len(data) > chunkSize {
			payload = data[:chunkSize]
		}
		n += copy(chks[n:], payload)
		data = data[len(payload):]
		lastChunk.basic.fmt = 3
	}
	return chks[:n], nil
}

// chunkBatch accumulates the wire bytes of several rtmp messages, keeping the
// first error, so a whole exchange can be built without checking every step
type chunkBatch struct {
	buf []byte
	err error
}

// write chunks msg onto the given chunk stream. msgErr is the error of the
// call that produced msg, so message building and chunking can be chained.
func (b *chunkBatch) write(cs *chunkStreamWriter, msg []byte, msgErr error, msgType MessageType, streamId uint32, ts uint32) *chunkBatch {
	if b.err != nil {
		return b
	}
	if msgErr != nil {
		b.err = msgErr
		return b
	}
	pkt, err := cs.writeData(msg, msgType, streamId, ts)
	if err != nil {
		b.err = err
		return b
	}
	b.buf = append(b.buf, pkt...)
	return b
}

func (b *chunkBatch) bytes() ([]byte, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.buf, nil
}

type chunkStream struct {
	firstChunkFmt uint8
	timestamp     uint32
	pkt           *chunkPacket
	hdr           []byte
	message       []byte
	// hasExtTs is set when the last fmt 0/1/2 header of this chunk stream carried 0xFFFFFF in the
	// timestamp field, which means fmt 3 continuation chunks also carry a 4 byte extended timestamp
	hasExtTs bool
	// chunkRead is the number of payload bytes of the current chunk already appended to message
	chunkRead int
}

func newChunkStream() *chunkStream {
	return &chunkStream{
		timestamp: 0,
		pkt:       &chunkPacket{},
		hdr:       make([]byte, 0, 14),
		message:   make([]byte, 0, FIX_CHUNK_SIZE),
	}
}

type chunkStreamReader struct {
	current   *chunkStream
	cks       map[uint32]*chunkStream
	chunkSize uint32
	state     ParserState
	headCache []byte
	// recvBytes counts every byte fed into readRtmpMessage, used for acknowledgement
	recvBytes uint64
	// limits on what an unauthenticated peer can make us buffer
	maxChunkStreams   int
	maxBufferedMsgLen int
	// buffered is the running total of payload held in incomplete messages
	buffered int
}

func newChunkStreamReader(chunkSize uint32) *chunkStreamReader {
	return &chunkStreamReader{
		current:           newChunkStream(),
		cks:               make(map[uint32]*chunkStream),
		state:             S_BASIC_HEAD,
		chunkSize:         chunkSize,
		headCache:         make([]byte, 0, 14),
		maxChunkStreams:   defaultMaxChunkStreams,
		maxBufferedMsgLen: defaultMaxBufferedMsgLen,
	}
}

func (reader *chunkStreamReader) readRtmpMessage(data []byte, onMsg func(*rtmpMessage) error) error {
	reader.recvBytes += uint64(len(data))
	// a zero length message does not need any payload bytes, so the payload state must also run
	// when no input is left, otherwise the message would only be emitted with the next input
	for len(data) > 0 || (reader.state == S_PAYLOAD && reader.current.pkt.msgHdr.msgLen == 0) {
		switch reader.state {
		case S_BASIC_HEAD:
			length := 0
			if len(reader.headCache) > 0 {
				length = clacBasicHeadLen(reader.headCache)
			} else {
				length = clacBasicHeadLen(data)
			}

			if length > len(reader.headCache)+len(data) {
				reader.headCache = append(reader.headCache, data...)
				return nil
			} else {
				appendLen := length - len(reader.headCache)
				reader.headCache = append(reader.headCache, data[:appendLen]...)
				data = data[appendLen:]
			}
			basic := basicHead{}
			basic.decode(reader.headCache)
			if stream, found := reader.cks[basic.csid]; !found {
				if reader.maxChunkStreams > 0 && len(reader.cks) >= reader.maxChunkStreams {
					return fmt.Errorf("%w: %d", errTooManyChunkStreams, basic.csid)
				}
				reader.current = newChunkStream()
				reader.cks[basic.csid] = reader.current
			} else {
				reader.current = stream
			}
			reader.current.pkt.basic = basic
			reader.headCache = reader.headCache[:0]
			reader.state = S_MSG_HEAD
			if len(reader.current.message) == 0 {
				reader.current.firstChunkFmt = reader.current.pkt.basic.fmt
			}
			if basic.fmt == 3 {
				if reader.current.hasExtTs {
					reader.state = S_EXTEND_TS
				} else {
					reader.state = S_PAYLOAD
				}
			}
		case S_MSG_HEAD:
			length := int(ChunkType[reader.current.pkt.basic.fmt])
			if len(data)+len(reader.current.hdr) < length {
				reader.current.hdr = append(reader.current.hdr, data...)
				return nil
			} else {
				appendLen := length - len(reader.current.hdr)
				reader.current.hdr = append(reader.current.hdr, data[:appendLen]...)
				data = data[appendLen:]
			}
			if err := reader.current.pkt.msgHdr.decode(reader.current.pkt.basic.fmt, reader.current.hdr); err != nil {
				return err
			}
			reader.current.hasExtTs = reader.current.pkt.msgHdr.timestamp == 0x00ffffff
			if reader.current.hasExtTs {
				reader.state = S_EXTEND_TS
			} else {
				reader.state = S_PAYLOAD
			}
			reader.current.hdr = reader.current.hdr[:0]
		case S_EXTEND_TS:
			if len(data)+len(reader.current.hdr) < 4 {
				reader.current.hdr = append(reader.current.hdr, data...)
				return nil
			} else {
				appendLen := 4 - len(reader.current.hdr)
				reader.current.hdr = append(reader.current.hdr, data[:appendLen]...)
				data = data[appendLen:]
			}
			reader.current.pkt.msgHdr.timestamp = binary.BigEndian.Uint32(reader.current.hdr)
			reader.current.hdr = reader.current.hdr[:0]
			reader.state = S_PAYLOAD
		case S_PAYLOAD:
			cs := reader.current
			msgLen := int(cs.pkt.msgHdr.msgLen)
			if msgLen > maxRtmpMessageLen || len(cs.message) > msgLen {
				return errors.New("rtmp message length out of range")
			}
			// needLen is the size of the current chunk payload, computed from the message bytes that
			// were already complete when this chunk started; chunkRead of it was already consumed
			needLen := msgLen - (len(cs.message) - cs.chunkRead)
			if needLen > int(reader.chunkSize) {
				needLen = int(reader.chunkSize)
			}
			remain := needLen - cs.chunkRead
			if reader.maxBufferedMsgLen > 0 && reader.buffered+remain > reader.maxBufferedMsgLen {
				return errBufferedTooMuch
			}
			if len(data) < remain {
				cs.message = append(cs.message, data...)
				cs.chunkRead += len(data)
				reader.buffered += len(data)
				return nil
			}
			cs.message = append(cs.message, data[:remain]...)
			reader.buffered += remain
			data = data[remain:]
			cs.chunkRead = 0
			// the chunk is complete, whatever happens next the parser must look for a new basic header
			reader.state = S_BASIC_HEAD

			if msgLen <= len(cs.message) {
				if cs.firstChunkFmt == 0 {
					cs.timestamp = cs.pkt.msgHdr.timestamp
				} else {
					cs.timestamp += cs.pkt.msgHdr.timestamp
				}
				msg := &rtmpMessage{
					timestamp: cs.timestamp,
					msg:       make([]byte, msgLen),
					msgtype:   MessageType(cs.pkt.msgHdr.msgTypeId),
					streamid:  cs.pkt.msgHdr.msgStreamId,
				}
				copy(msg.msg, cs.message)
				reader.buffered -= len(cs.message)
				cs.message = cs.message[:0]
				if err := onMsg(msg); err != nil {
					return err
				}
			}
		default:
			return errors.New("unknown chunk parser state")
		}
	}
	return nil
}
