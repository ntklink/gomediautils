package ogg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ntklink/gomediautils/go-codec"
)

// errNotHeader is returned by oggParser.header for a packet that does not
// carry a codec header. The demuxer then treats the packet as media data
// instead of dropping it.
var errNotHeader = errors.New("ogg: packet is not a codec header")

// oggCodec recognises a codec from the first packet of a logical stream and
// builds the parser for it. Keeping both in one interface makes it impossible
// to register a codec without a parser.
type oggCodec interface {
	codecid() codec.CodecID
	magic() []byte
	magicSize() int
	newParser() oggParser
}

type OpusCodec struct {
}

func (opus OpusCodec) codecid() codec.CodecID {
	return codec.CODECID_AUDIO_OPUS
}

func (opus OpusCodec) magic() []byte {
	return []byte("OpusHead")
}

func (opus OpusCodec) magicSize() int {
	return 8
}

func (opus OpusCodec) newParser() oggParser {
	return &opusDemuxer{lastpts: ^uint64(0)}
}

type VP8Codec struct {
}

func (vp8 VP8Codec) codecid() codec.CodecID {
	return codec.CODECID_VIDEO_VP8
}

func (vp8 VP8Codec) magic() []byte {
	return []byte("OVP80")
}

func (vp8 VP8Codec) magicSize() int {
	return 5
}

func (vp8 VP8Codec) newParser() oggParser {
	return &vp8Demuxer{lastpts: ^uint64(0)}
}

var codecs = []oggCodec{OpusCodec{}, VP8Codec{}}

type oggParser interface {
	// header consumes a codec header packet. It returns errNotHeader when the
	// packet is media data rather than a header.
	header(stream *oggStream, packet []byte) (err error)
	// packet times one media packet. The timestamps are in the codec's own
	// clock; clockRate says what that clock is.
	packet(stream *oggStream, packet []byte) (frame []byte, pts uint64, dts uint64, err error)
	// clockRate is how many timestamp units of this parser make a second, so
	// the demuxer can report milliseconds like every other demuxer in this
	// module. A parser that cannot say yet returns 0.
	clockRate() uint64
	gptopts(granulePos uint64) uint64
	extraData() []byte
}

type opusDemuxer struct {
	extradata []byte
	ctx       codec.OpusContext
	lastpts   uint64
	granule   uint64
}

// opus ID head
// 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |      'O'      |      'p'      |      'u'      |      's'      |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |      'H'      |      'e'      |      'a'      |      'd'      |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |  Version = 1  | Channel Count |           Pre-skip            |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |                     Input Sample Rate (Hz)                    |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |   Output Gain (Q7.8 in dB)    | Mapping Family|               |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+               :
// |                                                               |
// :               Optional Channel Mapping Table...               :
// |                                                               |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

func (opus *opusDemuxer) header(stream *oggStream, packet []byte) (err error) {
	if len(packet) < 8 {
		return errNotHeader
	}
	if bytes.Equal([]byte("OpusHead"), packet[0:8]) {
		opus.extradata = make([]byte, len(packet))
		copy(opus.extradata, packet)
		return opus.ctx.ParseExtranData(packet)
	} else if bytes.Equal([]byte("OpusTags"), packet[0:8]) {
		return nil
	}
	return errNotHeader
}

func (opus *opusDemuxer) packet(stream *oggStream, packet []byte) (frame []byte, pts uint64, dts uint64, err error) {

	if len(packet) == 0 {
		// an opus packet always carries at least a TOC byte (RFC 6716 3.1)
		return nil, 0, 0, errors.New("ogg: empty opus packet")
	}

	if stream.lost == 1 {
		return packet, opus.lastpts, opus.lastpts, nil
	}

	if opus.lastpts == ^uint64(0) {
		opus.lastpts = 0
	}
	frame = packet
	pts = opus.lastpts
	dts = pts

	if opus.granule != stream.currentPage.granulePos && !stream.currentPage.eos {
		opus.lastpts = 0
		opus.granule = stream.currentPage.granulePos
	}

	if opus.lastpts == 0 {
		var duration uint64
		for _, seg := range stream.currentPage.packets {
			if len(seg) == 0 {
				continue
			}
			duration += codec.OpusPacketDuration(seg)
		}
		// granule = samples decoded at the end of the page (incl. pre-skip);
		// on the first page(s) this may be smaller than duration + preskip
		if opus.granule > duration+uint64(opus.ctx.Preskip) {
			opus.lastpts = opus.granule - duration - uint64(opus.ctx.Preskip)
		} else {
			opus.lastpts = 0
		}
	}

	opus.lastpts += codec.OpusPacketDuration(packet)
	return
}

