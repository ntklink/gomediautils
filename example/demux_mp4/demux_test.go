package main

import (
	"os"
	"testing"

	"github.com/yapingcat/gomedia/example/internal/mediatest"
	"github.com/yapingcat/gomedia/go-mp4"
)

// mp4 is the demux the b-frame cases matter for: samples are stored in decode
// order with a separate composition offset table, so a demuxer that reads
// ctts wrong hands the decoder pictures in the wrong order. Comparing decoded
// samples against ffmpeg's own extraction is what catches that; a frame count
// on its own would not.
func TestDemuxMP4(t *testing.T) {
	tools := mediatest.Require(t)

	cases := []struct {
		name     string
		clip     mediatest.Clip
		videoExt string
		audioExt string
	}{
		{
			name: "h264 and aac with b frames",
			clip: mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "aac",
				Seconds: 2, BFrames: 3},
			videoExt: "h264", audioExt: "aac",
		},
		{
			name: "h265 and mp3",
			clip: mediatest.Clip{Container: "mp4", Video: "libx265", Audio: "libmp3lame",
				Seconds: 2},
			videoExt: "h265", audioExt: "mp3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tools.MakeClip(t, tc.clip)
			outDir := t.TempDir()

			files, infos, err := DemuxMP4File(src, outDir)
			if err != nil {
				t.Fatalf("demux: %v", err)
			}
			if len(infos) != 2 {
				t.Fatalf("%d tracks in the header, want 2", len(infos))
			}

			videoPath, ok := files[tc.videoExt]
			if !ok {
				t.Fatalf("no %s stream came out, got %v", tc.videoExt, files)
			}
			audioPath, ok := files[tc.audioExt]
			if !ok {
				t.Fatalf("no %s stream came out, got %v", tc.audioExt, files)
			}
			for _, p := range []string{videoPath, audioPath} {
				if st, err := os.Stat(p); err != nil || st.Size() == 0 {
					t.Fatalf("%s is empty", p)
				}
			}

			tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:v:0", tc.videoExt), videoPath, "v:0")
			tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:a:0", tc.audioExt), audioPath, "a:0")

			srcVideo, _ := tools.Probe(t, src).Video()
			gotVideo, _ := tools.Probe(t, videoPath).Video()
			if gotVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames demuxed, want %d", gotVideo.Frames(), srcVideo.Frames())
			}
		})
	}
}

// A fragmented mp4 has no stbl: every moof carries its own sample table. The
// same demuxer has to read it, which is worth its own case because the two
// paths share nothing but ReadPacket.
func TestDemuxFragmentedMP4(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 2, BFrames: 2,
		Extra: []string{"-movflags", "frag_keyframe+empty_moov+default_base_moof"},
	})

	outDir := t.TempDir()
	files, infos, err := DemuxMP4File(src, outDir)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("%d tracks in the header, want 2", len(infos))
	}

	videoPath, ok := files["h264"]
	if !ok {
		t.Fatalf("no h264 stream came out, got %v", files)
	}
	tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:v:0", "h264"), videoPath, "v:0")
	if audioPath, ok := files["aac"]; ok {
		tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:a:0", "aac"), audioPath, "a:0")
	} else {
		t.Error("no aac stream came out of the fragmented file")
	}
}

// The track info the header reports has to agree with what the file says.
func TestReadHeadReportsTracks(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 2,
	})
	_, infos, err := DemuxMP4File(src, t.TempDir())
	if err != nil {
		t.Fatalf("demux: %v", err)
	}

	probe := tools.Probe(t, src)
	wantVideo, _ := probe.Video()

	var sawVideo, sawAudio bool
	for _, info := range infos {
		switch info.Cid {
		case mp4.MP4_CODEC_H264:
			sawVideo = true
			if info.Width != uint32(wantVideo.Width) || info.Height != uint32(wantVideo.Height) {
				t.Errorf("track reports %dx%d, want %dx%d",
					info.Width, info.Height, wantVideo.Width, wantVideo.Height)
			}
		case mp4.MP4_CODEC_AAC:
			sawAudio = true
			if info.SampleRate != 48000 {
				t.Errorf("audio track reports %d Hz, want 48000", info.SampleRate)
			}
		}
	}
	if !sawVideo || !sawAudio {
		t.Errorf("header reported %+v, want an h264 and an aac track", infos)
	}
}
