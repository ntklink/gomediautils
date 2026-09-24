package srt

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Handshake types. Values of 1000 and up are rejections.
const (
	hsInduction  = 0x00000001
	hsConclusion = 0xFFFFFFFF
	hsAgreement  = 0xFFFFFFFE
	hsDone       = 0xFFFFFFFD
	hsRejectBase = 1000
)

// Rejection reasons (SRT_REJ_*), sent as hsRejectBase + reason.
const (
	RejectUnknown    = 0
	RejectPeer       = 2
	RejectResource   = 3
	RejectRogue      = 4
	RejectVersion    = 8
	RejectBadSecret  = 10
	RejectUnsecure   = 11
	RejectMessageAPI = 12
)

const (
	hsMagic       = 0x4A17 // extension field of a version 5 induction response
	udtDgram      = 2      // extension field of an induction request
	srtVersion    = 0x010503
	defaultMTU    = 1500
	defaultWindow = 8192
)

// Handshake extension types.
const (
	extHSReq  = 1
	extHSRsp  = 2
	extKMReq  = 3
	extKMRsp  = 4
	extSID    = 5
	extConfig = 0x4 // extension field flag announcing SID and friends
)

// HSREQ flags.
const (
	flagTSBPDSnd    = 0x01
	flagTSBPDRcv    = 0x02
	flagCrypt       = 0x04
	flagTLPktDrop   = 0x08
	flagPeriodicNAK = 0x10
	flagRexmit      = 0x20
	flagStream      = 0x40
)

// handshake is the control information of a handshake packet.
type handshake struct {
	version    uint32
	encryption uint16
	extension  uint16
	isn        uint32
	mtu        uint32
	window     uint32
	hsType     uint32
	socketID   uint32
	cookie     uint32
	peerIP     [16]byte

	// extensions
	hsReq    *hsReq
	hsRsp    *hsReq
	kmReq    []byte
	kmRsp    []byte
	streamID string
}

// hsReq is the content of HSREQ and HSRSP: the SRT version, the feature
// flags and the latency each direction wants, in milliseconds.
type hsReq struct {
	version   uint32
	flags     uint32
	recvDelay uint16
	sendDelay uint16
}

func (h *hsReq) marshal() []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b[0:], h.version)
	binary.BigEndian.PutUint32(b[4:], h.flags)
	binary.BigEndian.PutUint16(b[8:], h.recvDelay)
	binary.BigEndian.PutUint16(b[10:], h.sendDelay)
	return b
}

func parseHSReq(b []byte) (*hsReq, error) {
	if len(b) < 12 {
		return nil, errors.New("srt: short HSREQ extension")
	}
	return &hsReq{
		version:   binary.BigEndian.Uint32(b[0:]),
		flags:     binary.BigEndian.Uint32(b[4:]),
		recvDelay: binary.BigEndian.Uint16(b[8:]),
		sendDelay: binary.BigEndian.Uint16(b[10:]),
	}, nil
}

const hsCIFSize = 48

func (h *handshake) marshal() []byte {
	b := make([]byte, hsCIFSize)
	binary.BigEndian.PutUint32(b[0:], h.version)
	binary.BigEndian.PutUint16(b[4:], h.encryption)
	binary.BigEndian.PutUint16(b[6:], h.extension)
	binary.BigEndian.PutUint32(b[8:], h.isn)
	binary.BigEndian.PutUint32(b[12:], h.mtu)
	binary.BigEndian.PutUint32(b[16:], h.window)
	binary.BigEndian.PutUint32(b[20:], h.hsType)
	binary.BigEndian.PutUint32(b[24:], h.socketID)
	binary.BigEndian.PutUint32(b[28:], h.cookie)
	copy(b[32:], h.peerIP[:])
	if h.hsReq != nil {
		b = appendExt(b, extHSReq, h.hsReq.marshal())
	}
	if h.hsRsp != nil {
		b = appendExt(b, extHSRsp, h.hsRsp.marshal())
	}
	if h.kmReq != nil {
		b = appendExt(b, extKMReq, h.kmReq)
	}
	if h.kmRsp != nil {
		b = appendExt(b, extKMRsp, h.kmRsp)
	}
	if h.streamID != "" {
		b = appendExt(b, extSID, packString(h.streamID))
	}
	return b
}

