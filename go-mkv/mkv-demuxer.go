package mkv

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/ntklink/gomediautils/go-codec"
)

// maxElementSize caps the elements the demuxer reads into memory: track
// headers and blocks. Larger ones only turn up in corrupt files.
const maxElementSize = 256 << 20

// TrackInfo describes one track of the file.
type TrackInfo struct {
	TrackNumber uint64
	// Cid is CODECID_UNRECOGNIZED for codecs this package does not know;
	// CodecID still names them and their frames are passed through as
	// they are stored.
	Cid          codec.CodecID
	CodecID      string
	Width        uint32
	Height       uint32
	SampleRate   uint32
	ChannelCount uint8
	BitDepth     uint8
	// ExtraData is the CodecPrivate: the avcC or hvcC record, the AAC
	// AudioSpecificConfig, the OpusHead, or the WAVEFORMATEX of A_MS/ACM.
	ExtraData []byte
	// CodecDelay is the opus pre-skip, in nanoseconds.
	CodecDelay uint64
	Language   string
}

// Packet is one frame. Data is in the form the other demuxers of this module
// produce: Annex-B for H.264 and H.265 (parameter sets in front of key
// frames), ADTS for AAC, raw frames otherwise. Pts and Dts are in
// milliseconds.
type Packet struct {
	TrackNumber uint64
	Cid         codec.CodecID
	Data        []byte
	Pts         uint64
	Dts         uint64
	KeyFrame    bool
	// DiscardPadding is how many nanoseconds at the end of the frame's
	// decoded audio are padding to throw away, as Opus encoders leave on
	// the last frame. Muxer.WritePadded writes it back.
	DiscardPadding int64
}

type compression struct {
	algo     uint64
	settings []byte
}

type demuxTrack struct {
	info            TrackInfo
	trackType       uint64
	defaultDuration uint64 // nanoseconds
	compressions    []compression
	encrypted       bool
	nalLen          int
	paramSets       []byte
	reorder         *dtsReorder
}

type queued struct {
	pkt      *Packet
	dts      int64
	resolved bool
}

// maxOffsetWait is how many frames ReadPacket holds back while it works out
// how far b frames push the timeline; past it the offset is settled with the
// video frames seen so far.
const maxOffsetWait = 512

// Demuxer reads a Matroska or WebM stream. It only reads forward, so a pipe
// or a socket works as well as a file, and so do live streams with unknown
// size segments and clusters.
type Demuxer struct {
	r              *bufio.Reader
	pos            int64
	peeked         *elementHeader
	docType        string
	timestampScale uint64
	duration       float64
	tracks         map[uint64]*demuxTrack
	order          []uint64
	headRead       bool
	eof            bool

	segmentEnd int64 // -1 while the segment has unknown size
	inSegment  bool
	clusterEnd int64 // -1 for an unknown size cluster
	inCluster  bool
	clusterTs  uint64

	queue []*queued
	// offset moves every timestamp of the file forward so that the decode
	// times derived for b frames never fall below zero
	offset      uint64
	offsetKnown bool
}

func NewDemuxer(r io.Reader) *Demuxer {
	return &Demuxer{
		r:              bufio.NewReaderSize(r, 64*1024),
		timestampScale: 1000000,
		tracks:         make(map[uint64]*demuxTrack),
		segmentEnd:     -1,
		clusterEnd:     -1,
	}
}

type elementHeader struct {
	id      uint32
	size    uint64
	unknown bool
	start   int64 // offset of the id
	dataAt  int64 // offset of the first payload byte
}

func (h *elementHeader) end() int64 {
	if h.unknown {
		return -1
	}
	return h.dataAt + int64(h.size)
}

func (d *Demuxer) readByte() (byte, error) {
	b, err := d.r.ReadByte()
	if err == nil {
		d.pos++
	}
	return b, err
}

