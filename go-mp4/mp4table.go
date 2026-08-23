package mp4

//based on ffmpeg

type sttsEntry struct {
	sampleCount uint32
	sampleDelta uint32
}

type subSampleEntry struct {
	bytesOfClearData     uint16
	bytesOfProtectedData uint32
}

type sencEntry struct {
	iv         []byte
	subSamples []subSampleEntry
}

type movstts struct {
	entryCount uint32
	entrys     []sttsEntry
}

type cttsEntry struct {
	sampleCount  uint32
	sampleOffset uint32
}

type movctts struct {
	version    uint8
	entryCount uint32
	entrys     []cttsEntry
}

// sampleOffset returns the signed composition offset of entry i, honouring
// the box version (version 1 offsets are signed 32-bit values).
func (ctts *movctts) sampleOffset(i int) int64 {
	if ctts.version == 1 {
		return int64(int32(ctts.entrys[i].sampleOffset))
	}
	return int64(ctts.entrys[i].sampleOffset)
}

type stscEntry struct {
	firstChunk             uint32
	samplesPerChunk        uint32
	sampleDescriptionIndex uint32
}

type elstEntry struct {
	segmentDuration   uint64
	mediaTime         int64
	mediaRateInteger  int16
	mediaRateFraction int16
}

type trunEntry struct {
	sampleDuration              uint32
	sampleSize                  uint32
	sampleFlags                 uint32
	sampleCompositionTimeOffset uint32
}

type movstsc struct {
	entryCount uint32
	entrys     []stscEntry
}

type movstsz struct {
	sampleSize    uint32
	sampleCount   uint32
	entrySizelist []uint32
}

type movstco struct {
	entryCount      uint32
	chunkOffsetlist []uint64
}

type movstss struct {
	sampleNumber []uint32
}

type movelst struct {
	entryCount uint32
	entrys     []elstEntry
}

type movtrun struct {
	entrys []trunEntry
}

type movsenc struct {
	entrys []sencEntry
}

type movstbl struct {
	stts *movstts
	ctts *movctts
	stsc *movstsc
	stsz *movstsz
	stco *movstco
	stss *movstss
}

type fragEntry struct {
	time       uint64
	moofOffset uint64
}

type movtfra struct {
	frags []fragEntry
}
