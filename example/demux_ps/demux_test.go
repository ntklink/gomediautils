package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
	"github.com/ntklink/gomediautils/go-mpeg2"
)

// ffmpeg has no program stream muxer that gomedia's demuxer would recognise
// (its vob/mpeg muxer only carries mpeg1/mpeg2 video), so the ps file under
// test is written by gomedia's own ps muxer from an ffmpeg encoded ts. What
// the test then checks is the round trip: the elementary streams that come
// back out have to decode to the same pictures ffmpeg put in.
func TestDemuxPS(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mpegts", Video: "libx264", Audio: "aac", Seconds: 2, BFrames: 2,
	})

	dir := t.TempDir()
	psPath := filepath.Join(dir, "stream.ps")
	if err := muxTSToPS(t, src, psPath); err != nil {
		t.Fatalf("build the ps input: %v", err)
	}

	files, err := DemuxPS(psPath, dir)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}

	videoPath, ok := files["h264"]
	if !ok {
		t.Fatalf("no h264 stream came out, got %v", files)
	}
	audioPath, ok := files["aac"]
	if !ok {
		t.Fatalf("no aac stream came out, got %v", files)
	}
	for _, p := range []string{videoPath, audioPath} {
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Fatalf("%s is empty", p)
		}
	}

	tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:v:0", "h264"), videoPath, "v:0")
	tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:a:0", "aac"), audioPath, "a:0")

	in := tools.Probe(t, src)
	srcVideo, _ := in.Video()
	gotVideo, _ := tools.Probe(t, videoPath).Video()
	if gotVideo.Frames() != srcVideo.Frames() {
		t.Errorf("%d video frames demuxed, want %d", gotVideo.Frames(), srcVideo.Frames())
	}
}

// muxTSToPS is the mux_ps example inlined, so this package stays independent
// of it while still starting from a file ffmpeg produced.
func muxTSToPS(t *testing.T, tsPath, psPath string) error {
	t.Helper()
	tsFile, err := os.Open(tsPath)
	if err != nil {
		return err
	}
	defer tsFile.Close()
	psFile, err := os.Create(psPath)
	if err != nil {
		return err
	}
	defer psFile.Close()

	var writeErr error
	muxer := mpeg2.NewPsMuxer()
	muxer.OnPacket = func(pkg []byte) {
		if writeErr == nil {
			_, writeErr = psFile.Write(pkg)
		}
	}
	vid := muxer.AddStream(mpeg2.PS_STREAM_H264)
	aid := muxer.AddStream(mpeg2.PS_STREAM_AAC)

	demuxer := mpeg2.NewTSDemuxer()
	demuxer.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts, dts uint64) {
		if writeErr != nil {
			return
		}
		switch cid {
		case mpeg2.TS_STREAM_H264:
			writeErr = muxer.Write(vid, frame, pts, dts)
		case mpeg2.TS_STREAM_AAC:
			writeErr = muxer.Write(aid, frame, pts, dts)
		}
	}
	if err := demuxer.Input(tsFile); err != nil {
		return err
	}
	return writeErr
}
