// Package wav reads and writes RIFF/WAVE files: linear PCM, IEEE float and
// the G.711 A-law and μ-law formats that GB28181 cameras and RTSP/RTMP
// sources carry, so a G.711 stream pulled out of a PS, FLV or MKV file can
// be listened to directly and a WAV file can feed the muxers of this module.
package wav

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-es"
)

// WAVE format tags.
const (
	FormatPCM        = 0x0001
	FormatIEEEFloat  = 0x0003
	FormatALaw       = 0x0006
	FormatMuLaw      = 0x0007
	FormatExtensible = 0xFFFE
)

// Format describes the samples of a WAVE file.
type Format struct {
	// AudioFormat is the format tag. For a WAVE_FORMAT_EXTENSIBLE file it
	// is the tag of the sub format, so callers never see FormatExtensible.
	AudioFormat   uint16
	Channels      uint16
	SampleRate    uint32
	BitsPerSample uint16
}

// FormatOf returns the WAVE format that carries cid: A-law or μ-law at 8
// bits per sample.
func FormatOf(cid codec.CodecID, sampleRate uint32, channels uint16) (Format, error) {
	f := Format{Channels: channels, SampleRate: sampleRate, BitsPerSample: 8}
	switch cid {
	case codec.CODECID_AUDIO_G711A:
		f.AudioFormat = FormatALaw
	case codec.CODECID_AUDIO_G711U:
		f.AudioFormat = FormatMuLaw
	default:
		return f, fmt.Errorf("wav: no wave format for %s", codec.CodecString(cid))
	}
	return f, nil
}

// Codec is the codec of G.711 files and CODECID_UNRECOGNIZED for the rest,
// which no codec id of this module describes.
func (f Format) Codec() codec.CodecID {
	switch f.AudioFormat {
	case FormatALaw:
		return codec.CODECID_AUDIO_G711A
	case FormatMuLaw:
		return codec.CODECID_AUDIO_G711U
	}
	return codec.CODECID_UNRECOGNIZED
}

// BlockAlign is the size of one sample of every channel.
func (f Format) BlockAlign() int {
	return int(f.Channels) * int((f.BitsPerSample+7)/8)
}

// Limits well past any real file, which a corrupt header is held to.
const (
	maxSampleRate = 1 << 22 // 4 MHz; DSD-over-PCM tops out at 1.4 MHz
	maxBits       = 64
	maxFrameBytes = 1 << 20
)

func (f Format) validate() error {
	if f.Channels == 0 || f.SampleRate == 0 || f.BitsPerSample == 0 {
		return errors.New("wav: channels, sample rate and bits per sample must be set")
	}
	if f.SampleRate > maxSampleRate || f.BitsPerSample > maxBits {
		return fmt.Errorf("wav: %d Hz at %d bits is not a real format", f.SampleRate, f.BitsPerSample)
	}
	switch f.AudioFormat {
	case FormatPCM, FormatIEEEFloat, FormatALaw, FormatMuLaw:
		return nil
	}
	return fmt.Errorf("wav: unsupported format tag 0x%04X", f.AudioFormat)
}

// Writer writes a WAVE file. Given an io.WriteSeeker that can seek, Close
// fills in the chunk sizes; otherwise they are left at 0xFFFFFFFF, the value
// streaming writers use for "until the end".
type Writer struct {
	w        io.Writer
	ws       io.WriteSeeker
	start    int64
	format   Format
	dataAt   int64 // offset of the data chunk size
	factAt   int64 // offset of the fact sample count, 0 without one
	written  uint64
	closed   bool
	headerSz int64
}

