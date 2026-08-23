package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yapingcat/gomedia/example/internal/mediatest"
)

// The dates are patched in place, so two things have to hold: ffprobe reads
// back the date that was written, and the file is otherwise untouched.
// Nothing about a creation time should change what the file decodes to, and
// a patch that moved a byte would break exactly that.
func TestSetMP4Time(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 2, BFrames: 2,
	})

	want := time.Date(2021, time.June, 5, 12, 34, 56, 0, time.UTC)
	dst := filepath.Join(t.TempDir(), "edited.mp4")
	copyFile(t, src, dst)

	before := fileSize(t, dst)
	if err := SetMP4Time(dst, want); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if after := fileSize(t, dst); after != before {
		t.Errorf("the file grew from %d to %d bytes; the edit is meant to be in place", before, after)
	}

	got := creationTime(t, tools, dst)
	if !got.Equal(want) {
		t.Errorf("ffprobe reads back %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	tools.AssertDecodable(t, dst)
	tools.AssertSameDecoded(t, src, dst, "v:0")
	tools.AssertSameDecoded(t, src, dst, "a:0")

	// every track header carries its own date, not only the movie header
	for _, stream := range tools.Probe(t, dst).Streams {
		if stream.Tags.CreationTime == "" {
			t.Errorf("stream %d has no creation time; only the mvhd was patched", stream.Index)
			continue
		}
		ts, err := time.Parse(time.RFC3339, stream.Tags.CreationTime)
		if err != nil {
			t.Errorf("stream %d creation time %q does not parse: %v", stream.Index, stream.Tags.CreationTime, err)
			continue
		}
		if !ts.UTC().Equal(want) {
			t.Errorf("stream %d says %s, want %s", stream.Index, ts.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}
}

// A version 1 movie header stores 64 bit dates. ffmpeg writes those when
// asked, and the same code has to patch them at the right width.
func TestSetMP4TimeVersion1Headers(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Seconds: 2,
		Extra: []string{"-movflags", "+use_metadata_tags", "-video_track_timescale", "90000"},
	})
	dst := filepath.Join(t.TempDir(), "edited.mp4")
	copyFile(t, src, dst)

	want := time.Date(2019, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := SetMP4Time(dst, want); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := creationTime(t, tools, dst); !got.Equal(want) {
		t.Errorf("ffprobe reads back %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	tools.AssertSameDecoded(t, src, dst, "v:0")
}

// A file that is not an mp4 has to be reported, not silently accepted.
func TestSetMP4TimeRejectsNonMP4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.mp4")
	if err := os.WriteFile(path, []byte("this is not a container at all, not even close"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := SetMP4Time(path, time.Now()); err == nil {
		t.Error("editing a file with no boxes was accepted")
	}
}

func creationTime(t *testing.T, tools mediatest.Tools, path string) time.Time {
	t.Helper()
	probe := tools.Probe(t, path)
	if probe.Format.Tags.CreationTime == "" {
		t.Fatalf("ffprobe reports no creation time for %s", path)
	}
	ts, err := time.Parse(time.RFC3339, probe.Format.Tags.CreationTime)
	if err != nil {
		t.Fatalf("creation time %q does not parse: %v", probe.Format.Tags.CreationTime, err)
	}
	return ts.UTC()
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

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}
