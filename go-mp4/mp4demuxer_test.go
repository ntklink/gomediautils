package mp4

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
)

func TestCreateMovDemuxer(t *testing.T) {
	f, err := os.Open("source.200kbps.768x320.flv.mp4")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	vfile, _ := os.OpenFile("v.h264", os.O_CREATE|os.O_RDWR, 0666)
	defer vfile.Close()
	afile, _ := os.OpenFile("a.aac", os.O_CREATE|os.O_RDWR, 0666)
	defer afile.Close()
	demuxer := CreateMp4Demuxer(f)
	if infos, err := demuxer.ReadHead(); err != nil && err != io.EOF {
		fmt.Println(err)
	} else {
		fmt.Printf("%+v\n", infos)
	}
	mp4info := demuxer.GetMp4Info()
	fmt.Printf("%+v\n", mp4info)
	for {
		pkg, err := demuxer.ReadPacket()
		if err != nil {
			fmt.Println(err)
			break
		}
		fmt.Printf("track:%d,cid:%+v,pts:%d dts:%d\n", pkg.TrackId, pkg.Cid, pkg.Pts, pkg.Dts)
		if pkg.Cid == MP4_CODEC_H264 {
			vfile.Write(pkg.Data)
		} else if pkg.Cid == MP4_CODEC_AAC {
			afile.Write(pkg.Data)
		}
	}
}

// A malformed esds descriptor must be reported as an error. The decoder used
// to rely on the bit reader panicking and a recover() to turn that into one.
func TestDecodeESDescriptorRejectsMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		esd  []byte
	}{
		{name: "tag only", esd: []byte{0x03}},
		{name: "size field never ends", esd: []byte{0x03, 0x80, 0x80, 0x80, 0x80, 0x80}},
		{name: "es descriptor body truncated", esd: []byte{0x03, 0x0a, 0x00}},
		{name: "decoder config truncated", esd: []byte{0x04, 0x0d, 0x40}},
		{name: "unknown object type", esd: []byte{0x04, 0x0d, 0x2f, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
		{name: "decoder specific info truncated", esd: []byte{0x05, 0x7f, 0x12, 0x10}},
		{name: "trailing descriptor truncated", esd: []byte{0x06, 0x08, 0x02}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			track := &mp4track{}
			if _, err := decodeESDescriptor(tc.esd, track); err == nil {
				t.Fatalf("malformed esds %x was accepted", tc.esd)
			}
		})
	}
}

// An aac esds box that carries no AudioSpecificConfig cannot be turned back
// into ADTS, so it is rejected.
func TestDecodeESDescriptorRequiresAacConfig(t *testing.T) {
	esd, err := makeESDescriptor(1, MP4_CODEC_AAC, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeESDescriptor(esd, &mp4track{}); err == nil {
		t.Fatal("aac esds without decoder specific info was accepted")
	}
}

// What the muxer writes must survive a trip back through the demuxer.
func TestESDescriptorRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		cid MP4_CODEC_TYPE
		asc []byte
	}{
		{cid: MP4_CODEC_AAC, asc: []byte{0x12, 0x10}},
		{cid: MP4_CODEC_MP3, asc: nil},
	} {
		esd, err := makeESDescriptor(7, tc.cid, tc.asc)
		if err != nil {
			t.Fatal(err)
		}
		track := &mp4track{}
		vosData, err := decodeESDescriptor(esd, track)
		if err != nil {
			t.Fatalf("cid %d: %v", tc.cid, err)
		}
		if track.cid != tc.cid {
			t.Fatalf("cid = %d, want %d", track.cid, tc.cid)
		}
		if !bytes.Equal(vosData, tc.asc) {
			t.Fatalf("decoder specific info = %x, want %x", vosData, tc.asc)
		}
	}
}
