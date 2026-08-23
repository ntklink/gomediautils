package rtp

import (
	"encoding/binary"
	"errors"

	"github.com/ntklink/gomediautils/go-codec"
)

//RFC3640
// mpeg4-generic
// +---------+-----------+-----------+---------------+
// | RTP     | AU Header | Auxiliary | Access Unit   |
// | Header  | Section   | Section   | Data Section  |
// +---------+-----------+-----------+---------------+
// 	<----------RTP Packet Payload----------->
//
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+- .. -+-+-+-+-+-+-+-+-+-+
// |AU-headers-length|AU-header|AU-header|      |AU-header|padding|
// |                 |   (1)   |   (2)   |      |   (n)   | bits  |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+- .. -+-+-+-+-+-+-+-+-+-+

// Au-headers-length 2 bytes

//      Au-header
// +---------------------------------------+
// |     AU-size                           |
// +---------------------------------------+
// |     AU-Index / AU-Index-delta         |
// +---------------------------------------+
// |     CTS-flag                          |
// +---------------------------------------+
// |     CTS-delta                         |
// +---------------------------------------+
// |     DTS-flag                          |
// +---------------------------------------+
// |     DTS-delta                         |
// +---------------------------------------+
// |     RAP-flag                          |
// +---------------------------------------+
// |     Stream-state                      |
// +---------------------------------------+

type AACPacker struct {
	CommPacker
	pt       uint8
	ssrc     uint32
	sequence uint16
}

func NewAACPacker(pt uint8, ssrc uint32, sequence uint16, mtu int) *AACPacker {
	return &AACPacker{
		pt:         pt,
		ssrc:       ssrc,
		sequence:   sequence,
		CommPacker: CommPacker{mtu: mtu},
	}
}

func (packer *AACPacker) Pack(data []byte, timestamp uint32) error {
	if len(data)+4+RTP_FIX_HEAD_LEN > packer.mtu {
		return errors.New("unsupport fragment aac into multi rtp packet")
	}
	pkg := RtpPacket{}
	pkg.Header.PayloadType = packer.pt
	pkg.Header.SequenceNumber = packer.sequence
	pkg.Header.SSRC = packer.ssrc
	pkg.Header.Timestamp = timestamp
	pkg.Header.Marker = 1
	pkg.Payload = make([]byte, 4+len(data))
	binary.BigEndian.PutUint16(pkg.Payload, 16)
	size := uint16(len(data))
	pkg.Payload[2] = uint8(size >> 5)
	pkg.Payload[3] = uint8((size & 0x1F) << 3)
	copy(pkg.Payload[4:], data)
	packer.sequence++
	if packer.onRtp != nil {
		packer.onRtp(&pkg)
	}
	if packer.onPacket != nil {
		return packer.onPacket(pkg.Encode())
	}

	return nil
}

type AACUnPacker struct {
	CommUnPacker
	sizeLenth   int
	indexLength int
	asc         []byte
}

func NewAACUnPacker(sizeLength int, indexLength int, asc []byte) *AACUnPacker {
	if sizeLength <= 0 {
		// RFC3640 AAC-hbr defaults
		sizeLength = 13
		indexLength = 3
	}
	unpacker := &AACUnPacker{
		sizeLenth:   sizeLength,
		indexLength: indexLength,
		asc:         make([]byte, len(asc)),
	}
	copy(unpacker.asc, asc)
	return unpacker
}

func (unpacker *AACUnPacker) UnPack(pkt []byte) error {
	pkg := &RtpPacket{}
	if err := pkg.Decode(pkt); err != nil {
		return err
	}
	if len(pkg.Payload) < 4 {
		return errors.New("aac rtp pakcet less than 4 byte")
	}

	if unpacker.onRtp != nil {
		unpacker.onRtp(pkg)
	}

	auHeaderBits := int(unpacker.sizeLenth + unpacker.indexLength)
	if auHeaderBits <= 0 || unpacker.sizeLenth <= 0 || unpacker.sizeLenth > 16 {
		return errors.New("aac rtp illegal sizeLength/indexLength")
	}
	headLengthBits := int(binary.BigEndian.Uint16(pkg.Payload))
	headLength := (headLengthBits + 7) / 8
	payload := pkg.Payload[2:]
	if headLength > len(payload) {
		return errors.New("aac rtp AU-headers-length exceeds payload")
	}
	auNum := headLengthBits / auHeaderBits
	if auNum == 0 {
		return errors.New("aac rtp packet without AU header")
	}
	sizes := make([]int, auNum)
	bs := codec.NewBitStream(payload[:headLength])
	for i := 0; i < auNum; i++ {
		sizes[i] = int(bs.Uint16(unpacker.sizeLenth))
		if unpacker.indexLength > 0 {
			bs.SkipBits(unpacker.indexLength)
		}
	}
	payload = payload[headLength:]

	for i := 0; i < len(sizes); i++ {
		if sizes[i] > len(payload) {
			return errors.New("aac rtp AU size exceeds payload")
		}
		var adts []byte
		if len(unpacker.asc) > 0 {
			adtsHdr, err := codec.ConvertASCToADTS(unpacker.asc, sizes[i]+7)
			if err != nil || adtsHdr == nil {
				return errors.New("aac rtp illegal AudioSpecificConfig")
			}
			adts = adtsHdr.Encode()
		}
		adts = append(adts, payload[:sizes[i]]...)
		if unpacker.onFrame != nil {
			unpacker.onFrame(adts, pkg.Header.Timestamp, false)
		}
		payload = payload[sizes[i]:]
	}
	return nil
}
