package mediatest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// mediamtxConfig is the smallest configuration that serves rtsp, rtmp and hls
// on the ports a test picked, and stays quiet unless something breaks.
// mediamtxConfig serves rtsp, rtmp and hls on the ports a test picked and
// binds nothing else.
//
// Every listener has to be named. mediamtx enables webrtc, srt and moq by
// default on fixed ports, so two instances, or one instance next to anything
// else on the machine, collide on a port the test never asked for.
const mediamtxConfig = `logLevel: error
api: no
metrics: no
pprof: no
playback: no
webrtc: no
srt: no
moq: no

rtsp: yes
rtspTransports: [tcp, udp]
rtspEncryption: "no"
rtspAddress: 127.0.0.1:%d
rtpAddress: 127.0.0.1:%d
rtcpAddress: 127.0.0.1:%d

rtmp: yes
rtmpEncryption: "no"
rtmpAddress: 127.0.0.1:%d

hls: yes
hlsAddress: 127.0.0.1:%d
hlsAlwaysRemux: yes
hlsVariant: mpegts

paths:
  all_others:
`

// RequireStreamingServer returns a streaming server for the interop tests.
//
// A local mediamtx is preferred: it makes the tests deterministic, so they can
// assert on exact frame counts rather than on "enough frames arrived". Point
// GOMEDIA_MEDIAMTX at the binary, or put it on PATH. Failing that,
// GOMEDIA_REMOTE names an already running server somewhere else, where the
// link is out of the test's control and the assertions have to be looser;
// Remote.Local says which one a test got.
func RequireStreamingServer(t *testing.T) Remote {
	t.Helper()
	if bin := findMediaMTX(); bin != "" {
		return startMediaMTX(t, bin)
	}
	if os.Getenv("GOMEDIA_REMOTE") != "" {
		return RequireRemote(t)
	}
	t.Skip("no streaming server available; install mediamtx (or set GOMEDIA_MEDIAMTX) to run the interop tests")
	return Remote{}
}

func findMediaMTX() string {
	if p := os.Getenv("GOMEDIA_MEDIAMTX"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	p, err := exec.LookPath("mediamtx")
	if err != nil {
		return ""
	}
	return p
}

// startMediaMTX runs a private mediamtx for the duration of the test.
//
// Finding a free port means binding one and letting go of it again, so
// between that and mediamtx binding it for real another test binary can take
// it. That is a race no amount of care in picking the port closes, so a
// server that fails to come up is retried on a fresh set rather than failing
// the test it was only scaffolding for.
func startMediaMTX(t *testing.T, bin string) Remote {
	t.Helper()
	const attempts = 5
	for i := 0; i < attempts; i++ {
		if r, ok := tryStartMediaMTX(t, bin, i == attempts-1); ok {
			return r
		}
	}
	t.Fatalf("mediamtx would not start on %d different sets of ports", attempts)
	return Remote{}
}

// tryStartMediaMTX starts one attempt. It reports false when the server did
// not come up, unless last is set, in which case it fails the test with the
// server's own log.
func tryStartMediaMTX(t *testing.T, bin string, last bool) (Remote, bool) {
	t.Helper()
	// the rtp/rtcp pair has to be consecutive and even/odd, and mediamtx
	// binds them whether or not a test asks for udp
	rtpPort := FreeUDPPortPair(t)
	r := Remote{
		Addr:     "127.0.0.1",
		Host:     "127.0.0.1",
		Local:    true,
		RTSPPort: FreePort(t),
		RTMPPort: FreePort(t),
		HLSPort:  FreePort(t),
		RTPPort:  rtpPort,
	}

	dir := t.TempDir()
	cfg := filepath.Join(dir, "mediamtx.yml")
	body := fmt.Sprintf(mediamtxConfig, r.RTSPPort, rtpPort, rtpPort+1, r.RTMPPort, r.HLSPort)
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatalf("write mediamtx config: %v", err)
	}
	logPath := filepath.Join(dir, "mediamtx.log")

	cmd := exec.Command(bin, cfg)
	cmd.Dir = dir
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create mediamtx log: %v", err)
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatalf("start mediamtx: %v", err)
	}

	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	stop := func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		log.Close()
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			if b, err := os.ReadFile(logPath); err == nil && len(b) > 0 {
				t.Logf("mediamtx log:\n%s", b)
			}
		}
	})

	for _, addr := range []string{r.RTMPAddr(), r.RTSPAddr()} {
		if !waitPort(addr, 10*time.Second) {
			if last {
				stop()
				b, _ := os.ReadFile(logPath)
				t.Fatalf("mediamtx never listened on %s\n%s", addr, b)
			}
			stop()
			return Remote{}, false
		}
	}
	return r, true
}
