package mkv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/ntklink/gomediautils/go-codec"
)

// Document types a Muxer can declare. WebM is the subset of Matroska that
// browsers play; it only admits VP8 video and Opus audio here.
const (
	DocTypeMatroska = "matroska"
	DocTypeWebM     = "webm"
)

const (
	appName = "gomediautils"

	// defaultClusterDuration is how long a cluster may grow, in
	// milliseconds, before the muxer starts a new one without waiting for a
	// key frame. Players seek to cluster boundaries, so audio only files get
	// one cue per cluster.
	defaultClusterDuration = 5000
	// maxClusterBytes starts a new cluster early for high bitrate streams so
	// the in memory cluster buffer stays small.
	maxClusterBytes = 5 << 20
	// maxPendingFrames bounds how many frames are held back while waiting
	// for every track to reveal its codec configuration. Past that the
	// header is written with the tracks that are ready and the tracks that
	// never produced a frame are left out.
	maxPendingFrames = 1024
	// seekHeadReserve is the room kept after the Segment header for the
	// SeekHead written when the file is finished: three Seek entries with 8
	// byte positions take 68 bytes, the rest stays a Void.
	seekHeadReserve = 96
)

type MuxerOption func(*Muxer)

// WithDocType selects DocTypeMatroska (the default) or DocTypeWebM.
func WithDocType(docType string) MuxerOption {
	return func(m *Muxer) {
		m.docType = docType
	}
}

// WithClusterDuration caps how many milliseconds of media a cluster holds.
func WithClusterDuration(milliseconds uint64) MuxerOption {
	return func(m *Muxer) {
		if milliseconds > 0 {
			m.clusterDuration = milliseconds
		}
	}
}

type TrackOption func(*muxTrack)

// WithVideoSize sets the picture size. H.264 and H.265 read it from the
// SPS and VP8 from the first key frame when it is not given.
func WithVideoSize(width, height uint32) TrackOption {
	return func(t *muxTrack) {
		t.width, t.height = width, height
	}
}

// WithAudioSampleRate overrides the sample rate. AAC and MP3 read it from
// the frame headers; G.711 defaults to 8000.
func WithAudioSampleRate(sampleRate uint32) TrackOption {
	return func(t *muxTrack) {
		t.sampleRate = sampleRate
	}
}

// WithAudioChannelCount overrides the channel count. G.711 defaults to 1.
func WithAudioChannelCount(channels uint8) TrackOption {
	return func(t *muxTrack) {
		t.channels = channels
	}
}

// WithExtraData supplies the codec configuration up front: the avcC or hvcC
// record, the AAC AudioSpecificConfig or the OpusHead. A track given one is
// ready at once and does not hold the header back.
func WithExtraData(extraData []byte) TrackOption {
	return func(t *muxTrack) {
		t.extraData = append([]byte(nil), extraData...)
	}
}

// WithLanguage sets the track language (ISO 639-2, "und" by default).
func WithLanguage(language string) TrackOption {
	return func(t *muxTrack) {
		t.language = language
	}
}

type muxTrack struct {
	number    uint64
	cid       codec.CodecID
	codecID   string
	width     uint32
	height    uint32
	channels  uint8
	language  string
	extraData []byte

	sampleRate uint32
	// private is the CodecPrivate written into the track entry; nil until
	// the configuration is known
	private    []byte
	codecDelay uint64 // nanoseconds, opus pre-skip
	ready      bool
	omitted    bool

	spss, ppss [][]byte
	hvcc       *codec.HEVCRecordConfiguration
	hasVPS     bool
	hasSPS     bool
	hasPPS     bool

	// endTs is where the latest frame ends, in milliseconds
	endTs uint64

	// G.711 has no frames, so writes are regrouped into 20 ms blocks: pcm
	// holds the bytes not yet in a block, pcmBase the time of the run they
	// belong to and pcmDone how many bytes of that run are already out
	pcm     []byte
	pcmBase uint64
	pcmDone uint64
}