// NewWriter writes the header and returns a writer for the sample data.
func NewWriter(w io.Writer, format Format) (*Writer, error) {
	if err := format.validate(); err != nil {
		return nil, err
	}
	wr := &Writer{w: w, format: format}
	if ws, ok := w.(io.WriteSeeker); ok {
		if pos, err := ws.Seek(0, io.SeekCurrent); err == nil {
			wr.ws, wr.start = ws, pos
		}
	}
	// non-PCM formats carry the cbSize field and a fact chunk (the WAVE
	// rules since the 1990s; players accept files without, but tools warn)
	pcm := format.AudioFormat == FormatPCM
	fmtSize := uint32(18)
	if pcm {
		fmtSize = 16
	}
	hdr := make([]byte, 0, 58)
	hdr = append(hdr, "RIFF"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, 0xFFFFFFFF)
	hdr = append(hdr, "WAVEfmt "...)
	hdr = binary.LittleEndian.AppendUint32(hdr, fmtSize)
	hdr = binary.LittleEndian.AppendUint16(hdr, format.AudioFormat)
	hdr = binary.LittleEndian.AppendUint16(hdr, format.Channels)
	hdr = binary.LittleEndian.AppendUint32(hdr, format.SampleRate)
	hdr = binary.LittleEndian.AppendUint32(hdr, format.SampleRate*uint32(format.BlockAlign()))
	hdr = binary.LittleEndian.AppendUint16(hdr, uint16(format.BlockAlign()))
	hdr = binary.LittleEndian.AppendUint16(hdr, format.BitsPerSample)
	if !pcm {
		hdr = binary.LittleEndian.AppendUint16(hdr, 0) // cbSize
		hdr = append(hdr, "fact"...)
		hdr = binary.LittleEndian.AppendUint32(hdr, 4)
		wr.factAt = int64(len(hdr))
		hdr = binary.LittleEndian.AppendUint32(hdr, 0xFFFFFFFF)
	}
	hdr = append(hdr, "data"...)
	wr.dataAt = int64(len(hdr))
	hdr = binary.LittleEndian.AppendUint32(hdr, 0xFFFFFFFF)
	wr.headerSz = int64(len(hdr))
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	return wr, nil
}

// Write appends samples. It implements io.Writer.
func (wr *Writer) Write(p []byte) (int, error) {
	if wr.closed {
		return 0, errors.New("wav: write after Close")
	}
	n, err := wr.w.Write(p)
	wr.written += uint64(n)
	return n, err
}

// Close pads the data chunk to an even size and, when the writer can seek,
// fills in the sizes. It does not close the underlying writer.
func (wr *Writer) Close() error {
	if wr.closed {
		return nil
	}
	wr.closed = true
	end := wr.headerSz + int64(wr.written)
	if wr.written%2 == 1 {
		// chunks are word aligned; the pad byte is not part of the data
		if _, err := wr.w.Write([]byte{0}); err != nil {
			return err
		}
		end++
	}
	if wr.ws == nil {
		return nil
	}
	if wr.written > 0xFFFFFFFF-uint64(wr.headerSz) {
		return errors.New("wav: more than 4 GB of samples do not fit a RIFF file")
	}
	patch := func(at int64, v uint32) error {
		if _, err := wr.ws.Seek(wr.start+at, io.SeekStart); err != nil {
			return err
		}
		return binary.Write(wr.ws, binary.LittleEndian, v)
	}
	if err := patch(4, uint32(end-8)); err != nil {
		return err
	}
	if err := patch(wr.dataAt, uint32(wr.written)); err != nil {
		return err
	}
	if wr.factAt > 0 {
		frames := wr.written / uint64(wr.format.BlockAlign())
		if err := patch(wr.factAt, uint32(frames)); err != nil {
			return err
		}
	}
	_, err := wr.ws.Seek(wr.start+end, io.SeekStart)
	return err
}

// extensibleSubFormat is the tail every KSDATAFORMAT_SUBTYPE GUID shares;
// the first two bytes are the format tag.
var extensibleSubFormat = []byte{0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}

// Reader reads the samples of a WAVE file. It is an io.Reader over the
// sample data and an es.Reader that hands the samples out in frames, 20 ms
// by default.
type Reader struct {
	r       io.Reader
	format  Format
	remain  uint64 // bytes of sample data left, when the size is known
	sized   bool
	frameMs uint32
	samples uint64
}

// ReaderOption configures a Reader.
type ReaderOption func(*Reader)

// WithFrameDuration sets how many milliseconds ReadFrame hands out at a
// time.
func WithFrameDuration(ms uint32) ReaderOption {
	return func(r *Reader) {
		if ms > 0 {
			r.frameMs = ms
		}
	}
}

