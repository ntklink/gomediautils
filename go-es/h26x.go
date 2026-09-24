package es

import (
	"bufio"
	"errors"
	"io"

	"github.com/ntklink/gomediautils/go-codec"
)

// maxNaluSize caps one nal unit; a stream that goes further without a start
// code is not Annex-B.
const maxNaluSize = 64 << 20

// nalScanner cuts an Annex-B stream into nal units, without start codes.
type nalScanner struct {
	r   *bufio.Reader
	buf []byte
	eof bool
}

func newNalScanner(r io.Reader) *nalScanner {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 64*1024)
	}
	return &nalScanner{r: br}
}

// next returns the next nal unit, or io.EOF once the stream is used up.
func (s *nalScanner) next() ([]byte, error) {
	for {
		start, sc := codec.FindStartCode(s.buf, 0)
		if start >= 0 {
			body := start + int(sc)
			end, _ := codec.FindStartCode(s.buf, body)
			if end >= 0 {
				nalu := trimTrailingZeros(s.buf[body:end])
				s.buf = s.buf[end:]
				if len(nalu) > 0 {
					return append([]byte(nil), nalu...), nil
				}
				continue
			}
			if s.eof {
				nalu := trimTrailingZeros(s.buf[body:])
				s.buf = nil
				if len(nalu) > 0 {
					return nalu, nil
				}
				return nil, io.EOF
			}
		} else if s.eof {
			return nil, io.EOF
		}
		if len(s.buf) > maxNaluSize {
			return nil, errNaluTooLarge
		}
		chunk := make([]byte, 64*1024)
		n, err := s.r.Read(chunk)
		s.buf = append(s.buf, chunk[:n]...)
		if err == io.EOF {
			s.eof = true
		} else if err != nil {
			return nil, err
		}
	}
}

// trimTrailingZeros drops trailing_zero_8bits and the leading zero of a
// four byte start code that FindStartCode counted as a three byte one.
func trimTrailingZeros(nalu []byte) []byte {
	for len(nalu) > 0 && nalu[len(nalu)-1] == 0 {
		nalu = nalu[:len(nalu)-1]
	}
	return nalu
}

// picture is what the access unit splitter learns from a slice header.
type picture struct {
	firstSlice bool
	key        bool
	// newPeriod is set where the picture order count restarts (an IDR, or
	// an IRAP that ends the previous coded video sequence)
	newPeriod bool
	poc       int64
	hasPoc    bool
}

// codecParser is the per codec part of the video reader.
type codecParser interface {
	// startsAU reports whether a non-VCL nal unit opens a new access unit
	// when one with a slice is already being gathered.
	startsAU(nalu []byte) bool
	isVCL(nalu []byte) bool
	// parameterSet records sps/pps/vps nal units
	parameterSet(nalu []byte)
	// slice parses a slice header far enough to place the picture
	slice(nalu []byte) picture
	frameRate() float64
}

type videoReader struct {
	cid     codec.CodecID
	scanner *nalScanner
	parser  codecParser
	opts    options

	au      []byte
	auPic   picture
	auVCL   bool
	pending []byte // nal units read ahead that belong to the next access unit

	order    *orderer
	out      []*Frame
	finished bool
	period   int64
	counter  int64
}

// NewH264Reader reads an Annex-B H.264 stream.
func NewH264Reader(r io.Reader, opts ...Option) Reader {
	return newVideoReader(codec.CODECID_VIDEO_H264, r, newH264Parser(), opts)
}

// NewH265Reader reads an Annex-B H.265 stream.
func NewH265Reader(r io.Reader, opts ...Option) Reader {
	return newVideoReader(codec.CODECID_VIDEO_H265, r, newH265Parser(), opts)
}

func newVideoReader(cid codec.CodecID, r io.Reader, p codecParser, opts []Option) *videoReader {
	return &videoReader{
		cid:     cid,
		scanner: newNalScanner(r),
		parser:  p,
		opts:    applyOptions(opts),
		order:   newOrderer(),
	}
}

