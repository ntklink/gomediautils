package mediatest

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Clip describes a source file for ffmpeg to synthesise. The defaults produce
// a short clip that is quick to encode but still exercises the interesting
// paths: several gops, b-frames (so composition offsets are non trivial) and
// a second stream to interleave.
type Clip struct {
	Container string // mp4, flv, mpegts, ...
	Video     string // encoder name, "" for no video track
	Audio     string // encoder name, "" for no audio track
	Seconds   float64
	Width     int
	Height    int
	FPS       int
	GOP       int
	BFrames   int
	Extra     []string // extra output flags, e.g. -bsf or muxer options
}

func (c Clip) withDefaults() Clip {
	if c.Seconds == 0 {
		c.Seconds = 2
	}
	if c.Width == 0 {
		c.Width = 320
	}
	if c.Height == 0 {
		c.Height = 240
	}
	if c.FPS == 0 {
		c.FPS = 25
	}
	if c.GOP == 0 {
		c.GOP = 25
	}
	return c
}

// name builds a stable file name so a clip generated once can be reused.
//
// Every field that changes the bytes has to appear, Extra included: two
// clips that differ only in an extra flag would otherwise share a cache
// entry and the second test would silently be handed the first one's file.
func (c Clip) name() string {
	name := fmt.Sprintf("src_%s_%s_%s_%gs_%dx%d_%dfps_g%d_b%d",
		c.Container, orNone(c.Video), orNone(c.Audio), c.Seconds,
		c.Width, c.Height, c.FPS, c.GOP, c.BFrames)
	if len(c.Extra) > 0 {
		sum := sha1.Sum([]byte(strings.Join(c.Extra, "\x00")))
		name += "_x" + hex.EncodeToString(sum[:4])
	}
	return name + "." + extensionFor(c.Container)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func extensionFor(container string) string {
	switch container {
	case "mpegts":
		return "ts"
	case "mov":
		return "mov"
	default:
		return container
	}
}

// MakeClip encodes a source file with ffmpeg and returns its path. Clips are
// cached in a directory shared by the whole test binary, so a suite that
// needs the same input many times only pays for it once.
func (tools Tools) MakeClip(t *testing.T, c Clip) string {
	t.Helper()
	c = c.withDefaults()
	path := filepath.Join(cacheDir(t), c.name())
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return path
	}

	args := FFmpegArgs()
	if c.Video != "" {
		// testsrc2 is deterministic and has enough detail that a dropped or
		// reordered frame shows up in the decoded checksum
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("testsrc2=size=%dx%d:rate=%d:duration=%g", c.Width, c.Height, c.FPS, c.Seconds))
	}
	if c.Audio != "" {
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("sine=frequency=440:sample_rate=48000:duration=%g", c.Seconds))
	}
	if c.Video != "" {
		args = append(args,
			"-c:v", c.Video,
			"-pix_fmt", "yuv420p",
			"-g", strconv.Itoa(c.GOP),
			"-bf", strconv.Itoa(c.BFrames),
		)
		switch c.Video {
		case "libx264", "libx265":
			// keep the encode fast and, more importantly, reproducible
			args = append(args, "-preset", "ultrafast", "-crf", "30")
		}
		if c.Video == "libx265" {
			args = append(args, "-x265-params", "log-level=none")
		}
	}
	if c.Audio != "" {
		args = append(args, "-c:a", c.Audio)
		switch c.Audio {
		case "pcm_alaw", "pcm_mulaw":
			args = append(args, "-ar", "8000", "-ac", "1")
		case "libmp3lame":
			args = append(args, "-ar", "44100", "-ac", "2")
		case "libopus":
			args = append(args, "-ar", "48000", "-ac", "2")
		}
	}
	args = append(args, c.Extra...)
	args = append(args, "-f", c.Container, path)

	tmp := tempName(path)
	args[len(args)-1] = tmp
	tools.run(t, tools.FFmpeg, args...)
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("cache source clip: %v", err)
	}
	return path
}