// NewReader reads the header up to the start of the sample data.
func NewReader(r io.Reader, options ...ReaderOption) (*Reader, error) {
	rd := &Reader{r: r, frameMs: 20}
	for _, opt := range options {
		opt(rd)
	}
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return nil, errors.New("wav: file too short for a RIFF header")
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return nil, errors.New("wav: not a RIFF/WAVE file")
	}
	haveFmt := false
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, errors.New("wav: no data chunk")
		}
		id := string(hdr[0:4])
		size := binary.LittleEndian.Uint32(hdr[4:])
		switch id {
		case "fmt ":
			if size < 16 || size > 1024 {
				return nil, fmt.Errorf("wav: fmt chunk of %d bytes", size)
			}
			buf := make([]byte, size+size%2)
			if _, err := io.ReadFull(r, buf); err != nil {
				return nil, errors.New("wav: truncated fmt chunk")
			}
			if err := rd.parseFmt(buf[:size]); err != nil {
				return nil, err
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return nil, errors.New("wav: data chunk before the fmt chunk")
			}
			// 0 and 0xFFFFFFFF are what streaming writers leave behind
			rd.sized = size != 0 && size != 0xFFFFFFFF
			rd.remain = uint64(size)
			return rd, nil
		default:
			// LIST, fact, bext, ...: word aligned, skipped
			if _, err := io.CopyN(io.Discard, r, int64(size)+int64(size%2)); err != nil {
				return nil, fmt.Errorf("wav: truncated %q chunk", id)
			}
		}
	}
}

func (rd *Reader) parseFmt(buf []byte) error {
	f := Format{
		AudioFormat:   binary.LittleEndian.Uint16(buf[0:]),
		Channels:      binary.LittleEndian.Uint16(buf[2:]),
		SampleRate:    binary.LittleEndian.Uint32(buf[4:]),
		BitsPerSample: binary.LittleEndian.Uint16(buf[14:]),
	}
	if f.AudioFormat == FormatExtensible {
		// cbSize 22: valid bits, channel mask, then the sub format GUID
		if len(buf) < 40 || string(buf[26:40]) != string(extensibleSubFormat) {
			return errors.New("wav: unsupported WAVE_FORMAT_EXTENSIBLE sub format")
		}
		f.AudioFormat = binary.LittleEndian.Uint16(buf[24:])
	}
	if err := f.validate(); err != nil {
		return err
	}
	rd.format = f
	return nil
}

// Format describes the samples.
func (rd *Reader) Format() Format {
	return rd.format
}

// Duration is the length of the file in milliseconds, or 0 when the header
// does not say (a file written as a stream).
func (rd *Reader) Duration() uint64 {
	if !rd.sized {
		return 0
	}
	frames := rd.remain / uint64(rd.format.BlockAlign())
	return (rd.samples + frames) * 1000 / uint64(rd.format.SampleRate)
}

// Read reads sample data. It implements io.Reader.
func (rd *Reader) Read(p []byte) (int, error) {
	if rd.sized {
		if rd.remain == 0 {
			return 0, io.EOF
		}
		if uint64(len(p)) > rd.remain {
			p = p[:rd.remain]
		}
	}
	n, err := rd.r.Read(p)
	if rd.sized {
		rd.remain -= uint64(n)
		if err == io.EOF && rd.remain > 0 {
			// a file cut short: what is there is still audio
			rd.remain = 0
		}
	}
	return n, err
}

// Codec is the codec of the samples, see Format.Codec.
func (rd *Reader) Codec() codec.CodecID {
	return rd.format.Codec()
}

// ReadFrame hands out the next frame of samples, making the Reader an
// es.Reader that feeds a muxer directly. Pts and Dts count the samples
// read so far.
func (rd *Reader) ReadFrame() (*es.Frame, error) {
	align := rd.format.BlockAlign()
	size := uint64(rd.format.SampleRate) * uint64(rd.frameMs) / 1000 * uint64(align)
	// a long frame of a high rate format is handed out in pieces
	size = min(size, maxFrameBytes/uint64(align)*uint64(align))
	buf := make([]byte, max(size, uint64(align)))
	n, err := io.ReadFull(rd, buf)
	n -= n % align
	if n == 0 {
		if err == nil || err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		return nil, err
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	ts := rd.samples * 1000 / uint64(rd.format.SampleRate)
	rd.samples += uint64(n / align)
	return &es.Frame{Cid: rd.Codec(), Data: buf[:n], Pts: ts, Dts: ts, KeyFrame: true}, nil
}
