package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
	"github.com/ntklink/gomediautils/go-codec"
	"github.com/ntklink/gomediautils/go-mpeg2"
)

// ffmpeg has no program stream demuxer that reads what gomedia writes (its
// mpeg demuxer only handles mpeg1/mpeg2 video), so the check goes the other
// way: gomedia's own ps demuxer takes the file apart again and the
// elementary stream that comes out is handed to ffmpeg, which decodes it and
// has to see the same pictures that went in.
func TestMuxElementaryStreamToPS(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name string
		clip mediatest.Clip
		ext  string
	}{
		{"h264", mediatest.Clip{Video: "libx264", Seconds: 2, GOP: 12}, "h264"},
		{"h265", mediatest.Clip{Video: "libx265", Seconds: 2, GOP: 12}, "h265"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeElementaryStream(t, tc.clip, tc.ext)
			// the example picks the codec from the file name
			named := filepath.Join(t.TempDir(), "input."+tc.ext)
			copyFile(t, src, named)
			psPath := filepath.Join(t.TempDir(), "out.ps")

			if err := MuxElementaryStreamToPS(named, psPath); err != nil {
				t.Fatalf("mux: %v", err)
			}
			if st, err := os.Stat(psPath); err != nil || st.Size() == 0 {
				t.Fatalf("the ps file is empty")
			}

			back := filepath.Join(t.TempDir(), "back."+tc.ext)
			frames := demuxPS(t, psPath, back, tc.ext == "h265")

			want, _ := tools.Probe(t, src).Video()
			if frames != want.Frames() {
				t.Errorf("%d access units survived the round trip, want %d", frames, want.Frames())
			}
			tools.AssertSameDecoded(t, src, back, "v:0")
		})
	}
}

// The muxer has to reject a stream id it never handed out rather than write
// a packet for it.
func TestWriteRejectsUnknownStream(t *testing.T) {
	muxer := mpeg2.NewPsMuxer()
	muxer.OnPacket = func([]byte) {}
	sid := muxer.AddStream(mpeg2.PS_STREAM_H264)

	err := muxer.Write(sid+1, []byte{0, 0, 0, 1, 0x65, 0x88}, 0, 0)
	if !errors.Is(err, mpeg2.ErrStreamIdNotFound) {
		t.Errorf("writing to an unknown stream id gave %v, want ErrStreamIdNotFound", err)
	}
}

func demuxPS(t *testing.T, psPath, esPath string, hevc bool) int {
	t.Helper()
	buf, err := os.ReadFile(psPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(esPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	frames := 0
	demuxer := mpeg2.NewPSDemuxer()
	demuxer.OnFrame = func(frame []byte, cid mpeg2.PS_STREAM_TYPE, pts, dts uint64) {
		if hevc {
			if codec.H265NaluType(frame) == codec.H265_NAL_AUD {
				return
			}
			if codec.IsH265VCLNaluType(codec.H265NaluType(frame)) {
				frames++
			}
		} else {
			if codec.H264NaluType(frame) == codec.H264_NAL_AUD {
				return
			}
			if codec.IsH264VCLNaluType(codec.H264NaluType(frame)) {
				frames++
			}
		}
		if _, err := out.Write(frame); err != nil {
			t.Error(err)
		}
	}
	if err := demuxer.Input(buf); err != nil && !errors.Is(err, mpeg2.ErrNeedMore) {
		t.Fatalf("demux the ps back: %v", err)
	}
	demuxer.Flush()
	return frames
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o666); err != nil {
		t.Fatal(err)
	}
}