// pcmBlockMs is the length of the blocks G.711 is regrouped into.
const pcmBlockMs = 20

func (t *muxTrack) pcmTime(bytes uint64) uint64 {
	return t.pcmBase + bytes*1000/(uint64(t.sampleRate)*uint64(t.channels))
}

// regroupPCM turns G.711 writes of any length into 20 ms blocks. Sources
// like mov hand out a few samples at a time, whose millisecond timestamps
// would collide; RTP style 20 ms writes pass through as they are. A write
// that does not continue the previous one starts a new run.
func (t *muxTrack) regroupPCM(data []byte, pts uint64) []muxBlock {
	var blocks []muxBlock
	if len(t.pcm) > 0 {
		expect := t.pcmTime(t.pcmDone + uint64(len(t.pcm)))
		if pts > expect+1 || pts+1 < expect {
			blocks = t.flushPCM()
		}
	}
	if len(t.pcm) == 0 && (len(blocks) > 0 || t.pcmDone == 0 || pts != t.pcmTime(t.pcmDone)) {
		t.pcmBase, t.pcmDone = pts, 0
	}
	t.pcm = append(t.pcm, data...)
	chunk := int(t.sampleRate) * int(t.channels) * pcmBlockMs / 1000
	for len(t.pcm) >= chunk {
		block := append([]byte(nil), t.pcm[:chunk]...)
		blocks = append(blocks, muxBlock{track: t, data: block, pts: t.pcmTime(t.pcmDone), key: true})
		t.pcmDone += uint64(chunk)
		t.pcm = append(t.pcm[:0], t.pcm[chunk:]...)
	}
	return blocks
}

// flushPCM emits whatever is left of the current run as a short block.
func (t *muxTrack) flushPCM() []muxBlock {
	if len(t.pcm) == 0 {
		return nil
	}
	block := muxBlock{track: t, data: append([]byte(nil), t.pcm...), pts: t.pcmTime(t.pcmDone), key: true}
	t.pcmDone += uint64(len(t.pcm))
	t.pcm = t.pcm[:0]
	return []muxBlock{block}
}

type muxBlock struct {
	track   *muxTrack
	data    []byte
	pts     uint64
	key     bool
	padding int64
}

type cuePoint struct {
	time     uint64
	track    uint64
	position uint64
}

// Muxer writes a Matroska or WebM file. Each Write takes one access unit in
// the form the other muxers of this module take it: Annex-B for H.264 and
// H.265, ADTS for AAC (several frames per write are fine), raw frames for
// VP8, Opus, MP3 and G.711. Timestamps are in milliseconds.
//
// Given an io.WriteSeeker that can seek, the muxer finishes the file with a
// segment size, a duration, cues and a seek head, so players can seek in
// it. Given a plain io.Writer it writes a live stream: an unknown size
// segment with no index, which is what browsers and streaming servers
// expect from a live WebM source.
type Muxer struct {
	w               io.Writer
	ws              io.WriteSeeker
	start           int64
	written         int64
	docType         string
	clusterDuration uint64

	tracks        []*muxTrack
	headerWritten bool
	finished      bool
	pending       []muxBlock

	segmentSizeAt  int64 // offset of the 8 byte segment size
	segmentDataAt  int64 // offset of the first byte inside the segment
	durationAt     int64 // offset of the Duration float payload, 0 if absent
	infoPos        uint64
	tracksPos      uint64
	hasVideoTracks bool

	cluster      []byte
	clusterOpen  bool
	clusterTs    uint64
	clusterStart uint64
	cues         []cuePoint
	endTs        uint64
}

