package rtcp

import (
	"encoding/binary"
	"errors"
)

type Comm struct {
	Padding     bool
	PT          uint8
	Length      uint16
	PayloadLen  uint16
	payloadLen  int
	PaddingData []byte
}

func (pkt *Comm) payloadSize() int {
	if pkt.payloadLen > 0 || pkt.PayloadLen == 0 {
		return pkt.payloadLen
	}
	return int(pkt.PayloadLen)
}

func (pkt *Comm) Decode(data []byte) error {
	if len(data) < 4 {
		return errors.New("length of app rtcp < 4")
	}
	v := data[0] >> 6
	if v != 2 {
		return errors.New("unsupport rtcp version")
	}
	p := (data[0] >> 5) & 0x01
	if p > 0 {
		pkt.Padding = true
	} else {
		pkt.Padding = false
	}
	pkt.PT = data[1]
	pkt.Length = binary.BigEndian.Uint16(data[2:])
	payloadLen := int(pkt.Length) * 4
	total := payloadLen + 4
	if total > len(data) {
		return errors.New("rtcp packet need more data")
	}
	pkt.PaddingData = nil
	if pkt.Padding {
		if pkt.Length == 0 {
			return errors.New("rtcp padding flag set on empty packet")
		}
		paddingLen := int(data[total-1])
		if paddingLen == 0 || paddingLen > payloadLen {
			return errors.New("rtcp illegal padding length")
		}
		payloadLen -= paddingLen
		pkt.PaddingData = data[total-paddingLen : total]
	}
	pkt.payloadLen = payloadLen
	if payloadLen > 0xFFFF {
		pkt.PayloadLen = 0xFFFF
	} else {
		pkt.PayloadLen = uint16(payloadLen)
	}
	return nil
}

func (pkt *Comm) Encode() []byte {
	data := make([]byte, int(pkt.Length)*4+4)
	data[0] = 0x80
	if pkt.Padding {
		data[0] |= 0x20
		data[len(data)-1] = byte(len(pkt.PaddingData)) + 1
		copy(data[len(data)-1-len(pkt.PaddingData):], pkt.PaddingData)
	}
	data[1] = pkt.PT
	binary.BigEndian.PutUint16(data[2:], pkt.Length)
	return data
}
