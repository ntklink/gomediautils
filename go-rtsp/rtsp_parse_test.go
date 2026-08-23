package rtsp

import (
	"strings"
	"testing"
)

func TestRequestParseMalformedHeaderLines(t *testing.T) {
	raw := "OPTIONS rtsp://127.0.0.1/live RTSP/1.0\r\nCSeq: 2\r\nthis-line-has-no-colon\r\nUser-Agent: test\r\n\r\n"
	req := RtspRequest{} // nil Fileds on purpose
	n, err := req.parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if n != len(raw) {
		t.Fatalf("consumed %d, want %d", n, len(raw))
	}
	if req.Method != OPTIONS || req.Fileds[CSeq] != "2" || req.Fileds["User-Agent"] != "test" {
		t.Fatalf("unexpected request %+v", req)
	}
}

func TestResponseParseNilFieldsAndBody(t *testing.T) {
	body := "v=0\r\n"
	raw := "RTSP/1.0 200 OK\r\nCSeq: 3\r\nbogus line\r\nContent-Length: " + itoa(len(body)) + "\r\n\r\n" + body
	res := RtspResponse{}
	n, err := res.parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if n != len(raw) || res.Body != body || res.StatusCode != 200 {
		t.Fatalf("unexpected response n=%d %+v", n, res)
	}
	// need more data for incomplete body
	res2 := RtspResponse{}
	if _, err := res2.parse(raw[:len(raw)-2]); err != errNeedMore {
		t.Fatalf("want errNeedMore, got %v", err)
	}
	// illegal content length must not panic
	res3 := RtspResponse{}
	if _, err := res3.parse("RTSP/1.0 200 OK\r\nContent-Length: abc\r\n\r\n"); err == nil {
		t.Fatalf("want error for bad content length")
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(i))
}