// NewMuxer creates a muxer writing to w. Nothing is written until the first
// frames arrive and every track's codec configuration is known.
func NewMuxer(w io.Writer, options ...MuxerOption) (*Muxer, error) {
	m := &Muxer{
		w:               w,
		docType:         DocTypeMatroska,
		clusterDuration: defaultClusterDuration,
	}
	for _, opt := range options {
		opt(m)
	}
	if m.docType != DocTypeMatroska && m.docType != DocTypeWebM {
		return nil, fmt.Errorf("mkv: unknown doc type %q", m.docType)
	}
	if ws, ok := w.(io.WriteSeeker); ok {
		// a pipe or a socket can satisfy the interface without being able to
		// seek; only a writer that reports its position is trusted with the
		// back patching
		if pos, err := ws.Seek(0, io.SeekCurrent); err == nil {
			m.ws = ws
			m.start = pos
		}
	}
	return m, nil
}

// AddVideoTrack declares a video track and returns its track number.
func (m *Muxer) AddVideoTrack(cid codec.CodecID, options ...TrackOption) (uint64, error) {
	if !isVideo(cid) {
		return 0, fmt.Errorf("%w: %s is not a video codec", ErrUnsupportedCodec, codec.CodecString(cid))
	}
	return m.addTrack(cid, options...)
}

// AddAudioTrack declares an audio track and returns its track number.
func (m *Muxer) AddAudioTrack(cid codec.CodecID, options ...TrackOption) (uint64, error) {
	if !isAudio(cid) {
		return 0, fmt.Errorf("%w: %s is not an audio codec", ErrUnsupportedCodec, codec.CodecString(cid))
	}
	return m.addTrack(cid, options...)
}

func (m *Muxer) addTrack(cid codec.CodecID, options ...TrackOption) (uint64, error) {
	if m.headerWritten || m.finished {
		return 0, errors.New("mkv: tracks must be added before the first frame is written")
	}
	if m.docType == DocTypeWebM && !allowedInWebM(cid) {
		return 0, fmt.Errorf("%w: webm cannot carry %s", ErrUnsupportedCodec, codec.CodecString(cid))
	}
	codecID, err := codecIDString(cid)
	if err != nil {
		return 0, err
	}
	t := &muxTrack{
		number:   uint64(len(m.tracks) + 1),
		cid:      cid,
		codecID:  codecID,
		language: "und",
	}
	for _, opt := range options {
		opt(t)
	}
	if err := t.init(); err != nil {
		return 0, err
	}
	if isVideo(cid) {
		m.hasVideoTracks = true
	}
	m.tracks = append(m.tracks, t)
	return t.number, nil
}

// init applies the options that settle the codec configuration up front.
func (t *muxTrack) init() error {
	switch t.cid {
	case codec.CODECID_VIDEO_H264:
		if len(t.extraData) > 0 {
			spss, ppss, err := codec.CovertExtradata(t.extraData)
			if err != nil {
				return err
			}
			for _, sps := range spss {
				t.addH264ParamSet(sps, true)
			}
			for _, pps := range ppss {
				t.addH264ParamSet(pps, false)
			}
			return t.updateH264()
		}
	case codec.CODECID_VIDEO_H265:
		t.hvcc = codec.NewHEVCRecordConfiguration()
		if len(t.extraData) > 0 {
			if err := t.hvcc.Decode(t.extraData); err != nil {
				return err
			}
			t.private = t.extraData
			t.hasVPS, t.hasSPS, t.hasPPS = true, true, true
			if t.width == 0 || t.height == 0 {
				for _, array := range t.hvcc.Arrays {
					if array.NAL_unit_type != uint8(codec.H265_NAL_SPS) || len(array.NalUnits) == 0 {
						continue
					}
					unit := array.NalUnits[0]
					if w, h, err := codec.GetH265Resolution(unit.Nalu[:unit.NalUnitLength]); err == nil {
						t.width, t.height = w, h
					}
				}
			}
			t.ready = t.width > 0 && t.height > 0
		}
	case codec.CODECID_VIDEO_VP8:
		t.ready = t.width > 0 && t.height > 0
	case codec.CODECID_AUDIO_AAC:
		if len(t.extraData) > 0 {
			return t.setASC(t.extraData)
		}
	case codec.CODECID_AUDIO_OPUS:
		if len(t.extraData) > 0 {
			return t.setOpusHead(t.extraData)
		}
	case codec.CODECID_AUDIO_G711A, codec.CODECID_AUDIO_G711U:
		if t.sampleRate == 0 {
			t.sampleRate = 8000
		}
		if t.channels == 0 {
			t.channels = 1
		}
		tag := uint16(waveFormatALaw)
		if t.cid == codec.CODECID_AUDIO_G711U {
			tag = waveFormatMuLaw
		}
		t.private = waveFormatEx(tag, uint16(t.channels), t.sampleRate, 8)
		t.ready = true
	}
	return nil
}

