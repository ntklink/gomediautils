package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ntklink/gomediautils/example/internal/mediatest"
	"github.com/ntklink/gomediautils/go-mp4"
)

// The segments are built on demand, so the test drives the server the way a
// player does: fetch the playlist, then fetch every segment it names. ffmpeg
// does both, and what it decodes has to be the clip that went in.
func TestServeMP4AsHLS(t *testing.T) {
	tools := mediatest.Require(t)

	dir := t.TempDir()
	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 6, GOP: 25, BFrames: 2,
	})
	copyFile(t, src, filepath.Join(dir, "movie.mp4"))

	base := serve(t, &Server{Dir: dir, SegmentSeconds: 2})

	resp := get(t, base+"movie.m3u8")
	for _, want := range []string{"#EXTM3U", "#EXTINF:", "#EXT-X-ENDLIST", "sequence-0.ts?start="} {
		if !strings.Contains(resp, want) {
			t.Errorf("playlist is missing %q:\n%s", want, resp)
		}
	}
	if segments := strings.Count(resp, "#EXTINF:"); segments < 2 {
		t.Fatalf("the playlist has %d segments, expected several:\n%s", segments, resp)
	}

	tools.AssertDecodable(t, base+"movie.m3u8")
	srcVideo, _ := tools.Probe(t, src).Video()
	out := tools.Probe(t, base+"movie.m3u8")
	gotVideo, ok := out.Video()
	if !ok {
		t.Fatal("ffmpeg found no video in the presentation")
	}
	if gotVideo.Frames() != srcVideo.Frames() {
		t.Errorf("%d frames playable over hls, want %d", gotVideo.Frames(), srcVideo.Frames())
	}
	tools.AssertSameDecoded(t, src, base+"movie.m3u8", "v:0")
}

// Every segment has to start with a keyframe, or a player that starts there,
// or that seeks there, gets nothing until the next one. This is the property
// the whole cut-on-sync-samples design exists for, so it is checked directly
// rather than inferred from the presentation playing.
func TestEverySegmentStartsWithAKeyframe(t *testing.T) {
	tools := mediatest.Require(t)

	dir := t.TempDir()
	src := tools.MakeClip(t, mediatest.Clip{
		Container: "mp4", Video: "libx264", Audio: "aac", Seconds: 6, GOP: 25,
	})
	copyFile(t, src, filepath.Join(dir, "movie.mp4"))

	base := serve(t, &Server{Dir: dir, SegmentSeconds: 2})
	playlist := get(t, base+"movie.m3u8")

	uris := segmentURIs(playlist)
	if len(uris) < 2 {
		t.Fatalf("the playlist has %d segments, expected several", len(uris))
	}
	for i, uri := range uris {
		seg := filepath.Join(t.TempDir(), fmt.Sprintf("seg%d.ts", i))
		download(t, base+uri, seg)

		packets := tools.Packets(t, seg, "v:0")
		if len(packets) == 0 {
			t.Errorf("segment %d has no video", i)
			continue
		}
		if !packets[0].Key() {
			t.Errorf("segment %d starts with a non keyframe, so a player cannot start there", i)
		}
		tools.AssertDecodable(t, seg)
	}
}

// The cutting itself is arithmetic, so it is worth checking on its own
// against the awkward inputs a real file produces.
func TestSegments(t *testing.T) {
	sync := func(dts ...uint64) []mp4.SyncSample {
		out := make([]mp4.SyncSample, len(dts))
		for i, d := range dts {
			out[i] = mp4.SyncSample{Dts: d}
		}
		return out
	}

	cases := []struct {
		name   string
		table  []mp4.SyncSample
		end    uint64
		target int
		want   []segment
	}{
		{
			name: "no sync samples at all", table: nil, end: 5000, target: 2, want: nil,
		},
		{
			name:  "one gop shorter than the target",
			table: sync(0), end: 1000, target: 2,
			want: []segment{{0, 1000}},
		},
		{
			name:  "cuts land on sync samples, never between",
			table: sync(0, 1000, 2000, 3000, 4000), end: 5000, target: 2,
			want: []segment{{0, 2000}, {2000, 4000}, {4000, 5000}},
		},
		{
			name:  "a gop longer than the target is not split",
			table: sync(0, 9000), end: 12000, target: 2,
			want: []segment{{0, 9000}, {9000, 12000}},
		},
		{
			name:  "the last sync sample is not a segment of length zero",
			table: sync(0, 2000, 4000), end: 4000, target: 2,
			want: []segment{{0, 2000}, {2000, 4000}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Segments(tc.table, tc.end, tc.target)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("segment %d is %v, want %v", i, got[i], tc.want[i])
				}
			}
			// whatever the cut, the segments have to tile the timeline
			for i := 1; i < len(got); i++ {
				if got[i].start != got[i-1].end {
					t.Errorf("segment %d starts at %d but the one before ends at %d",
						i, got[i].start, got[i-1].end)
				}
			}
		})
	}
}

// A request must not be able to name a file outside the served directory.
func TestServerStaysInsideItsDirectory(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(filepath.Dir(dir), "secret.mp4")
	if err := os.WriteFile(secret, []byte("not yours"), 0o666); err != nil {
		t.Fatal(err)
	}

	base := serve(t, &Server{Dir: dir})
	resp, err := http.Get(base + "../secret.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("the server served a presentation for a file outside its directory")
	}
}

func serve(t *testing.T, s *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/vod/", s)
	server := &http.Server{Handler: mux}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	return fmt.Sprintf("http://%s/vod/", ln.Addr().String())
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s\n%s", url, resp.Status, body)
	}
	return string(body)
}

func download(t *testing.T, url, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(get(t, url)), 0o666); err != nil {
		t.Fatal(err)
	}
}

func segmentURIs(playlist string) []string {
	var out []string
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o666); err != nil {
		t.Fatal(err)
	}
}
