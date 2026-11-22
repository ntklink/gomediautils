package rtsp

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestRtspBufferLimitEnforcement(t *testing.T) {
	oversized := bytes.Repeat([]byte("A"), maxRtspBufferSize+1)

	cli, err := NewRtspClient("rtsp://127.0.0.1/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Input(oversized); err != errRtspBufferOverflow {
		t.Fatalf("client expected errRtspBufferOverflow, got %v", err)
	}
	if len(cli.cache) != 0 {
		t.Fatalf("client cache not reset after overflow: len=%d", len(cli.cache))
	}

	srv, err := NewRtspServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Input(oversized); err != errRtspBufferOverflow {
		t.Fatalf("server expected errRtspBufferOverflow, got %v", err)
	}
	if srv.buffer.Len() != 0 {
		t.Fatalf("server buffer not reset after overflow: len=%d", srv.buffer.Len())
	}
}

func TestHandleSetupWithNilClientHandle(t *testing.T) {
	cli, err := NewRtspClient("rtsp://127.0.0.1/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	cli.SetOutput(func(b []byte) error {
		out = append(out, append([]byte(nil), b...))
		return nil
	})
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	// OPTIONS response
	optResp := "RTSP/1.0 200 OK\r\nCSeq: 1\r\nPublic: OPTIONS, DESCRIBE, SETUP, PLAY, TEARDOWN\r\n\r\n"
	if err := cli.Input([]byte(optResp)); err != nil {
		t.Fatalf("options: %v", err)
	}
	// DESCRIBE response
	sdpBody := "v=0\r\no=- 0 0 IN IP4 0.0.0.0\r\ns=test\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:track0\r\n"
	descResp := "RTSP/1.0 200 OK\r\nCSeq: 2\r\nContent-Type: application/sdp\r\nContent-Length: " +
		string(bytes.TrimSpace([]byte("132"))) + "\r\n\r\n" + sdpBody
	// Use exact length
	res := RtspResponse{
		Version:    RTSP_1_0,
		StatusCode: OK,
		Fileds:     HeadFiled{CSeq: "2", ContentType: "application/sdp"},
		Body:       sdpBody,
	}
	_ = descResp
	if err := cli.Input([]byte(res.Encode())); err != nil {
		t.Fatalf("describe: %v", err)
	}
	// SETUP response
	setupResp := "RTSP/1.0 200 OK\r\nCSeq: 3\r\nSession: 123456;timeout=45\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n\r\n"
	if err := cli.Input([]byte(setupResp)); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if cli.sessionId != "123456" || cli.timeout != 45 {
		t.Fatalf("session not parsed with nil handle: id=%q timeout=%d", cli.sessionId, cli.timeout)
	}
	// PLAY request must carry Session header
	lastOut := string(out[len(out)-1])
	if !strings.Contains(lastOut, "PLAY ") || !strings.Contains(lastOut, "Session: 123456") {
		t.Fatalf("expected PLAY with Session header, got:\n%s", lastOut)
	}
}

func TestRtspClientKeepAliveConcurrentRace(t *testing.T) {
	cli, err := NewRtspClient("rtsp://127.0.0.1/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	cli.SetOutput(func(b []byte) error { return nil })
	cli.state = STATE_Playing
	cli.sessionId = "sess1"

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = cli.KeepAlive(OPTIONS)
		}
	}()
	go func() {
		defer wg.Done()
		resp := []byte("RTSP/1.0 200 OK\r\nCSeq: 1\r\n\r\n")
		for i := 0; i < 50; i++ {
			_ = cli.Input(resp)
		}
	}()
	wg.Wait()
}

func TestAuthFixes(t *testing.T) {
	b := &basicAuth{}
	b.setRealm("my-realm")
	b.setUserInfo("u", "p")
	if got := b.wwwAuthenticate(); got != `Basic realm="my-realm"` {
		t.Fatalf("basic wwwAuthenticate=%q", got)
	}

	d := &digestAuth{}
	d.setUserInfo("alice", "secret")
	d.setRealm("live")
	d.setMethod(DESCRIBE)
	d.setUri("rtsp://host/stream")

	// Empty nonce must fail check
	if d.check(`Digest username="alice", nonce="", uri="rtsp://host/stream", response="00"`) {
		t.Fatal("digest check accepted empty server nonce")
	}

	n1 := d.createNonce()
	n2 := d.createNonce()
	if n1 == "" || n1 == n2 {
		t.Fatalf("nonces not random: %q vs %q", n1, n2)
	}

	// Missing username or nonce must fail
	validResp := d.makeResponse()
	if d.check(`Digest nonce="` + n2 + `", uri="rtsp://host/stream", response="` + validResp + `"`) {
		t.Fatal("accepted digest without username")
	}
	if d.check(`Digest username="alice", uri="rtsp://host/stream", response="` + validResp + `"`) {
		t.Fatal("accepted digest without nonce")
	}
	// Mismatched URI must fail and must not overwrite d.uri
	if d.check(`Digest username="alice", nonce="` + n2 + `", uri="rtsp://evil/other", response="` + validResp + `"`) {
		t.Fatal("accepted digest with mismatched uri")
	}
	if d.uri != "rtsp://host/stream" {
		t.Fatalf("digest.uri was mutated to %q", d.uri)
	}
	// Valid header must succeed
	if !d.check(d.authenticateInfo()) {
		t.Fatal("rejected valid digest authorization")
	}
}

func TestRtspHeaderCanonicalization(t *testing.T) {
	if RTPInfo != "RTP-Info" || UserAgent != "User-Agent" || CacheControl != "Cache-Control" {
		t.Fatalf("unexpected header constants: %q %q %q", RTPInfo, UserAgent, CacheControl)
	}
	raw := "RTSP/1.0 200 OK\r\ncseq: 7\r\nwww-authenticate: Basic realm=\"x\"\r\nrtp-info: url=track0;seq=1\r\nuser-agent: demo\r\n\r\n"
	var res RtspResponse
	if _, err := res.parseBytes([]byte(raw)); err != nil {
		t.Fatalf("parseBytes: %v", err)
	}
	if res.Fileds[CSeq] != "7" || res.Fileds[WWWAuthenticate] != `Basic realm="x"` ||
		res.Fileds[RTPInfo] != "url=track0;seq=1" || res.Fileds[UserAgent] != "demo" {
		t.Fatalf("canonicalization failed: %+v", res.Fileds)
	}
}
