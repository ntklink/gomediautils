package ogg

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ntklink/gomediautils/go-codec"
)

type DemuxState int

const (
	DEMUX_PAGE_HEAD DemuxState = iota
	DEMUX_PAGE_PAYLOAD
)

type VideoParam struct {
	CodecId     codec.CodecID
	Width       uint32
	Height      uint32
	FrameRate   uint32
	Aspectratio uint32
	ExtraData   []byte
}

type AudioParam struct {
	CodecId        codec.CodecID
	SampleRate     uint32
	ChannelCount   uint32
	InitialPadding uint32
	ExtraData      []byte
}

type Demuxer struct {
	currentStream *oggStream
	streams       map[uint32]*oggStream
	headCache     []byte
	OnPage        func(page *oggPage)
	OnPacket      func(streamId uint32, granule uint64, packet []byte, lost int)
	// OnFrame reports one media frame. pts and dts are in milliseconds, the
	// unit every demuxer in this module reports and every muxer takes, not
	// the codec's own clock.
	OnFrame func(streamId uint32, cid codec.CodecID, frame []byte, pts uint64, dts uint64, lost int)
	state   DemuxState
	vparam  *VideoParam
	aparam  *AudioParam
}

func NewDemuxer() *Demuxer {
	return &Demuxer{
		headCache: make([]byte, 0, 282),
		state:     DEMUX_PAGE_HEAD,
		streams:   make(map[uint32]*oggStream),
	}
}

func (demuxer *Demuxer) Input(buf []byte) (err error) {

	if len(buf) == 0 {
		return nil
	}

	for {
		switch demuxer.state {
		case DEMUX_PAGE_HEAD:
			headLen := 0
			if len(demuxer.headCache)+len(buf) < 27 {
				demuxer.headCache = append(demuxer.headCache, buf...)
				return nil
			} else {
				segCount := 0
				if len(demuxer.headCache) >= 27 {
					segCount = int(demuxer.headCache[26])
				} else {
					segCount = int(buf[26-len(demuxer.headCache)])
				}

				if len(demuxer.headCache)+len(buf) < int(segCount)+27 {
					demuxer.headCache = append(demuxer.headCache, buf...)
					return nil
				}
				headLen = int(segCount) + 27
			}
			var hdr []byte
			if len(demuxer.headCache) > 0 {
				hdr = demuxer.headCache
				hdr = append(hdr, buf[:headLen-len(demuxer.headCache)]...)
			} else {
				hdr = buf
			}
			page, err := readPage(hdr)
			if err != nil {
				return err
			}
			if demuxer.OnPage != nil {
				demuxer.OnPage(page)
			}
			stream, found := demuxer.streams[page.streamId]

			if found {
				if stream.currentPage.pageSeq+1 != page.pageSeq {
					// page loss: the partially assembled packet can never be
					// completed, discard it instead of emitting a truncated packet
					stream.lost = 1
					stream.cache = stream.cache[:0]
				} else {
					stream.lost = 0
				}
			} else {
				stream = &oggStream{
					currentPage: page,
					streamId:    page.streamId,
					cache:       make([]byte, 0, 1024),
					cid:         codec.CODECID_UNRECOGNIZED,
				}
				demuxer.streams[page.streamId] = stream
			}
			stream.currentPage = page
			demuxer.currentStream = stream
			demuxer.state = DEMUX_PAGE_PAYLOAD
			buf = buf[headLen-len(demuxer.headCache):]
			if len(demuxer.headCache) > 0 {
				demuxer.headCache = demuxer.headCache[:0]
			}
		case DEMUX_PAGE_PAYLOAD:
			stream := demuxer.currentStream
			page := stream.currentPage
			needLen := int(page.payloadLen) - len(page.cache)
			if needLen > len(buf) {
				page.cache = append(page.cache, buf...)
				return nil
			}

			var tmp []byte
			if len(page.cache) > 0 {
				page.cache = append(page.cache, buf[0:needLen]...)
				buf = buf[needLen:]
				tmp = page.cache
			} else {
				tmp = buf[0:page.payloadLen]
				buf = buf[page.payloadLen:]
			}

			idx := 0
			if stream.lost > 0 && page.isContinuePacket {
				removeLen := 0
				for ; idx < int(page.segmentsCount); idx++ {
					if page.seqmentTable[idx] < 255 {
						removeLen += int(page.seqmentTable[idx])
					} else {
						tmp = tmp[removeLen:]
						break
					}
				}
			} else if stream.lost == 0 && page.isContinuePacket {
				// finish the packet continued from the previous page: it ends at
				// the first segment shorter than 255 bytes
				appendLen := 0
				completed := false
				for ; idx < int(page.segmentsCount); idx++ {
					appendLen += int(page.seqmentTable[idx])
					if page.seqmentTable[idx] < 255 {
						completed = true
						idx++
						break
					}
				}
				stream.cache = append(stream.cache, tmp[:appendLen]...)
				tmp = tmp[appendLen:]
				if completed {
					packet := make([]byte, len(stream.cache))
					copy(packet, stream.cache)
					if demuxer.OnPacket != nil {
						demuxer.OnPacket(stream.streamId, stream.currentPage.granulePos, packet, 0)
					}
					page.packets = append(page.packets, packet)
					stream.cache = stream.cache[:0]
				}
			}

			start := 0
			packetLen := 0
			for ; idx < int(page.segmentsCount); idx++ {
				packetLen += int(page.seqmentTable[idx])
				if page.seqmentTable[idx] < 255 {
					packet := tmp[start : start+packetLen]
					if demuxer.OnPacket != nil {
						demuxer.OnPacket(stream.streamId, stream.currentPage.granulePos, packet, 0)
					}
					page.packets = append(page.packets, packet)
					start = start + packetLen
					packetLen = 0
				}
			}

			for _, pkt := range page.packets {
				if err := demuxer.readPacket(stream, pkt); err != nil {
					return err
				}
			}

			if packetLen > 0 {
				stream.cache = append(stream.cache, tmp[start:]...)
			}
			page.cache = page.cache[:0]
			demuxer.state = DEMUX_PAGE_HEAD

		default:
			return fmt.Errorf("ogg: demuxer in unknown state %d", demuxer.state)
		}
	}
}

