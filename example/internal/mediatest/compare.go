package mediatest

import (
	"strings"
	"testing"
)

// DecodedHash decodes one stream and returns the checksum of the raw samples.
//
// This is the assertion that actually proves a remux is correct. A container
// test that only compares packet counts passes just as happily when the
// payloads were truncated, reordered or had their parameter sets dropped;
// decoding to raw frames and hashing those does not. The stream selector is
// the ffmpeg one, "v:0" or "a:0".
func (tools Tools) DecodedHash(t *testing.T, path, stream string) string {
	t.Helper()
	args := FFmpegArgs("-i", path, "-map", "0:"+stream)
	if strings.HasPrefix(stream, "v") {
		args = append(args, "-c:v", "rawvideo", "-pix_fmt", "yuv420p")
	} else {
		args = append(args, "-c:a", "pcm_s16le")
	}
	args = append(args, "-f", "md5", "-")
	out := tools.run(t, tools.FFmpeg, args...)
	return strings.TrimSpace(string(out))
}

// AssertSameDecoded fails when two files do not decode to identical samples.
// Both files have to hold the same stream kind at the same index.
func (tools Tools) AssertSameDecoded(t *testing.T, want, got, stream string) {
	t.Helper()
	wantHash := tools.DecodedHash(t, want, stream)
	gotHash := tools.DecodedHash(t, got, stream)
	if wantHash != gotHash {
		t.Errorf("stream %s decodes differently after the round trip\n  %s: %s\n  %s: %s",
			stream, want, wantHash, got, gotHash)
	}
}

// Decodable fails when ffmpeg cannot decode the whole file without errors.
// ffmpeg is lenient by default, so the strict flags matter: without them a
// file with a broken index or a truncated last frame still "works".
func (tools Tools) AssertDecodable(t *testing.T, path string) {
	t.Helper()
	_, stderr, err := tools.runErr(tools.FFmpeg,
		"-hide_banner", "-loglevel", "warning", "-nostdin",
		"-xerror", "-err_detect", "explode",
		"-i", path, "-f", "null", "-")
	if err != nil {
		t.Errorf("ffmpeg cannot decode %s: %v\n%s", path, err, stderr)
		return
	}
	if stderr != "" {
		t.Errorf("ffmpeg reported problems decoding %s:\n%s", path, stderr)
	}
}

// AssertMonotonicDts fails when decode timestamps are not non decreasing,
// which every container requires and a muxer that mishandles b-frames breaks.
func AssertMonotonicDts(t *testing.T, packets []Packet, what string) {
	t.Helper()
	for i := 1; i < len(packets); i++ {
		if packets[i].Dts() < packets[i-1].Dts() {
			t.Errorf("%s: dts goes backwards at packet %d: %g after %g",
				what, i, packets[i].Dts(), packets[i-1].Dts())
			return
		}
	}
}

// AssertSameTimestamps fails when two packet lists do not carry the same
// presentation times, within tolerance. GoMediaUtils rounds timestamps to
// milliseconds on the way through, so an exact comparison would be wrong.
func AssertSameTimestamps(t *testing.T, want, got []Packet, tolerance float64, what string) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: %d packets after the round trip, want %d", what, len(got), len(want))
		return
	}
	for i := range want {
		if diff := want[i].Pts() - got[i].Pts(); diff > tolerance || diff < -tolerance {
			t.Errorf("%s: packet %d pts %g, want %g (tolerance %g)",
				what, i, got[i].Pts(), want[i].Pts(), tolerance)
			return
		}
	}
}
