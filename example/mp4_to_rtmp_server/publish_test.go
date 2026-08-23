package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
	"github.com/ntklink/gomediautils/go-codec"
)

// mp4 is the awkward source to publish from: samples come out of the
// demuxer a track at a time, and video carries a composition offset that has
// to survive the trip into rtmp's separate pts and dts. GoMediaUtils publishes,
// a third party server relays, and ffmpeg pulls the result back.
func TestPublishMP4ToRemoteServer(t *testing.T) {
	tools := mediatest.Require(t)
	remote := mediatest.RequireStreamingServer(t)

	// long enough that the stream is still live once the puller has
	// connected: a server drops a live path the moment its publisher leaves
	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 12, GOP: 25, BFrames: 2,
	})
	const pullSeconds = 4
	url := remote.RTMPURL(mediatest.UniquePath(t, "mp4-pub"))

	dst := filepath.Join(t.TempDir(), "pulled.flv")
	published := make(chan error, 1)
	go func() { published <- PublishMP4(url, src, true) }()

	remote.WaitStreamReady(t, tools, url, 20*time.Second)

	tools.Start(t, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", url, "-t", fmt.Sprint(pullSeconds), "-c", "copy", "-f", "flv", dst).Wait(t)

	select {
	case err := <-published:
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("the publisher never finished")
	}

	out := tools.Probe(t, dst)
	video, ok := out.Video()
	if !ok {
		t.Fatalf("the server gave back no video (format %q)", out.Format.FormatName)
	}
	if video.CodecName != "h264" {
		t.Errorf("video codec %q, want h264", video.CodecName)
	}
	if video.Width != 320 || video.Height != 240 {
		t.Errorf("resolution %dx%d, want 320x240", video.Width, video.Height)
	}
	if _, ok := out.Audio(); !ok {
		t.Error("the server gave back no audio; the tracks were probably not interleaved")
	}

	want := pullSeconds * 25
	floor := want * 3 / 4
	if !remote.Local {
		// somebody else's link, only check the stream is really flowing
		floor = 25
	}
	if video.Frames() < floor {
		t.Errorf("%d frames came back from a %d second pull, want at least %d",
			video.Frames(), pullSeconds, floor)
	}
	tools.AssertDecodable(t, dst)

	// b frames mean pts and dts differ, and rtmp carries the difference as a
	// composition offset; a publisher that drops it makes every frame
	// present in decode order
	mediatest.AssertMonotonicDts(t, tools.Packets(t, dst, "v:0"), "published video")
	reordered := false
	for _, p := range tools.Packets(t, dst, "v:0") {
		if p.Pts() != p.Dts() {
			reordered = true
			break
		}
	}
	if !reordered {
		t.Error("no packet has a composition offset; the b frame timing was lost on the way through rtmp")
	}
}

// The tracks have to arrive interleaved by decode time, not one after the
// other, which is what the sort in readPackets exists for.
func TestReadPacketsInterleavesTracks(t *testing.T) {
	tools := mediatest.Require(t)

	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 4, GOP: 25, BFrames: 2,
	})

	packets, err := readPackets(src)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(packets) == 0 {
		t.Fatal("no packets came out")
	}

	for i := 1; i < len(packets); i++ {
		if packets[i].dts < packets[i-1].dts {
			t.Fatalf("packet %d has dts %d after %d; the timeline is not sorted",
				i, packets[i].dts, packets[i-1].dts)
		}
	}

	// both tracks have to be present throughout, not one then the other
	half := len(packets) / 2
	firstHalfAudio, secondHalfAudio := 0, 0
	for i, p := range packets {
		if p.cid == codec.CODECID_AUDIO_AAC || p.cid == codec.CODECID_AUDIO_MP3 {
			if i < half {
				firstHalfAudio++
			} else {
				secondHalfAudio++
			}
		}
	}
	if firstHalfAudio == 0 || secondHalfAudio == 0 {
		t.Errorf("audio packets are bunched at one end: %d in the first half, %d in the second",
			firstHalfAudio, secondHalfAudio)
	}
}
