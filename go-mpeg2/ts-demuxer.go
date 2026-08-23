package mpeg2

import (
	"errors"
	"fmt"
	"io"

	"github.com/ntklink/gomediautils/go-codec"
)

type pakcet_t struct {
	payload []byte
	pts     uint64
	dts     uint64
}

func newPacket_t(size uint32) *pakcet_t {
	return &pakcet_t{
		payload: make([]byte, 0, size),
		pts:     0,
		dts:     0,
	}
}

type tsstream struct {
	cid     TS_STREAM_TYPE
	pes_sid PES_STREMA_ID
	pes_pkg *PesPacket
	pkg     *pakcet_t
	// continuity counter of the last packet carrying a payload for this pid
	cc      uint8
	ccValid bool
	// partial PES header that did not fit in a single ts packet
	hdrCache []byte
	// true while waiting for the next payload_unit_start after a discontinuity
	waitPUSI bool

	// Incremental access unit scanner state. A ts packet carries at most 184
	// payload bytes, so rescanning the whole frame buffer on every packet
	// would be quadratic in the frame size. These fields let the scan resume
	// where the previous packet left off; every offset is relative to the
	// start of pkg.payload and is rebased when the buffer is compacted.
	scanPos  int // next index to search for a start code
	frameBeg int // index the current access unit starts at, -1 until known
	vcl      int // vcl nalus seen in the current access unit
}

// checkContinuity validates the continuity counter of a packet carrying a
// payload. It returns false when the packet must be ignored (duplicate).
// On a discontinuity the partially assembled frame is dropped.
func (stream *tsstream) checkContinuity(pkg *TSPacket) bool {
	if pkg.Adaptation_field_control&0x01 == 0 {
		return true
	}
	if stream.ccValid {
		expected := (stream.cc + 1) % 16
		if pkg.Continuity_counter == stream.cc {
			// duplicate packet
			return false
		}
		if pkg.Continuity_counter != expected {
			signalled := pkg.Field != nil && pkg.Field.Discontinuity_indicator == 1
			if !signalled {
				stream.dropPartial()
			}
		}
	}
	stream.cc = pkg.Continuity_counter
	stream.ccValid = true
	return true
}

func (stream *tsstream) dropPartial() {
	if stream.pkg != nil {
		stream.pkg.payload = stream.pkg.payload[:0]
	}
	stream.hdrCache = stream.hdrCache[:0]
	stream.waitPUSI = true
	stream.resetScan()
}

// resetScan puts the access unit scanner back to the start of an empty buffer.
func (stream *tsstream) resetScan() {
	stream.scanPos = 0
	stream.frameBeg = -1
	stream.vcl = 0
}

type tsprogram struct {
	pn      uint16
	streams map[uint16]*tsstream
}

type TSDemuxer struct {
	programs map[uint16]*tsprogram
	// esStreams indexes every elementary stream by its pid, so dispatching a
	// ts packet is a single map lookup instead of a walk over every program
	esStreams  map[uint16]*tsstream
	OnFrame    func(cid TS_STREAM_TYPE, frame []byte, pts uint64, dts uint64)
	OnTSPacket func(pkg *TSPacket)
}

func NewTSDemuxer() *TSDemuxer {
	return &TSDemuxer{
		programs:   make(map[uint16]*tsprogram),
		esStreams:  make(map[uint16]*tsstream),
		OnFrame:    nil,
		OnTSPacket: nil,
	}
}

