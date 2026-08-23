package mediatest

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// Stream is the subset of an ffprobe stream description the tests assert on.
type Stream struct {
	Index      int    `json:"index"`
	CodecName  string `json:"codec_name"`
	CodecType  string `json:"codec_type"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	SampleRate string `json:"sample_rate"`
	Channels   int    `json:"channels"`
	NbFrames   string `json:"nb_read_frames"`
	NbPackets  string `json:"nb_read_packets"`
	Duration   string `json:"duration"`
	Tags       Tags   `json:"tags"`
}

// Tags are the container metadata ffprobe reports, for the tests that care
// what a muxer wrote into the headers rather than what it wrote into the
// samples.
type Tags struct {
	CreationTime string `json:"creation_time"`
	Language     string `json:"language"`
	HandlerName  string `json:"handler_name"`
}

// Frames returns the number of frames ffprobe could actually decode.
func (s Stream) Frames() int {
	n, _ := strconv.Atoi(s.NbFrames)
	return n
}

// Packets returns the number of packets ffprobe read out of the container.
func (s Stream) Packets() int {
	n, _ := strconv.Atoi(s.NbPackets)
	return n
}

// Format is the subset of an ffprobe format description the tests assert on.
type Format struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	NbStreams  int    `json:"nb_streams"`
	Tags       Tags   `json:"tags"`
}

// Seconds parses the duration, returning 0 when ffprobe could not determine
// one (which some containers legitimately do not carry).
func (f Format) Seconds() float64 {
	v, _ := strconv.ParseFloat(f.Duration, 64)
	return v
}

// Probe is what ffprobe reports about a file.
type Probe struct {
	Format  Format   `json:"format"`
	Streams []Stream `json:"streams"`
}

// Video returns the first video stream, or a zero Stream and false.
func (p Probe) Video() (Stream, bool) { return p.byType("video") }

// Audio returns the first audio stream, or a zero Stream and false.
func (p Probe) Audio() (Stream, bool) { return p.byType("audio") }

func (p Probe) byType(kind string) (Stream, bool) {
	for _, s := range p.Streams {
		if s.CodecType == kind {
			return s, true
		}
	}
	return Stream{}, false
}

// Probe runs ffprobe over a file and decodes every frame so that the reported
// frame counts reflect what a player would really get, not just what the
// container headers claim.
func (tools Tools) Probe(t *testing.T, path string) Probe {
	t.Helper()
	out := tools.run(t, tools.FFprobe,
		"-hide_banner", "-loglevel", "error",
		"-count_frames", "-count_packets",
		"-show_format", "-show_streams",
		"-print_format", "json", path)
	var p Probe
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("parse ffprobe output for %s: %v\n%s", path, err, out)
	}
	return p
}

// MustProbe is Probe plus the checks every "did gomedia write a real file"
// test wants: ffprobe recognised the container and found the streams.
func (tools Tools) MustProbe(t *testing.T, path string, wantStreams int) Probe {
	t.Helper()
	p := tools.Probe(t, path)
	if len(p.Streams) != wantStreams {
		t.Fatalf("%s: ffprobe found %d streams, want %d (format %q)",
			path, len(p.Streams), wantStreams, p.Format.FormatName)
	}
	return p
}

// RunFFprobe runs ffprobe over a url and reports whether it could open it.
// A test that expects a request to be refused needs the error, not a fatal.
func (tools Tools) RunFFprobe(url string) (stdout []byte, stderr string, err error) {
	return tools.runErr(tools.FFprobe,
		"-hide_banner", "-loglevel", "error",
		"-i", url, "-show_streams", "-of", "csv=p=0")
}

// Packet is one packet as ffprobe reports it, used to check timestamps.
type Packet struct {
	PtsTime  string `json:"pts_time"`
	DtsTime  string `json:"dts_time"`
	Flags    string `json:"flags"`
	SizeStr  string `json:"size"`
	PosStr   string `json:"pos"`
	Duration string `json:"duration_time"`
}

// Key reports whether the packet starts a random access point.
func (p Packet) Key() bool { return strings.HasPrefix(p.Flags, "K") }

// Pts is the presentation time in seconds.
func (p Packet) Pts() float64 { v, _ := strconv.ParseFloat(p.PtsTime, 64); return v }

// Dts is the decode time in seconds.
func (p Packet) Dts() float64 { v, _ := strconv.ParseFloat(p.DtsTime, 64); return v }

// Size is the packet's payload length in bytes.
func (p Packet) Size() int { n, _ := strconv.Atoi(p.SizeStr); return n }

// Pos is the byte offset the packet was read from, or -1 when the container
// does not report one.
func (p Packet) Pos() int {
	n, err := strconv.Atoi(p.PosStr)
	if err != nil {
		return -1
	}
	return n
}

// Packets lists the packets of one stream, selected the ffmpeg way ("v:0",
// "a:0"). Timestamps are what a container library gets wrong most easily, so
// tests compare them between the source and what gomedia wrote.
func (tools Tools) Packets(t *testing.T, path, stream string) []Packet {
	t.Helper()
	out := tools.run(t, tools.FFprobe,
		"-hide_banner", "-loglevel", "error",
		"-select_streams", stream, "-show_packets",
		"-print_format", "json", path)
	var doc struct {
		Packets []Packet `json:"packets"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("parse ffprobe packets for %s: %v\n%s", path, err, out)
	}
	return doc.Packets
}