// readHeader reads the next element id and data size.
func (d *Demuxer) readHeader() (*elementHeader, error) {
	if h := d.peeked; h != nil {
		d.peeked = nil
		return h, nil
	}
	h := &elementHeader{start: d.pos}
	first, err := d.readByte()
	if err != nil {
		return nil, err
	}
	n := vintLen(first)
	if n == 0 || n > 4 {
		return nil, fmt.Errorf("mkv: invalid element id at offset %d", h.start)
	}
	h.id = uint32(first)
	for i := 1; i < n; i++ {
		b, err := d.readByte()
		if err != nil {
			return nil, noEOF(err)
		}
		h.id = h.id<<8 | uint32(b)
	}
	first, err = d.readByte()
	if err != nil {
		return nil, noEOF(err)
	}
	n = vintLen(first)
	if n == 0 {
		return nil, fmt.Errorf("mkv: invalid element size at offset %d", d.pos-1)
	}
	buf := []byte{first}
	for i := 1; i < n; i++ {
		b, err := d.readByte()
		if err != nil {
			return nil, noEOF(err)
		}
		buf = append(buf, b)
	}
	h.size, _, h.unknown, _ = readVint(buf)
	h.dataAt = d.pos
	return h, nil
}

// noEOF reports a stream that ends inside an element as truncated.
func noEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// readPayload reads an element's payload. The buffer grows with the data
// that actually arrives, so a corrupt size cannot force a huge allocation.
func (d *Demuxer) readPayload(h *elementHeader) ([]byte, error) {
	if h.unknown || h.size > maxElementSize {
		return nil, fmt.Errorf("%w: element 0x%X of %d bytes", errTooLarge, h.id, h.size)
	}
	var buf bytes.Buffer
	n, err := io.CopyN(&buf, d.r, int64(h.size))
	d.pos += n
	if err != nil {
		return nil, noEOF(err)
	}
	return buf.Bytes(), nil
}

func (d *Demuxer) skip(h *elementHeader) error {
	if h.unknown {
		return fmt.Errorf("mkv: cannot skip element 0x%X of unknown size", h.id)
	}
	n, err := d.r.Discard(int(min(h.size, math.MaxInt32)))
	d.pos += int64(n)
	for err == nil && uint64(d.pos-h.dataAt) < h.size {
		n, err = d.r.Discard(int(min(h.size-uint64(d.pos-h.dataAt), math.MaxInt32)))
		d.pos += int64(n)
	}
	return noEOF(err)
}

// eachChild walks the elements of an in memory master payload.
func eachChild(buf []byte, fn func(id uint32, payload []byte) error) error {
	for len(buf) > 0 {
		n := vintLen(buf[0])
		if n == 0 || n > 4 || len(buf) < n {
			return errors.New("mkv: invalid child element id")
		}
		var id uint32
		for i := 0; i < n; i++ {
			id = id<<8 | uint32(buf[i])
		}
		buf = buf[n:]
		size, sn, unknown, err := readVint(buf)
		if err != nil {
			return err
		}
		buf = buf[sn:]
		if unknown {
			size = uint64(len(buf))
		}
		if size > uint64(len(buf)) {
			return fmt.Errorf("mkv: child element 0x%X overruns its parent", id)
		}
		if err := fn(id, buf[:size]); err != nil {
			return err
		}
		buf = buf[size:]
	}
	return nil
}

// endsCluster reports whether an element id cannot live inside a cluster,
// which is how an unknown size cluster ends.
func endsCluster(id uint32) bool {
	switch id {
	case idCluster, idCues, idTags, idChapters, idAttachments, idSeekHead,
		idInfo, idTracks, idEBML, idSegment:
		return true
	}
	return false
}

// DocType is "matroska" or "webm", once ReadHead has run.
func (d *Demuxer) DocType() string {
	return d.docType
}

// Duration is the segment duration in milliseconds, or 0 when the file does
// not declare one (live streams).
func (d *Demuxer) Duration() uint64 {
	return uint64(d.duration * float64(d.timestampScale) / 1e6)
}