func (demuxer *Demuxer) GetVideoParam() *VideoParam {
	return demuxer.vparam
}

func (demuxer *Demuxer) GetAudioParam() *AudioParam {
	return demuxer.aparam
}

func (demuxer *Demuxer) findCodec(stream *oggStream, packet []byte) {
	for _, ogg_codec := range codecs {
		if len(packet) >= ogg_codec.magicSize() && bytes.Equal(ogg_codec.magic(), packet[0:ogg_codec.magicSize()]) {
			stream.cid = ogg_codec.codecid()
			stream.parser = ogg_codec.newParser()
			return
		}
	}
}

func (demuxer *Demuxer) readPacket(stream *oggStream, packet []byte) error {
	if stream.currentPage.isFirstPage {
		if stream.cid == codec.CODECID_UNRECOGNIZED {
			demuxer.findCodec(stream, packet)
		}
	}

	if stream.cid == codec.CODECID_UNRECOGNIZED || stream.parser == nil {
		return errors.New("ogg: codec of the logical stream is unknown")
	}

	// header pages are the first page of the stream and any page that has not
	// decoded a sample yet; a packet that turns out to be media data is handed
	// to the media path instead of being dropped
	if stream.currentPage.isFirstPage || stream.currentPage.granulePos == 0 {
		err := stream.parser.header(stream, packet)
		if err == nil {
			return demuxer.updateParam(stream)
		}
		if !errors.Is(err, errNotHeader) {
			return err
		}
	}

	frame, pts, dts, err := stream.parser.packet(stream, packet)
	if err != nil {
		return err
	}
	if demuxer.OnFrame != nil {
		rate := stream.parser.clockRate()
		demuxer.OnFrame(stream.streamId, stream.cid, frame,
			toMilliseconds(pts, rate), toMilliseconds(dts, rate), stream.lost)
	}
	return nil
}

// toMilliseconds converts a timestamp from the codec's clock. A parser that
// does not know its clock yet leaves the value alone rather than dividing by
// zero.
func toMilliseconds(ts uint64, clockRate uint64) uint64 {
	if clockRate == 0 {
		return ts
	}
	return ts * 1000 / clockRate
}

// updateParam publishes the stream parameters as soon as the codec header of
// the stream has been decoded.
func (demuxer *Demuxer) updateParam(stream *oggStream) error {
	switch stream.cid {
	case codec.CODECID_AUDIO_OPUS:
		if demuxer.aparam != nil {
			return nil
		}
		opus, ok := stream.parser.(*opusDemuxer)
		if !ok {
			return fmt.Errorf("ogg: opus stream parsed by %T", stream.parser)
		}
		if opus.extradata == nil {
			// OpusTags before OpusHead, nothing to publish yet
			return nil
		}
		demuxer.aparam = &AudioParam{
			CodecId:        codec.CODECID_AUDIO_OPUS,
			SampleRate:     uint32(opus.ctx.SampleRate),
			ChannelCount:   uint32(opus.ctx.ChannelCount),
			InitialPadding: uint32(opus.ctx.Preskip),
			ExtraData:      opus.extradata,
		}
	case codec.CODECID_VIDEO_VP8:
		if demuxer.vparam != nil {
			return nil
		}
		vp8, ok := stream.parser.(*vp8Demuxer)
		if !ok {
			return fmt.Errorf("ogg: vp8 stream parsed by %T", stream.parser)
		}
		if vp8.extradata == nil {
			return nil
		}
		demuxer.vparam = &VideoParam{
			CodecId:     codec.CODECID_VIDEO_VP8,
			Width:       uint32(vp8.width),
			Height:      uint32(vp8.height),
			FrameRate:   vp8.frameRate,
			Aspectratio: vp8.sampleAspectratio,
			ExtraData:   vp8.extradata,
		}
	default:
		return fmt.Errorf("ogg: unsupported codec id %d", stream.cid)
	}
	return nil
}
