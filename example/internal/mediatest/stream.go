package mediatest

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"testing"
	"time"
)

// Proc is a running ffmpeg process a test started in the background, such as
// a publisher pushing into a GoMediaUtils server.
type Proc struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stderr *bytes.Buffer
	// done is closed when the process has exited, so both Wait and the
	// cleanup can observe it; err is safe to read once it is closed
	done chan struct{}
	err  error
}

// Start launches ffmpeg in the background. The process is killed when the
// test ends, so a hung publisher cannot outlive its test.
func (tools Tools) Start(t *testing.T, args ...string) *Proc {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	cmd := exec.CommandContext(ctx, tools.FFmpeg, args...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start ffmpeg: %v", err)
	}
	p := &Proc{cmd: cmd, cancel: cancel, stderr: &errBuf, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-p.done
	})
	return p
}

// Wait blocks until the process exits and fails the test when it did not
// finish cleanly.
func (p *Proc) Wait(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		if p.err != nil {
			t.Fatalf("ffmpeg exited with %v\n%s", p.err, p.stderr.String())
		}
	case <-time.After(commandTimeout):
		p.cancel()
		t.Fatalf("ffmpeg did not finish in %v\n%s", commandTimeout, p.stderr.String())
	}
}

// Stop ends the process without caring how it exited, for a publisher that a
// test deliberately cuts short.
func (p *Proc) Stop() {
	p.cancel()
	<-p.done
}

// Stderr is what ffmpeg reported, for error messages.
func (p *Proc) Stderr() string { return p.stderr.String() }

// PublishOpts controls how a test publishes into a server.
type PublishOpts struct {
	// Realtime paces the push at wall clock speed, the way a live encoder
	// does, so a player has time to connect.
	Realtime bool
	// Loop restarts the input when it runs out. A server drops a live path
	// the moment its publisher disconnects, so a test that has to connect,
	// negotiate and then record for a while needs the stream to outlast the
	// clip rather than a clip long enough to guess the timing right.
	Loop bool
}

// PublishRTMP pushes a file into an rtmp server without transcoding.
func (tools Tools) PublishRTMP(t *testing.T, src, url string, opts PublishOpts) *Proc {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if opts.Loop {
		args = append(args, "-stream_loop", "-1")
	}
	if opts.Realtime {
		args = append(args, "-re")
	}
	args = append(args, "-i", src, "-c", "copy", "-f", "flv", url)
	return tools.Start(t, args...)
}

// PublishRTSP pushes a file into an rtsp server over tcp.
func (tools Tools) PublishRTSP(t *testing.T, src, url string, opts PublishOpts) *Proc {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if opts.Loop {
		args = append(args, "-stream_loop", "-1")
	}
	if opts.Realtime {
		args = append(args, "-re")
	}
	args = append(args, "-i", src, "-c", "copy",
		"-f", "rtsp", "-rtsp_transport", "tcp", url)
	return tools.Start(t, args...)
}

// FreePort asks the kernel for a port that is free right now, so parallel
// tests do not collide on a hard coded one.
func FreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// FreeUDPPortPair reserves an even udp port whose successor is also free,
// the shape an rtp/rtcp pair has to have.
func FreeUDPPortPair(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		a, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve a udp port: %v", err)
		}
		port := a.LocalAddr().(*net.UDPAddr).Port
		a.Close()
		if port%2 != 0 {
			continue
		}
		b, err := net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", port+1))
		if err != nil {
			continue
		}
		b.Close()
		return port
	}
	t.Fatal("could not find a free even udp port with a free successor")
	return 0
}

// WaitFor polls cond until it holds or the deadline passes. Network tests
// have to wait for a client to connect and announce itself; polling a
// condition beats sleeping a guessed amount of time.
func WaitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// waitPort reports whether something started listening on addr in time.
func waitPort(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// WaitPort waits until something is listening on addr.
func WaitPort(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	WaitFor(t, fmt.Sprintf("a listener on %s", addr), timeout, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		c.Close()
		return true
	})
}
