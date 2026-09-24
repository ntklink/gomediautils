package es

import (
	"errors"
	"fmt"
	"io"

	"github.com/ntklink/gomediautils/go-codec"
)

// Writer writes frames out as a bare elementary stream. Frames already in
// that form, which is what the demuxers of this module hand out, are written
// as they are. The writer fills in what a stream needs and a container frame
// may lack:
//
//   - H.264 and H.265 given as length prefixed nal units (the avcC/hvcC form
//     of mp4 and Matroska) are rewritten into Annex-B, and the parameter
//     sets from the extra data are written in front of every key frame that
//     does not carry its own, so the stream can be decoded from any of them.
//   - AAC given as raw frames gets an ADTS header built from the
//     AudioSpecificConfig.
type Writer struct {
	w         io.Writer
	cid       codec.CodecID
	nalLen    int
	paramSets []byte
	asc       []byte
	// lengthFirst is set when an avcC/hvcC record was given: frames are then
	// expected in its length prefixed form. A 4 byte length of 256 to 511
	// begins 00 00 01 like a start code, so the form expected is tried first.
	lengthFirst bool
}

// NewWriter creates a writer for cid. extraData is optional: the avcC or
// hvcC record for H.264 and H.265, the AudioSpecificConfig for raw AAC.
func NewWriter(w io.Writer, cid codec.CodecID, extraData []byte) (*Writer, error) {
	wr := &Writer{w: w, cid: cid, nalLen: 4}
	switch cid {
	case codec.CODECID_VIDEO_H264:
		if len(extraData) > 0 {
			spss, ppss, err := codec.CovertExtradata(extraData)
			if err != nil {
				return nil, err
			}
			for _, n := range append(spss, ppss...) {
				wr.paramSets = append(wr.paramSets, n...)
			}
			wr.nalLen = int(extraData[4]&0x03) + 1
			wr.lengthFirst = true
		}
	case codec.CODECID_VIDEO_H265:
		if len(extraData) > 0 {
			hvcc := codec.NewHEVCRecordConfiguration()
			if err := hvcc.Decode(extraData); err != nil {
				return nil, err
			}
			wr.paramSets = hvcc.ToNalus()
			if len(extraData) > 21 {
				wr.nalLen = int(extraData[21]&0x03) + 1
			}
			wr.lengthFirst = true
		}
	case codec.CODECID_AUDIO_AAC:
		wr.asc = append([]byte(nil), extraData...)
	case codec.CODECID_AUDIO_MP3, codec.CODECID_AUDIO_G711A, codec.CODECID_AUDIO_G711U:
	default:
		return nil, fmt.Errorf("es: no elementary stream format for %s", codec.CodecString(cid))
	}
	return wr, nil
}

// WriteFrame writes one access unit or audio frame.
func (wr *Writer) WriteFrame(frame []byte) error {
	if len(frame) == 0 {
		return nil
	}
	switch wr.cid {
	case codec.CODECID_VIDEO_H264, codec.CODECID_VIDEO_H265:
		out, err := wr.annexB(frame)
		if err != nil {
			return err
		}
		return wr.write(out)
	case codec.CODECID_AUDIO_AAC:
		if frame[0] == 0xFF && len(frame) > 1 && frame[1]&0xF6 == 0xF0 {
			return wr.write(frame)
		}
		if len(wr.asc) < 2 {
			return errors.New("es: raw aac needs its AudioSpecificConfig to become adts")
		}
		hdr, err := codec.ConvertASCToADTS(wr.asc, len(frame)+7)
		if err != nil {
			return err
		}
		return wr.write(append(hdr.Encode(), frame...))
	default:
		return wr.write(frame)
	}
}

func (wr *Writer) write(b []byte) error {
	_, err := wr.w.Write(b)
	return err
}

func (wr *Writer) annexB(frame []byte) ([]byte, error) {
	h265 := wr.cid == codec.CODECID_VIDEO_H265
	nalus, ok := wr.lengthPrefixed(frame)
	startCode, _ := codec.FindStartCode(frame, 0)
	if startCode == 0 && (!ok || !wr.lengthFirst) {
		nalus = nalus[:0]
		codec.SplitFrame(frame, func(nalu []byte) bool {
			nalus = append(nalus, nalu)
			return true
		})
	} else if !ok {
		return nil, errors.New("es: frame is neither Annex-B nor length prefixed")
	}
	key, hasSPS := false, false
	for _, nalu := range nalus {
		if h265 {
			t := codec.H265NaluTypeWithoutStartCode(nalu)
			key = key || (t >= codec.H265_NAL_SLICE_BLA_W_LP && t <= codec.H265_NAL_SLICE_CRA)
			hasSPS = hasSPS || t == codec.H265_NAL_SPS
		} else {
			t := codec.H264NaluTypeWithoutStartCode(nalu)
			key = key || t == codec.H264_NAL_I_SLICE
			hasSPS = hasSPS || t == codec.H264_NAL_SPS
		}
	}
	out := make([]byte, 0, len(frame)+len(wr.paramSets)+4*len(nalus))
	if key && !hasSPS {
		out = append(out, wr.paramSets...)
	}
	for _, nalu := range nalus {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nalu...)
	}
	return out, nil
}

// lengthPrefixed splits a frame of length prefixed nal units, reporting
// false unless the lengths account for every byte exactly.
func (wr *Writer) lengthPrefixed(frame []byte) ([][]byte, bool) {
	var nalus [][]byte
	for rest := frame; len(rest) > 0; {
		if len(rest) < wr.nalLen {
			return nil, false
		}
		var n uint64
		for i := 0; i < wr.nalLen; i++ {
			n = n<<8 | uint64(rest[i])
		}
		rest = rest[wr.nalLen:]
		if n == 0 || n > uint64(len(rest)) || rest[0]&0x80 != 0 {
			return nil, false
		}
		nalus = append(nalus, rest[:n])
		rest = rest[n:]
	}
	return nalus, len(nalus) > 0
}
