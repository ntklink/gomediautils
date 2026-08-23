package rtp

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/ntklink/gomediautils/go-codec"
)

// rfc6184 https://datatracker.ietf.org/doc/html/rfc6184
//
// Payload Packet    Single NAL    Non-Interleaved    Interleaved
// Type    Type      Unit Mode           Mode             Mode
// -------------------------------------------------------------
// 0      reserved      ig               ig               ig
// 1-23   NAL unit     yes              yes               no
// 24     STAP-A        no              yes               no
// 25     STAP-B        no               no              yes
// 26     MTAP16        no               no              yes
// 27     MTAP24        no               no              yes
// 28     FU-A          no              yes              yes
// 29     FU-B          no               no              yes
// 30-31  reserved      ig               ig               ig
//

type H264Packer struct {
	CommPacker
	ssrc     uint32
	pt       uint8
	sequence uint16
	stap_a   bool
	sps      []byte
	pps      []byte
}

func NewH264Packer(pt uint8, ssrc uint32, sequence uint16, mtu int) *H264Packer {
	return &H264Packer{
		pt:         pt,
		ssrc:       ssrc,
		sequence:   sequence,
		stap_a:     false,
		CommPacker: CommPacker{mtu: mtu},
	}
}

func (pack *H264Packer) EnableStapA() {
	pack.stap_a = true
}

