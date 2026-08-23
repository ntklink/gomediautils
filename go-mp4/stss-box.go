package mp4

import (
	"encoding/binary"
	"io"
)

// aligned(8) class SyncSampleBox extends FullBox(‘stss’, version = 0, 0) {
//  	unsigned int(32) entry_count;
//  	int i;
//  	for (i=0; i < entry_count; i++) {
//  		unsigned int(32) sample_number;
//  	}
//  }

type SyncSampleBox struct {
	box    *FullBox
	entrys []uint32
}

func NewSyncSampleBox() *SyncSampleBox {
	return &SyncSampleBox{
		box: NewFullBox([4]byte{'s', 't', 's', 's'}, 0),
	}
}

func (stss *SyncSampleBox) Size() uint64 {
	if len(stss.entrys) == 0 {
		return stss.box.Size() + 4
	} else {
		return stss.box.Size() + 4 + 4*uint64(len(stss.entrys))
	}
}

func (stss *SyncSampleBox) Encode() (int, []byte) {
	stss.box.Box.Size = stss.Size()
	offset, buf := stss.box.Encode()
	binary.BigEndian.PutUint32(buf[offset:], uint32(len(stss.entrys)))
	offset += 4
	for _, sampleNumber := range stss.entrys {
		binary.BigEndian.PutUint32(buf[offset:], sampleNumber)
		offset += 4
	}
	return offset, buf
}

func (stss *SyncSampleBox) Decode(r io.Reader) (offset int, err error) {
	if _, err = stss.box.Decode(r); err != nil {
		return
	}
	tmp := make([]byte, 4)
	if _, err = io.ReadFull(r, tmp); err != nil {
		return
	}
	offset = 8
	entry_count := binary.BigEndian.Uint32(tmp[:])
	remain, err := stss.box.tableRemain(4)
	if err != nil {
		return
	}
	if err = checkTableSize(entry_count, 4, remain); err != nil {
		return
	}
	stss.entrys = make([]uint32, entry_count)
	buf := make([]byte, int(entry_count)*4)
	if _, err = io.ReadFull(r, buf); err != nil {
		return
	}
	idx := 0
	for i := 0; i < int(entry_count); i++ {
		stss.entrys[i] = binary.BigEndian.Uint32(buf[idx:])
		idx += 4
	}
	offset += idx
	return
}

func decodeStssBox(demuxer *MovDemuxer, size uint64) (err error) {
	track, err := demuxer.lastStbl()
	if err != nil {
		return err
	}
	stss := SyncSampleBox{box: &FullBox{Box: &BasicBox{Size: size}}}
	if _, err = stss.Decode(demuxer.reader); err != nil {
		return
	}
	track.stbltable.stss = &movstss{
		sampleNumber: stss.entrys,
	}
	return
}

func makeStss(track *mp4track) (boxdata []byte) {
	stss := NewSyncSampleBox()
	for i, sample := range track.samplelist {
		if sample.isKeyFrame {
			stss.entrys = append(stss.entrys, uint32(i+1))
		}
	}
	_, boxdata = stss.Encode()
	return
}
