package rtcp

import (
	"encoding/binary"
	"errors"
)

//  	  0                   1                   2                   3
//  	  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
// 	     +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// 	     |V=2|P|    SC   |   PT=BYE=203  |             length            |
// 	     +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// 	     |                           SSRC/CSRC                           |
// 	     +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// 	     :                              ...                              :
// 	     +=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+
// (opt) |     length    |            reason for leaving     ...
// 	     +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

type Bye struct {
	Comm
	SC        uint8
	ReasonLen uint8
	Reason    string
	SSRCS     []uint32
}

func NewBye() *Bye {
	return &Bye{
		Comm: Comm{PT: RTCP_BYE},
	}
}

func (pkt *Bye) Decode(data []byte) error {

	if err := pkt.Comm.Decode(data); err != nil {
		return err
	}
	pkt.SC = data[0] & 0x1F
	end := int(pkt.PayloadLen) + 4
	if int(pkt.SC)*4 > end-4 {
		return errors.New("bye rtcp packet need more data")
	}
	offset := 4
	pkt.SSRCS = pkt.SSRCS[:0]
	for i := 0; i < int(pkt.SC); i++ {
		pkt.SSRCS = append(pkt.SSRCS, binary.BigEndian.Uint32(data[offset:]))
		offset += 4
	}
	pkt.ReasonLen = 0
	pkt.Reason = ""
	if offset < end {
		pkt.ReasonLen = data[offset]
		offset++
		if offset+int(pkt.ReasonLen) > end {
			return errors.New("bye rtcp reason exceeds packet")
		}
		pkt.Reason = string(data[offset : offset+int(pkt.ReasonLen)])
	}
	return nil
}

func (pkt *Bye) Encode() []byte {
	pkt.Comm.Length = pkt.calcLength()
	data := pkt.Comm.Encode()
	data[0] |= (0x1F & pkt.SC)
	offset := 4
	for _, ssrc := range pkt.SSRCS {
		binary.BigEndian.PutUint32(data[offset:], ssrc)
		offset += 4
	}
	if len(pkt.Reason) > 0 {
		data[offset] = byte(len(pkt.Reason))
		copy(data[offset+1:], []byte(pkt.Reason))
	}
	return data
}

func (pkt *Bye) calcLength() uint16 {
	length := len(pkt.SSRCS) * 4
	if (len(pkt.Reason)+1)%4 == 0 {
		length += len(pkt.Reason) + 1
	} else {
		length += (len(pkt.Reason) + 4) / 4 * 4
	}
	return uint16(length) / 4
}