// ReadHead reads everything up to the first cluster and reports the tracks.
func (d *Demuxer) ReadHead() ([]TrackInfo, error) {
	if !d.headRead {
		for !d.headRead {
			h, err := d.next()
			if err != nil {
				if err == io.EOF && len(d.order) > 0 {
					d.headRead = true
					break
				}
				if err == io.EOF {
					return nil, errors.New("mkv: no tracks before the end of the stream")
				}
				return nil, err
			}
			if h != nil {
				// the first block of the first cluster: keep it for
				// ReadPacket
				d.peeked = h
				d.headRead = true
			}
		}
		if len(d.order) == 0 {
			return nil, errors.New("mkv: no Tracks element before the first cluster")
		}
	}
	infos := make([]TrackInfo, 0, len(d.order))
	for _, n := range d.order {
		infos = append(infos, d.tracks[n].info)
	}
	return infos, nil
}

// next reads elements until it reaches one a cluster carries frames in,
// handling everything else on the way. It returns that element's header,
// or io.EOF at the end of the stream.
func (d *Demuxer) next() (*elementHeader, error) {
	for {
		if d.inCluster && d.clusterEnd >= 0 && d.pos >= d.clusterEnd {
			d.inCluster = false
		}
		if d.inSegment && d.segmentEnd >= 0 && d.pos >= d.segmentEnd {
			d.inSegment = false
			d.inCluster = false
		}
		h, err := d.readHeader()
		if err != nil {
			return nil, err
		}
		if d.inCluster && d.clusterEnd < 0 && endsCluster(h.id) {
			d.inCluster = false
		}
		if d.inCluster {
			switch h.id {
			case idClusterTime:
				payload, err := d.readPayload(h)
				if err != nil {
					return nil, err
				}
				if d.clusterTs, err = readUint(payload); err != nil {
					return nil, err
				}
			case idSimpleBlock, idBlockGroup:
				return h, nil
			default:
				if err := d.skip(h); err != nil {
					return nil, err
				}
			}
			continue
		}
		switch h.id {
		case idEBML:
			payload, err := d.readPayload(h)
			if err != nil {
				return nil, err
			}
			if err := d.parseEBMLHeader(payload); err != nil {
				return nil, err
			}
		case idSegment:
			if d.docType == "" {
				return nil, errBadHeader
			}
			d.inSegment = true
			d.segmentEnd = h.end()
		case idCluster:
			if !d.inSegment {
				return nil, errors.New("mkv: cluster outside a segment")
			}
			d.inCluster = true
			d.clusterEnd = h.end()
			d.clusterTs = 0
		case idInfo:
			payload, err := d.readPayload(h)
			if err != nil {
				return nil, err
			}
			if err := d.parseInfo(payload); err != nil {
				return nil, err
			}
		case idTracks:
			payload, err := d.readPayload(h)
			if err != nil {
				return nil, err
			}
			if err := d.parseTracks(payload); err != nil {
				return nil, err
			}
		default:
			if d.docType == "" {
				return nil, errBadHeader
			}
			if err := d.skip(h); err != nil {
				return nil, err
			}
		}
	}
}