func (t *muxTrack) setASC(asc []byte) error {
	c := codec.NewAudioSpecificConfiguration()
	if err := c.Decode(asc); err != nil {
		return err
	}
	rate := codec.AACSampleIdxToSample(int(c.Sample_freq_index))
	if rate <= 0 {
		return errors.New("mkv: aac config with an unknown sample rate index")
	}
	if t.sampleRate == 0 {
		t.sampleRate = uint32(rate)
	}
	if t.channels == 0 {
		t.channels = c.Channel_configuration
	}
	t.private = append([]byte(nil), asc...)
	t.ready = true
	return nil
}

func (t *muxTrack) setOpusHead(head []byte) error {
	ctx := &codec.OpusContext{}
	if err := ctx.ParseExtranData(head); err != nil {
		return err
	}
	// opus always decodes at 48 kHz whatever rate the source had
	t.sampleRate = 48000
	if t.channels == 0 {
		t.channels = uint8(ctx.ChannelCount)
	}
	t.codecDelay = uint64(ctx.Preskip) * 1000000000 / 48000
	t.private = append([]byte(nil), head...)
	t.ready = true
	return nil
}

func (t *muxTrack) addH264ParamSet(nalu []byte, sps bool) {
	list := &t.ppss
	id := codec.GetPPSIdWithStartCode
	if sps {
		list = &t.spss
		id = codec.GetSPSIdWithStartCode
	}
	for i, old := range *list {
		if id(old) == id(nalu) {
			(*list)[i] = nalu
			return
		}
	}
	*list = append(*list, nalu)
}

func (t *muxTrack) updateH264() error {
	if len(t.spss) == 0 || len(t.ppss) == 0 {
		return nil
	}
	private, err := codec.CreateH264AVCCExtradata(t.spss, t.ppss)
	if err != nil {
		return err
	}
	if t.width == 0 || t.height == 0 {
		if t.width, t.height, err = codec.GetH264Resolution(t.spss[0]); err != nil {
			return err
		}
	}
	t.private = private
	t.ready = true
	return nil
}

// scanParamSets picks the parameter sets out of an Annex-B access unit, as
// long as the header has not been written yet.
func (t *muxTrack) scanParamSets(au []byte) (err error) {
	h265 := t.cid == codec.CODECID_VIDEO_H265
	codec.SplitFrame(au, func(nalu []byte) bool {
		withSC := append([]byte{0, 0, 0, 1}, nalu...)
		if h265 {
			switch codec.H265NaluTypeWithoutStartCode(nalu) {
			case codec.H265_NAL_VPS:
				err, t.hasVPS = t.hvcc.UpdateVPS(withSC), true
			case codec.H265_NAL_SPS:
				if err, t.hasSPS = t.hvcc.UpdateSPS(withSC), true; err == nil && (t.width == 0 || t.height == 0) {
					t.width, t.height, err = codec.GetH265Resolution(withSC)
				}
			case codec.H265_NAL_PPS:
				err, t.hasPPS = t.hvcc.UpdatePPS(withSC), true
			}
		} else {
			switch codec.H264NaluTypeWithoutStartCode(nalu) {
			case codec.H264_NAL_SPS:
				t.addH264ParamSet(withSC, true)
			case codec.H264_NAL_PPS:
				t.addH264ParamSet(withSC, false)
			}
		}
		return err == nil
	})
	if err != nil {
		return err
	}
	if h265 {
		if t.hasVPS && t.hasSPS && t.hasPPS {
			if t.private, err = t.hvcc.Encode(); err != nil {
				return err
			}
			t.ready = t.width > 0 && t.height > 0
		}
		return nil
	}
	return t.updateH264()
}

