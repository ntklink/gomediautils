package ogg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"

	"github.com/ntklink/gomediautils/go-codec"
)

const (
	// a page is closed once its body reaches this size or spans this many
	// 48 kHz samples, which keeps seeking granular (the values libogg and
	// ffmpeg use)
	pageTargetBytes   = 4096
	pageTargetSamples = 48000
	// maxPendingPackets bounds how many packets are held while a stream
	// waits for its first packet to reveal its channel count
	maxPendingPackets = 256

	vendorString = "gomediautils"
)

const (
	flagContinued = 0x01
	flagBOS       = 0x02
	flagEOS       = 0x04
)

// noGranule marks a page on which no packet ends.
const noGranule = ^uint64(0)

type muxStream struct {
	serial uint32
	seq    uint32
	head   []byte
	ready  bool

	started  bool
	granule  uint64 // 48 kHz samples at the end of the last packet
	padding  uint64 // samples to trim off the end of the stream
	lastDur  uint64 // samples in the last packet
	ended    bool
	body     []byte
	segments []byte
	// pageGranule is the granule of the last packet ending on the open page
	pageGranule uint64
	pageStart   uint64
	continued   bool
	// flushDue is set once the open page is full enough. It is written when
	// the next packet arrives, so the last page of a stream is still open at
	// the end and can carry the end of stream flag and the end trimming.
	flushDue bool
}

type pendingPacket struct {
	stream  *muxStream
	data    []byte
	pts     uint64
	padding int64
}

// Muxer writes an Ogg Opus stream (RFC 7845), the format of .opus files.
// Several Opus streams can be multiplexed into one file. Packets are raw
// Opus packets, timestamps in milliseconds.
type Muxer struct {
	w        io.Writer
	streams  []*muxStream
	started  bool
	finished bool
	pending  []pendingPacket
	writeErr error
}

// NewMuxer creates a muxer writing to w.
func NewMuxer(w io.Writer) *Muxer {
	return &Muxer{w: w}
}

// AddOpusStream declares an Opus stream and returns its serial number.
// head is the OpusHead (the Opus extra data of the other containers of this
// module). Without one the header is built from the first packet: mono or
// stereo, no pre-skip, which is right for Opus from an RTP source.
func (m *Muxer) AddOpusStream(head []byte) (uint32, error) {
	if m.started || m.finished {
		return 0, errors.New("ogg: streams must be added before the first packet is written")
	}
	s := &muxStream{pageGranule: noGranule}
	if len(head) > 0 {
		ctx := codec.OpusContext{}
		if err := ctx.ParseExtranData(head); err != nil {
			return 0, err
		}
		s.head = append([]byte(nil), head...)
		s.ready = true
	}
	for {
		// serial numbers only need to differ between the streams of a file
		s.serial = rand.Uint32()
		unique := true
		for _, other := range m.streams {
			unique = unique && other.serial != s.serial
		}
		if unique {
			break
		}
	}
	m.streams = append(m.streams, s)
	return s.serial, nil
}

func (m *Muxer) stream(serial uint32) (*muxStream, error) {
	for _, s := range m.streams {
		if s.serial == serial {
			return s, nil
		}
	}
	return nil, fmt.Errorf("ogg: unknown stream %d", serial)
}

// Write adds one Opus packet. dts is accepted for symmetry with the other
// muxers; Opus is never reordered.
func (m *Muxer) Write(serial uint32, packet []byte, pts uint64, dts uint64) error {
	return m.WritePadded(serial, packet, pts, dts, 0)
}

// WritePadded is Write for the last packet of a stream whose final
// discardPadding nanoseconds are encoder padding, as Matroska's
// DiscardPadding says. Ogg can only trim the end of a stream, so padding
// on any other packet is ignored.
func (m *Muxer) WritePadded(serial uint32, packet []byte, pts uint64, dts uint64, discardPadding int64) error {
	if m.finished {
		return errors.New("ogg: write after WriteTrailer")
	}
	if m.writeErr != nil {
		return m.writeErr
	}
	s, err := m.stream(serial)
	if err != nil {
		return err
	}
	if len(packet) == 0 {
		// an opus packet has at least its TOC byte
		return errors.New("ogg: empty opus packet")
	}
	if !s.ready {
		channels := 1
		if p := codec.DecodeOpusPacket(packet); p != nil && p.Stereo != 0 {
			channels = 2
		}
		ctx := codec.OpusContext{ChannelCount: channels, SampleRate: 48000}
		s.head = ctx.WriteOpusExtraData()
		s.ready = true
	}
	p := pendingPacket{stream: s, data: append([]byte(nil), packet...), pts: pts, padding: discardPadding}
	if !m.started {
		m.pending = append(m.pending, p)
		for _, st := range m.streams {
			if !st.ready && len(m.pending) < maxPendingPackets {
				return nil
			}
		}
		return m.start()
	}
	return m.addPacket(p)
}

