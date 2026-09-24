// Package srt implements the SRT (Secure Reliable Transport) protocol in
// live mode, the mode media is sent in: a caller (Dial) and a listener
// (Listen, Accept) exchange messages over UDP, lost packets are recovered
// by retransmission within a fixed latency, and payloads can be encrypted
// with AES-CTR from a passphrase. It interoperates with libsrt (ffmpeg,
// OBS, srt-live-transmit, SRS, ...), and carries the MPEG-TS the go-mpeg2
// muxer and demuxer produce and consume.
//
// Protocol reference: https://datatracker.ietf.org/doc/html/draft-sharabayko-srt
package srt

import (
	"encoding/binary"
	"errors"
)

const headerSize = 16

// Control packet types.
const (
	ctrlHandshake  = 0x0000
	ctrlKeepalive  = 0x0001
	ctrlACK        = 0x0002
	ctrlNAK        = 0x0003
	ctrlShutdown   = 0x0005
	ctrlACKACK     = 0x0006
	ctrlDropReq    = 0x0007
	ctrlPeerError  = 0x0008
	ctrlUserDefine = 0x7FFF
)

// Packet position flags of a data packet: SRT live mode sends every
// message in a single packet.
const positionSolo = 3

// packet is one SRT packet, data or control.
type packet struct {
	control bool

	// data packets
	seq        uint32
	position   byte
	inOrder    bool
	keyFlags   byte // 0 clear, 1 even key, 2 odd key
	retransmit bool
	msgNo      uint32

	// control packets
	ctrlType uint16
	subtype  uint16
	typeInfo uint32

	timestamp uint32
	dstSocket uint32
	payload   []byte
}

var errShortPacket = errors.New("srt: packet shorter than its header")

func parsePacket(b []byte) (*packet, error) {
	if len(b) < headerSize {
		return nil, errShortPacket
	}
	p := &packet{
		timestamp: binary.BigEndian.Uint32(b[8:]),
		dstSocket: binary.BigEndian.Uint32(b[12:]),
		payload:   b[headerSize:],
	}
	w0 := binary.BigEndian.Uint32(b[0:])
	w1 := binary.BigEndian.Uint32(b[4:])
	if w0&0x80000000 != 0 {
		p.control = true
		p.ctrlType = uint16(w0>>16) & 0x7FFF
		p.subtype = uint16(w0)
		p.typeInfo = w1
		return p, nil
	}
	p.seq = w0
	p.position = byte(w1 >> 30)
	p.inOrder = w1&(1<<29) != 0
	p.keyFlags = byte(w1>>27) & 0x03
	p.retransmit = w1&(1<<26) != 0
	p.msgNo = w1 & 0x03FFFFFF
	return p, nil
}

func (p *packet) marshal() []byte {
	b := make([]byte, headerSize+len(p.payload))
	if p.control {
		binary.BigEndian.PutUint32(b[0:], 0x80000000|uint32(p.ctrlType)<<16|uint32(p.subtype))
		binary.BigEndian.PutUint32(b[4:], p.typeInfo)
	} else {
		binary.BigEndian.PutUint32(b[0:], p.seq&seqMask)
		w1 := uint32(p.position)<<30 | uint32(p.keyFlags&0x03)<<27 | p.msgNo&0x03FFFFFF
		if p.inOrder {
			w1 |= 1 << 29
		}
		if p.retransmit {
			w1 |= 1 << 26
		}
		binary.BigEndian.PutUint32(b[4:], w1)
	}
	binary.BigEndian.PutUint32(b[8:], p.timestamp)
	binary.BigEndian.PutUint32(b[12:], p.dstSocket)
	copy(b[headerSize:], p.payload)
	return b
}

// markRetransmitted sets the R flag in an already marshalled data packet.
func markRetransmitted(raw []byte) {
	raw[4] |= 0x04
}

// Sequence numbers are 31 bits and wrap; comparisons go by the shorter way
// round the circle.
const (
	seqMask = 0x7FFFFFFF
	seqHalf = 0x40000000
)

func seqAdd(s uint32, n int32) uint32 {
	return uint32(int32(s)+n) & seqMask
}

func seqNext(s uint32) uint32 {
	return (s + 1) & seqMask
}

// seqDiff is b-a, taken the short way round.
func seqDiff(a, b uint32) int32 {
	d := int64((b - a) & seqMask)
	if d >= seqHalf {
		d -= seqMask + 1
	}
	return int32(d)
}

func seqLess(a, b uint32) bool {
	return seqDiff(a, b) > 0
}

// encodeLossList packs sequence numbers (sorted, unique) into the NAK
// format: a single number, or a range as start with the top bit set
// followed by end.
func encodeLossList(seqs []uint32) []byte {
	var out []byte
	for i := 0; i < len(seqs); {
		j := i
		for j+1 < len(seqs) && seqs[j+1] == seqNext(seqs[j]) {
			j++
		}
		if j == i {
			out = binary.BigEndian.AppendUint32(out, seqs[i])
		} else {
			out = binary.BigEndian.AppendUint32(out, seqs[i]|0x80000000)
			out = binary.BigEndian.AppendUint32(out, seqs[j])
		}
		i = j + 1
	}
	return out
}

// maxLossRange bounds how many sequence numbers one decoded NAK range may
// name, so a corrupt report cannot demand billions of retransmissions.
const maxLossRange = 1 << 16

// decodeLossList calls fn for every sequence number a NAK reports lost.
func decodeLossList(b []byte, fn func(seq uint32)) {
	for len(b) >= 4 {
		v := binary.BigEndian.Uint32(b)
		b = b[4:]
		if v&0x80000000 == 0 {
			fn(v)
			continue
		}
		if len(b) < 4 {
			return
		}
		start, end := v&seqMask, binary.BigEndian.Uint32(b)&seqMask
		b = b[4:]
		n := seqDiff(start, end)
		if n < 0 || n > maxLossRange {
			continue
		}
		for s, i := start, int32(0); i <= n; s, i = seqNext(s), i+1 {
			fn(s)
		}
	}
}
