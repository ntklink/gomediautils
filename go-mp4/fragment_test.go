package mp4

import (
	"bytes"
	"testing"
)

// makeTraf used to be built twice per fragment: once with a zero moof size to
// learn the size of the moof box and once with the real one. It is now built
// once and the sample offsets are patched. The output must stay identical.
func TestPatchTrafDataOffsetMatchesRebuild(t *testing.T) {
	build := func(track *mp4track, moofOffset, moofSize uint64) []byte {
		return makeTraf(track, moofOffset, moofSize)
	}

	for _, tc := range []struct {
		name    string
		samples []sampleEntry
		cid     MP4_CODEC_TYPE
	}{
		{
			name: "contiguous",
			cid:  MP4_CODEC_G711A,
			samples: []sampleEntry{
				{pts: 0, dts: 0, offset: 0, size: 160},
				{pts: 20, dts: 20, offset: 160, size: 160},
				{pts: 40, dts: 40, offset: 320, size: 160},
			},
		},
		{
			// a gap in the sample offsets splits the run into several trun boxes
			name: "multi trun",
			cid:  MP4_CODEC_G711A,
			samples: []sampleEntry{
				{pts: 0, dts: 0, offset: 0, size: 160},
				{pts: 20, dts: 20, offset: 160, size: 160},
				{pts: 40, dts: 40, offset: 1000, size: 100},
				{pts: 60, dts: 60, offset: 1100, size: 100},
				{pts: 80, dts: 80, offset: 5000, size: 90},
			},
		},
		{
			name: "video with cts offsets",
			cid:  MP4_CODEC_H264,
			samples: []sampleEntry{
				{pts: 80, dts: 0, offset: 0, size: 5000, isKeyFrame: true},
				{pts: 40, dts: 40, offset: 5000, size: 300},
				{pts: 120, dts: 80, offset: 5300, size: 280},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			track := &mp4track{
				cid:        tc.cid,
				trackId:    1,
				timescale:  1000,
				samplelist: tc.samples,
				lastSample: &sampleCache{dts: tc.samples[len(tc.samples)-1].dts + 20},
			}
			const moofOffset = 4096
			const delta = 1234

			patched := build(track, moofOffset, 0)
			if err := patchTrafDataOffset(patched, delta); err != nil {
				t.Fatal(err)
			}
			want := build(track, moofOffset, delta)
			if !bytes.Equal(patched, want) {
				t.Fatalf("patched traf differs from rebuilt traf\n got %x\nwant %x", patched, want)
			}
		})
	}
}

func TestPatchTrafDataOffsetRejectsGarbage(t *testing.T) {
	if err := patchTrafDataOffset([]byte{0, 1, 2}, 1); err == nil {
		t.Fatal("short traf must be rejected")
	}
	// a child box claiming a size beyond the traf
	traf := []byte{0, 0, 0, 24, 't', 'r', 'a', 'f', 0, 0, 0, 200, 't', 'r', 'u', 'n'}
	if err := patchTrafDataOffset(traf, 1); err == nil {
		t.Fatal("oversized child box must be rejected")
	}
}

// An audio only fragmented mp4 used to buffer the whole stream because only
// video key frames triggered a flush.
func TestAudioOnlyFragmentFlush(t *testing.T) {
	w := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(w, WithMp4Flag(MP4_FLAG_FRAGMENT), WithFragmentDuration(100))
	if err != nil {
		t.Fatal(err)
	}
	a, err := muxer.AddAudioTrack(MP4_CODEC_G711A, WithAudioSampleRate(8000), WithAudioChannelCount(1), WithAudioSampleBits(8))
	if err != nil {
		t.Fatal(err)
	}

	var fragments int
	muxer.OnNewFragment(func(duration uint32, firstPts, firstDts uint64) {
		fragments++
	})
	for i := 0; i < 40; i++ {
		if err := muxer.Write(a, make([]byte, 160), uint64(i*20), uint64(i*20)); err != nil {
			t.Fatal(err)
		}
	}
	if fragments < 5 {
		t.Fatalf("audio only stream produced %d fragments, want several", fragments)
	}
	ws := muxer.tracks[a].writer.(*fmp4WriterSeeker)
	if len(ws.buffer) > 8*160 {
		t.Fatalf("fragment buffer holds %d bytes, samples are not being flushed", len(ws.buffer))
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
}

// startPts/startDts must describe the fragment that is being written, they were
// never assigned so sidx and the onNewFragment callback always reported 0.
func TestFragmentStartTimestamps(t *testing.T) {
	w := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(w, WithMp4Flag(MP4_FLAG_FRAGMENT))
	if err != nil {
		t.Fatal(err)
	}
	a, err := muxer.AddAudioTrack(MP4_CODEC_G711A, WithAudioSampleRate(8000), WithAudioChannelCount(1), WithAudioSampleBits(8))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := muxer.Write(a, make([]byte, 160), uint64(1000+i*20), uint64(1000+i*20)); err != nil {
			t.Fatal(err)
		}
	}
	if err := muxer.FlushFragment(); err != nil {
		t.Fatal(err)
	}
	if got := muxer.tracks[a].startDts; got != 1000 {
		t.Fatalf("startDts = %d, want 1000", got)
	}
	if got := muxer.tracks[a].startPts; got != 1000 {
		t.Fatalf("startPts = %d, want 1000", got)
	}

	for i := 5; i < 9; i++ {
		if err := muxer.Write(a, make([]byte, 160), uint64(1000+i*20), uint64(1000+i*20)); err != nil {
			t.Fatal(err)
		}
	}
	if err := muxer.FlushFragment(); err != nil {
		t.Fatal(err)
	}
	if got := muxer.tracks[a].startDts; got != 1100 {
		t.Fatalf("second fragment startDts = %d, want 1100", got)
	}
}

func TestFmp4WriterSeekerGrowth(t *testing.T) {
	ws := newFmp4WriterSeeker(16)
	chunk := make([]byte, 100)
	for i := 0; i < 200; i++ {
		if _, err := ws.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if len(ws.buffer) != 200*len(chunk) {
		t.Fatalf("buffer len %d, want %d", len(ws.buffer), 200*len(chunk))
	}
	// geometric growth must not leave the capacity pinned to the exact length
	if cap(ws.buffer) < len(ws.buffer) {
		t.Fatalf("cap %d < len %d", cap(ws.buffer), len(ws.buffer))
	}
}