func appendExt(b []byte, typ uint16, content []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, typ)
	b = binary.BigEndian.AppendUint16(b, uint16(len(content)/4))
	return append(b, content...)
}

// packString lays a string out the way libsrt sends the stream id: padded
// with zeros to whole 32 bit words, the bytes of each word reversed.
func packString(s string) []byte {
	b := make([]byte, (len(s)+3)/4*4)
	copy(b, s)
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return b
}

func unpackString(b []byte) string {
	out := make([]byte, len(b)/4*4)
	for i := 0; i+4 <= len(b); i += 4 {
		out[i], out[i+1], out[i+2], out[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	for len(out) > 0 && out[len(out)-1] == 0 {
		out = out[:len(out)-1]
	}
	return string(out)
}

// maxStreamID is the longest stream id libsrt accepts.
const maxStreamID = 512

func parseHandshake(b []byte) (*handshake, error) {
	if len(b) < hsCIFSize {
		return nil, errors.New("srt: short handshake")
	}
	h := &handshake{
		version:    binary.BigEndian.Uint32(b[0:]),
		encryption: binary.BigEndian.Uint16(b[4:]),
		extension:  binary.BigEndian.Uint16(b[6:]),
		isn:        binary.BigEndian.Uint32(b[8:]) & seqMask,
		mtu:        binary.BigEndian.Uint32(b[12:]),
		window:     binary.BigEndian.Uint32(b[16:]),
		hsType:     binary.BigEndian.Uint32(b[20:]),
		socketID:   binary.BigEndian.Uint32(b[24:]),
		cookie:     binary.BigEndian.Uint32(b[28:]),
	}
	copy(h.peerIP[:], b[32:48])
	if h.version < 5 {
		// version 4 carries no extensions
		return h, nil
	}
	for rest := b[hsCIFSize:]; len(rest) >= 4; {
		typ := binary.BigEndian.Uint16(rest)
		size := int(binary.BigEndian.Uint16(rest[2:])) * 4
		rest = rest[4:]
		if size > len(rest) {
			return nil, fmt.Errorf("srt: handshake extension %d overruns the packet", typ)
		}
		content := rest[:size]
		rest = rest[size:]
		var err error
		switch typ {
		case extHSReq:
			h.hsReq, err = parseHSReq(content)
		case extHSRsp:
			h.hsRsp, err = parseHSReq(content)
		case extKMReq:
			h.kmReq = append([]byte(nil), content...)
		case extKMRsp:
			h.kmRsp = append([]byte(nil), content...)
		case extSID:
			if size > maxStreamID {
				return nil, errors.New("srt: stream id longer than 512 bytes")
			}
			h.streamID = unpackString(content)
		}
		if err != nil {
			return nil, err
		}
	}
	return h, nil
}

// RejectError is returned by Dial when the listener turned the connection
// down, and by an Authorize callback to turn one down.
type RejectError struct {
	Reason int
}

func (e *RejectError) Error() string {
	names := map[int]string{
		RejectUnknown: "unknown", RejectPeer: "rejected by peer", RejectResource: "no resources",
		RejectRogue: "malformed handshake", RejectVersion: "version mismatch",
		RejectBadSecret: "wrong passphrase", RejectUnsecure: "encryption required on one side only",
		RejectMessageAPI: "stream mode not supported",
	}
	if name, ok := names[e.Reason]; ok {
		return "srt: connection rejected: " + name
	}
	return fmt.Sprintf("srt: connection rejected (reason %d)", e.Reason)
}