// Pack turns one access unit into rtp packets. The rtp marker bit delimits an
// access unit, not a nalu, so the whole frame is split first and the marker is
// set on the last packet only.
func (pack *H264Packer) Pack(frame []byte, timestamp uint32) error {
	var nalus [][]byte
	codec.SplitFrame(frame, func(nalu []byte) bool {
		if len(nalu) > 0 {
			nalus = append(nalus, nalu)
		}
		return true
	})

	// payloads holds what goes on the wire; an entry with more than one nalu
	// is aggregated into a STAP-A
	var payloads [][][]byte
	flushParamSets := func() {
		if len(pack.sps) == 0 || len(pack.pps) == 0 {
			return
		}
		// copy: pack.sps/pack.pps are reused across calls
		sps := append([]byte(nil), pack.sps...)
		pps := append([]byte(nil), pack.pps...)
		if len(sps)+len(pps)+5+RTP_FIX_HEAD_LEN <= pack.mtu {
			payloads = append(payloads, [][]byte{sps, pps})
		} else {
			payloads = append(payloads, [][]byte{sps}, [][]byte{pps})
		}
		pack.sps = pack.sps[:0]
		pack.pps = pack.pps[:0]
	}

	for _, nalu := range nalus {
		if pack.stap_a {
			switch codec.H264NaluType(nalu) {
			case codec.H264_NAL_SPS:
				pack.sps = append(pack.sps[:0], nalu...)
				continue
			case codec.H264_NAL_PPS:
				pack.pps = append(pack.pps[:0], nalu...)
				continue
			}
			flushParamSets()
		}
		payloads = append(payloads, [][]byte{nalu})
	}
	if pack.stap_a {
		// a frame that carried nothing but parameter sets still has to go out
		flushParamSets()
	}

	for i, p := range payloads {
		last := i == len(payloads)-1
		var err error
		switch {
		case len(p) > 1:
			err = pack.packStapA(p, timestamp, last)
		case len(p[0])+RTP_FIX_HEAD_LEN <= pack.mtu:
			err = pack.packSingleNalu(p[0], timestamp, last)
		default:
			err = pack.packFuA(p[0], timestamp, last)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (pack *H264Packer) packSingleNalu(nalu []byte, timestamp uint32, last bool) error {
	pkg := RtpPacket{}
	pkg.Header.PayloadType = pack.pt
	pkg.Header.SSRC = pack.ssrc
	pkg.Header.SequenceNumber = pack.sequence
	pkg.Header.Timestamp = timestamp
	if last {
		pkg.Header.Marker = 1
	}
	pkg.Payload = nalu
	pack.sequence++
	if pack.onRtp != nil {
		pack.onRtp(&pkg)
	}
	if pack.onPacket != nil {
		return pack.onPacket(pkg.Encode())
	}
	return nil
}

//  0                   1                   2                   3
//  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// | FU indicator  |   FU header   |                               |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+                               |
// |                                                               |
// |                         FU payload                            |
// |                                                               |
// |                               +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |                               :...OPTIONAL RTP padding        |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

// FU indicator
// +---------------+
// |0|1|2|3|4|5|6|7|
// +-+-+-+-+-+-+-+-+
// |F|NRI|  Type   |
// +---------------+

// FU header
// +---------------+
// |0|1|2|3|4|5|6|7|
// +-+-+-+-+-+-+-+-+
// |S|E|R|  Type   |
// +---------------+

func (pack *H264Packer) packFuA(nalu []byte, timestamp uint32, last bool) (err error) {
	if len(nalu) < 2 {
		return errors.New("h264 nalu too short for FU-A")
	}
	if pack.mtu <= RTP_FIX_HEAD_LEN+2 {
		return errors.New("h264 mtu too small for FU-A")
	}
	var fuIndicator byte = nalu[0]&0xE0 | 0x1c
	var fuHeader byte = nalu[0]&0x1F | 0x80
	nalu = nalu[1:]
	for {
		pkg := RtpPacket{}
		pkg.Header.PayloadType = pack.pt
		pkg.Header.SSRC = pack.ssrc
		pkg.Header.SequenceNumber = pack.sequence
		pkg.Header.Timestamp = timestamp
		if len(nalu)+RTP_FIX_HEAD_LEN+2 <= pack.mtu {
			if last {
				pkg.Header.Marker = 1
			}
			fuHeader |= 0x40
			pkg.Payload = make([]byte, 0, 2+len(nalu))
			pkg.Payload = append(pkg.Payload, fuIndicator)
			pkg.Payload = append(pkg.Payload, fuHeader)
			pkg.Payload = append(pkg.Payload, nalu...)
			if pack.onRtp != nil {
				pack.onRtp(&pkg)
			}
			if pack.onPacket != nil {
				err = pack.onPacket(pkg.Encode())
			}
			pack.sequence++
			return
		}
		pkg.Payload = make([]byte, 0, 2+pack.mtu)
		pkg.Payload = append(pkg.Payload, fuIndicator)
		pkg.Payload = append(pkg.Payload, fuHeader)
		if fuHeader&0x80 > 0 {
			fuHeader &= 0x7F
		}
		pkg.Payload = append(pkg.Payload, nalu[:pack.mtu-2-RTP_FIX_HEAD_LEN]...)
		nalu = nalu[pack.mtu-2-RTP_FIX_HEAD_LEN:]
		if pack.onRtp != nil {
			pack.onRtp(&pkg)
		}
		if pack.onPacket != nil {
			err = pack.onPacket(pkg.Encode())
		}
		pack.sequence++
		if err != nil {
			return
		}
	}
}

//  0                   1                   2                   3
//  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |                          RTP Header                           |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |STAP-A NAL HDR |         NALU 1 Size           | NALU 1 HDR    |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |                         NALU 1 Data                           |
// :                                                               :
// +               +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |               | NALU 2 Size                   | NALU 2 HDR    |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |                         NALU 2 Data                           |
// :                                                               :
// |                               +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |                               :...OPTIONAL RTP padding        |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

func (pack *H264Packer) packStapA(nalus [][]byte, timestamp uint32, last bool) error {
	pkg := RtpPacket{}
	pkg.Header.PayloadType = pack.pt
	pkg.Header.SSRC = pack.ssrc
	pkg.Header.SequenceNumber = pack.sequence
	pkg.Header.Timestamp = timestamp
	if last {
		pkg.Header.Marker = 1
	}

	length := 0
	for _, nalu := range nalus {
		length += len(nalu) + 2
	}

	pkg.Payload = make([]byte, 1, length+1)
	// STAP-A NAL header: F=0, NRI = max NRI of aggregated nalus, type 24
	var nri byte
	for _, nalu := range nalus {
		if len(nalu) > 0 && nalu[0]&0x60 > nri {
			nri = nalu[0] & 0x60
		}
	}
	pkg.Payload[0] = nri | 24
	var tmp [2]byte
	for _, nalu := range nalus {
		binary.BigEndian.PutUint16(tmp[:], uint16(len(nalu)))
		pkg.Payload = append(pkg.Payload, tmp[:]...)
		pkg.Payload = append(pkg.Payload, nalu...)
	}
	pack.sequence++
	if pack.onRtp != nil {
		pack.onRtp(&pkg)
	}
	if pack.onPacket != nil {
		return pack.onPacket(pkg.Encode())
	}
	return nil
}

type H264UnPacker struct {
	CommUnPacker
	timestamp    uint32
	lastSequence uint16
	lost         bool
	frameBuffer  *bytes.Buffer
}

func NewH264UnPacker() *H264UnPacker {
	unpacker := &H264UnPacker{
		frameBuffer: new(bytes.Buffer),
	}
	unpacker.frameBuffer.Grow(1500)
	unpacker.frameBuffer.Write([]byte{0x00, 0x00, 0x00, 0x01})
	return unpacker
}

func (unpacker *H264UnPacker) UnPack(pkt []byte) error {
	pkg := &RtpPacket{}
	if err := pkg.Decode(pkt); err != nil {
		return err
	}

	if len(pkg.Payload) == 0 {
		return nil
	}

	if unpacker.onRtp != nil {
		unpacker.onRtp(pkg)
	}

	packType := pkg.Payload[0] & 0x1f
	switch {
	case 0 < packType && packType < 24:
		unpacker.frameBuffer.Write(pkg.Payload)
		if unpacker.onFrame != nil {
			unpacker.onFrame(unpacker.frameBuffer.Bytes(), pkg.Header.Timestamp, false)
		}
		unpacker.frameBuffer.Truncate(4)
	case packType == 24:
		return unpacker.unpackStapa(pkg)
	case packType == 25:
		fallthrough
	case packType == 26:
		fallthrough
	case packType == 27:
		return errors.New("unsupport h264 rtp packet type")
	case packType == 28:
		return unpacker.unpackFuA(pkg)
	case packType == 29:
		fallthrough
	default:
		return errors.New("unsupport h264 rtp packet type")
	}
	return nil
}

func (unpacker *H264UnPacker) unpackFuA(pkt *RtpPacket) error {
	if len(pkt.Payload) < 2 {
		return errors.New("h264 FU-A payload need 2 bytes at least")
	}
	s := pkt.Payload[1] & 0x80
	e := pkt.Payload[1] & 0x40
	if s > 0 {
		if unpacker.frameBuffer.Len() > 4 {
			if unpacker.onFrame != nil {
				unpacker.onFrame(unpacker.frameBuffer.Bytes(), unpacker.timestamp, true)
			}
			unpacker.frameBuffer.Truncate(4)
		}
		unpacker.timestamp = pkt.Header.Timestamp
		unpacker.frameBuffer.WriteByte((pkt.Payload[0] & 0xE0) | (pkt.Payload[1] & 0x1F))
		unpacker.lost = false
	} else {
		if unpacker.lastSequence+1 != pkt.Header.SequenceNumber {
			unpacker.lost = true
		}
	}
	unpacker.lastSequence = pkt.Header.SequenceNumber
	unpacker.frameBuffer.Write(pkt.Payload[2:])
	if e > 0 {
		if unpacker.onFrame != nil {
			unpacker.onFrame(unpacker.frameBuffer.Bytes(), unpacker.timestamp, unpacker.lost)
		}
		unpacker.frameBuffer.Truncate(4)
	}
	return nil
}

func (unpacker *H264UnPacker) unpackStapa(pkt *RtpPacket) error {
	nalus := pkt.Payload[1:]
	for len(nalus) > 0 {
		if len(nalus) < 2 {
			return errors.New("h264 STAP-A truncated nalu size")
		}
		naluLength := binary.BigEndian.Uint16(nalus)
		if naluLength == 0 || len(nalus)-2 < int(naluLength) {
			return errors.New("h264 STAP-A nalu size out of range")
		}
		unpacker.frameBuffer.Write(nalus[2 : 2+naluLength])
		if unpacker.onFrame != nil {
			unpacker.onFrame(unpacker.frameBuffer.Bytes(), pkt.Header.Timestamp, false)
		}
		nalus = nalus[2+naluLength:]
		unpacker.frameBuffer.Truncate(4)
	}
	return nil
}