func (v *videoReader) Codec() codec.CodecID {
	return v.cid
}

func (v *videoReader) ReadFrame() (*Frame, error) {
	for len(v.out) == 0 {
		if v.finished {
			return nil, io.EOF
		}
		if err := v.step(); err != nil {
			return nil, err
		}
	}
	f := v.out[0]
	v.out[0] = nil
	v.out = v.out[1:]
	return f, nil
}

// step reads one nal unit, finishing an access unit when it opens the next.
func (v *videoReader) step() error {
	nalu, err := v.scanner.next()
	if err == io.EOF {
		v.finishAU()
		v.finished = true
		v.emit(v.order.flush())
		return nil
	}
	if err != nil {
		return err
	}
	vcl := v.parser.isVCL(nalu)
	var pic picture
	if vcl {
		pic = v.parser.slice(nalu)
	} else {
		v.parser.parameterSet(nalu)
	}
	if v.auVCL && ((vcl && pic.firstSlice) || (!vcl && v.parser.startsAU(nalu))) {
		v.finishAU()
	}
	if vcl && !v.auVCL {
		v.auVCL = true
		v.auPic = pic
	} else if vcl && pic.key {
		v.auPic.key = true
	}
	v.au = append(v.au, 0, 0, 0, 1)
	v.au = append(v.au, nalu...)
	return nil
}

func (v *videoReader) finishAU() {
	if !v.auVCL {
		// parameter sets or SEI with no picture after them, at the end of
		// the stream: nothing to show
		v.au = v.au[:0]
		return
	}
	pic := v.auPic
	if pic.newPeriod {
		v.period++
	}
	// streams whose picture order follows decode order (H.264 poc type 2,
	// field pictures) are ordered by their position
	poc := pic.poc
	if !pic.hasPoc {
		poc = v.counter
	}
	v.counter++
	f := &Frame{Cid: v.cid, Data: v.au, KeyFrame: pic.key}
	v.au = nil
	v.auVCL = false
	v.emit(v.order.push(f, orderKey{period: v.period, poc: poc}))
}

func (v *videoReader) emit(frames []*orderedFrame) {
	fps := v.opts.frameRate
	if fps <= 0 {
		fps = v.parser.frameRate()
	}
	if fps <= 0 {
		fps = 25
	}
	for _, of := range frames {
		of.frame.Dts = uint64(float64(of.dtsIndex) * 1000 / fps)
		of.frame.Pts = uint64(float64(of.ptsIndex) * 1000 / fps)
		v.out = append(v.out, of.frame)
	}
}

// h264Parser keeps the parameter sets needed to read slice headers.
type h264Parser struct {
	spss map[uint64]*codec.SPS
	ppss map[uint64]*codec.PPS

	prevPocMsb int64
	prevPocLsb int64
	// poc type 1 state (8.2.1.2)
	prevFrameNum       int64
	prevFrameNumOffset int64
}

func newH264Parser() *h264Parser {
	return &h264Parser{spss: map[uint64]*codec.SPS{}, ppss: map[uint64]*codec.PPS{}}
}

func (p *h264Parser) isVCL(nalu []byte) bool {
	t := codec.H264NaluTypeWithoutStartCode(nalu)
	return t >= codec.H264_NAL_P_SLICE && t <= codec.H264_NAL_I_SLICE
}

// startsAU follows 7.4.1.2.3: an AUD, SPS, PPS, SEI or nal types 14 to 18
// before the first slice of a picture belong to that picture.
func (p *h264Parser) startsAU(nalu []byte) bool {
	t := codec.H264NaluTypeWithoutStartCode(nalu)
	switch {
	case t == codec.H264_NAL_AUD, t == codec.H264_NAL_SPS, t == codec.H264_NAL_PPS, t == codec.H264_NAL_SEI:
		return true
	case t >= 14 && t <= 18:
		return true
	}
	return false
}