// start writes the headers: every stream's OpusHead page first, as the
// beginning of stream pages must come before anything else, then every
// OpusTags page, then the held back packets.
func (m *Muxer) start() error {
	m.started = true
	var ready []*muxStream
	for _, s := range m.streams {
		if s.ready {
			ready = append(ready, s)
		} else {
			// no packet in time: the stream is left out of the file
			s.ended = true
		}
	}
	for _, s := range ready {
		s.addSegments(s.head)
		s.pageGranule = 0
		if err := m.flushPage(s, flagBOS); err != nil {
			return err
		}
	}
	tags := append([]byte("OpusTags"), binary.LittleEndian.AppendUint32(nil, uint32(len(vendorString)))...)
	tags = append(tags, vendorString...)
	tags = binary.LittleEndian.AppendUint32(tags, 0) // no user comments
	for _, s := range ready {
		s.addSegments(tags)
		s.pageGranule = 0
		if err := m.flushPage(s, 0); err != nil {
			return err
		}
	}
	pending := m.pending
	m.pending = nil
	for _, p := range pending {
		if err := m.addPacket(p); err != nil {
			return err
		}
	}
	return nil
}

func (m *Muxer) addPacket(p pendingPacket) error {
	s := p.stream
	if s.ended {
		return fmt.Errorf("ogg: stream %d was left out of the file", s.serial)
	}
	if !s.started {
		// the granule counts 48 kHz samples from the start of the file, so
		// a stream that starts late keeps its place in time
		s.started = true
		s.granule = p.pts * 48
		s.pageStart = s.granule
	}
	if s.flushDue {
		s.flushDue = false
		if err := m.flushPage(s, 0); err != nil {
			return err
		}
	}
	s.lastDur = codec.OpusPacketDuration(p.data)
	s.granule += s.lastDur
	s.padding = 0
	if p.padding > 0 {
		s.padding = uint64(p.padding) * 48000 / 1000000000
	}

	for {
		// a packet takes len/255+1 segments; what does not fit goes on to
		// the next page, marked as continued
		room := 255 - len(s.segments)
		need := len(p.data)/255 + 1
		if need <= room {
			s.addSegments(p.data)
			s.pageGranule = s.granule
			break
		}
		if room > 0 {
			n := room * 255
			s.body = append(s.body, p.data[:n]...)
			for i := 0; i < room; i++ {
				s.segments = append(s.segments, 255)
			}
			p.data = p.data[n:]
		}
		if err := m.flushPage(s, 0); err != nil {
			return err
		}
		s.continued = true
	}
	s.flushDue = len(s.body) >= pageTargetBytes || s.granule-s.pageStart >= pageTargetSamples
	return nil
}

// addSegments appends a whole packet to the open page.
func (s *muxStream) addSegments(packet []byte) {
	s.body = append(s.body, packet...)
	n := len(packet)
	for ; n >= 255; n -= 255 {
		s.segments = append(s.segments, 255)
	}
	s.segments = append(s.segments, byte(n))
}

// flushPage writes the open page of s.
func (m *Muxer) flushPage(s *muxStream, flags byte) error {
	if s.continued {
		flags |= flagContinued
	}
	granule := s.pageGranule
	page := make([]byte, 27, 27+len(s.segments)+len(s.body))
	copy(page, "OggS")
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:], granule)
	binary.LittleEndian.PutUint32(page[14:], s.serial)
	binary.LittleEndian.PutUint32(page[18:], s.seq)
	page[26] = byte(len(s.segments))
	page = append(page, s.segments...)
	page = append(page, s.body...)
	binary.LittleEndian.PutUint32(page[22:], makeChecksum(0, page))

	s.seq++
	s.body = s.body[:0]
	s.segments = s.segments[:0]
	s.continued = false
	s.pageGranule = noGranule
	s.pageStart = s.granule
	if _, err := m.w.Write(page); err != nil {
		m.writeErr = err
		return err
	}
	return nil
}

// WriteTrailer writes the last page of every stream, marked as the end of
// the stream, with the end trimmed by the padding of the last packet.
func (m *Muxer) WriteTrailer() error {
	if m.finished {
		return nil
	}
	if !m.started {
		if err := m.start(); err != nil {
			return err
		}
	}
	m.finished = true
	for _, s := range m.streams {
		if s.ended {
			continue
		}
		s.ended = true
		// the last packet always ends on this page, so its granule is the
		// end of the stream, less the padding the decoder is to drop
		s.pageGranule = s.granule
		if s.padding > 0 && s.padding <= s.lastDur {
			s.pageGranule -= s.padding
		}
		if err := m.flushPage(s, flagEOS); err != nil {
			return err
		}
	}
	return nil
}