// toBlocks turns one Write into the frames stored in the file.
func (m *Muxer) toBlocks(t *muxTrack, data []byte, pts uint64) ([]muxBlock, error) {
	switch t.cid {
	case codec.CODECID_VIDEO_H264, codec.CODECID_VIDEO_H265:
		if !m.headerWritten {
			if err := t.scanParamSets(data); err != nil {
				return nil, err
			}
		}
		block, key := annexBToLengthPrefixed(data, t.cid == codec.CODECID_VIDEO_H265)
		if len(block) == 0 {
			// parameter sets only; they live in CodecPrivate
			return nil, nil
		}
		return []muxBlock{{track: t, data: block, pts: pts, key: key}}, nil
	case codec.CODECID_VIDEO_VP8:
		key := codec.IsKeyFrame(data)
		if key && !t.ready {
			w, h, err := codec.GetResloution(data)
			if err != nil {
				return nil, err
			}
			t.width, t.height, t.ready = uint32(w), uint32(h), true
		}
		return []muxBlock{{track: t, data: data, pts: pts, key: key}}, nil
	case codec.CODECID_AUDIO_AAC:
		frames, asc, err := splitADTS(data)
		if err != nil {
			return nil, err
		}
		if !t.ready {
			if asc == nil {
				return nil, errors.New("mkv: raw aac needs its AudioSpecificConfig given with WithExtraData")
			}
			if err := t.setASC(asc); err != nil {
				return nil, err
			}
		}
		blocks := make([]muxBlock, len(frames))
		for i, f := range frames {
			blocks[i] = muxBlock{track: t, data: f, pts: pts + uint64(i)*1024*1000/uint64(t.sampleRate), key: true}
		}
		return blocks, nil
	case codec.CODECID_AUDIO_OPUS:
		if !t.ready {
			channels := t.channels
			if channels == 0 {
				channels = 1
				if p := codec.DecodeOpusPacket(data); p != nil && p.Stereo != 0 {
					channels = 2
				}
			}
			ctx := codec.OpusContext{ChannelCount: int(channels), SampleRate: 48000}
			if err := t.setOpusHead(ctx.WriteOpusExtraData()); err != nil {
				return nil, err
			}
		}
		return []muxBlock{{track: t, data: data, pts: pts, key: true}}, nil
	case codec.CODECID_AUDIO_MP3:
		var blocks []muxBlock
		var offset uint64
		err := codec.SplitMp3Frames(data, func(head *codec.MP3FrameHead, frame []byte) {
			rate := head.GetSampleRate()
			if !t.ready {
				t.sampleRate = uint32(rate)
				if t.channels == 0 {
					t.channels = uint8(head.GetChannelCount())
				}
				t.ready = true
			}
			blocks = append(blocks, muxBlock{track: t, data: frame, pts: pts + offset*1000/uint64(rate), key: true})
			offset += uint64(head.SampleSize)
		})
		return blocks, err
	case codec.CODECID_AUDIO_G711A, codec.CODECID_AUDIO_G711U:
		return t.regroupPCM(data, pts), nil
	default:
		return []muxBlock{{track: t, data: data, pts: pts, key: true}}, nil
	}
}

// Write adds one frame to track. dts is accepted for symmetry with the other
// muxers; Matroska stores presentation times only and relies on frames
// arriving in decode order.
func (m *Muxer) Write(track uint64, data []byte, pts uint64, dts uint64) error {
	return m.WritePadded(track, data, pts, dts, 0)
}

