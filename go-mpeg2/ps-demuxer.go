package mpeg2

import (
	"github.com/yapingcat/gomedia/go-codec"
)

type psstream struct {
	sid       uint8
	cid       PS_STREAM_TYPE
	pts       uint64
	dts       uint64
	streamBuf []byte
}

func newpsstream(sid uint8, cid PS_STREAM_TYPE) *psstream {
	return &psstream{
		sid:       sid,
		cid:       cid,
		streamBuf: make([]byte, 0, 4096),
	}
}

type PSDemuxer struct {
	streamMap map[uint8]*psstream
	pkg       *PSPacket
	mpeg1     bool
	cache     []byte
	OnFrame   func(frame []byte, cid PS_STREAM_TYPE, pts uint64, dts uint64)
	//解ps包过程中，解码回调psm，system header，pes包等
	//decodeResult 解码ps包时的产生的错误
	//这个回调主要用于debug，查看是否ps包存在问题
	OnPacket func(pkg Display, decodeResult error)
}

func NewPSDemuxer() *PSDemuxer {
	return &PSDemuxer{
		streamMap: make(map[uint8]*psstream),
		pkg:       new(PSPacket),
		cache:     make([]byte, 0, 256),
		OnFrame:   nil,
		OnPacket:  nil,
	}
}

// findPSStartCode returns the offset (relative to data) of the next
// 0x000001xx start code whose stream id is a PS level id (>= 0xB9), or -1.
// Elementary stream NAL start codes (first byte < 0x80) are skipped.
func findPSStartCode(data []byte, offset int) int {
	for i := offset; i+3 < len(data); i++ {
		if data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0x01 && data[i+3] >= 0xB9 {
			return i
		}
	}
	return -1
}

// seekByte moves the bitstream to the absolute byte position pos.
func seekByte(bs *codec.BitStream, pos int) {
	// align to a byte boundary first
	if partial := bs.RemainBits() % 8; partial > 0 {
		bs.SkipBits(partial)
	}
	cur := bs.ByteOffset()
	if pos > cur {
		bs.SkipBits((pos - cur) * 8)
	} else if pos < cur {
		bs.UnRead((cur - pos) * 8)
	}
}

func (psdemuxer *PSDemuxer) Input(data []byte) error {
	var bs *codec.BitStream
	if len(psdemuxer.cache) > 0 {
		psdemuxer.cache = append(psdemuxer.cache, data...)
		bs = codec.NewBitStream(psdemuxer.cache)
	} else {
		bs = codec.NewBitStream(data)
	}

	// keep the unconsumed tail for the next Input call; the buffer is
	// reused (append/memmove handles the overlapping case)
	saveReseved := func() {
		psdemuxer.cache = append(psdemuxer.cache[:0], bs.RemainData()...)
	}

	var lastErr error = nil
	for !bs.EOS() {
		if bs.RemainBits() < 32 {
			saveReseved()
			return errNeedMore
		}
		startPos := bs.ByteOffset()
		prefix_code := bs.NextBits(32)
		var ret error = nil
		switch prefix_code {
		case 0x000001BA: //pack header
			if psdemuxer.pkg.Header == nil {
				psdemuxer.pkg.Header = new(PSPackHeader)
			}
			ret = psdemuxer.pkg.Header.Decode(bs)
			if ret == nil {
				psdemuxer.mpeg1 = psdemuxer.pkg.Header.IsMpeg1
			}
			if psdemuxer.OnPacket != nil {
				psdemuxer.OnPacket(psdemuxer.pkg.Header, ret)
			}
		case 0x000001BB: //system header
			if psdemuxer.pkg.Header == nil {
				ret = errParser
			} else {
				if psdemuxer.pkg.System == nil {
					psdemuxer.pkg.System = new(System_header)
				}
				ret = psdemuxer.pkg.System.Decode(bs)
				if psdemuxer.OnPacket != nil {
					psdemuxer.OnPacket(psdemuxer.pkg.System, ret)
				}
			}
		case 0x000001BC: //program stream map
			if psdemuxer.pkg.Psm == nil {
				psdemuxer.pkg.Psm = new(Program_stream_map)
			}
			if ret = psdemuxer.pkg.Psm.Decode(bs); ret == nil {
				for _, streaminfo := range psdemuxer.pkg.Psm.Stream_map {
					if _, found := psdemuxer.streamMap[streaminfo.Elementary_stream_id]; !found {
						stream := newpsstream(streaminfo.Elementary_stream_id, PS_STREAM_TYPE(streaminfo.Stream_type))
						psdemuxer.streamMap[stream.sid] = stream
					}
				}
			}
			if psdemuxer.OnPacket != nil {
				psdemuxer.OnPacket(psdemuxer.pkg.Psm, ret)
			}
		case 0x000001BD, 0x000001BE, 0x000001BF, 0x000001F0, 0x000001F1,
			0x000001F2, 0x000001F3, 0x000001F4, 0x000001F5, 0x000001F6,
			0x000001F7, 0x000001F8, 0x000001F9, 0x000001FA, 0x000001FB:
			if psdemuxer.pkg.CommPes == nil {
				psdemuxer.pkg.CommPes = new(CommonPesPacket)
			}
			ret = psdemuxer.pkg.CommPes.Decode(bs)
		case 0x000001FF: //program stream directory
			if psdemuxer.pkg.Psd == nil {
				psdemuxer.pkg.Psd = new(Program_stream_directory)
			}
			ret = psdemuxer.pkg.Psd.Decode(bs)
		case 0x000001B9: //MPEG_program_end_code
			bs.SkipBits(32)
			continue
		default:
			if prefix_code&0xFFFFFFE0 == 0x000001C0 || prefix_code&0xFFFFFFE0 == 0x000001E0 {
				ret = psdemuxer.decodePes(bs, startPos)
			} else {
				bs.SkipBits(8)
				continue
			}
		}

		if ret == nil {
			continue
		}
		if mpegerr, ok := ret.(Error); ok && mpegerr.NeedMore() {
			// rewind to the start code of the incomplete packet and wait for more data
			seekByte(bs, startPos)
			saveReseved()
			return ret
		}
		// parser error: resync on the next PS start code after this one
		lastErr = ret
		next := findPSStartCode(bs.Bits(), startPos+1)
		if next < 0 {
			next = len(bs.Bits())
		}
		seekByte(bs, next)
	}

	// everything consumed
	if len(psdemuxer.cache) > 0 {
		psdemuxer.cache = psdemuxer.cache[:0]
	}
	return lastErr
}

