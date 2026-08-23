package mpeg2

import (
	"errors"
	"fmt"

	"github.com/ntklink/gomediautils/go-codec"
)

type pes_stream struct {
	pid        uint16
	cc         uint8
	streamtype TS_STREAM_TYPE
}

func NewPESStream(pid uint16, cid TS_STREAM_TYPE) *pes_stream {
	return &pes_stream{
		pid:        pid,
		cc:         0,
		streamtype: cid,
	}
}

type table_pmt struct {
	pid            uint16
	cc             uint8
	pcr_pid        uint16
	version_number uint8
	pm             uint16
	streams        []*pes_stream
}

func NewTablePmt() *table_pmt {
	return &table_pmt{
		pid:            0,
		cc:             0,
		pcr_pid:        0,
		version_number: 0,
		pm:             0,
		streams:        make([]*pes_stream, 0, 2),
	}
}

type table_pat struct {
	cc             uint8
	version_number uint8
	pmts           []*table_pmt
}

func NewTablePat() *table_pat {
	return &table_pat{
		cc:             0,
		version_number: 0,
		pmts:           make([]*table_pmt, 0, 8),
	}
}

type TSMuxer struct {
	pat        *table_pat
	stream_pid uint16
	pmt_pid    uint16
	pat_period uint64
	// OnPacket receives every 188 byte transport packet. The slice is owned by
	// the muxer and is overwritten by the next packet: copy it if it has to
	// outlive the callback.
	OnPacket func(pkg []byte)
	bsw      *codec.BitStreamWriter
}

// writer returns the muxer's reusable (reset) packet writer.
func (mux *TSMuxer) writer() *codec.BitStreamWriter {
	if mux.bsw == nil {
		mux.bsw = codec.NewBitStreamWriter(TS_PAKCET_SIZE)
	} else {
		mux.bsw.Reset()
	}
	return mux.bsw
}

// stuffing pads the packet under construction up to TS_PAKCET_SIZE. A packet
// that is already too long is left alone and rejected by emit.
func stuffing(bsw *codec.BitStreamWriter) {
	if n := TS_PAKCET_SIZE - bsw.ByteOffset(); n > 0 {
		bsw.PutRepetValue(0xff, n)
	}
}

// emit hands a finished packet to OnPacket. It reports a packet the muxer
// failed to build instead of letting a malformed one reach the output.
func (mux *TSMuxer) emit(bsw *codec.BitStreamWriter) error {
	if err := bsw.Err(); err != nil {
		return err
	}
	pkg := bsw.Bits()
	if len(pkg) != TS_PAKCET_SIZE {
		return fmt.Errorf("mpeg2: built a %d byte ts packet, want %d", len(pkg), TS_PAKCET_SIZE)
	}
	if mux.OnPacket != nil {
		mux.OnPacket(pkg)
	}
	return nil
}

func NewTSMuxer() *TSMuxer {
	return &TSMuxer{
		pat:        NewTablePat(),
		stream_pid: 0x100,
		pmt_pid:    0x200,
		pat_period: 0,
		OnPacket:   nil,
	}
}

func (mux *TSMuxer) AddStream(cid TS_STREAM_TYPE) uint16 {
	if mux.pat == nil {
		mux.pat = NewTablePat()
	}
	if len(mux.pat.pmts) == 0 {
		tmppmt := NewTablePmt()
		tmppmt.pid = mux.pmt_pid
		tmppmt.pm = 1
		mux.pmt_pid++
		mux.pat.pmts = append(mux.pat.pmts, tmppmt)
	}
	sid := mux.stream_pid
	tmpstream := NewPESStream(sid, cid)
	mux.stream_pid++
	mux.pat.pmts[0].streams = append(mux.pat.pmts[0].streams, tmpstream)
	return sid
}

