package mp4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"testing"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-mpeg2"
)

func TestCreateMp4Reader(t *testing.T) {
	f, err := os.Open("jellyfish-3-mbps-hd.h264.mp4")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	for err == nil {
		nn := int64(0)
		size := make([]byte, 4)
		_, err = io.ReadFull(f, size)
		if err != nil {
			break
		}
		nn += 4
		boxtype := make([]byte, 4)
		_, err = io.ReadFull(f, boxtype)
		if err != nil {
			break
		}
		nn += 4
		var isize uint64 = uint64(binary.BigEndian.Uint32(size))
		if isize == 1 {
			size := make([]byte, 8)
			_, err = io.ReadFull(f, size)
			if err != nil {
				break
			}
			isize = binary.BigEndian.Uint64(size)
			nn += 8
		}
		fmt.Printf("Read Box(%s) size:%d\n", boxtype, isize)
		f.Seek(int64(isize)-nn, 1)
	}
}

func TestCreateMp4Muxer(t *testing.T) {

	f, err := os.Open("jellyfish-3-mbps-hd.h265")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()

	mp4filename := "jellyfish-3-mbps-hd.h265.mp4"
	mp4file, err := os.OpenFile(mp4filename, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer mp4file.Close()

	buf, _ := ioutil.ReadAll(f)
	pts := uint64(0)
	dts := uint64(0)
	ii := [3]uint64{33, 33, 34}
	idx := 0

	type args struct {
		wh io.WriteSeeker
	}
	tests := []struct {
		name string
		args args
		want *Movmuxer
	}{
		{name: "muxer h264", args: args{wh: mp4file}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			muxer, err := CreateMp4Muxer(tt.args.wh)
			if err != nil {
				fmt.Println(err)
				return
			}
			tid, err := muxer.AddVideoTrack(MP4_CODEC_H265)
			if err != nil {
				t.Fatal(err)
			}
			cache := make([]byte, 0)
			codec.SplitFrameWithStartCode(buf, func(nalu []byte) bool {
				ntype := codec.H265NaluType(nalu)
				if !codec.IsH265VCLNaluType(ntype) {
					cache = append(cache, nalu...)
					return true
				}
				if len(cache) > 0 {
					cache = append(cache, nalu...)
					muxer.Write(tid, cache, pts, dts)
					cache = cache[:0]
				} else {
					muxer.Write(tid, nalu, pts, dts)
				}
				pts += ii[idx]
				dts += ii[idx]
				idx++
				idx = idx % 3
				return true
			})
			fmt.Printf("last dts %d\n", dts)
			muxer.WriteTrailer()
		})
	}
}