func (p *h264Parser) parameterSet(nalu []byte) {
	switch codec.H264NaluTypeWithoutStartCode(nalu) {
	case codec.H264_NAL_SPS:
		sps := &codec.SPS{}
		bs := codec.NewBitStream(codec.CovertRbspToSodb(nalu[1:]))
		sps.Decode(bs)
		if bs.Err() == nil {
			p.spss[sps.Seq_parameter_set_id] = sps
		}
	case codec.H264_NAL_PPS:
		pps := &codec.PPS{}
		bs := codec.NewBitStream(codec.CovertRbspToSodb(nalu[1:]))
		pps.Decode(bs)
		if bs.Err() == nil {
			p.ppss[pps.Pic_parameter_set_id] = pps
		}
	}
}

func (p *h264Parser) frameRate() float64 {
	for _, sps := range p.spss {
		vui := sps.VuiParameters
		if sps.Vui_parameters_present_flag == 1 && vui.TimingInfoPresentFlag == 1 && vui.NumUnitsInTick > 0 {
			// one tick is a field; a frame takes two
			return float64(vui.TimeScale) / float64(2*vui.NumUnitsInTick)
		}
	}
	return 0
}

// slice reads a slice header up to pic_order_cnt_lsb (7.3.3) and derives
// the picture order count for poc type 0 (8.2.1.1).
func (p *h264Parser) slice(nalu []byte) picture {
	t := codec.H264NaluTypeWithoutStartCode(nalu)
	refIdc := (nalu[0] >> 5) & 0x03
	idr := t == codec.H264_NAL_I_SLICE
	pic := picture{key: idr, newPeriod: idr}
	bs := codec.NewBitStream(codec.CovertRbspToSodb(nalu[1:]))
	pic.firstSlice = bs.ReadUE() == 0
	if !pic.firstSlice {
		// only the first slice places the picture
		return pic
	}
	bs.ReadUE() // slice_type
	pps, ok := p.ppss[bs.ReadUE()]
	if !ok {
		return pic
	}
	sps, ok := p.spss[pps.Seq_parameter_set_id]
	if !ok || bs.Err() != nil {
		return pic
	}
	if sps.Separate_colour_plane_flag == 1 {
		bs.SkipBits(2)
	}
	frameNum := int64(bs.GetBits(int(sps.Log2_max_frame_num_minus4 + 4)))
	if sps.Frame_mbs_only_flag == 0 && bs.GetBit() == 1 {
		// a field picture: its fields are placed by decode order
		return pic
	}
	if idr {
		bs.ReadUE() // idr_pic_id
	}
	var poc int64
	switch sps.Pic_order_cnt_type {
	case 0:
		poc = p.pocType0(sps, bs, idr, refIdc)
	case 1:
		poc = p.pocType1(sps, pps, bs, idr, refIdc, frameNum)
	default:
		// type 2 outputs in decode order: nothing to reorder
		return pic
	}
	if bs.Err() != nil {
		return pic
	}
	pic.poc, pic.hasPoc = poc, true
	return pic
}

// pocType0 derives the picture order count from pic_order_cnt_lsb and the
// msb carried over from the previous reference picture (8.2.1.1).
func (p *h264Parser) pocType0(sps *codec.SPS, bs *codec.BitStream, idr bool, refIdc byte) int64 {
	lsb := int64(bs.GetBits(int(sps.Log2_max_pic_order_cnt_lsb_minus4 + 4)))
	maxLsb := int64(1) << (sps.Log2_max_pic_order_cnt_lsb_minus4 + 4)
	if idr {
		p.prevPocMsb, p.prevPocLsb = 0, 0
	}
	msb := p.prevPocMsb
	switch {
	case lsb < p.prevPocLsb && p.prevPocLsb-lsb >= maxLsb/2:
		msb += maxLsb
	case lsb > p.prevPocLsb && lsb-p.prevPocLsb > maxLsb/2:
		msb -= maxLsb
	}
	if refIdc != 0 {
		p.prevPocMsb, p.prevPocLsb = msb, lsb
	}
	return msb + lsb
}