// MakeElementaryStream produces a bare elementary stream (Annex-B h264/h265,
// ADTS aac, ...), the input shape the mux examples take.
func (tools Tools) MakeElementaryStream(t *testing.T, c Clip, format string) string {
	t.Helper()
	c = c.withDefaults()
	c.Container = format
	path := filepath.Join(cacheDir(t), "es_"+c.name())
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return path
	}
	args := FFmpegArgs()
	if c.Video != "" {
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("testsrc2=size=%dx%d:rate=%d:duration=%g", c.Width, c.Height, c.FPS, c.Seconds),
			"-c:v", c.Video, "-pix_fmt", "yuv420p",
			"-g", strconv.Itoa(c.GOP), "-bf", strconv.Itoa(c.BFrames),
			"-preset", "ultrafast", "-crf", "30")
	} else {
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("sine=frequency=440:sample_rate=48000:duration=%g", c.Seconds),
			"-c:a", c.Audio)
	}
	args = append(args, c.Extra...)
	if format == "mp3" {
		// a bare elementary stream, with no id3 tag and no xing header
		// frame: the muxers under test treat every frame as audio, and a
		// header frame would show up as one extra frame of silence
		args = append(args, "-write_xing", "0", "-id3v2_version", "0")
	}
	muxer, ok := elementaryFormat[format]
	if !ok {
		muxer = format
	}
	tmp := tempName(path)
	args = append(args, "-f", muxer, tmp)
	tools.run(t, tools.FFmpeg, args...)
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("cache elementary stream: %v", err)
	}
	return path
}

var tempCounter atomic.Uint64

// tempName is where a clip is encoded before it is moved into place.
//
// `go test ./...` runs one binary per package at the same time, and they
// share the cache directory, so two of them can be encoding the same clip at
// the same moment. Giving each its own scratch name keeps them from renaming
// each other's file out from under themselves; the rename itself is atomic,
// so whichever finishes last simply wins with identical bytes.
func tempName(path string) string {
	return fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), tempCounter.Add(1))
}

var cacheOnce sync.Once

// cacheDir is a stable directory outside the repository. Source clips are a
// pure function of the Clip that names them, so caching them across runs
// turns a second `go test` from seconds of encoding into no encoding at all.
func cacheDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "gomediautils-mediatest-cache")
	cacheOnce.Do(func() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			panic(err)
		}
	})
	return dir
}

// elementaryFormat maps a file extension onto the ffmpeg muxer that writes
// that bare stream.
var elementaryFormat = map[string]string{
	"h264": "h264",
	"h265": "hevc",
	"hevc": "hevc",
	"aac":  "adts",
	"mp3":  "mp3",
	"opus": "opus",
}

// ExtractStream copies one stream out of a container into a bare elementary
// stream file, without re-encoding. This is the reference the demux examples
// are measured against: whatever ffmpeg pulls out is what GoMediaUtils has to
// pull out too.
//
// mapSpec is an ffmpeg -map selector such as "0:v:0"; ext names both the
// file extension and, through elementaryFormat, the muxer to use.
func (tools Tools) ExtractStream(t *testing.T, src, mapSpec, ext string) string {
	t.Helper()
	format, ok := elementaryFormat[ext]
	if !ok {
		t.Fatalf("no elementary stream muxer known for %q", ext)
	}
	out := filepath.Join(t.TempDir(), "reference."+ext)
	args := FFmpegArgs("-i", src, "-map", mapSpec, "-c", "copy")
	if format == "mp3" {
		// ffmpeg's mp3 muxer prepends an id3 tag and a xing/lame header
		// frame by default. That header carries an encoder delay, so the
		// reference would decode to fewer samples than the identical frames
		// a demuxer hands over. A bare stream is what we want to compare.
		args = append(args, "-write_xing", "0", "-id3v2_version", "0")
	}
	args = append(args, "-f", format, out)
	tools.run(t, tools.FFmpeg, args...)
	return out
}