func TestMuxAAC(t *testing.T) {
	f, err := os.Open("test.aac")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()

	mp4filename := "aac.mp4"
	mp4file, err := os.OpenFile(mp4filename, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer mp4file.Close()

	aac, _ := ioutil.ReadAll(f)
	var pts uint64 = 0
	//var dts uint64 = 0
	//var i int = 0
	samples := uint64(0)
	muxer, err := CreateMp4Muxer(mp4file)
	if err != nil {
		fmt.Println(err)
		return
	}

	tid, err := muxer.AddAudioTrack(MP4_CODEC_AAC)
	if err != nil {
		t.Fatal(err)
	}
	err = codec.SplitAACFrame(aac, func(aac []byte) {
		samples += 1024
		pts = samples * 1000 / 44100
		// if i < 3 {
		// 	pts += 23
		// 	dts += 23
		// 	i++
		// } else {
		// 	pts += 24
		// 	dts += 24
		// 	i = 0
		// }
		muxer.Write(tid, aac, pts, pts)
		//fmt.Println(pts)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
}

func TestMuxMp4(t *testing.T) {
	tsfilename := `demo.ts` // input
	tsfile, err := os.Open(tsfilename)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer tsfile.Close()

	mp4filename := "test14.mp4" // output
	mp4file, err := os.OpenFile(mp4filename, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer mp4file.Close()

	muxer, err := CreateMp4Muxer(mp4file)
	if err != nil {
		fmt.Println(err)
		return
	}
	vtid, err := muxer.AddVideoTrack(MP4_CODEC_H264)
	if err != nil {
		t.Fatal(err)
	}
	atid, err := muxer.AddAudioTrack(MP4_CODEC_AAC)
	if err != nil {
		t.Fatal(err)
	}

	afile, err := os.OpenFile("r.aac", os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer afile.Close()
	demuxer := mpeg2.NewTSDemuxer()
	demuxer.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts uint64, dts uint64) {

		if cid == mpeg2.TS_STREAM_AAC {
			err = muxer.Write(atid, frame, uint64(pts), uint64(dts))
			if err != nil {
				panic(err)
			}
		} else if cid == mpeg2.TS_STREAM_H264 {
			fmt.Println("pts,dts,len", pts, dts, len(frame))
			err = muxer.Write(vtid, frame, uint64(pts), uint64(dts))
			if err != nil {
				panic(err)
			}
		} else {
			panic("unkwon cid " + strconv.Itoa(int(cid)))
		}
	}

	err = demuxer.Input(tsfile)
	if err != nil {
		panic(err)
	}

	err = muxer.WriteTrailer()
	if err != nil {
		panic(err)
	}
}

// A codec id the muxer cannot write must be rejected when the track is added,
// so that it can never reach the box writers (which used to panic on it).
func TestAddTrackRejectsUnsupportedCodec(t *testing.T) {
	muxer, err := CreateMp4Muxer(newMemWriteSeeker())
	if err != nil {
		t.Fatal(err)
	}

	for _, cid := range []MP4_CODEC_TYPE{MP4_CODEC_TYPE(0), MP4_CODEC_TYPE(1000), MP4_CODEC_AAC, MP4_CODEC_G711A} {
		if _, err := muxer.AddVideoTrack(cid); !errors.Is(err, ErrUnsupportedCodec) {
			t.Fatalf("AddVideoTrack(%d) error = %v, want ErrUnsupportedCodec", cid, err)
		}
	}
	for _, cid := range []MP4_CODEC_TYPE{MP4_CODEC_TYPE(0), MP4_CODEC_TYPE(1000), MP4_CODEC_H264, MP4_CODEC_H265} {
		if _, err := muxer.AddAudioTrack(cid); !errors.Is(err, ErrUnsupportedCodec) {
			t.Fatalf("AddAudioTrack(%d) error = %v, want ErrUnsupportedCodec", cid, err)
		}
	}
	if len(muxer.tracks) != 0 {
		t.Fatalf("%d tracks were registered, want none", len(muxer.tracks))
	}

	for _, cid := range []MP4_CODEC_TYPE{MP4_CODEC_H264, MP4_CODEC_H265} {
		if _, err := muxer.AddVideoTrack(cid); err != nil {
			t.Fatalf("AddVideoTrack(%d): %v", cid, err)
		}
	}
	for _, cid := range []MP4_CODEC_TYPE{MP4_CODEC_AAC, MP4_CODEC_G711A, MP4_CODEC_G711U, MP4_CODEC_MP2, MP4_CODEC_MP3, MP4_CODEC_OPUS} {
		if _, err := muxer.AddAudioTrack(cid); err != nil {
			t.Fatalf("AddAudioTrack(%d): %v", cid, err)
		}
	}
}

// A track whose codec configuration was never filled in (no sample was ever
// written and no WithExtraData option was given) used to panic while building
// the stsd box. It must report an error instead.
func TestWriteTrailerWithoutExtraData(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []MuxerOption
	}{
		{name: "plain"},
		{name: "fragment", flags: []MuxerOption{WithMp4Flag(MP4_FLAG_FRAGMENT)}},
	} {
		for _, cid := range []MP4_CODEC_TYPE{MP4_CODEC_H264, MP4_CODEC_H265, MP4_CODEC_AAC} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, cid), func(t *testing.T) {
				muxer, err := CreateMp4Muxer(newMemWriteSeeker(), tc.flags...)
				if err != nil {
					t.Fatal(err)
				}
				if isVideo(cid) {
					_, err = muxer.AddVideoTrack(cid)
				} else {
					_, err = muxer.AddAudioTrack(cid)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := muxer.WriteTrailer(); err == nil {
					t.Fatal("WriteTrailer must report the missing codec configuration")
				}
			})
		}
	}
}

// The same track becomes writable once the configuration is supplied by hand.
func TestWriteTrailerWithSuppliedExtraData(t *testing.T) {
	muxer, err := CreateMp4Muxer(newMemWriteSeeker())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := muxer.AddAudioTrack(MP4_CODEC_AAC, WithExtraData([]byte{0x12, 0x10}),
		WithAudioSampleRate(44100), WithAudioChannelCount(2), WithAudioSampleBits(16)); err != nil {
		t.Fatal(err)
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
}

// Writing a sample to a track whose extra data is of the wrong kind used to
// panic; it must report an error.
func TestWriteSampleWithMismatchedExtraData(t *testing.T) {
	track := newmp4track(MP4_CODEC_H264, newMemWriteSeeker())
	track.extra = new(aacExtraData)
	if err := track.writeSample([]byte{0, 0, 0, 1, 0x67, 0x42}, 0, 0); err == nil {
		t.Fatal("h264 track with aac extra data must report an error")
	}
	track = newmp4track(MP4_CODEC_H265, newMemWriteSeeker())
	track.extra = new(aacExtraData)
	if err := track.writeSample([]byte{0, 0, 0, 1, 0x40, 0x01}, 0, 0); err == nil {
		t.Fatal("h265 track with aac extra data must report an error")
	}
}

// A truncated avcC record must be rejected rather than read out of bounds.
func TestLoadExtraDataRejectsGarbage(t *testing.T) {
	if err := (&h264ExtraData{}).load([]byte{0x01, 0x64}); err == nil {
		t.Fatal("truncated avcC record must be rejected")
	}
	if err := (&h265ExtraData{}).load([]byte{0x01}); err == nil {
		t.Fatal("hvcC load without a record configuration must be rejected")
	}
	if _, err := (&h265ExtraData{}).export(); err == nil {
		t.Fatal("hvcC export without a record configuration must be rejected")
	}
	if _, err := (&h264ExtraData{}).export(); err == nil {
		t.Fatal("avcC export without sps/pps must be rejected")
	}
}

var (
	testSPS = []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x64, 0x00, 0x0A, 0xAC, 0x72, 0x84, 0x44,
		0x26, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0xCA, 0x3C, 0x48, 0x96, 0x11, 0x80}
	testPPS = []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xE8, 0x43, 0x8F, 0x13, 0x21, 0x30}
	testIDR = []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x21, 0x3F, 0x00, 0x11, 0x22}
	testP   = []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A, 0x24, 0x6C, 0x41, 0x7F, 0xEA, 0x11}
)