// pocType1 derives the picture order count from frame_num and the expected
// cycle of reference picture offsets in the SPS (8.2.1.2). Unlike type 2
// it lets non-reference pictures be shown out of decode order, so b frames
// need it.
func (p *h264Parser) pocType1(sps *codec.SPS, pps *codec.PPS, bs *codec.BitStream, idr bool, refIdc byte, frameNum int64) int64 {
	var delta0, delta1 int64
	if sps.Delta_pic_order_always_zero_flag == 0 {
		delta0 = bs.ReadSE()
		if pps.Bottom_field_pic_order_in_frame_present_flag == 1 {
			delta1 = bs.ReadSE()
		}
	}
	maxFrameNum := int64(1) << (sps.Log2_max_frame_num_minus4 + 4)
	var frameNumOffset int64
	switch {
	case idr:
	case p.prevFrameNum > frameNum:
		frameNumOffset = p.prevFrameNumOffset + maxFrameNum
	default:
		frameNumOffset = p.prevFrameNumOffset
	}
	p.prevFrameNum, p.prevFrameNumOffset = frameNum, frameNumOffset

	cycle := int64(len(sps.Offset_for_ref_frame))
	var absFrameNum int64
	if cycle != 0 {
		absFrameNum = frameNumOffset + frameNum
	}
	if refIdc == 0 && absFrameNum > 0 {
		absFrameNum--
	}
	var expected int64
	if absFrameNum > 0 {
		var perCycle int64
		for _, off := range sps.Offset_for_ref_frame {
			perCycle += off
		}
		cycles, inCycle := (absFrameNum-1)/cycle, (absFrameNum-1)%cycle
		expected = cycles * perCycle
		for i := int64(0); i <= inCycle; i++ {
			expected += sps.Offset_for_ref_frame[i]
		}
	}
	if refIdc == 0 {
		expected += sps.Offset_for_non_ref_pic
	}
	top := expected + delta0
	bottom := top + sps.Offset_for_top_to_bottom_field + delta1
	return min(top, bottom)
}

type h265Parser struct {
	spss map[uint64]*codec.H265RawSPS
	ppss map[uint64]*codec.H265RawPPS

	prevTid0Poc int64
	started     bool
	// afterEOS is set by an end of sequence nal unit: the next CRA starts a
	// new coded video sequence like an IDR does
	afterEOS bool
}

func newH265Parser() *h265Parser {
	return &h265Parser{spss: map[uint64]*codec.H265RawSPS{}, ppss: map[uint64]*codec.H265RawPPS{}}
}

const (
	h265NalEOS         = 36
	h265NalPrefixSEI   = 39
	h265NalReservedMin = 41
	h265NalReservedMax = 44
	h265NalUnspecMin   = 48
	h265NalUnspecMax   = 55
)

func (p *h265Parser) isVCL(nalu []byte) bool {
	return codec.H265NaluTypeWithoutStartCode(nalu) < 32
}

// startsAU follows 7.4.2.4.4: VPS, SPS, PPS, AUD, prefix SEI and the
// reserved and unspecified types before the first slice belong to it.
func (p *h265Parser) startsAU(nalu []byte) bool {
	t := codec.H265NaluTypeWithoutStartCode(nalu)
	switch {
	case t == codec.H265_NAL_VPS, t == codec.H265_NAL_SPS, t == codec.H265_NAL_PPS,
		t == codec.H265_NAL_AUD, t == h265NalPrefixSEI:
		return true
	case t >= h265NalReservedMin && t <= h265NalReservedMax, t >= h265NalUnspecMin && t <= h265NalUnspecMax:
		return true
	}
	return false
}

func (p *h265Parser) parameterSet(nalu []byte) {
	switch codec.H265NaluTypeWithoutStartCode(nalu) {
	case codec.H265_NAL_SPS:
		sps := &codec.H265RawSPS{}
		if sps.Decode(nalu) == nil {
			p.spss[sps.Sps_seq_parameter_set_id] = sps
		}
	case codec.H265_NAL_PPS:
		pps := &codec.H265RawPPS{}
		if pps.Decode(nalu) == nil {
			p.ppss[pps.Pps_pic_parameter_set_id] = pps
		}
	case h265NalEOS:
		p.afterEOS = true
	}
}