func (demuxer *TSDemuxer) Input(r io.Reader) error {
	var err error = nil
	var buf []byte
	for {
		if len(buf) > TS_PAKCET_SIZE {
			buf = buf[TS_PAKCET_SIZE:]
		} else {
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					break
				}
				return err
			}
			buf, err = demuxer.probe(r)
			if err != nil && buf == nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return err
			}
		}

		bs := codec.NewBitStream(buf[:TS_PAKCET_SIZE])
		var pkg TSPacket
		if err := pkg.DecodeHeader(bs); err != nil {
			return err
		}
		if pkg.PID == uint16(TS_PID_PAT) {
			if pkg.Payload_unit_start_indicator == 1 {
				bs.SkipBits(8)
			}
			section, serr := ReadSection(TS_TID_PAS, bs)
			if serr != nil {
				return serr
			}
			pat, ok := section.(*Pat)
			if !ok {
				return fmt.Errorf("mpeg2: pid 0 carries a %T instead of a pat", section)
			}
			pkg.Payload = pat
			for _, pmt := range pat.Pmts {
				if pmt.Program_number != 0x0000 {
					if _, found := demuxer.programs[pmt.PID]; !found {
						demuxer.programs[pmt.PID] = &tsprogram{pn: 0, streams: make(map[uint16]*tsstream)}
					}
				}
			}
		} else if pkg.PID == uint16(TS_PID_Nil) {
			continue
		} else if prog, isPmt := demuxer.programs[pkg.PID]; isPmt {
			if pkg.Payload_unit_start_indicator == 1 {
				bs.SkipBits(8) //pointer filed
			}
			section, serr := ReadSection(TS_TID_PMS, bs)
			if serr != nil {
				return serr
			}
			pmt, ok := section.(*Pmt)
			if !ok {
				return fmt.Errorf("mpeg2: pid %d carries a %T instead of a pmt", pkg.PID, section)
			}
			pkg.Payload = pmt
			prog.pn = pmt.Program_number
			for _, ps := range pmt.Streams {
				if _, found := prog.streams[ps.Elementary_PID]; !found {
					stream := &tsstream{
						cid:      TS_STREAM_TYPE(ps.StreamType),
						pes_sid:  findPESIDByStreamType(TS_STREAM_TYPE(ps.StreamType)),
						pes_pkg:  NewPesPacket(),
						waitPUSI: true,
						frameBeg: -1,
					}
					prog.streams[ps.Elementary_PID] = stream
					demuxer.esStreams[ps.Elementary_PID] = stream
				}
			}
		} else if stream, isEs := demuxer.esStreams[pkg.PID]; isEs {
			if !stream.checkContinuity(&pkg) {
				continue
			}
			start := pkg.Payload_unit_start_indicator
			if start == 1 {
				stream.hdrCache = stream.hdrCache[:0]
				stream.waitPUSI = false
				stream.pes_pkg.Pes_payload = nil
				err := stream.pes_pkg.Decode(bs)
				if err != nil {
					if !errors.Is(err, errNeedMore) {
						return err
					}
					if stream.pes_pkg.Pes_payload == nil {
						// the PES header itself does not fit in this packet:
						// buffer it and finish decoding with the next packet
						stream.hdrCache = append(stream.hdrCache, bs.RemainData()...)
						continue
					}
				}
				pkg.Payload = stream.pes_pkg
			} else if stream.waitPUSI {
				// mid-frame data after a discontinuity (or before the first PUSI)
				continue
			} else if len(stream.hdrCache) > 0 {
				stream.hdrCache = append(stream.hdrCache, bs.RemainData()...)
				hbs := codec.NewBitStream(stream.hdrCache)
				stream.pes_pkg.Pes_payload = nil
				err := stream.pes_pkg.Decode(hbs)
				if err != nil {
					if !errors.Is(err, errNeedMore) {
						stream.dropPartial()
						return err
					}
					if stream.pes_pkg.Pes_payload == nil {
						continue
					}
				}
				start = 1
				pkg.Payload = stream.pes_pkg
			} else {
				stream.pes_pkg.Pes_payload = bs.RemainData()
				pkg.Payload = bs.RemainData()
			}
			stype := findPESIDByStreamType(stream.cid)
			if stype == PES_STREAM_AUDIO {
				demuxer.doAudioPesPacket(stream, start)
			} else if stype == PES_STREAM_VIDEO {
				demuxer.doVideoPesPacket(stream, start)
			}
			if len(stream.hdrCache) > 0 {
				// payload has been copied into the frame buffer
				stream.hdrCache = stream.hdrCache[:0]
			}
		}
		if demuxer.OnTSPacket != nil {
			demuxer.OnTSPacket(&pkg)
		}
	}
	demuxer.flush()
	return nil
}

func (demuxer *TSDemuxer) probe(r io.Reader) ([]byte, error) {
	buf := make([]byte, TS_PAKCET_SIZE, 2*TS_PAKCET_SIZE)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	if buf[0] == 0x47 {
		return buf, nil
	}
	buf = buf[:2*TS_PAKCET_SIZE]
	if _, err := io.ReadFull(r, buf[TS_PAKCET_SIZE:]); err != nil {
		return nil, err
	}
LOOP:
	i := 0
	for ; i < TS_PAKCET_SIZE; i++ {
		if buf[i] == 0x47 && buf[i+TS_PAKCET_SIZE] == 0x47 {
			break
		}
	}
	if i == 0 {
		return buf, nil
	} else if i < TS_PAKCET_SIZE {
		copy(buf, buf[i:])
		if _, err := io.ReadFull(r, buf[2*TS_PAKCET_SIZE-i:]); err != nil {
			return buf[:TS_PAKCET_SIZE], err
		} else {
			return buf, nil
		}
	} else {
		copy(buf, buf[TS_PAKCET_SIZE:])
		if _, err := io.ReadFull(r, buf[TS_PAKCET_SIZE:]); err != nil {
			return buf[:TS_PAKCET_SIZE], err
		}
		goto LOOP
	}
}