// clockRate is fixed for opus: granule positions and packet durations are
// always counted in 48 kHz samples, whatever the encoder's input rate was
// (rfc7845 section 4).
func (opus *opusDemuxer) clockRate() uint64 {
	return 48000
}

func (opus *opusDemuxer) gptopts(granulePos uint64) uint64 {
	return 0
}

func (opus *opusDemuxer) extraData() []byte {
	return opus.extradata
}

// ffmpeg oggparsevp8.c
type vp8Demuxer struct {
	pktIdx            uint64
	lastpts           uint64
	granule           uint64
	width             uint16
	height            uint16
	sampleAspectratio uint32
	frameRate         uint32
	extradata         []byte
}

func (vp8 *vp8Demuxer) header(stream *oggStream, packet []byte) (err error) {
	if len(packet) < 7 || !bytes.Equal([]byte("OVP80"), packet[0:5]) {
		return errNotHeader
	}

	switch packet[5] {
	case 0x01:
		if packet[6] != 1 {
			return fmt.Errorf("ogg: unsupported vp8 header version %d", packet[6])
		}
		if len(packet) < 26 {
			return errors.New("ogg: vp8 identification header too short")
		}
		vp8.width = binary.BigEndian.Uint16(packet[8:])
		vp8.height = binary.BigEndian.Uint16(packet[10:])
		num := uint32(packet[12])
		num = (num << 8) | uint32(packet[13])
		num = (num << 8) | uint32(packet[14])
		den := uint32(packet[15])
		den = (den << 8) | uint32(packet[16])
		den = (den << 8) | uint32(packet[17])
		if den != 0 {
			vp8.sampleAspectratio = num / den
		}
		num = binary.BigEndian.Uint32(packet[18:])
		den = binary.BigEndian.Uint32(packet[22:])
		if den != 0 {
			vp8.frameRate = num / den
		}
		vp8.extradata = make([]byte, len(packet))
		copy(vp8.extradata, packet)
	case 0x02:
		if packet[6] != 0x20 {
			return errors.New("ogg: malformed vp8 comment header")
		}
		//TODO Parse Comment
	default:
		return fmt.Errorf("ogg: unknown vp8 header type %#x", packet[5])
	}
	return nil
}

func (vp8 *vp8Demuxer) packet(stream *oggStream, packet []byte) (frame []byte, pts uint64, dts uint64, err error) {

	if stream.lost == 1 {
		return packet, vp8.lastpts, vp8.lastpts, nil
	}

	if vp8.granule != stream.currentPage.granulePos {
		vp8.lastpts = 0
		vp8.pktIdx = 0
		vp8.granule = stream.currentPage.granulePos
	}
	var duration uint64 = 0
	for i := int(vp8.pktIdx); i < len(stream.currentPage.packets); i++ {
		if len(stream.currentPage.packets[i]) == 0 {
			continue
		}
		duration += uint64((stream.currentPage.packets[i][0] >> 4) & 1)
	}
	if pts := vp8.gptopts(stream.currentPage.granulePos); pts > duration {
		vp8.lastpts = pts - duration
	} else {
		vp8.lastpts = 0
	}
	frame = packet
	pts = vp8.lastpts
	dts = pts
	vp8.pktIdx++
	return
}

// clockRate for vp8 is the frame rate: its timestamps count frames.
func (vp8 *vp8Demuxer) clockRate() uint64 {
	return uint64(vp8.frameRate)
}

func (vp8 *vp8Demuxer) gptopts(granulePos uint64) uint64 {
	var invcnt uint64 = 0
	if ((granulePos >> 30) & 3) == 0 {
		invcnt = 1
	}
	pts := (granulePos >> 32) - invcnt
	return pts
}

func (vp8 *vp8Demuxer) extraData() []byte {
	return vp8.extradata
}
