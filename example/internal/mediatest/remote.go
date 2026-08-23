package mediatest

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Remote is an external streaming server the tests interoperate with.
//
// The ffmpeg tests put a real client on the other end of GoMediaUtils' servers.
// A remote server is the mirror image: it puts a real *server* on the other
// end of GoMediaUtils' clients. mediamtx is a good one to point at, its rtsp side
// is gortsplib and its rtmp side is its own, so neither shares any code or
// assumption with this library.
type Remote struct {
	// Addr is the server's IP. The host name is resolved once, up front:
	// statically linked ffmpeg builds crash resolving names themselves, and
	// an ip in the url sidesteps that as well as any dns flakiness in the
	// middle of a test.
	Addr     string
	Host     string // what the user configured, for messages
	RTMPPort int
	RTSPPort int
	HLSPort  int
	// RTPPort is the base of the udp rtp/rtcp pair a local server listens on.
	RTPPort int
	// Local says the server runs on this machine and was started by the
	// test. Only then can a test hold the delivery to an exact frame count:
	// over somebody else's link the rate is not the test's to control.
	Local bool
}

// RequireRemote returns the configured remote server, skipping the test when
// there is none. Set GOMEDIAUTILS_REMOTE to a host name or address, optionally
// with ports: "example.com" or "example.com:1935:8554:8888".
func RequireRemote(t *testing.T) Remote {
	t.Helper()
	spec := os.Getenv("GOMEDIAUTILS_REMOTE")
	if spec == "" {
		t.Skip("no remote streaming server configured; set GOMEDIAUTILS_REMOTE=<host> to run the interop tests")
	}
	r, err := parseRemote(spec)
	if err != nil {
		t.Fatalf("GOMEDIAUTILS_REMOTE=%q: %v", spec, err)
	}
	if err := r.reachable(); err != nil {
		t.Skipf("remote server %s is not reachable: %v", r.Host, err)
	}
	return r
}

func parseRemote(spec string) (Remote, error) {
	r := Remote{RTMPPort: 1935, RTSPPort: 8554, HLSPort: 8888}
	parts := strings.Split(spec, ":")
	r.Host = parts[0]
	for i, p := range parts[1:] {
		n, err := strconv.Atoi(p)
		if err != nil {
			return r, fmt.Errorf("port %q is not a number", p)
		}
		switch i {
		case 0:
			r.RTMPPort = n
		case 1:
			r.RTSPPort = n
		case 2:
			r.HLSPort = n
		}
	}
	if ip := net.ParseIP(r.Host); ip != nil {
		r.Addr = r.Host
		return r, nil
	}
	addrs, err := net.LookupHost(r.Host)
	if err != nil {
		return r, err
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			r.Addr = a
			return r, nil
		}
	}
	return r, fmt.Errorf("no ipv4 address for %s", r.Host)
}

func (r Remote) reachable() error {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(r.Addr, strconv.Itoa(r.RTMPPort)), 4*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

// RTMPAddr is the host:port to dial for rtmp.
func (r Remote) RTMPAddr() string {
	return net.JoinHostPort(r.Addr, strconv.Itoa(r.RTMPPort))
}

// RTSPAddr is the host:port to dial for rtsp.
func (r Remote) RTSPAddr() string {
	return net.JoinHostPort(r.Addr, strconv.Itoa(r.RTSPPort))
}

// RTMPURL builds a publish or play url for a stream path.
func (r Remote) RTMPURL(path string) string {
	return fmt.Sprintf("rtmp://%s/%s", r.RTMPAddr(), path)
}

// RTSPURL builds a publish or play url for a stream path.
func (r Remote) RTSPURL(path string) string {
	return fmt.Sprintf("rtsp://%s/%s", r.RTSPAddr(), path)
}

// HLSURL is the playlist url mediamtx serves for a stream path.
func (r Remote) HLSURL(path string) string {
	return fmt.Sprintf("http://%s:%d/%s/index.m3u8", r.Addr, r.HLSPort, path)
}

var pathCounter uint64

// UniquePath names a stream nobody else is using. The remote server is
// shared, so two runs (or two tests in one run) must not collide on a path.
//
// The name has two segments on purpose. An rtmp url carries the app in the
// connect command and the stream name in publish/play, so a single segment
// url leaves the stream name empty and the server has nothing to key the
// stream on. Two segments map cleanly onto both rtmp and rtsp.
func UniquePath(t *testing.T, prefix string) string {
	t.Helper()
	n := atomic.AddUint64(&pathCounter, 1)
	return fmt.Sprintf("live/gomediautils-%s-%d-%d", prefix, os.Getpid(), n)
}

// WaitStreamReady blocks until the remote server answers a play request for
// the url, which is how a test knows a publisher has finished announcing.
//
// The url has to use the same protocol the stream was published with. A
// server is free to keep its protocols in separate namespaces, and mediamtx
// answers 404 over rtsp for a path that only exists over rtmp unless it is
// configured to republish.
func (r Remote) WaitStreamReady(t *testing.T, tools Tools, url string, timeout time.Duration) {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error"}
	if strings.HasPrefix(url, "rtsp://") {
		args = append(args, "-rtsp_transport", "tcp")
	}
	// keep the probe cheap: it runs in a poll loop and only has to answer
	// "is there a stream here yet"
	args = append(args,
		"-analyzeduration", "2000000", "-probesize", "500000",
		"-i", url, "-show_entries", "stream=codec_type", "-of", "csv=p=0")
	WaitFor(t, "the remote server to serve "+url, timeout, func() bool {
		out, _, err := tools.runErr(tools.FFprobe, args...)
		return err == nil && len(out) > 0
	})
}
