package mediatest

import (
	"errors"
	"math/rand/v2"
	"net"
	"sync"
	"testing"
)

// LossyUDPProxy relays UDP datagrams between one client and target,
// dropping the given share of them in both directions. It returns the
// address clients connect to instead of target. A transport test put
// behind it has to recover the losses to produce the same output.
//
// SRT handshake packets always get through, so a test is about recovering
// media rather than about connection setup retries.
func LossyUDPProxy(t *testing.T, target string, loss float64) string {
	t.Helper()
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	taddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		t.Fatal(err)
	}
	back, err := net.DialUDP("udp", nil, taddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		front.Close()
		back.Close()
	})

	var mu sync.Mutex
	var client *net.UDPAddr
	keep := func(b []byte) bool {
		srtHandshake := len(b) >= 2 && b[0] == 0x80 && b[1] == 0x00
		return srtHandshake || rand.Float64() >= loss
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := front.ReadFromUDP(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				continue
			}
			mu.Lock()
			client = from
			mu.Unlock()
			if keep(buf[:n]) {
				back.Write(buf[:n])
			}
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := back.Read(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				// the target not listening yet shows up as a refused read;
				// the proxy keeps going like a real link would
				continue
			}
			mu.Lock()
			to := client
			mu.Unlock()
			if to != nil && keep(buf[:n]) {
				front.WriteToUDP(buf[:n], to)
			}
		}
	}()
	return front.LocalAddr().String()
}
