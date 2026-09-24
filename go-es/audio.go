package es

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/ntklink/gomediautils/go-codec"
)

// maxResync is how far a reader scans for the next sync word before it
// gives up on a stream.
const maxResync = 64 * 1024

func bufferedReader(r io.Reader) *bufio.Reader {
	if br, ok := r.(*bufio.Reader); ok {
		return br
	}
	return bufio.NewReaderSize(r, 64*1024)
}

// sampleClock turns a running sample count into milliseconds without the
// drift of adding up rounded frame durations.
type sampleClock struct {
	samples uint64
}

func (c *sampleClock) advance(samples uint64, rate uint32) uint64 {
	ts := c.samples * 1000 / uint64(rate)
	c.samples += samples
	return ts
}

type aacReader struct {
	r     *bufio.Reader
	clock sampleClock
	rate  uint32
}

// NewAACReader reads an ADTS AAC stream. Every frame keeps its ADTS header,
// the form the muxers of this module take.
func NewAACReader(r io.Reader) Reader {
	return &aacReader{r: bufferedReader(r)}
}

func (a *aacReader) Codec() codec.CodecID {
	return codec.CODECID_AUDIO_AAC
}

func (a *aacReader) ReadFrame() (*Frame, error) {
	skipped := 0
	for {
		head, err := a.r.Peek(7)
		if len(head) < 7 {
			if len(head) == 0 || err == io.EOF {
				return nil, io.EOF
			}
			return nil, err
		}
		if head[0] == 0xFF && head[1]&0xF6 == 0xF0 {
			hdr := codec.NewAdtsFrameHeader()
			if hdr.Decode(head) == nil {
				size := int(hdr.Variable_Header.Frame_length)
				rate := codec.AACSampleIdxToSample(int(hdr.Fix_Header.Sampling_frequency_index))
				if size >= adtsHeaderLen(head) && rate > 0 {
					frame := make([]byte, size)
					if _, err := io.ReadFull(a.r, frame); err != nil {
						if err == io.ErrUnexpectedEOF {
							// a frame cut off at the end of the stream
							return nil, io.EOF
						}
						return nil, err
					}
					a.rate = uint32(rate)
					// one raw data block per frame is 1024 samples
					blocks := uint64(hdr.Variable_Header.Number_of_raw_data_blocks_in_frame) + 1
					ts := a.clock.advance(1024*blocks, a.rate)
					return &Frame{Cid: codec.CODECID_AUDIO_AAC, Data: frame, Pts: ts, Dts: ts, KeyFrame: true}, nil
				}
			}
		}
		// not a frame here: move on to the next byte that could be one
		if skipped++; skipped > maxResync {
			return nil, errors.New("es: no adts sync word found")
		}
		a.r.Discard(1)
	}
}

func adtsHeaderLen(head []byte) int {
	if head[1]&0x01 == 0 {
		return 9
	}
	return 7
}

type mp3Reader struct {
	r       *bufio.Reader
	clock   sampleClock
	started bool
}

// NewMP3Reader reads an MPEG audio stream. An ID3v2 tag at the start and a
// Xing, Info or VBRI header frame are skipped, as players do: they hold no
// audio.
func NewMP3Reader(r io.Reader) Reader {
	return &mp3Reader{r: bufferedReader(r)}
}

func (m *mp3Reader) Codec() codec.CodecID {
	return codec.CODECID_AUDIO_MP3
}

func (m *mp3Reader) ReadFrame() (*Frame, error) {
	skipped := 0
	for {
		head, err := m.r.Peek(10)
		if len(head) < 4 {
			if len(head) == 0 || err == io.EOF {
				return nil, io.EOF
			}
			return nil, err
		}
		if bytes.HasPrefix(head, []byte("ID3")) && len(head) == 10 {
			// ID3v2 sizes are 7 bits per byte
			size := int(head[6]&0x7F)<<21 | int(head[7]&0x7F)<<14 | int(head[8]&0x7F)<<7 | int(head[9]&0x7F)
			if head[5]&0x10 != 0 {
				size += 10 // footer
			}
			if _, err := m.r.Discard(10 + size); err != nil {
				return nil, io.EOF
			}
			continue
		}
		if bytes.HasPrefix(head, []byte("TAG")) {
			// an ID3v1 tag closes the file
			return nil, io.EOF
		}
		if hdr, err := codec.DecodeMp3Head(head); err == nil {
			frame := make([]byte, hdr.FrameSize)
			if _, err := io.ReadFull(m.r, frame); err != nil {
				if err == io.ErrUnexpectedEOF {
					return nil, io.EOF
				}
				return nil, err
			}
			first := !m.started
			m.started = true
			if first && isInfoFrame(frame) {
				continue
			}
			ts := m.clock.advance(uint64(hdr.SampleSize), uint32(hdr.GetSampleRate()))
			return &Frame{Cid: codec.CODECID_AUDIO_MP3, Data: frame, Pts: ts, Dts: ts, KeyFrame: true}, nil
		}
		if skipped++; skipped > maxResync {
			return nil, errors.New("es: no mpeg audio sync word found")
		}
		m.r.Discard(1)
	}
}

// isInfoFrame reports whether the first frame of a file is a Xing, Info or
// VBRI header, which sits after the side information of an empty frame.
func isInfoFrame(frame []byte) bool {
	n := min(len(frame), 64)
	return bytes.Contains(frame[:n], []byte("Xing")) ||
		bytes.Contains(frame[:n], []byte("Info")) ||
		bytes.Contains(frame[:n], []byte("VBRI"))
}

type g711Reader struct {
	cid   codec.CodecID
	r     io.Reader
	opts  options
	clock sampleClock
}

// NewG711Reader reads raw G.711 samples (cid is CODECID_AUDIO_G711A or
// CODECID_AUDIO_G711U) and hands them out 20 ms at a time. The sample rate,
// channel count and frame length can be set with options.
func NewG711Reader(r io.Reader, cid codec.CodecID, opts ...Option) Reader {
	return &g711Reader{cid: cid, r: r, opts: applyOptions(opts)}
}

func (g *g711Reader) Codec() codec.CodecID {
	return g.cid
}

func (g *g711Reader) ReadFrame() (*Frame, error) {
	channels := uint64(g.opts.channels)
	samples := uint64(g.opts.sampleRate) * uint64(g.opts.frameDurationMs) / 1000
	frame := make([]byte, samples*channels)
	n, err := io.ReadFull(g.r, frame)
	if n == 0 {
		if err == io.ErrUnexpectedEOF || err == nil {
			err = io.EOF
		}
		return nil, err
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	// a short last frame keeps whole samples only
	frame = frame[:uint64(n)/channels*channels]
	if len(frame) == 0 {
		return nil, io.EOF
	}
	ts := g.clock.advance(uint64(len(frame))/channels, g.opts.sampleRate)
	return &Frame{Cid: g.cid, Data: frame, Pts: ts, Dts: ts, KeyFrame: true}, nil
}