func (demuxer *TSDemuxer) flush() {
	for _, pm := range demuxer.programs {
		for _, stream := range pm.streams {
			if stream.pkg == nil || len(stream.pkg.payload) == 0 {
				continue
			}

			if demuxer.OnFrame == nil {
				continue
			}
			if stream.cid == TS_STREAM_H264 || stream.cid == TS_STREAM_H265 {
				audLen := leadingAudLen(stream.cid, stream.pkg.payload)
				demuxer.OnFrame(stream.cid, stream.pkg.payload[audLen:], stream.pkg.pts/90, stream.pkg.dts/90)
			} else {
				demuxer.OnFrame(stream.cid, stream.pkg.payload, stream.pkg.pts/90, stream.pkg.dts/90)
			}
			stream.pkg = nil
		}
	}
}

func (demuxer *TSDemuxer) doVideoPesPacket(stream *tsstream, start uint8) {
	if stream.cid != TS_STREAM_H264 && stream.cid != TS_STREAM_H265 {
		return
	}
	if stream.pkg == nil {
		stream.pkg = newPacket_t(1024)
		stream.pkg.pts = stream.pes_pkg.Pts
		stream.pkg.dts = stream.pes_pkg.Dts
		stream.resetScan()
	}
	stream.pkg.payload = append(stream.pkg.payload, stream.pes_pkg.Pes_payload...)
	if update := demuxer.splitH26xFrame(stream); update {
		stream.pkg.pts = stream.pes_pkg.Pts
		stream.pkg.dts = stream.pes_pkg.Dts
	}
}

func (demuxer *TSDemuxer) doAudioPesPacket(stream *tsstream, start uint8) {
	if stream.cid != TS_STREAM_AAC && stream.cid != TS_STREAM_AUDIO_MPEG1 && stream.cid != TS_STREAM_AUDIO_MPEG2 {
		return
	}

	if stream.pkg == nil {
		stream.pkg = newPacket_t(1024)
		stream.pkg.pts = stream.pes_pkg.Pts
		stream.pkg.dts = stream.pes_pkg.Dts
	}

	if len(stream.pkg.payload) > 0 && (start == 1 || stream.pes_pkg.Pts != stream.pkg.pts) {
		if demuxer.OnFrame != nil {
			demuxer.OnFrame(stream.cid, stream.pkg.payload, stream.pkg.pts/90, stream.pkg.dts/90)
		}
		stream.pkg.payload = stream.pkg.payload[:0]
	}
	stream.pkg.payload = append(stream.pkg.payload, stream.pes_pkg.Pes_payload...)
	stream.pkg.pts = stream.pes_pkg.Pts
	stream.pkg.dts = stream.pes_pkg.Dts
}