// / Muxer audio/video stream data
// / pid: stream id by AddStream
// / pts: audio/video stream timestamp in ms
// / dts: audio/video stream timestamp in ms
func (mux *TSMuxer) Write(pid uint16, data []byte, pts uint64, dts uint64) error {
	var whichpmt *table_pmt = nil
	var whichstream *pes_stream = nil
search:
	for _, pmt := range mux.pat.pmts {
		for _, stream := range pmt.streams {
			if stream.pid == pid {
				whichpmt = pmt
				whichstream = stream
				break search
			}
		}
	}
	if whichpmt == nil || whichstream == nil {
		return errors.New("not Found pid stream")
	}
	if whichpmt.pcr_pid == 0 || (findPESIDByStreamType(whichstream.streamtype) == PES_STREAM_VIDEO && whichpmt.pcr_pid != pid) {
		whichpmt.pcr_pid = pid
	}

	var withaud bool = false

	if whichstream.streamtype == TS_STREAM_H264 || whichstream.streamtype == TS_STREAM_H265 {
		codec.SplitFrame(data, func(nalu []byte) bool {
			if whichstream.streamtype == TS_STREAM_H264 {
				nalu_type := codec.H264NaluTypeWithoutStartCode(nalu)
				if nalu_type == codec.H264_NAL_AUD {
					withaud = true
					return false
				} else if codec.IsH264VCLNaluType(nalu_type) {
					return false
				}
				return true
			} else {
				nalu_type := codec.H265NaluTypeWithoutStartCode(nalu)
				if nalu_type == codec.H265_NAL_AUD {
					withaud = true
					return false
				} else if codec.IsH265VCLNaluType(nalu_type) {
					return false
				}
				return true
			}
		})
	}

	if mux.pat_period == 0 || mux.pat_period+400 < dts {
		mux.pat_period = dts
		if mux.pat_period == 0 {
			mux.pat_period = 1 //avoid write pat twice
		}
		tmppat := NewPat()
		tmppat.Version_number = mux.pat.version_number
		for _, pmt := range mux.pat.pmts {
			tmppm := PmtPair{
				Program_number: pmt.pm,
				PID:            pmt.pid,
			}
			tmppat.Pmts = append(tmppat.Pmts, tmppm)
		}
		if err := mux.writePat(tmppat); err != nil {
			return err
		}

		for _, pmt := range mux.pat.pmts {
			tmppmt := NewPmt()
			tmppmt.Program_number = pmt.pm
			tmppmt.Version_number = pmt.version_number
			tmppmt.PCR_PID = pmt.pcr_pid
			for _, stream := range pmt.streams {
				var sp StreamPair
				sp.StreamType = uint8(stream.streamtype)
				sp.Elementary_PID = stream.pid
				sp.ES_Info_Length = 0
				tmppmt.Streams = append(tmppmt.Streams, sp)
			}
			if err := mux.writePmt(tmppmt, pmt); err != nil {
				return err
			}
		}
	}

	flag := false
	switch whichstream.streamtype {
	case TS_STREAM_H264:
		flag = codec.IsH264IDRFrame(data)
	case TS_STREAM_H265:
		flag = codec.IsH265IDRFrame(data)
	}

	return mux.writePES(whichstream, whichpmt, data, pts*90, dts*90, flag, withaud)
}

func (mux *TSMuxer) writePat(pat *Pat) error {
	var tshdr TSPacket
	tshdr.Payload_unit_start_indicator = 1
	tshdr.PID = 0
	tshdr.Adaptation_field_control = 0x01
	tshdr.Continuity_counter = mux.pat.cc
	mux.pat.cc = (mux.pat.cc + 1) % 16
	bsw := mux.writer()
	tshdr.EncodeHeader(bsw)
	bsw.PutByte(0x00) //pointer
	pat.Encode(bsw)
	stuffing(bsw)
	return mux.emit(bsw)
}

func (mux *TSMuxer) writePmt(pmt *Pmt, t_pmt *table_pmt) error {
	var tshdr TSPacket
	tshdr.Payload_unit_start_indicator = 1
	tshdr.PID = t_pmt.pid
	tshdr.Adaptation_field_control = 0x01
	tshdr.Continuity_counter = t_pmt.cc
	t_pmt.cc = (t_pmt.cc + 1) % 16
	bsw := mux.writer()
	tshdr.EncodeHeader(bsw)
	bsw.PutByte(0x00) //pointer
	pmt.Encode(bsw)
	stuffing(bsw)
	return mux.emit(bsw)
}

