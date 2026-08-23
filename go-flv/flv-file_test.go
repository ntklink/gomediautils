package flv

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

// openSample opens a sample media file that is not part of the repository.
// The test is skipped when the file is absent so that `go test ./...` works on
// a clean checkout.
func openSample(t *testing.T, name string) *os.File {
	t.Helper()
	fd, err := os.Open(name)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("sample file %s not present", name)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { fd.Close() })
	return fd
}

// createOutput creates a scratch output file inside the test's temp dir.
func createOutput(t *testing.T, name string) *os.File {
	t.Helper()
	fd, err := os.Create(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fd.Close() })
	return fd
}

func TestFlvReader_Input(t *testing.T) {
	t.Run("test_1", func(t *testing.T) {
		fd := openSample(t, "source.200kbps.768x320.flv")
		videoFile := createOutput(t, "v.h264")
		audioFile := createOutput(t, "a.aac")
		f := CreateFlvReader()
		f.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
			if cid == codec.CODECID_VIDEO_H264 {
				videoFile.Write(frame)
			} else if cid == codec.CODECID_AUDIO_AAC {
				audioFile.Write(frame)
			}
		}
		cache := make([]byte, 4096)
		for {
			n, err := fd.Read(cache)
			if err != nil {
				break
			}
			if err := f.Input(cache[0:n]); err != nil {
				t.Fatalf("FlvReader.Input() error = %v", err)
			}
		}
	})
}

func TestFlvWriter_Write(t *testing.T) {
	t.Run("test_2", func(t *testing.T) {
		fd := openSample(t, "source.200kbps.768x320.flv")
		newflv := createOutput(t, "new.flv")
		wf := CreateFlvWriter(newflv)
		if err := wf.WriteFlvHeader(); err != nil {
			t.Fatal(err)
		}
		rf := CreateFlvReader()
		rf.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
			if cid == codec.CODECID_VIDEO_H264 {
				if err := wf.WriteH264(frame, pts, dts); err != nil {
					t.Errorf("WriteH264() error = %v", err)
				}
			} else if cid == codec.CODECID_AUDIO_AAC {
				if err := wf.WriteAAC(frame, pts, dts); err != nil {
					t.Errorf("WriteAAC() error = %v", err)
				}
			}
		}
		content, err := ioutil.ReadAll(fd)
		if err != nil {
			t.Fatal(err)
		}
		if err := rf.Input(content); err != nil {
			t.Errorf("FlvReader.Input() error = %v", err)
		}
	})
}

func TestFlvWriter_WriteHevc(t *testing.T) {
	t.Run("test_3", func(t *testing.T) {
		rawh265 := openSample(t, "1.h265")
		newflv := createOutput(t, "h265.flv")
		wf := CreateFlvWriter(newflv)
		if err := wf.WriteFlvHeader(); err != nil {
			t.Fatal(err)
		}
		var pts uint32 = 0
		var dts uint32 = 0
		buf, err := ioutil.ReadAll(rawh265)
		if err != nil {
			t.Fatal(err)
		}
		codec.SplitFrameWithStartCode(buf, func(nalu []byte) bool {
			if err := wf.WriteH265(nalu, pts, dts); err != nil {
				t.Errorf("WriteH265() error = %v", err)
			}
			pts += 40
			dts += 40
			return true
		})
	})
}

func TestFlvReadH265(t *testing.T) {
	t.Run("test_4", func(t *testing.T) {
		fd := openSample(t, "l.flv")
		videoFile := createOutput(t, "v2.h265")
		f := CreateFlvReader()
		f.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
			if cid == codec.CODECID_VIDEO_H265 {
				videoFile.Write(frame)
			}
		}
		content, err := ioutil.ReadAll(fd)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Input(content); err != nil {
			t.Errorf("FlvReader.Input() error = %v", err)
		}
	})
}