// adtsFrame builds one AAC LC / 44100Hz / stereo ADTS frame with a dummy payload.
func adtsFrame(payload int) []byte {
	frame := make([]byte, 7+payload)
	l := len(frame)
	frame[0] = 0xFF
	frame[1] = 0xF1
	frame[2] = 0x50 // profile AAC LC, sampling frequency index 4 (44100)
	frame[3] = 0x80 | byte(l>>11)
	frame[4] = byte(l >> 3)
	frame[5] = byte(l&0x07)<<5 | 0x1F
	frame[6] = 0xFC
	return frame
}

// An end to end mux of a synthetic h264 + aac stream, read back with the
// demuxer. This is what the refactor of the box building chain must not break;
// the file based tests above all bail out when their media is not checked in.
func TestMuxAndDemuxRoundTrip(t *testing.T) {
	w := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(w)
	if err != nil {
		t.Fatal(err)
	}
	vtid, err := muxer.AddVideoTrack(MP4_CODEC_H264)
	if err != nil {
		t.Fatal(err)
	}
	atid, err := muxer.AddAudioTrack(MP4_CODEC_AAC)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		frame := testP
		if i == 0 {
			frame = append(append(append([]byte{}, testSPS...), testPPS...), testIDR...)
		}
		if err := muxer.Write(vtid, frame, uint64(i*40), uint64(i*40)); err != nil {
			t.Fatalf("video frame %d: %v", i, err)
		}
		if err := muxer.Write(atid, adtsFrame(100), uint64(i*23), uint64(i*23)); err != nil {
			t.Fatalf("audio frame %d: %v", i, err)
		}
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}

	// the muxer must have picked the resolution up out of the sps
	if muxer.tracks[vtid].width == 0 || muxer.tracks[vtid].height == 0 {
		t.Fatalf("video track resolution %dx%d, want it read from the sps",
			muxer.tracks[vtid].width, muxer.tracks[vtid].height)
	}
	if muxer.tracks[atid].sampleRate != 44100 || muxer.tracks[atid].chanelCount != 2 {
		t.Fatalf("audio track %dHz/%dch, want 44100Hz/2ch",
			muxer.tracks[atid].sampleRate, muxer.tracks[atid].chanelCount)
	}

	demuxer := CreateMp4Demuxer(bytes.NewReader(w.buf))
	infos, err := demuxer.ReadHead()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("demuxed %d tracks, want 2", len(infos))
	}
	got := map[MP4_CODEC_TYPE]TrackInfo{}
	for _, info := range infos {
		got[info.Cid] = info
	}
	if v, ok := got[MP4_CODEC_H264]; !ok {
		t.Fatal("h264 track is missing from the demuxed file")
	} else if v.Width != muxer.tracks[vtid].width || v.Height != muxer.tracks[vtid].height {
		t.Fatalf("demuxed resolution %dx%d, want %dx%d", v.Width, v.Height,
			muxer.tracks[vtid].width, muxer.tracks[vtid].height)
	}
	if a, ok := got[MP4_CODEC_AAC]; !ok {
		t.Fatal("aac track is missing from the demuxed file")
	} else if a.SampleRate != 44100 || a.ChannelCount != 2 {
		t.Fatalf("demuxed audio %dHz/%dch, want 44100Hz/2ch", a.SampleRate, a.ChannelCount)
	}

	var video, audio int
	for {
		pkg, err := demuxer.ReadPacket()
		if err != nil {
			break
		}
		switch pkg.Cid {
		case MP4_CODEC_H264:
			video++
		case MP4_CODEC_AAC:
			audio++
		}
	}
	if video != 10 || audio != 10 {
		t.Fatalf("read back %d video and %d audio samples, want 10 and 10", video, audio)
	}
}