// splitH26xFrame carves access units out of the bytes accumulated for the
// stream and reports each complete one through OnFrame. It returns true when
// at least one access unit was emitted, which tells the caller that the
// timestamp of the buffer has to be refreshed from the current pes packet.
//
// The scan is incremental: a ts packet adds at most 184 bytes, so restarting
// it at the head of the buffer on every packet would cost O(frame size) per
// packet, i.e. quadratic in the size of a frame. stream.scanPos, frameBeg and
// vcl carry the scan across calls, and every offset is rebased when the
// buffer is compacted.
func (demuxer *TSDemuxer) splitH26xFrame(stream *tsstream) bool {
	isH265 := stream.cid == TS_STREAM_H265
	// bytes of nalu header that have to be present before a nalu type can be
	// read, plus the byte holding first_slice_segment_in_pic_flag
	hdrLen := 2
	if isH265 {
		hdrLen = 3
	}

	data := stream.pkg.payload
	needUpdate := false

	for {
		start, sct := codec.FindStartCode(data, stream.scanPos)
		if start < 0 {
			// nothing found; a start code may still straddle the tail, so
			// keep the last two bytes in view for the next round
			if n := len(data) - 2; n > stream.scanPos {
				stream.scanPos = n
			}
			break
		}
		if stream.frameBeg < 0 {
			stream.frameBeg = start
		}
		if len(data)-start < int(sct)+hdrLen {
			// the nalu header has not arrived yet, resume at this start code
			stream.scanPos = start
			break
		}

		newAccessUnit := false
		hdr := data[start+int(sct):]
		if isH265 {
			switch codec.H265NaluTypeWithoutStartCode(hdr) {
			case codec.H265_NAL_AUD, codec.H265_NAL_SPS,
				codec.H265_NAL_PPS, codec.H265_NAL_VPS, codec.H265_NAL_SEI:
				if stream.vcl > 0 {
					newAccessUnit = true
				}
			case codec.H265_NAL_Slice_TRAIL_N, codec.H265_NAL_LICE_TRAIL_R,
				codec.H265_NAL_SLICE_TSA_N, codec.H265_NAL_SLICE_TSA_R,
				codec.H265_NAL_SLICE_STSA_N, codec.H265_NAL_SLICE_STSA_R,
				codec.H265_NAL_SLICE_RADL_N, codec.H265_NAL_SLICE_RADL_R,
				codec.H265_NAL_SLICE_RASL_N, codec.H265_NAL_SLICE_RASL_R,
				codec.H265_NAL_SLICE_BLA_W_LP, codec.H265_NAL_SLICE_BLA_W_RADL,
				codec.H265_NAL_SLICE_BLA_N_LP, codec.H265_NAL_SLICE_IDR_W_RADL,
				codec.H265_NAL_SLICE_IDR_N_LP, codec.H265_NAL_SLICE_CRA:
				// first_slice_segment_in_pic_flag starts a new picture
				if stream.vcl > 0 {
					if hdr[2]&0x80 > 0 {
						newAccessUnit = true
					}
				} else {
					stream.vcl++
				}
			}
		} else {
			switch codec.H264NaluTypeWithoutStartCode(hdr) {
			case codec.H264_NAL_AUD, codec.H264_NAL_SPS,
				codec.H264_NAL_PPS, codec.H264_NAL_SEI:
				if stream.vcl > 0 {
					newAccessUnit = true
				}
			case codec.H264_NAL_I_SLICE, codec.H264_NAL_P_SLICE,
				codec.H264_NAL_SLICE_A, codec.H264_NAL_SLICE_B, codec.H264_NAL_SLICE_C:
				// first_mb_in_slice == 0 starts a new picture
				if stream.vcl > 0 {
					if hdr[1]&0x80 > 0 {
						newAccessUnit = true
					}
				} else {
					stream.vcl++
				}
			}
		}

		if stream.vcl > 0 && newAccessUnit {
			demuxer.emitAccessUnit(stream, data[stream.frameBeg:start])
			stream.frameBeg = start
			needUpdate = true
			stream.vcl = 0
		}
		stream.scanPos = start + 3
	}

	if stream.frameBeg > 0 {
		// drop everything before the access unit under construction and
		// rebase the scanner onto the compacted buffer
		n := copy(stream.pkg.payload, data[stream.frameBeg:])
		stream.pkg.payload = stream.pkg.payload[:n]
		stream.scanPos -= stream.frameBeg
		if stream.scanPos < 0 {
			stream.scanPos = 0
		}
		stream.frameBeg = 0
	}
	return needUpdate
}

// emitAccessUnit hands one access unit to OnFrame, stripping a leading
// access unit delimiter.
func (demuxer *TSDemuxer) emitAccessUnit(stream *tsstream, au []byte) {
	if demuxer.OnFrame == nil || len(au) == 0 {
		return
	}
	demuxer.OnFrame(stream.cid, au[leadingAudLen(stream.cid, au):], stream.pkg.pts/90, stream.pkg.dts/90)
}

// leadingAudLen returns the length of the access unit delimiter at the head
// of au, or 0 when there is none.
func leadingAudLen(cid TS_STREAM_TYPE, au []byte) int {
	audLen := 0
	codec.SplitFrameWithStartCode(au, func(nalu []byte) bool {
		if cid == TS_STREAM_H264 {
			if codec.H264NaluType(nalu) == codec.H264_NAL_AUD {
				audLen = len(nalu)
			}
		} else if codec.H265NaluType(nalu) == codec.H265_NAL_AUD {
			audLen = len(nalu)
		}
		return false
	})
	return audLen
}
