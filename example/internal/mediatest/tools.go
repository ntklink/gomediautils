// Package mediatest runs the examples against real media produced and
// inspected by ffmpeg.
//
// A unit test can only show that gomedia agrees with itself: mux something,
// demux it again, compare. That misses everything about whether the bytes
// gomedia writes are a file the rest of the world can read, which is the
// whole point of a container library. These helpers close that gap by making
// ffmpeg the source of the input and ffprobe/ffmpeg the judge of the output.
//
// ffmpeg is not a build dependency. Tests that need it call Require, which
// skips when no usable binary is found. Set GOMEDIA_FFMPEG and
// GOMEDIA_FFPROBE to point at specific binaries, otherwise PATH is used.
package mediatest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tools holds the paths of the ffmpeg binaries a test may use.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

var (
	lookupOnce sync.Once
	found      Tools
	lookupErr  string
)

func lookup() (Tools, string) {
	lookupOnce.Do(func() {
		resolve := func(env, name string) (string, bool) {
			if p := os.Getenv(env); p != "" {
				if _, err := os.Stat(p); err == nil {
					return p, true
				}
				return p, false
			}
			p, err := exec.LookPath(name)
			return p, err == nil
		}
		ffmpeg, okA := resolve("GOMEDIA_FFMPEG", "ffmpeg")
		ffprobe, okB := resolve("GOMEDIA_FFPROBE", "ffprobe")
		if !okA || !okB {
			lookupErr = "ffmpeg and ffprobe not found; install them or set GOMEDIA_FFMPEG / GOMEDIA_FFPROBE"
			return
		}
		found = Tools{FFmpeg: ffmpeg, FFprobe: ffprobe}
	})
	return found, lookupErr
}

// Require returns the ffmpeg tools, skipping the test when they are not
// installed. Everything in this package is meant to be optional: a checkout
// without ffmpeg still builds and its unit tests still run.
func Require(t *testing.T) Tools {
	t.Helper()
	tools, err := lookup()
	if err != "" {
		t.Skip(err)
	}
	return tools
}

// commandTimeout bounds every ffmpeg invocation. A test that hangs on a
// network example is far more confusing than one that fails.
const commandTimeout = 90 * time.Second

// Run executes one of the tools and returns its stdout, failing the test if
// the command does. Tests reach for it when they need an ffmpeg invocation
// this package has no helper for, such as building an unusual input.
func (tools Tools) Run(t *testing.T, bin string, args ...string) []byte {
	t.Helper()
	return tools.run(t, bin, args...)
}

// run executes one of the tools and returns its stdout. ffmpeg writes its
// progress to stderr, which is only reported when the command fails.
func (tools Tools) run(t *testing.T, bin string, args ...string) []byte {
	t.Helper()
	out, errOut, err := tools.runErr(bin, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(bin), strings.Join(args, " "), err, errOut)
	}
	return out
}

func (tools Tools) runErr(bin string, args ...string) (stdout []byte, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.Bytes(), errBuf.String(), err
}

// FFmpegArgs are the flags every invocation gets: no banner, no progress
// noise, and never block on a prompt.
func FFmpegArgs(args ...string) []string {
	return append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, args...)
}
