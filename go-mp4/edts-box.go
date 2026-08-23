package mp4

import (
	"encoding/binary"
	"io"
)

// aligned(8) class EditListBox extends FullBox(‘elst’, version, 0) {
// 	unsigned int(32) entry_count;
// 	for (i=1; i <= entry_count; i++) {
// 		  if (version==1) {
// 			 unsigned int(64) segment_duration;
// 			 int(64) media_time;
// 		  } else { // version==0
// 			 unsigned int(32) segment_duration;
// 			 int(32)  media_time;
// 		  }
// 		  int(16) media_rate_integer;
// 		  int(16) media_rate_fraction = 0;
// 	}
// }

type EditListBox struct {
	box    *FullBox
	entrys *movelst
}

func NewEditListBox(version uint32) *EditListBox {
	return &EditListBox{
		box:    NewFullBox([4]byte{'e', 'l', 's', 't'}, uint8(version)),
		entrys: new(movelst),
	}
}

func (elst *EditListBox) Encode() (int, []byte) {
	offset, elstdata := elst.box.Encode()
	binary.BigEndian.PutUint32(elstdata[offset:], uint32(elst.entrys.entryCount))
	offset += 4
	for _, entry := range elst.entrys.entrys {
		if elst.box.Version == 1 {
			binary.BigEndian.PutUint64(elstdata[offset:], entry.segmentDuration)
			offset += 8
			binary.BigEndian.PutUint64(elstdata[offset:], uint64(entry.mediaTime))
			offset += 8
		} else {
			binary.BigEndian.PutUint32(elstdata[offset:], uint32(entry.segmentDuration))
			offset += 4
			binary.BigEndian.PutUint32(elstdata[offset:], uint32(entry.mediaTime))
			offset += 4
		}
		binary.BigEndian.PutUint16(elstdata[offset:], uint16(entry.mediaRateInteger))
		offset += 2
		binary.BigEndian.PutUint16(elstdata[offset:], uint16(entry.mediaRateFraction))
		offset += 2
	}
	return offset, elstdata
}

func (elst *EditListBox) Decode(r io.Reader) (offset int, err error) {
	if offset, err = elst.box.Decode(r); err != nil {
		return 0, err
	}

	entryCountBuf := make([]byte, 4)
	if _, err = io.ReadFull(r, entryCountBuf); err != nil {
		return
	}
	entryCount := binary.BigEndian.Uint32(entryCountBuf)
	offset += 4
	entrySize := 12
	if elst.box.Version != 0 {
		entrySize = 20
	}
	remain, err := elst.box.tableRemain(4)
	if err != nil {
		return 0, err
	}
	if err = checkTableSize(entryCount, entrySize, remain); err != nil {
		return 0, err
	}
	buf := make([]byte, int(entryCount)*entrySize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}
	if elst.entrys == nil {
		elst.entrys = new(movelst)
	}
	nn := 0
	elst.entrys.entryCount = entryCount
	elst.entrys.entrys = make([]elstEntry, entryCount)
	for i := 0; i < int(entryCount); i++ {
		if elst.box.Version == 0 {
			elst.entrys.entrys[i].segmentDuration = uint64(binary.BigEndian.Uint32(buf[nn:]))
			nn += 4
			elst.entrys.entrys[i].mediaTime = int64(int32(binary.BigEndian.Uint32(buf[nn:])))
			nn += 4
		} else {
			elst.entrys.entrys[i].segmentDuration = uint64(binary.BigEndian.Uint64(buf[nn:]))
			nn += 8
			elst.entrys.entrys[i].mediaTime = int64(binary.BigEndian.Uint64(buf[nn:]))
			nn += 8
		}
		elst.entrys.entrys[i].mediaRateInteger = int16(binary.BigEndian.Uint16(buf[nn:]))
		nn += 2
		elst.entrys.entrys[i].mediaRateFraction = int16(binary.BigEndian.Uint16(buf[nn:]))
		nn += 2
	}
	return offset + nn, nil
}

// makeElstBox writes the edit list that maps the media timeline onto the
// presentation.
//
// The sample table always starts the media timeline at decode time 0, so with
// reordered frames the first picture is presented at its composition offset,
// not at 0. media_time has to name that offset: an edit that starts at 0
// tells the player to present from a point where no picture exists yet, and
// ffmpeg reacts by reporting "missing key frame while searching for timestamp
// 0" and dropping the frames of the reorder delay.
//
// media_time is in the media timescale, segment_duration in the movie one.
func makeElstBox(track *mp4track) (boxdata []byte) {
	// composition offset of the first sample, in media timescale units
	mediaTime := int64(track.samplelist[0].pts) - int64(track.samplelist[0].dts)
	if mediaTime < 0 {
		mediaTime = 0
	}

	duration := uint64(track.mediaDuration())

	version := uint32(0)
	entrySize := 12
	if duration > 0xFFFFFFFF || mediaTime > 0xFFFFFFFF {
		version = 1
		entrySize = 20
	}

	elst := NewEditListBox(version)
	elst.entrys = new(movelst)
	elst.entrys.entryCount = 1
	elst.entrys.entrys = []elstEntry{{
		segmentDuration:   duration,
		mediaTime:         mediaTime,
		mediaRateInteger:  0x0001,
		mediaRateFraction: 0,
	}}

	elst.box.Box.Size = uint64(FullBoxLen + 4 + entrySize)
	_, boxdata = elst.Encode()
	return
}

func makeEdtsBox(track *mp4track) []byte {
	elst := makeElstBox(track)
	edts := BasicBox{Type: [4]byte{'e', 'd', 't', 's'}}
	edts.Size = 8 + uint64(len(elst))
	offset, edtsbox := edts.Encode()
	copy(edtsbox[offset:], elst)
	return edtsbox
}

func decodeElstBox(demuxer *MovDemuxer, size uint64) (err error) {
	track := demuxer.lastTrack()
	if track == nil {
		return errNoTrack
	}
	elst := &EditListBox{box: &FullBox{Box: &BasicBox{Size: size}}}
	if _, err = elst.Decode(demuxer.reader); err != nil {
		return
	}
	track.elst = elst.entrys
	return
}
