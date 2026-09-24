// Package es reads and writes bare elementary streams: Annex-B H.264 and
// H.265 (.h264, .h265), ADTS AAC (.aac), MPEG audio (.mp3) and G.711
// (.alaw, .ulaw).
//
// Readers split a stream into the frames the muxers of this module take
// and give them timestamps, which a bare stream does not carry: video runs
// at the frame rate from the SPS (or WithFrameRate), audio at the rate from
// its frame headers. For H.264 and H.265 with b frames the presentation
// order is recovered from the picture order count of every slice, so frames
// come out with the decode and presentation times a container needs.
package es

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/ntklink/gomediautils/go-codec"
)

// Frame is one access unit of video or one frame of audio. Pts and Dts are
// in milliseconds.
type Frame struct {
	Cid      codec.CodecID
	Data     []byte
	Pts      uint64
	Dts      uint64
	KeyFrame bool
}

// Reader hands out the frames of an elementary stream, io.EOF at the end.
type Reader interface {
	ReadFrame() (*Frame, error)
	Codec() codec.CodecID
}

type options struct {
	frameRate       float64
	sampleRate      uint32
	channels        uint8
	frameDurationMs uint32
}

type Option func(*options)

// WithFrameRate sets the video frame rate. By default it comes from the
// timing information of the SPS, or is 25 when the SPS has none.
func WithFrameRate(fps float64) Option {
	return func(o *options) {
		o.frameRate = fps
	}
}

// WithSampleRate sets the G.711 sample rate, 8000 by default.
func WithSampleRate(rate uint32) Option {
	return func(o *options) {
		o.sampleRate = rate
	}
}

// WithChannelCount sets the G.711 channel count, 1 by default.
func WithChannelCount(channels uint8) Option {
	return func(o *options) {
		o.channels = channels
	}
}

// WithFrameDuration sets how many milliseconds of G.711 go into a frame,
// 20 by default (the usual RTP packet).
func WithFrameDuration(ms uint32) Option {
	return func(o *options) {
		o.frameDurationMs = ms
	}
}

func applyOptions(opts []Option) options {
	o := options{sampleRate: 8000, channels: 1, frameDurationMs: 20}
	for _, opt := range opts {
		opt(&o)
	}
	if o.sampleRate == 0 {
		o.sampleRate = 8000
	}
	if o.channels == 0 {
		o.channels = 1
	}
	if o.frameDurationMs == 0 {
		o.frameDurationMs = 20
	}
	return o
}

// ErrUnknownFormat is reported by NewReader for data it cannot identify.
var ErrUnknownFormat = errors.New("es: unrecognised elementary stream")

// NewReader identifies the stream from its first bytes and returns the
// matching reader. G.711 has no header and cannot be identified; use
// NewG711Reader for it.
func NewReader(r io.Reader, opts ...Option) (Reader, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	head, _ := br.Peek(4096)
	switch Probe(head) {
	case codec.CODECID_VIDEO_H264:
		return NewH264Reader(br, opts...), nil
	case codec.CODECID_VIDEO_H265:
		return NewH265Reader(br, opts...), nil
	case codec.CODECID_AUDIO_AAC:
		return NewAACReader(br), nil
	case codec.CODECID_AUDIO_MP3:
		return NewMP3Reader(br), nil
	}
	return nil, ErrUnknownFormat
}

// Probe identifies an elementary stream from its first bytes, returning
// CODECID_UNRECOGNIZED when it cannot tell.
func Probe(head []byte) codec.CodecID {
	if bytes.HasPrefix(head, []byte("ID3")) {
		return codec.CODECID_AUDIO_MP3
	}
	if len(head) >= 2 && head[0] == 0xFF && head[1]&0xF6 == 0xF0 {
		// ADTS: sync word, layer 0
		hdr := codec.NewAdtsFrameHeader()
		if hdr.Decode(head) == nil {
			return codec.CODECID_AUDIO_AAC
		}
	}
	if len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0 {
		if _, err := codec.DecodeMp3Head(head); err == nil {
			return codec.CODECID_AUDIO_MP3
		}
	}
	start, sc := codec.FindStartCode(head, 0)
	if start < 0 || start+int(sc)+2 > len(head) {
		return codec.CODECID_UNRECOGNIZED
	}
	nal := head[start+int(sc):]
	if nal[0]&0x80 != 0 {
		return codec.CODECID_UNRECOGNIZED
	}
	// the second byte of an H.265 nal header holds nuh_layer_id (0) and
	// temporal_id_plus1 (1 for the parameter sets and key frames a stream
	// opens with); in H.264 it is payload, and an SPS starting with
	// profile_idc 1 or an AUD with that value does not exist
	switch t := codec.H265NaluTypeWithoutStartCode(nal); {
	case nal[1] == 0x01 && (t == codec.H265_NAL_VPS || t == codec.H265_NAL_SPS || t == codec.H265_NAL_PPS ||
		t == codec.H265_NAL_AUD || t == codec.H265_NAL_SEI ||
		(t >= codec.H265_NAL_SLICE_BLA_W_LP && t <= codec.H265_NAL_SLICE_CRA)):
		return codec.CODECID_VIDEO_H265
	}
	switch codec.H264NaluTypeWithoutStartCode(nal) {
	case codec.H264_NAL_SPS, codec.H264_NAL_PPS, codec.H264_NAL_AUD, codec.H264_NAL_SEI, codec.H264_NAL_I_SLICE:
		return codec.CODECID_VIDEO_H264
	}
	return codec.CODECID_UNRECOGNIZED
}