func fmtInt(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func TestClientServerMessageWhitespaceOnly(t *testing.T) {
	cli, err := NewRtspClient("rtsp://u:p@127.0.0.1/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Input([]byte("   ")); err != nil {
		t.Fatalf("whitespace input should not fail: %v", err)
	}
	// response with a header line lacking ':' and a nil response handler
	if err := cli.Input([]byte("RTSP/1.0 200 OK\r\nCSeq: 1\r\nnocolon\r\n\r\n")); err != nil {
		t.Fatalf("input failed: %v", err)
	}
	// interleaved data for tracks that were never SETUP must not panic
	cli.tracks["video"] = NewVideoTrack(NewVideoCodec("H264", 96, 90000))
	if err := cli.Input([]byte{'$', 0, 0, 2, 1, 2}); err != nil {
		t.Fatalf("interleaved input failed: %v", err)
	}

	srv, err := NewRtspServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.tracks["video"] = NewVideoTrack(NewVideoCodec("H264", 96, 90000))
	if err := srv.Input([]byte{'$', 0, 0, 2, 1, 2}); err != nil {
		t.Fatalf("server interleaved input failed: %v", err)
	}
	if err := srv.Input([]byte("RTSP/1.0 200 OK\r\nCSeq: 1\r\n\r\n")); err != nil {
		t.Fatalf("server response input failed: %v", err)
	}
}

func TestUnAuthRetryLimit(t *testing.T) {
	cli, err := NewRtspClient("rtsp://user:wrong@127.0.0.1/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	cli.SetOutput(func(b []byte) error { sent++; return nil })
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	unauth := []byte("RTSP/1.0 401 Unauthorized\r\nCSeq: 1\r\nWWW-Authenticate: Digest realm=\"r\", nonce=\"abc\"\r\n\r\n")
	if err := cli.Input(unauth); err != nil {
		t.Fatalf("first 401 should retry: %v", err)
	}
	if sent != 2 {
		t.Fatalf("expected retry to be sent, sent=%d", sent)
	}
	if err := cli.Input(unauth); err == nil {
		t.Fatalf("second 401 must return an error instead of looping")
	}
	if sent != 2 {
		t.Fatalf("no further retries expected, sent=%d", sent)
	}
	// unknown scheme must not panic
	cli2, _ := NewRtspClient("rtsp://user:pw@127.0.0.1/live", nil)
	cli2.SetOutput(func(b []byte) error { return nil })
	_ = cli2.Start()
	if err := cli2.Input([]byte("RTSP/1.0 401 Unauthorized\r\nCSeq: 1\r\nWWW-Authenticate: Bearer xyz\r\n\r\n")); err == nil {
		t.Fatalf("unknown auth scheme must error")
	}
}

func TestTransportDecodeRobust(t *testing.T) {
	tr := NewRtspTransport()
	if err := tr.DecodeString("RTP/AVP;unicast;client_port=3456-3457;mode=\"PLAY\";bogus"); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if tr.Proto != UDP || tr.Client_ports[0] != 3456 || tr.Client_ports[1] != 3457 || tr.mode != "PLAY" {
		t.Fatalf("unexpected transport %+v", tr)
	}
	tr2 := NewRtspTransport()
	if err := tr2.DecodeString("RTP/AVP/TCP;unicast;interleaved=2-3;mode"); err == nil {
		t.Fatalf("mode without value should fail")
	}
	tr3 := NewRtspTransport()
	if err := tr3.DecodeString("RTP/AVP;unicast;client_port=abc"); err == nil {
		t.Fatalf("bad client_port should fail")
	}
	tr4 := NewRtspTransport()
	if err := tr4.DecodeString("RTP/AVP/TCP;unicast;interleaved"); err == nil {
		t.Fatalf("interleaved without value should fail")
	}
	// unknown parameters without a value are ignored rather than rejected
	tr5 := NewRtspTransport()
	if err := tr5.DecodeString("RTP/AVP/TCP;unicast;interleaved=0-1;ssrc"); err != nil {
		t.Fatalf("unknown valueless parameter should be ignored: %v", err)
	}
	if tr5.Interleaved[0] != 0 || tr5.Interleaved[1] != 1 {
		t.Fatalf("unexpected interleaved %+v", tr5.Interleaved)
	}
}

func TestRangeParseRobust(t *testing.T) {
	if _, err := parseRange("npt"); err == nil {
		t.Fatalf("range without '=' must fail")
	}
	rt, err := parseRange("npt=now-")
	if err != nil || rt.begin != -1 || rt.end != -1 {
		t.Fatalf("npt=now- parse: %v %+v", err, rt)
	}
	rt, err = parseRange("npt=1.5-10")
	if err != nil || rt.begin != 1500 || rt.end != 10000 {
		t.Fatalf("npt parse: %v %+v", err, rt)
	}
	rt, err = parseRange("clock=20230101T000000Z-20230101T000010Z")
	if err != nil || rt.end-rt.begin != 10000 {
		t.Fatalf("clock parse: %v %+v", err, rt)
	}
	if _, err := parseRange("clock=garbage"); err == nil {
		t.Fatalf("bad clock must fail")
	}
	if _, err := parseRange("smpte=0:10:20-"); err == nil {
		t.Fatalf("unsupported range type must fail")
	}
	if s := (RangeTime{rangeType: RANGE_NPT, begin: 1005, end: -1}).EncodeString(); s != "npt=1.005-" {
		t.Fatalf("encode npt: %s", s)
	}
}

func TestAuthParseRobust(t *testing.T) {
	if _, err := createAuthByAuthenticate("Bearer"); err == nil {
		t.Fatalf("unknown scheme must return error")
	}
	b := &basicAuth{userName: "u", passwd: "p"}
	if b.check("Basic") || b.check("") {
		t.Fatalf("truncated basic auth must not match")
	}
	if !b.check(b.authenticateInfo()) {
		t.Fatalf("basic auth round trip failed")
	}
	d := &digestAuth{}
	d.decode("Digest realm=\"live\", nonce=\"abcdef\", stale=FALSE, junk")
	if d.realm != "live" || d.nonce != "abcdef" {
		t.Fatalf("digest decode %+v", d)
	}
	d.decode("Digest nonce=\"\"")
	if d.nonce != "" {
		t.Fatalf("empty nonce should be accepted: %q", d.nonce)
	}
	if d.check("Digest") || d.check("Digest username=\"u\", realm") {
		t.Fatalf("truncated digest must not match")
	}
	d.setUserInfo("u", "p")
	d.setMethod("DESCRIBE")
	d.setUri("rtsp://x/y")
	d.nonce = "n1"
	if !d.check(d.authenticateInfo()) {
		t.Fatalf("digest round trip failed")
	}
}

func TestRtpInfoEncodeDecode(t *testing.T) {
	info := NewRtpInfo("rtsp://h/track0", 123)
	if s := info.EncodeString(); s != "url=rtsp://h/track0;seq=123" {
		t.Fatalf("encode: %s", s)
	}
	info.Rtptime = 456
	if s := info.EncodeString(); s != "url=rtsp://h/track0;seq=123;rtptime=456" {
		t.Fatalf("encode with rtptime: %s", s)
	}
	var dec RtpInfo
	dec.Decode("url=rtsp://h/track1;seq=7;rtptime=99;noequals")
	if dec.Url != "rtsp://h/track1" || dec.Seq != 7 || dec.Rtptime != 99 {
		t.Fatalf("decode: %+v", dec)
	}
	dec.Decode("url")
	if dec.Rtptime != -1 {
		t.Fatalf("rtptime should be absent")
	}
}

func TestCodecNameMapping(t *testing.T) {
	if cid, ok := GetCodecIdByEncodeName("PCMU"); !ok || cid != RTSP_CODEC_G711U {
		t.Fatalf("pcmu must map to G711U")
	}
	if cid, ok := GetCodecIdByEncodeName("pcma"); !ok || cid != RTSP_CODEC_G711A {
		t.Fatalf("pcma must map to G711A")
	}
	if _, ok := GetCodecIdByEncodeName("JPEG"); ok {
		t.Fatalf("jpeg must be unknown")
	}
	if GetEncodeNameByCodecId(RTSP_CODEC_G711U) != "PCMU" || GetEncodeNameByCodecId(RTSP_CODEC_G711A) != "PCMA" {
		t.Fatalf("encode name mismatch")
	}
	if GetEncodeNameByCodecId(RTSP_CODEC_UNKNOWN) != "" {
		t.Fatalf("unknown codec id must give empty name")
	}
	// constructing a track with an unknown codec must not panic
	tr := NewMetaTrack(NewApplicatioCodec("vnd.onvif.metadata", 107))
	if tr.Codec.Cid != RTSP_CODEC_UNKNOWN {
		t.Fatalf("unexpected cid %d", tr.Codec.Cid)
	}
	if err := tr.Input([]byte{0x80, 0x6b, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 1}, false); err != nil {
		t.Fatalf("input on unknown codec track: %v", err)
	}
}

func TestDescribeSkipsUnsupportedTracks(t *testing.T) {
	cli, err := NewRtspClient("rtsp://127.0.0.1/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	cli.SetOutput(func(b []byte) error { out = append(out, b); return nil })
	sdpBody := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=x\r\na=control:*\r\n" +
		"m=video 0 RTP/AVP 26\r\na=rtpmap:26 JPEG/90000\r\na=control:track0\r\n" +
		"m=application 0 RTP/AVP 107\r\na=rtpmap:107 vnd.onvif.metadata/90000\r\na=control:track1\r\n" +
		"m=audio 0 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=control:track2\r\n"
	res := &RtspResponse{StatusCode: 200, Fileds: make(HeadFiled), Body: sdpBody}
	if err := cli.handleDescribe(res); err != nil {
		t.Fatalf("handleDescribe: %v", err)
	}
	if _, found := cli.tracks["video"]; found {
		t.Fatalf("jpeg track should be skipped")
	}
	if _, found := cli.tracks["application"]; found {
		t.Fatalf("application track should be skipped")
	}
	audio, found := cli.tracks["audio"]
	if !found || audio.Codec.Cid != RTSP_CODEC_G711U {
		t.Fatalf("audio track missing or wrong codec: %+v", audio)
	}
	if len(out) != 1 || !strings.Contains(string(out[0]), "SETUP rtsp://127.0.0.1/live/track2") {
		t.Fatalf("expected SETUP for audio track, got %q", out)
	}
	if cli.setupStep != 3 {
		t.Fatalf("setupStep=%d want 3", cli.setupStep)
	}
	// setup reply -> PLAY
	out = nil
	if err := cli.handleSetup(&RtspResponse{StatusCode: 200, Fileds: HeadFiled{Session: "abc;timeout", Transport: "RTP/AVP/TCP;unicast;interleaved=4-5"}}); err != nil {
		t.Fatalf("handleSetup: %v", err)
	}
	if len(out) != 1 || !strings.HasPrefix(string(out[0]), "PLAY ") {
		t.Fatalf("expected PLAY, got %q", out)
	}
}

func TestServerOptionsReportBadValues(t *testing.T) {
	if _, err := NewRtspServer(nil, WithAuthType("Bearer")); err == nil {
		t.Fatalf("an unknown auth type must be reported instead of leaving the server open")
	}
	if _, err := NewRtspServer(nil, WithUserInfo("user", "")); err == nil {
		t.Fatalf("an empty password must be reported")
	}
	srv, err := NewRtspServer(nil, WithUserInfo("user", "pass"), WithAuthType("Basic"))
	if err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	if srv.auth == nil {
		t.Fatalf("authentication was requested but not configured")
	}
	// a request without credentials is answered with 401
	var out [][]byte
	srv.SetOutput(func(b []byte) error { out = append(out, b); return nil })
	if _, err := srv.handleRequest([]byte("OPTIONS rtsp://127.0.0.1/live RTSP/1.0\r\nCSeq: 1\r\n\r\n")); err != nil {
		t.Fatalf("handle request: %v", err)
	}
	if len(out) != 1 || !strings.Contains(string(out[0]), "401") {
		t.Fatalf("expected a 401 response, got %q", out)
	}
}

func TestServerWithoutHandleAnswersRequests(t *testing.T) {
	srv, err := NewRtspServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	srv.SetOutput(func(b []byte) error { out = append(out, b); return nil })
	// a nil handle used to be dereferenced here
	if err := srv.Input([]byte("OPTIONS rtsp://127.0.0.1/live RTSP/1.0\r\nCSeq: 1\r\n\r\n")); err != nil {
		t.Fatalf("options: %v", err)
	}
	if len(out) != 1 || !strings.HasPrefix(string(out[0]), "RTSP/1.0 200") {
		t.Fatalf("unexpected response %q", out)
	}
}

func TestServerRejectsIllegalRange(t *testing.T) {
	srv, err := NewRtspServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	srv.SetOutput(func(b []byte) error { out = append(out, b); return nil })
	req := "PLAY rtsp://127.0.0.1/live RTSP/1.0\r\nCSeq: 3\r\nRange: npt=abc-\r\n\r\n"
	if err := srv.Input([]byte(req)); err != nil {
		t.Fatalf("play: %v", err)
	}
	if len(out) != 1 || !strings.Contains(string(out[0]), "400") {
		t.Fatalf("expected a 400 response, got %q", out)
	}
}

func TestClientDigestChallengeWithoutNonce(t *testing.T) {
	cli, err := NewRtspClient("rtsp://user:pw@127.0.0.1/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	cli.SetOutput(func(b []byte) error { return nil })
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	unauth := []byte("RTSP/1.0 401 Unauthorized\r\nCSeq: 1\r\nWWW-Authenticate: Digest realm=\"live\"\r\n\r\n")
	if err := cli.Input(unauth); err == nil {
		t.Fatalf("a digest challenge without a nonce must be reported")
	}
}

func TestResponseIllegalStatusCode(t *testing.T) {
	var res RtspResponse
	if _, err := res.parse("RTSP/1.0 OK OK\r\nCSeq: 1\r\n\r\n"); err == nil {
		t.Fatalf("a non numeric status code must be reported")
	}
}

func TestRtpInfoDecodeErrors(t *testing.T) {
	var info RtpInfo
	if err := info.Decode("url=rtsp://h/t0;seq=abc"); err == nil {
		t.Fatalf("an illegal seq must be reported")
	}
	if err := info.Decode("url=rtsp://h/t0;seq=7;rtptime=zz"); err == nil {
		t.Fatalf("an illegal rtptime must be reported")
	}
	if err := info.Decode("url=rtsp://h/t0;seq=7;rtptime=99"); err != nil {
		t.Fatalf("valid RTP-Info rejected: %v", err)
	}
}

func TestNptRangeReportsIllegalTime(t *testing.T) {
	if _, err := parseRange("npt=abc-"); err == nil {
		t.Fatalf("an illegal npt begin must be reported")
	}
	if _, err := parseRange("npt=0-xyz"); err == nil {
		t.Fatalf("an illegal npt end must be reported")
	}
	rt, err := parseRange("npt=0:10:20-")
	if err != nil || rt.begin != 620000 {
		t.Fatalf("h:mm:ss npt: %v %+v", err, rt)
	}
}

func TestTrackWithoutPacketSink(t *testing.T) {
	track := NewVideoTrack(NewVideoCodec("H264", 96, 90000))
	track.OnPacket(nil)
	// packing must report the missing sink instead of crashing
	if err := track.WriteSample(RtspSample{Sample: []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88}, Timestamp: 0}); err == nil {
		t.Fatalf("writing to a track without an output must be reported")
	}
	if err := track.SendReport(); err == nil {
		t.Fatalf("sending a report without an output must be reported")
	}
}
