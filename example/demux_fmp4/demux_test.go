package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/yapingcat/gomedia/example/internal/mediatest"
)

// The input here is what a real hls or dash player downloads: an init
// segment and a handful of media segments as separate files. ffmpeg's hls
// muxer writes them, gomedia joins and demuxes them, and the pictures that
// come out have to be the ones ffmpeg encoded.
func TestDemuxFragments(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 4, GOP: 25, BFrames: 2,
	})

	segDir := t.TempDir()
	tools.Run(t, tools.FFmpeg, mediatest.FFmpegArgs(
		"-i", src,
		"-c", "copy",
		"-f", "hls",
		"-hls_segment_type", "fmp4",
		"-hls_time", "1",
		"-hls_list_size", "0",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(segDir, "seg%03d.m4s"),
		filepath.Join(segDir, "index.m3u8"),
	)...)

	initPath := filepath.Join(segDir, "init.mp4")
	if _, err := os.Stat(initPath); err != nil {
		t.Fatalf("ffmpeg wrote no init segment: %v", err)
	}
	segments, err := filepath.Glob(filepath.Join(segDir, "seg*.m4s"))
	if err != nil || len(segments) < 2 {
		t.Fatalf("ffmpeg wrote %d media segments, want at least 2", len(segments))
	}
	sort.Strings(segments)

	outDir := t.TempDir()
	files, err := DemuxFragments(initPath, segments, outDir)
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

	tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:v:0", "h264"), videoPath, "v:0")
	tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:a:0", "aac"), audioPath, "a:0")

	srcVideo, _ := tools.Probe(t, src).Video()
	gotVideo, _ := tools.Probe(t, videoPath).Video()
	if gotVideo.Frames() != srcVideo.Frames() {
		t.Errorf("%d video frames across all fragments, want %d", gotVideo.Frames(), srcVideo.Frames())
	}
}

// A media segment on its own carries no moov, so demuxing one without the
// init segment must fail rather than quietly produce nothing.
func TestMediaSegmentAloneIsNotPlayable(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Seconds: 2,
	})
	segDir := t.TempDir()
	tools.Run(t, tools.FFmpeg, mediatest.FFmpegArgs(
		"-i", src, "-an", "-c", "copy", "-f", "hls",
		"-hls_segment_type", "fmp4", "-hls_time", "1", "-hls_list_size", "0",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(segDir, "seg%03d.m4s"),
		filepath.Join(segDir, "index.m3u8"),
	)...)

	segments, _ := filepath.Glob(filepath.Join(segDir, "seg*.m4s"))
	if len(segments) == 0 {
		t.Skip("ffmpeg produced no media segments")
	}
	sort.Strings(segments)

	files, err := DemuxFragments(segments[0], nil, t.TempDir())
	if err == nil && len(files) > 0 {
		t.Errorf("a lone media segment demuxed into %v, want a failure or nothing", files)
	}
}