// The same stream as a fragmented mp4: the moov is built inside flushFragment,
// which is the other path through the box building chain.
func TestFragmentedMuxRoundTrip(t *testing.T) {
	w := newMemWriteSeeker()
	muxer, err := CreateMp4Muxer(w, WithMp4Flag(MP4_FLAG_FRAGMENT))
	if err != nil {
		t.Fatal(err)
	}
	vtid, err := muxer.AddVideoTrack(MP4_CODEC_H264)
	if err != nil {
		t.Fatal(err)
	}
	atid, err := muxer.AddAudioTrack(MP4_CODEC_AAC)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		frame := testP
		if i%5 == 0 {
			frame = append(append(append([]byte{}, testSPS...), testPPS...), testIDR...)
		}
		if err := muxer.Write(vtid, frame, uint64(i*40), uint64(i*40)); err != nil {
			t.Fatalf("video frame %d: %v", i, err)
		}
		if err := muxer.Write(atid, adtsFrame(100), uint64(i*23), uint64(i*23)); err != nil {
			t.Fatalf("audio frame %d: %v", i, err)
		}
	}
	if err := muxer.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(w.buf, []byte("moov")) || !bytes.Contains(w.buf, []byte("moof")) {
		t.Fatal("fragmented output carries no moov/moof box")
	}
	if !bytes.Contains(w.buf, []byte("avcC")) || !bytes.Contains(w.buf, []byte("esds")) {
		t.Fatal("fragmented output carries no codec configuration")
	}

	demuxer := CreateMp4Demuxer(bytes.NewReader(w.buf))
	infos, err := demuxer.ReadHead()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("demuxed %d tracks, want 2", len(infos))
	}
}