// WritePadded is Write for a frame whose last discardPadding nanoseconds of
// decoded audio are padding, typically the final opus frame of a stream. A
// player drops them, so the decoded length matches the source exactly.
func (m *Muxer) WritePadded(track uint64, data []byte, pts uint64, dts uint64, discardPadding int64) error {
	if m.finished {
		return errors.New("mkv: write after WriteTrailer")
	}
	if track == 0 || track > uint64(len(m.tracks)) {
		return fmt.Errorf("mkv: unknown track %d", track)
	}
	t := m.tracks[track-1]
	if t.omitted {
		return fmt.Errorf("mkv: track %d was left out of the header, it produced no frame in time", track)
	}
	if len(data) == 0 {
		return nil
	}
	data = append([]byte(nil), data...)
	blocks, err := m.toBlocks(t, data, pts)
	if err != nil {
		return err
	}
	if discardPadding != 0 && len(blocks) > 0 {
		blocks[len(blocks)-1].padding = discardPadding
	}
	if m.headerWritten {
		for _, b := range blocks {
			if err := m.writeBlock(b); err != nil {
				return err
			}
		}
		return nil
	}
	m.pending = append(m.pending, blocks...)
	for _, t := range m.tracks {
		if !t.ready {
			if len(m.pending) < maxPendingFrames {
				return nil
			}
			return m.startWithReadyTracks()
		}
	}
	return m.flushPending()
}

// startWithReadyTracks writes the header without the tracks that have not
// produced a single frame; a track that has frames but no configuration yet
// is an error, since its frames could not be decoded.
func (m *Muxer) startWithReadyTracks() error {
	used := make(map[*muxTrack]bool)
	for _, b := range m.pending {
		used[b.track] = true
	}
	for _, t := range m.tracks {
		if t.ready {
			continue
		}
		if used[t] {
			return fmt.Errorf("mkv: track %d (%s) has frames but no codec configuration", t.number, t.codecID)
		}
		t.omitted = true
	}
	return m.flushPending()
}

func (m *Muxer) flushPending() error {
	if err := m.writeHeader(); err != nil {
		return err
	}
	pending := m.pending
	m.pending = nil
	for _, b := range pending {
		if err := m.writeBlock(b); err != nil {
			return err
		}
	}
	return nil
}

func (m *Muxer) write(b []byte) error {
	n, err := m.w.Write(b)
	m.written += int64(n)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}

func (m *Muxer) writeHeader() error {
	docTypeVersion := uint64(4)
	header := master(idEBML,
		uintElement(idEBMLVersion, 1),
		uintElement(idEBMLReadVersion, 1),
		uintElement(idEBMLMaxIDLength, 4),
		uintElement(idEBMLMaxSizeLength, 8),
		stringElement(idDocType, m.docType),
		uintElement(idDocTypeVersion, docTypeVersion),
		// SimpleBlock needs a version 2 reader
		uintElement(idDocTypeReadVersion, 2),
	)
	// the segment size is always 8 bytes so it can be patched in place
	header = appendID(header, idSegment)
	m.segmentSizeAt = int64(len(header))
	header = appendSize(header, unknownSize, 8)
	m.segmentDataAt = int64(len(header))
	if m.ws != nil {
		header = append(header, voidElement(seekHeadReserve)...)
	}

	m.infoPos = uint64(int64(len(header)) - m.segmentDataAt)
	infoChildren := [][]byte{
		uintElement(idTimestampScale, 1000000),
		stringElement(idMuxingApp, appName),
		stringElement(idWritingApp, appName),
	}
	if m.ws != nil {
		infoChildren = append(infoChildren, floatElement(idDuration, 0))
	}
	info := master(idInfo, infoChildren...)
	if m.ws != nil {
		// Duration is the last child and its 8 byte payload the last bytes
		m.durationAt = int64(len(header)) + int64(len(info)) - 8
	}
	header = append(header, info...)

	m.tracksPos = uint64(int64(len(header)) - m.segmentDataAt)
	var entries [][]byte
	for _, t := range m.tracks {
		if !t.omitted {
			entries = append(entries, t.entry())
		}
	}
	header = append(header, master(idTracks, entries...)...)
	m.headerWritten = true
	return m.write(header)
}

