package mkv

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/ntklink/gomediautils/go-codec"
)

func TestMuxerLiveNoCueLeak(t *testing.T) {
	var live bytes.Buffer // io.Writer only, not io.WriteSeeker
	m, err := NewMuxer(&live)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.AddVideoTrack(codec.CODECID_VIDEO_H264)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := m.Write(v, h264Frame(true), uint64(i*1000), uint64(i*1000)); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.WriteTrailer(); err != nil {
		t.Fatal(err)
	}
	if len(m.cues) != 0 {
		t.Fatalf("live muxer accumulated %d cues, want 0", len(m.cues))
	}
}

func TestDemuxerBlockGroupChecksSlicesForKeyFrame(t *testing.T) {
	// Build a minimal Matroska stream with an H.264 track and a non-IDR P-frame
	// inside a BlockGroup that omits ReferenceBlock.
	avcc, err := codec.CreateH264AVCCExtradata([][]byte{h264SPS}, [][]byte{h264PPS})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	out.Write(master(idEBML,
		uintElement(idEBMLVersion, 1),
		uintElement(idEBMLReadVersion, 1),
		uintElement(idEBMLMaxIDLength, 4),
		uintElement(idEBMLMaxSizeLength, 8),
		stringElement(idDocType, DocTypeMatroska),
		uintElement(idDocTypeVersion, 4),
		uintElement(idDocTypeReadVersion, 2),
	))

	track := master(idTrackEntry,
		uintElement(idTrackNumber, 1),
		uintElement(idTrackUID, 1),
		uintElement(idTrackType, trackTypeVideo),
		stringElement(idCodecID, "V_MPEG4/ISO/AVC"),
		element(idCodecPrivate, avcc),
		master(idVideo, uintElement(idPixelWidth, 64), uintElement(idPixelHeight, 48)),
	)
	// Non-IDR P-slice in 4-byte length-prefixed form
	pFrame := []byte{0, 0, 0, 4, 0x41, 0x9a, 0x21, 0x6c}
	blk := appendSize(nil, 1, 1)
	blk = binary.BigEndian.AppendUint16(blk, 0)
	blk = append(blk, 0)
	blk = append(blk, pFrame...)
	cluster := master(idCluster,
		uintElement(idClusterTime, 0),
		master(idBlockGroup, element(idBlock, blk)),
	)
	out.Write(master(idSegment,
		master(idInfo, uintElement(idTimestampScale, 1000000)),
		master(idTracks, track),
		cluster,
	))

	d := NewDemuxer(bytes.NewReader(out.Bytes()))
	if _, err := d.ReadHead(); err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	pkt, err := d.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if pkt.KeyFrame {
		t.Fatal("non-IDR H.264 frame in BlockGroup without ReferenceBlock must not be marked KeyFrame")
	}
	if _, err := d.ReadPacket(); err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestRegroupPCMClampsSmallChunk(t *testing.T) {
	var buf bytes.Buffer
	m, err := NewMuxer(&buf)
	if err != nil {
		t.Fatal(err)
	}
	// sampleRate=10 => 10 * 1 * 20 / 1000 == 0 before clamp
	a, err := m.AddAudioTrack(codec.CODECID_AUDIO_G711A, WithAudioSampleRate(10), WithAudioChannelCount(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(a, []byte{0x55, 0xAA}, 0, 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := m.WriteTrailer(); err != nil {
		t.Fatalf("WriteTrailer: %v", err)
	}
}