func (d *Demuxer) parseEBMLHeader(payload []byte) error {
	var docType string
	err := eachChild(payload, func(id uint32, p []byte) error {
		if id == idDocType {
			docType = readString(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if docType != DocTypeMatroska && docType != DocTypeWebM {
		return fmt.Errorf("mkv: unsupported doc type %q", docType)
	}
	d.docType = docType
	return nil
}

func (d *Demuxer) parseInfo(payload []byte) error {
	return eachChild(payload, func(id uint32, p []byte) (err error) {
		switch id {
		case idTimestampScale:
			if d.timestampScale, err = readUint(p); err == nil && d.timestampScale == 0 {
				err = errors.New("mkv: zero timestamp scale")
			}
		case idDuration:
			d.duration, err = readFloat(p)
			if math.IsNaN(d.duration) || math.IsInf(d.duration, 0) || d.duration < 0 {
				d.duration = 0
			}
		}
		return err
	})
}

func (d *Demuxer) parseTracks(payload []byte) error {
	return eachChild(payload, func(id uint32, p []byte) error {
		if id != idTrackEntry {
			return nil
		}
		t := &demuxTrack{info: TrackInfo{Language: "eng"}}
		if err := t.parseEntry(p); err != nil {
			return err
		}
		if t.info.TrackNumber == 0 {
			return errors.New("mkv: track entry without a track number")
		}
		t.setup()
		if _, dup := d.tracks[t.info.TrackNumber]; !dup {
			d.order = append(d.order, t.info.TrackNumber)
		}
		d.tracks[t.info.TrackNumber] = t
		return nil
	})
}

func (t *demuxTrack) parseEntry(payload []byte) error {
	return eachChild(payload, func(id uint32, p []byte) (err error) {
		var v uint64
		switch id {
		case idTrackNumber:
			t.info.TrackNumber, err = readUint(p)
		case idTrackType:
			t.trackType, err = readUint(p)
		case idCodecID:
			t.info.CodecID = readString(p)
		case idCodecPrivate:
			t.info.ExtraData = append([]byte(nil), p...)
		case idCodecDelay:
			t.info.CodecDelay, err = readUint(p)
		case idDefaultDuration:
			t.defaultDuration, err = readUint(p)
		case idLanguage:
			t.info.Language = readString(p)
		case idVideo:
			err = eachChild(p, func(id uint32, p []byte) (err error) {
				switch id {
				case idPixelWidth:
					v, err = readUint(p)
					t.info.Width = uint32(min(v, math.MaxUint32))
				case idPixelHeight:
					v, err = readUint(p)
					t.info.Height = uint32(min(v, math.MaxUint32))
				}
				return err
			})
		case idAudio:
			err = eachChild(p, func(id uint32, p []byte) (err error) {
				switch id {
				case idSamplingFrequency:
					var f float64
					if f, err = readFloat(p); err == nil && f > 0 && f < math.MaxUint32 {
						t.info.SampleRate = uint32(f)
					}
				case idChannels:
					v, err = readUint(p)
					t.info.ChannelCount = uint8(min(v, math.MaxUint8))
				case idBitDepth:
					v, err = readUint(p)
					t.info.BitDepth = uint8(min(v, math.MaxUint8))
				}
				return err
			})
		case idContentEncodings:
			err = t.parseEncodings(p)
		}
		return err
	})
}

func (t *demuxTrack) parseEncodings(payload []byte) error {
	return eachChild(payload, func(id uint32, p []byte) error {
		if id != idContentEncoding {
			return nil
		}
		var encType uint64
		comp := compression{}
		hasComp := false
		err := eachChild(p, func(id uint32, p []byte) (err error) {
			switch id {
			case idContentEncodingTyp:
				encType, err = readUint(p)
			case idContentCompression:
				hasComp = true
				err = eachChild(p, func(id uint32, p []byte) (err error) {
					switch id {
					case idContentCompAlgo:
						comp.algo, err = readUint(p)
					case idContentCompSetting:
						comp.settings = append([]byte(nil), p...)
					}
					return err
				})
			}
			return err
		})
		if err != nil {
			return err
		}
		if encType != 0 {
			t.encrypted = true
		} else if hasComp {
			t.compressions = append(t.compressions, comp)
		}
		return nil
	})
}

// setup works out what the frames of the track need on the way out.
func (t *demuxTrack) setup() {
	t.info.Cid = codecFromTrack(t.info.CodecID, t.info.ExtraData)
	private := t.info.ExtraData
	switch t.info.Cid {
	case codec.CODECID_VIDEO_H264:
		t.nalLen = 4
		if len(private) >= 5 {
			t.nalLen = int(private[4]&0x03) + 1
		}
		if spss, ppss, err := codec.CovertExtradata(private); err == nil {
			for _, n := range append(spss, ppss...) {
				t.paramSets = append(t.paramSets, n...)
			}
		}
		t.reorder = newDtsReorder()
	case codec.CODECID_VIDEO_H265:
		t.nalLen = 4
		if len(private) >= 22 {
			t.nalLen = int(private[21]&0x03) + 1
		}
		hvcc := codec.NewHEVCRecordConfiguration()
		if err := hvcc.Decode(private); err == nil {
			t.paramSets = hvcc.ToNalus()
		}
		t.reorder = newDtsReorder()
	case codec.CODECID_AUDIO_OPUS:
		if t.info.SampleRate == 0 {
			t.info.SampleRate = 48000
		}
	case codec.CODECID_AUDIO_G711A, codec.CODECID_AUDIO_G711U:
		if len(private) >= 16 {
			if t.info.ChannelCount == 0 {
				t.info.ChannelCount = uint8(binary.LittleEndian.Uint16(private[2:]))
			}
			if t.info.SampleRate == 0 {
				t.info.SampleRate = binary.LittleEndian.Uint32(private[4:])
			}
		}
	}
}

// reorderMeasured reports whether every track that derives decode times has
// seen enough frames to know how far it reorders.
func (d *Demuxer) reorderMeasured() bool {
	for _, n := range d.order {
		if r := d.tracks[n].reorder; r != nil && !r.measured {
			return false
		}
	}
	return true
}

// settleOffset fixes the timeline offset: the largest shift any b frame
// track needs. A track with no frames yet needs none.
func (d *Demuxer) settleOffset() {
	d.offsetKnown = true
	for _, n := range d.order {
		if r := d.tracks[n].reorder; r != nil {
			r.measure()
			d.offset = max(d.offset, r.shift())
		}
	}
}

// ReadPacket returns the next frame in file order, io.EOF at the end.
//
// Matroska stores presentation times only. For H.264 and H.265 the decode
// times are derived from the reordering of the first frames, and when the
// file starts at zero with b frames every timestamp of the file, audio
// included, is moved forward by the few frames the first decode times need,
// the way an mp4 edit list does.
func (d *Demuxer) ReadPacket() (*Packet, error) {
	if !d.headRead {
		if _, err := d.ReadHead(); err != nil {
			return nil, err
		}
	}
	for {
		if !d.offsetKnown && (d.eof || len(d.queue) >= maxOffsetWait || d.reorderMeasured()) {
			d.settleOffset()
		}
		if d.offsetKnown && len(d.queue) > 0 && d.queue[0].resolved {
			q := d.queue[0]
			d.queue[0] = nil
			d.queue = d.queue[1:]
			q.pkt.Pts += d.offset
			q.pkt.Dts = uint64(max(q.dts+int64(d.offset), 0))
			return q.pkt, nil
		}
		if d.eof {
			if len(d.queue) > 0 {
				// every reorder buffer was flushed at the end
				return nil, errors.New("mkv: unresolved frame left in the queue")
			}
			return nil, io.EOF
		}
		h, err := d.next()
		if err == io.EOF {
			d.eof = true
			for _, n := range d.order {
				if r := d.tracks[n].reorder; r != nil {
					r.flush()
				}
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := d.readBlock(h); err != nil {
			return nil, err
		}
	}
}

func (d *Demuxer) readBlock(h *elementHeader) error {
	payload, err := d.readPayload(h)
	if err != nil {
		return err
	}
	if h.id == idSimpleBlock {
		return d.parseBlock(payload, true, false, 0)
	}
	var block []byte
	var padding int64
	hasRef := false
	err = eachChild(payload, func(id uint32, p []byte) (err error) {
		switch id {
		case idBlock:
			block = p
		case idReferenceBlock:
			hasRef = true
		case idDiscardPadding:
			padding, err = readInt(p)
		}
		return err
	})
	if err != nil {
		return err
	}
	if block == nil {
		return nil
	}
	return d.parseBlock(block, false, !hasRef, padding)
}

// parseBlock splits a SimpleBlock or Block into its frames. For a Block the
// key frame flag and the discard padding come from the enclosing
// BlockGroup; the padding belongs to the last frame.
func (d *Demuxer) parseBlock(buf []byte, simple bool, groupKey bool, padding int64) error {
	number, n, _, err := readVint(buf)
	if err != nil {
		return err
	}
	buf = buf[n:]
	if len(buf) < 3 {
		return errors.New("mkv: truncated block header")
	}
	rel := int16(binary.BigEndian.Uint16(buf))
	flags := buf[2]
	buf = buf[3:]
	t, ok := d.tracks[number]
	if !ok {
		// a block for a track the header does not declare
		return nil
	}
	key := groupKey
	if simple {
		key = flags&0x80 != 0
	}
	frames, err := splitLaces(buf, (flags>>1)&0x03)
	if err != nil {
		return err
	}
	if t.encrypted {
		return fmt.Errorf("mkv: track %d is encrypted", number)
	}

	// block times are in timestamp scale units relative to the cluster
	ts := int64(d.clusterTs) + int64(rel)
	nanos := float64(max(ts, 0)) * float64(d.timestampScale)
	for i, frame := range frames {
		if frame, err = t.decompress(frame); err != nil {
			return err
		}
		pkt := &Packet{TrackNumber: number, Cid: t.info.Cid, KeyFrame: key}
		if i == len(frames)-1 {
			pkt.DiscardPadding = padding
		}
		pkt.Pts = uint64(nanos / 1e6)
		if pkt.Data, err = t.output(frame, &pkt.KeyFrame, simple); err != nil {
			return err
		}
		nanos += float64(t.frameDuration(frame))
		q := &queued{pkt: pkt, dts: int64(pkt.Pts), resolved: t.reorder == nil}
		d.queue = append(d.queue, q)
		if t.reorder != nil {
			t.reorder.push(q)
		}
	}
	return nil
}

// splitLaces cuts a block payload into frames by its lacing mode: 0 none,
// 1 Xiph, 2 fixed size, 3 EBML.
func splitLaces(buf []byte, lacing byte) ([][]byte, error) {
	if lacing == 0 {
		return [][]byte{buf}, nil
	}
	if len(buf) < 1 {
		return nil, errors.New("mkv: truncated lace header")
	}
	count := int(buf[0]) + 1
	buf = buf[1:]
	sizes := make([]int, count)
	switch lacing {
	case 1:
		for i := 0; i < count-1; i++ {
			for {
				if len(buf) == 0 {
					return nil, errors.New("mkv: truncated xiph lace")
				}
				b := buf[0]
				buf = buf[1:]
				sizes[i] += int(b)
				if b != 0xFF {
					break
				}
			}
		}
	case 3:
		first, n, _, err := readVint(buf)
		if err != nil {
			return nil, err
		}
		buf = buf[n:]
		if first > uint64(len(buf)) {
			return nil, errors.New("mkv: ebml lace size out of range")
		}
		sizes[0] = int(first)
		for i := 1; i < count-1; i++ {
			raw, n, _, err := readVint(buf)
			if err != nil {
				return nil, err
			}
			buf = buf[n:]
			// the differences are signed: the raw value minus half the
			// range of an n byte vint
			diff := int64(raw) - (int64(1)<<(7*n-1) - 1)
			size := int64(sizes[i-1]) + diff
			if size < 0 || size > int64(len(buf)) {
				return nil, errors.New("mkv: ebml lace size out of range")
			}
			sizes[i] = int(size)
		}
	case 2:
		if len(buf)%count != 0 {
			return nil, errors.New("mkv: fixed lace does not divide the block")
		}
		for i := range sizes {
			sizes[i] = len(buf) / count
		}
	}
	if lacing != 2 {
		used := 0
		for _, s := range sizes[:count-1] {
			used += s
		}
		if used > len(buf) {
			return nil, errors.New("mkv: laced frames overrun the block")
		}
		sizes[count-1] = len(buf) - used
	}
	frames := make([][]byte, count)
	for i, s := range sizes {
		frames[i] = buf[:s]
		buf = buf[s:]
	}
	return frames, nil
}

// decompress undoes the track's content compression. Header stripping
// (algorithm 3) is what mkvmerge used to apply by default; zlib (0) is
// rarer but cheap to support.
func (t *demuxTrack) decompress(frame []byte) ([]byte, error) {
	for i := len(t.compressions) - 1; i >= 0; i-- {
		c := t.compressions[i]
		switch c.algo {
		case 3:
			frame = append(append([]byte(nil), c.settings...), frame...)
		case 0:
			zr, err := zlib.NewReader(bytes.NewReader(frame))
			if err != nil {
				return nil, err
			}
			var out bytes.Buffer
			_, err = io.Copy(&out, io.LimitReader(zr, maxElementSize+1))
			zr.Close()
			if err != nil {
				return nil, err
			}
			if out.Len() > maxElementSize {
				return nil, errTooLarge
			}
			frame = out.Bytes()
		default:
			return nil, fmt.Errorf("mkv: unsupported content compression %d", c.algo)
		}
	}
	return frame, nil
}

// output converts a stored frame into what ReadPacket hands out.
func (t *demuxTrack) output(frame []byte, key *bool, simple bool) ([]byte, error) {
	switch t.info.Cid {
	case codec.CODECID_VIDEO_H264, codec.CODECID_VIDEO_H265:
		h265 := t.info.Cid == codec.CODECID_VIDEO_H265
		if !simple && !*key {
			// a BlockGroup without references is a key frame; trust the
			// slices over a missing ReferenceBlock
			*key = isKeyAccessUnit(frame, t.nalLen, h265)
		}
		annexb, err := lengthPrefixedToAnnexB(frame, t.nalLen)
		if err != nil {
			return nil, err
		}
		if *key && len(t.paramSets) > 0 && !hasParamSets(annexb, h265) {
			annexb = append(append([]byte(nil), t.paramSets...), annexb...)
		}
		return annexb, nil
	case codec.CODECID_AUDIO_AAC:
		if len(t.info.ExtraData) < 2 {
			return nil, errors.New("mkv: aac track without an AudioSpecificConfig")
		}
		hdr, err := codec.ConvertASCToADTS(t.info.ExtraData, len(frame)+7)
		if err != nil {
			return nil, err
		}
		return append(hdr.Encode(), frame...), nil
	}
	return frame, nil
}

func hasParamSets(annexb []byte, h265 bool) bool {
	found := false
	codec.SplitFrame(annexb, func(nalu []byte) bool {
		if h265 {
			found = codec.H265NaluTypeWithoutStartCode(nalu) == codec.H265_NAL_SPS
		} else {
			found = codec.H264NaluTypeWithoutStartCode(nalu) == codec.H264_NAL_SPS
		}
		return !found
	})
	return found
}

// frameDuration is how long a frame lasts in nanoseconds, used to space out
// the frames of a laced block.
func (t *demuxTrack) frameDuration(frame []byte) uint64 {
	if t.defaultDuration > 0 {
		return t.defaultDuration
	}
	rate := uint64(t.info.SampleRate)
	switch t.info.Cid {
	case codec.CODECID_AUDIO_OPUS:
		return codec.OpusPacketDuration(frame) * 1000000000 / 48000
	case codec.CODECID_AUDIO_AAC:
		if rate > 0 {
			return 1024 * 1000000000 / rate
		}
	case codec.CODECID_AUDIO_MP3:
		if head, err := codec.DecodeMp3Head(frame); err == nil && rate > 0 {
			return uint64(head.SampleSize) * 1000000000 / rate
		}
	case codec.CODECID_AUDIO_G711A, codec.CODECID_AUDIO_G711U:
		if rate > 0 {
			return uint64(len(frame)) * 1000000000 / rate / uint64(max(t.info.ChannelCount, 1))
		}
	}
	return 0
}
