package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
	"github.com/ntklink/gomediautils/go-srt"
)

// GoMediaUtils publishes an mp4 as MPEG-TS over SRT to ffmpeg listening
// with libsrt, the reference implementation; ffmpeg has to decode the same
// pictures out of what arrived. Through a link that loses packets both
// ways, retransmission has to make up every loss within the latency.
func TestPushMP4OverSRT(t *testing.T) {
	tools := mediatest.Require(t)
	tools.RequireProtocol(t, "srt")
	src := tools.MakeClip(t, mediatest.Clip{Container: "mp4", Video: "libx264", Audio: "aac", BFrames: 2})

	// the pushes go at the pace of a live source: libsrt throws away what
	// its application has not read yet when the sender shuts down, and a
	// burst of two seconds of media outruns ffmpeg while it probes
	cases := []struct {
		name       string
		passphrase string
		loss       float64
	}{
		{"clear", "", 0},
		{"encrypted", "correct horse battery", 0},
		{"5% loss each way", "", 0.05},
		{"encrypted with 5% loss", "correct horse battery", 0.05},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := mediatest.FreePort(t)
			dst := filepath.Join(t.TempDir(), "received.ts")
			url := fmt.Sprintf("srt://127.0.0.1:%d?mode=listener&latency=400000", port)
			if tc.passphrase != "" {
				url += "&passphrase=" + tc.passphrase
			}
			ffmpeg := tools.Start(t, mediatest.FFmpegArgs("-i", url, "-c", "copy", "-f", "mpegts", dst)...)

			addr := fmt.Sprintf("127.0.0.1:%d", port)
			if tc.loss > 0 {
				addr = mediatest.LossyUDPProxy(t, addr, tc.loss)
			}
			cfg := srt.Config{Passphrase: tc.passphrase, Latency: 400 * time.Millisecond, ConnectTimeout: 10 * time.Second}
			stats, err := PushMP4OverSRT(src, addr, cfg, true)
			if err != nil {
				t.Fatalf("push: %v", err)
			}
			if tc.loss > 0 && stats.PacketsRetransmitted == 0 {
				t.Errorf("nothing sent again through a lossy link: %+v", stats)
			}
			ffmpeg.Wait(t)

			out := tools.MustProbe(t, dst, 2)
			srcVideo, _ := tools.Probe(t, src).Video()
			dstVideo, _ := out.Video()
			if dstVideo.Frames() != srcVideo.Frames() {
				t.Errorf("%d video frames arrived, want %d", dstVideo.Frames(), srcVideo.Frames())
			}
			tools.AssertDecodable(t, dst)
			tools.AssertSameDecoded(t, src, dst, "v:0")
			// the mp4 edit list hides the aac priming, which a transport
			// stream keeps; the bare stream is the fair reference
			tools.AssertSameDecoded(t, tools.ExtractStream(t, src, "0:a:0", "aac"), dst, "a:0")
		})
	}
}