// decodePes decodes one PES packet starting at startPos and dispatches its payload.
func (psdemuxer *PSDemuxer) decodePes(bs *codec.BitStream, startPos int) error {
	if psdemuxer.pkg.Pes == nil {
		psdemuxer.pkg.Pes = NewPesPacket()
	}
	pes := psdemuxer.pkg.Pes
	pes.Pes_payload = nil
	var ret error
	if psdemuxer.mpeg1 {
		ret = pes.DecodeMpeg1(bs)
	} else {
		ret = pes.Decode(bs)
	}
	if ret == nil && pes.PES_packet_length == 0 {
		// unbounded PES packet: in a program stream the payload ends at the next
		// PS start code. Without one we have to wait for more data.
		idx := findPSStartCode(pes.Pes_payload, 0)
		if idx < 0 {
			seekByte(bs, startPos)
			return errNeedMore
		}
		bs.UnRead((len(pes.Pes_payload) - idx) * 8)
		pes.Pes_payload = pes.Pes_payload[:idx]
	}
	if psdemuxer.OnPacket != nil {
		psdemuxer.OnPacket(pes, ret)
	}
	if ret != nil {
		return ret
	}
	if stream, found := psdemuxer.streamMap[pes.Stream_id]; found {
		if psdemuxer.mpeg1 && stream.cid == PS_STREAM_UNKNOW {
			psdemuxer.guessCodecid(stream)
		}
		psdemuxer.demuxPespacket(stream, pes)
	} else if psdemuxer.mpeg1 {
		stream := newpsstream(pes.Stream_id, PS_STREAM_UNKNOW)
		psdemuxer.streamMap[stream.sid] = stream
		stream.streamBuf = append(stream.streamBuf, pes.Pes_payload...)
		stream.pts = pes.Pts
		stream.dts = pes.Dts
	}
	return nil
}

// Flush reports the tail of every stream that is still buffered. The buffers
// are consumed, so calling it twice does not emit the same data again.
func (psdemuxer *PSDemuxer) Flush() {
	for _, stream := range psdemuxer.streamMap {
		if len(stream.streamBuf) == 0 {
			continue
		}
		if psdemuxer.OnFrame != nil {
			psdemuxer.OnFrame(stream.streamBuf, stream.cid, stream.pts/90, stream.dts/90)
		}
		stream.streamBuf = stream.streamBuf[:0]
	}
}