func (mux *TSMuxer) writePES(pes *pes_stream, pmt *table_pmt, data []byte, pts uint64, dts uint64, idr_flag bool, withaud bool) error {
	var firstPesPacket bool = true
	for {
		bsw := mux.writer()
		var tshdr TSPacket
		if firstPesPacket {
			tshdr.Payload_unit_start_indicator = 1
		}
		tshdr.PID = pes.pid
		tshdr.Adaptation_field_control = 0x01
		tshdr.Continuity_counter = pes.cc
		headlen := 4
		pes.cc = (pes.cc + 1) % 16
		var adaptation *Adaptation_field = nil
		if firstPesPacket && idr_flag {
			adaptation = new(Adaptation_field)
			tshdr.Adaptation_field_control = tshdr.Adaptation_field_control | 0x02
			adaptation.Random_access_indicator = 1
			headlen += 2
		}

		if firstPesPacket && pes.pid == pmt.pcr_pid {
			if adaptation == nil {
				adaptation = new(Adaptation_field)
				headlen += 2
			}
			tshdr.Adaptation_field_control = tshdr.Adaptation_field_control | 0x02
			adaptation.PCR_flag = 1
			// pcr is in 27MHz units: base = 90kHz clock, extension = remainder of 300
			if dts == 0 {
				adaptation.Program_clock_reference_base = pts
			} else {
				adaptation.Program_clock_reference_base = dts
			}
			adaptation.Program_clock_reference_extension = 0
			headlen += 6
		}

		var payload []byte
		var pespkg *PesPacket = nil
		if firstPesPacket {
			oldheadlen := headlen
			headlen += 19
			if !withaud && pes.streamtype == TS_STREAM_H264 {
				headlen += 6
				payload = append(payload, H264_AUD_NALU...)
			} else if !withaud && pes.streamtype == TS_STREAM_H265 {
				payload = append(payload, H265_AUD_NALU...)
				headlen += 7
			}
			pespkg = NewPesPacket()
			pespkg.PTS_DTS_flags = 0x03
			pespkg.PES_header_data_length = 10
			pespkg.Pts = pts
			pespkg.Dts = dts
			pespkg.Stream_id = uint8(findPESIDByStreamType(pes.streamtype))
			if idr_flag {
				pespkg.Data_alignment_indicator = 1
			}
			if headlen-oldheadlen-6+len(data) > 0xFFFF {
				pespkg.PES_packet_length = 0
			} else {
				pespkg.PES_packet_length = uint16(len(data) + headlen - oldheadlen - 6)
			}

		}

		if len(data)+headlen < TS_PAKCET_SIZE {
			if adaptation == nil {
				adaptation = new(Adaptation_field)
				headlen += 1
				if TS_PAKCET_SIZE-len(data)-headlen >= 1 {
					headlen += 1
				} else {
					adaptation.SingleStuffingByte = true
				}
			}
			adaptation.Stuffing_byte = uint8(TS_PAKCET_SIZE - len(data) - headlen)
			payload = append(payload, data...)
			data = data[:0]
		} else {
			payload = append(payload, data[0:TS_PAKCET_SIZE-headlen]...)
			data = data[TS_PAKCET_SIZE-headlen:]
		}

		if adaptation != nil {
			tshdr.Field = adaptation
			tshdr.Adaptation_field_control |= 0x02
		}
		tshdr.EncodeHeader(bsw)
		if pespkg != nil {
			pespkg.Pes_payload = payload
			pespkg.Encode(bsw)
		} else {
			bsw.PutBytes(payload)
		}
		firstPesPacket = false
		if err := mux.emit(bsw); err != nil {
			return err
		}
		if len(data) == 0 {
			return nil
		}
	}
}