func (t *muxTrack) entry() []byte {
	children := [][]byte{
		uintElement(idTrackNumber, t.number),
		uintElement(idTrackUID, t.number),
		uintElement(idFlagLacing, 0),
		stringElement(idLanguage, t.language),
		stringElement(idCodecID, t.codecID),
	}
	if len(t.private) > 0 {
		children = append(children, element(idCodecPrivate, t.private))
	}
	if isVideo(t.cid) {
		children = append(children,
			uintElement(idTrackType, trackTypeVideo),
			master(idVideo,
				uintElement(idPixelWidth, uint64(t.width)),
				uintElement(idPixelHeight, uint64(t.height))))
		return master(idTrackEntry, children...)
	}
	if t.cid == codec.CODECID_AUDIO_OPUS {
		children = append(children,
			uintElement(idCodecDelay, t.codecDelay),
			// 80 ms of pre-roll lets the decoder converge after a seek
			uintElement(idSeekPreRoll, 80000000))
	}
	audio := [][]byte{
		floatElement(idSamplingFrequency, float64(t.sampleRate)),
		uintElement(idChannels, uint64(max(t.channels, 1))),
	}
	if t.cid == codec.CODECID_AUDIO_G711A || t.cid == codec.CODECID_AUDIO_G711U {
		audio = append(audio, uintElement(idBitDepth, 8))
	}
	children = append(children, uintElement(idTrackType, trackTypeAudio), master(idAudio, audio...))
	return master(idTrackEntry, children...)
}

func (m *Muxer) writeBlock(b muxBlock) error {
	// a key frame of a video track opens a cluster so every cluster is a
	// seek point; audio only files cut clusters by time instead
	cueTrack := b.key && (isVideo(b.track.cid) || !m.hasVideoTracks)
	rel := int64(b.pts) - int64(m.clusterTs)
	newCluster := !m.clusterOpen ||
		rel < math.MinInt16 || rel > math.MaxInt16 ||
		b.pts >= m.clusterTs+m.clusterDuration ||
		len(m.cluster) >= maxClusterBytes ||
		(isVideo(b.track.cid) && b.key && len(m.cluster) > 0)
	if newCluster {
		if err := m.flushCluster(); err != nil {
			return err
		}
		m.clusterOpen = true
		m.clusterTs = b.pts
		m.clusterStart = uint64(m.written - m.segmentDataAt)
		m.cluster = append(m.cluster[:0], uintElement(idClusterTime, b.pts)...)
		rel = 0
		if cueTrack {
			m.cues = append(m.cues, cuePoint{time: b.pts, track: b.track.number, position: m.clusterStart})
		}
	}

	flags := byte(0)
	if b.key {
		flags |= 0x80
	}
	hdr := appendSize(nil, b.track.number, sizeLen(b.track.number))
	hdr = binary.BigEndian.AppendUint16(hdr, uint16(int16(rel)))
	if b.padding != 0 {
		// DiscardPadding only exists in a BlockGroup, whose Block has no
		// key frame flag: a group without ReferenceBlock is a key frame
		block := element(idBlock, append(append(hdr, 0), b.data...))
		m.cluster = append(m.cluster, master(idBlockGroup, block, intElement(idDiscardPadding, b.padding))...)
	} else {
		hdr = append(hdr, flags)
		m.cluster = appendID(m.cluster, idSimpleBlock)
		size := uint64(len(hdr) + len(b.data))
		m.cluster = appendSize(m.cluster, size, sizeLen(size))
		m.cluster = append(m.cluster, hdr...)
		m.cluster = append(m.cluster, b.data...)
	}

	end := b.pts + frameDurationMs(b.track, b.data)
	b.track.endTs = max(b.track.endTs, end)
	m.endTs = max(m.endTs, end)
	return nil
}