func (psdemuxer *PSDemuxer) guessCodecid(stream *psstream) {
	if stream.sid&0xE0 == uint8(PES_STREAM_AUDIO) {
		stream.cid = PS_STREAM_AAC
	} else if stream.sid&0xE0 == uint8(PES_STREAM_VIDEO) {
		h264score := 0
		h265score := 0
		codec.SplitFrame(stream.streamBuf, func(nalu []byte) bool {
			h264nalutype := codec.H264NaluTypeWithoutStartCode(nalu)
			h265nalutype := codec.H265NaluTypeWithoutStartCode(nalu)
			if h264nalutype == codec.H264_NAL_PPS ||
				h264nalutype == codec.H264_NAL_SPS ||
				h264nalutype == codec.H264_NAL_I_SLICE {
				h264score += 2
			} else if h264nalutype < 5 {
				h264score += 1
			} else if h264nalutype > 20 {
				h264score -= 1
			}

			if h265nalutype == codec.H265_NAL_PPS ||
				h265nalutype == codec.H265_NAL_SPS ||
				h265nalutype == codec.H265_NAL_VPS ||
				(h265nalutype >= codec.H265_NAL_SLICE_BLA_W_LP && h265nalutype <= codec.H265_NAL_SLICE_CRA) {
				h265score += 2
			} else if h265nalutype >= codec.H265_NAL_Slice_TRAIL_N && h265nalutype <= codec.H265_NAL_SLICE_RASL_R {
				h265score += 1
			} else if h265nalutype > 40 {
				h265score -= 1
			}
			if h264score > h265score && h264score >= 4 {
				stream.cid = PS_STREAM_H264
			} else if h264score < h265score && h265score >= 4 {
				stream.cid = PS_STREAM_H265
			}
			return true
		})
	}
}

func (psdemuxer *PSDemuxer) demuxPespacket(stream *psstream, pes *PesPacket) error {
	switch stream.cid {
	case PS_STREAM_AAC, PS_STREAM_G711A, PS_STREAM_G711U:
		return psdemuxer.demuxAudio(stream, pes)
	case PS_STREAM_H264, PS_STREAM_H265:
		return psdemuxer.demuxH26x(stream, pes)
	case PS_STREAM_UNKNOW:
		if stream.pts != pes.Pts {
			stream.streamBuf = stream.streamBuf[:0]
		}
		stream.streamBuf = append(stream.streamBuf, pes.Pes_payload...)
		stream.pts = pes.Pts
		stream.dts = pes.Dts
	}
	return nil
}

func (psdemuxer *PSDemuxer) demuxAudio(stream *psstream, pes *PesPacket) error {
	if stream.pts != pes.Pts && len(stream.streamBuf) > 0 {
		if psdemuxer.OnFrame != nil {
			psdemuxer.OnFrame(stream.streamBuf, stream.cid, stream.pts/90, stream.dts/90)
		}
		stream.streamBuf = stream.streamBuf[:0]
	}
	stream.streamBuf = append(stream.streamBuf, pes.Pes_payload...)
	stream.pts = pes.Pts
	stream.dts = pes.Dts
	return nil
}

func (psdemuxer *PSDemuxer) demuxH26x(stream *psstream, pes *PesPacket) error {
	if len(stream.streamBuf) == 0 {
		stream.pts = pes.Pts
		stream.dts = pes.Dts
	}
	stream.streamBuf = append(stream.streamBuf, pes.Pes_payload...)
	start, sc := codec.FindStartCode(stream.streamBuf, 0)
	if start < 0 {
		// no start code yet, keep buffering
		stream.pts = pes.Pts
		stream.dts = pes.Dts
		return nil
	}
	for {
		end, sc2 := codec.FindStartCode(stream.streamBuf, start+int(sc))
		if end < 0 {
			break
		}
		if stream.cid == PS_STREAM_H264 {
			naluType := codec.H264NaluType(stream.streamBuf[start:])
			if naluType != codec.H264_NAL_AUD {
				if psdemuxer.OnFrame != nil {
					psdemuxer.OnFrame(stream.streamBuf[start:end], stream.cid, stream.pts/90, stream.dts/90)
				}
			}
		} else if stream.cid == PS_STREAM_H265 {
			naluType := codec.H265NaluType(stream.streamBuf[start:])
			if naluType != codec.H265_NAL_AUD {
				if psdemuxer.OnFrame != nil {
					psdemuxer.OnFrame(stream.streamBuf[start:end], stream.cid, stream.pts/90, stream.dts/90)
				}
			}
		}
		start = end
		sc = sc2
	}
	if start > 0 {
		// keep the trailing (incomplete) nalu at the front of the buffer
		n := copy(stream.streamBuf, stream.streamBuf[start:])
		stream.streamBuf = stream.streamBuf[:n]
	}
	stream.pts = pes.Pts
	stream.dts = pes.Dts
	return nil
}