func (p *h265Parser) frameRate() float64 {
	for _, sps := range p.spss {
		vui := sps.Vui
		if sps.Vui_parameters_present_flag == 1 && vui.Vui_timing_info_present_flag == 1 && vui.Vui_num_units_in_tick > 0 {
			return float64(vui.Vui_time_scale) / float64(vui.Vui_num_units_in_tick)
		}
	}
	return 0
}

// slice reads a slice segment header up to slice_pic_order_cnt_lsb
// (7.3.6.1) and derives the picture order count (8.3.1).
func (p *h265Parser) slice(nalu []byte) picture {
	if len(nalu) < 3 {
		return picture{}
	}
	t := codec.H265NaluTypeWithoutStartCode(nalu)
	tid := int(nalu[1]&0x07) - 1
	irap := t >= codec.H265_NAL_SLICE_BLA_W_LP && t <= 23
	idr := t == codec.H265_NAL_SLICE_IDR_W_RADL || t == codec.H265_NAL_SLICE_IDR_N_LP
	pic := picture{key: irap}

	bs := codec.NewBitStream(codec.CovertRbspToSodb(nalu[2:]))
	pic.firstSlice = bs.GetBit() == 1
	if !pic.firstSlice {
		// only the first slice segment of a picture places it
		return pic
	}
	// an IRAP ends the coded video sequence when it is an IDR or BLA, the
	// first picture of the stream, or a CRA after an end of sequence
	noRaslOutput := irap && (idr || t <= codec.H265_NAL_SLICE_BLA_N_LP || !p.started || p.afterEOS)
	pic.newPeriod = noRaslOutput
	if irap {
		bs.SkipBits(1) // no_output_of_prior_pics_flag
	}
	pps, ok := p.ppss[bs.ReadUE()]
	if !ok {
		return pic
	}
	sps, ok := p.spss[pps.Pps_seq_parameter_set_id]
	if !ok || bs.Err() != nil {
		return pic
	}
	bs.SkipBits(int(pps.Num_extra_slice_header_bits))
	bs.ReadUE() // slice_type
	if pps.Output_flag_present_flag == 1 {
		bs.SkipBits(1)
	}
	if sps.Separate_colour_plane_flag == 1 {
		bs.SkipBits(2)
	}
	var lsb int64
	if !idr {
		lsb = int64(bs.GetBits(int(sps.Log2_max_pic_order_cnt_lsb_minus4 + 4)))
	}
	if bs.Err() != nil {
		return pic
	}
	maxLsb := int64(1) << (sps.Log2_max_pic_order_cnt_lsb_minus4 + 4)
	var msb int64
	if !noRaslOutput {
		prevLsb := p.prevTid0Poc & (maxLsb - 1)
		prevMsb := p.prevTid0Poc - prevLsb
		msb = prevMsb
		switch {
		case lsb < prevLsb && prevLsb-lsb >= maxLsb/2:
			msb += maxLsb
		case lsb > prevLsb && lsb-prevLsb > maxLsb/2:
			msb -= maxLsb
		}
	}
	poc := msb + lsb
	// RASL, RADL and sub-layer non-reference pictures (even types below
	// 16) are not used to track the msb
	subLayerNonRef := t < 16 && t%2 == 0
	if tid == 0 && !(t >= codec.H265_NAL_SLICE_RADL_N && t <= codec.H265_NAL_SLICE_RASL_R) && !subLayerNonRef {
		p.prevTid0Poc = poc
	}
	p.started = true
	if irap {
		p.afterEOS = false
	}
	pic.poc, pic.hasPoc = poc, true
	return pic
}

// errNaluTooLarge is reported for data with no start code in 64 MB.
var errNaluTooLarge = errors.New("es: no start code in 64 MB, not an Annex-B stream")