// frameDurationMs estimates how long a frame lasts so the file duration
// covers the last frame; video frames count as zero.
func frameDurationMs(t *muxTrack, data []byte) uint64 {
	if t.sampleRate == 0 {
		return 0
	}
	switch t.cid {
	case codec.CODECID_AUDIO_AAC:
		return 1024 * 1000 / uint64(t.sampleRate)
	case codec.CODECID_AUDIO_OPUS:
		return codec.OpusPacketDuration(data) * 1000 / 48000
	case codec.CODECID_AUDIO_MP3:
		if head, err := codec.DecodeMp3Head(data); err == nil {
			return uint64(head.SampleSize) * 1000 / uint64(t.sampleRate)
		}
	case codec.CODECID_AUDIO_G711A, codec.CODECID_AUDIO_G711U:
		return uint64(len(data)) * 1000 / uint64(t.sampleRate) / uint64(max(t.channels, 1))
	}
	return 0
}

func (m *Muxer) flushCluster() error {
	if !m.clusterOpen {
		return nil
	}
	m.clusterOpen = false
	return m.write(element(idCluster, m.cluster))
}

// WriteTrailer flushes the last cluster and, for a seekable writer, writes
// the cues and fills in the segment size, the duration and the seek head.
// The writer is left positioned at the end of the file.
func (m *Muxer) WriteTrailer() error {
	if m.finished {
		return nil
	}
	for _, t := range m.tracks {
		for _, b := range t.flushPCM() {
			if !m.headerWritten {
				m.pending = append(m.pending, b)
			} else if err := m.writeBlock(b); err != nil {
				return err
			}
		}
	}
	if !m.headerWritten {
		if err := m.startWithReadyTracks(); err != nil {
			return err
		}
	}
	m.finished = true
	if err := m.flushCluster(); err != nil {
		return err
	}
	if m.ws == nil {
		return nil
	}

	cuesPos := uint64(m.written - m.segmentDataAt)
	if len(m.cues) > 0 {
		points := make([][]byte, len(m.cues))
		for i, c := range m.cues {
			points[i] = master(idCuePoint,
				uintElement(idCueTime, c.time),
				master(idCueTrackPositions,
					uintElement(idCueTrack, c.track),
					uintElement(idCueClusterPosition, c.position)))
		}
		if err := m.write(master(idCues, points...)); err != nil {
			return err
		}
	}
	end := m.written

	seeks := [][]byte{seekEntry(idInfo, m.infoPos), seekEntry(idTracks, m.tracksPos)}
	if len(m.cues) > 0 {
		seeks = append(seeks, seekEntry(idCues, cuesPos))
	}
	seekHead := master(idSeekHead, seeks...)
	seekHead = append(seekHead, voidElement(seekHeadReserve-len(seekHead))...)

	segmentSize := appendSize(nil, uint64(end-m.segmentDataAt), 8)
	duration := make([]byte, 8)
	binary.BigEndian.PutUint64(duration, math.Float64bits(float64(m.endTs)))

	for _, patch := range []struct {
		at   int64
		data []byte
	}{
		{m.segmentSizeAt, segmentSize},
		{m.segmentDataAt, seekHead},
		{m.durationAt, duration},
	} {
		if _, err := m.ws.Seek(m.start+patch.at, io.SeekStart); err != nil {
			return err
		}
		if _, err := m.ws.Write(patch.data); err != nil {
			return err
		}
	}
	_, err := m.ws.Seek(m.start+end, io.SeekStart)
	return err
}

// seekEntry points at a level 1 element. The position is always written in
// 8 bytes so the seek head has a fixed size.
func seekEntry(id uint32, position uint64) []byte {
	pos := make([]byte, 8)
	binary.BigEndian.PutUint64(pos, position)
	return master(idSeek,
		element(idSeekID, appendID(nil, id)),
		element(idSeekPosition, pos))
}
