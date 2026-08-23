package rtmp

import (
	"bytes"
	"errors"
	"testing"

	"github.com/yapingcat/gomedia/go-codec"
)

// pump moves queued bytes between client and server until nothing is in flight
func pump(t *testing.T, cli *RtmpClient, srv *RtmpServerHandle, toSrv, toCli *bytes.Buffer) {
	t.Helper()
	for toSrv.Len() > 0 || toCli.Len() > 0 {
		if toSrv.Len() > 0 {
			b := append([]byte{}, toSrv.Bytes()...)
			toSrv.Reset()
			if err := srv.Input(b); err != nil {
				t.Fatal("server input:", err)
			}
		}
		if toCli.Len() > 0 {
			b := append([]byte{}, toCli.Bytes()...)
			toCli.Reset()
			if err := cli.Input(b); err != nil {
				t.Fatal("client input:", err)
			}
		}
	}
}

func TestLoopbackPublish(t *testing.T) {
	var toSrv, toCli bytes.Buffer

	srv := NewRtmpServerHandle()
	srv.SetOutput(func(b []byte) error { toCli.Write(b); return nil })
	var pubApp, pubStream string
	srv.OnPublish(func(app, streamName string) StatusCode {
		pubApp, pubStream = app, streamName
		return NETSTREAM_PUBLISH_START
	})

	cli := NewRtmpClient(WithEnablePublish(), WithComplexHandshake())
	cli.SetOutput(func(b []byte) error { toSrv.Write(b); return nil })
	var states []RtmpState
	cli.OnStateChange(func(s RtmpState) { states = append(states, s) })

	if err := cli.Start("rtmp://127.0.0.1:1935/live/stream1"); err != nil {
		t.Fatal(err)
	}
	pump(t, cli, srv, &toSrv, &toCli)

	if cli.GetState() != STATE_RTMP_PUBLISH_START {
		t.Fatalf("client state %d, want publish start (states %v)", cli.GetState(), states)
	}
	if srv.GetState() != STATE_RTMP_PUBLISH_START {
		t.Fatalf("server state %d", srv.GetState())
	}
	if pubApp != "live" || pubStream != "stream1" || srv.GetApp() != "live" || srv.GetStreamName() != "stream1" {
		t.Fatalf("server saw app %q stream %q", pubApp, pubStream)
	}

	// a ping from the server must be answered with a pong carrying the same value
	ping := makeUserControlMessage(PingRequest, 1234)
	if err := cli.Input(mustWrite(t, srv.userCtrlChan, ping, USER_CONTROL, 0, 0)); err != nil {
		t.Fatal(err)
	}
	got := &collected{}
	if err := srv.reader.readRtmpMessage(toSrv.Bytes(), got.on); err != nil {
		t.Fatal(err)
	}
	toSrv.Reset()
	if len(got.msgs) != 1 || got.msgs[0].msgtype != USER_CONTROL {
		t.Fatalf("expected a pong, got %d messages", len(got.msgs))
	}
	ev, err := decodeUserControlMsg(got.msgs[0].msg)
	if err != nil || ev.code != PingResponse || ev.data[0] != 1234 {
		t.Fatalf("bad pong %v %v", ev, err)
	}

	// the server advertises a window ack size, after receiving that many bytes the client must ack
	cli.peerWndAckSize = 4096
	cli.lastAckBytes = cli.reader.recvBytes
	payload := makePayload(3000)
	for i := 0; i < 2; i++ {
		if err := cli.Input(mustWrite(t, srv.userCtrlChan, payload, VIDEO, 1, uint32(i))); err != nil {
			t.Fatal(err)
		}
	}
	got = &collected{}
	if err := srv.reader.readRtmpMessage(toSrv.Bytes(), got.on); err != nil {
		t.Fatal(err)
	}
	toSrv.Reset()
	if len(got.msgs) != 1 || got.msgs[0].msgtype != ACKNOWLEDGEMENT {
		t.Fatalf("expected one acknowledgement, got %d messages", len(got.msgs))
	}

	// invalid chunk size must be rejected
	bad := mustWrite(t, srv.userCtrlChan, makeSetChunkSize(0), SET_CHUNK_SIZE, 0, 0)
	if err := cli.Input(bad); err == nil {
		t.Fatal("chunk size 0 must be rejected")
	}
}

func TestLoopbackPlay(t *testing.T) {
	var toSrv, toCli bytes.Buffer

	srv := NewRtmpServerHandle()
	srv.SetOutput(func(b []byte) error { toCli.Write(b); return nil })
	srv.OnPlay(func(app, streamName string, start, duration float64, reset bool) StatusCode {
		return NETSTREAM_PLAY_START
	})

	cli := NewRtmpClient()
	cli.SetOutput(func(b []byte) error { toSrv.Write(b); return nil })
	var statusCodes []string
	cli.OnStatus(func(code, level, describe string) { statusCodes = append(statusCodes, code) })

	if err := cli.Start("rtmp://127.0.0.1/live/abc"); err != nil {
		t.Fatal(err)
	}
	pump(t, cli, srv, &toSrv, &toCli)

	if cli.GetState() != STATE_RTMP_PLAY_START || srv.GetState() != STATE_RTMP_PLAY_START {
		t.Fatalf("states client %d server %d", cli.GetState(), srv.GetState())
	}
	if len(statusCodes) == 0 || statusCodes[len(statusCodes)-1] != string(NETSTREAM_PLAY_START) {
		t.Fatalf("status codes %v", statusCodes)
	}
}

func TestClientHandshakeTrailingBytes(t *testing.T) {
	var toSrv, toCli bytes.Buffer
	srv := NewRtmpServerHandle()
	srv.SetOutput(func(b []byte) error { toCli.Write(b); return nil })
	cli := NewRtmpClient()
	cli.SetOutput(func(b []byte) error { toSrv.Write(b); return nil })

	if err := cli.Start("rtmp://127.0.0.1/live/abc"); err != nil {
		t.Fatal(err)
	}
	// C0C1 -> server answers S0S1S2
	if err := srv.Input(append([]byte{}, toSrv.Bytes()...)); err != nil {
		t.Fatal(err)
	}
	toSrv.Reset()
	s0s1s2 := append([]byte{}, toCli.Bytes()...)
	toCli.Reset()
	if len(s0s1s2) != 1+1536*2 {
		t.Fatalf("unexpected handshake length %d", len(s0s1s2))
	}
	// give the client S0 S1 and only half of S2, then the rest of S2 glued to a rtmp chunk
	if err := cli.Input(s0s1s2[:1+1536+700]); err != nil {
		t.Fatal(err)
	}
	if cli.GetState() != STATE_HANDSHAKEING {
		t.Fatalf("handshake must not be done yet")
	}
	ping := mustWrite(t, srv.userCtrlChan, makeUserControlMessage(PingRequest, 77), USER_CONTROL, 0, 0)
	rest := append(append([]byte{}, s0s1s2[1+1536+700:]...), ping...)
	if err := cli.Input(rest); err != nil {
		t.Fatal(err)
	}
	if cli.GetState() != STATE_RTMP_CONNECTING {
		t.Fatalf("client state %d", cli.GetState())
	}
	// the server side must now see C2, the connect command and the pong
	if err := srv.Input(append([]byte{}, toSrv.Bytes()...)); err != nil {
		t.Fatal(err)
	}
	// the server replies to connect, drain it to be sure nothing breaks
	toSrv.Reset()
	got := &collected{}
	_ = got
	if srv.hs.getState() != HANDSHAKE_DONE || srv.GetState() != STATE_RTMP_CONNECTING {
		t.Fatalf("server handshake %d state %d", srv.hs.getState(), srv.GetState())
	}
}

func TestHandleErrorCallbackOnce(t *testing.T) {
	cli := NewRtmpClient()
	cli.SetOutput(func(b []byte) error { return nil })
	calls := 0
	var gotCode, gotDesc string
	cli.OnError(func(code, describe string) { calls++; gotCode, gotDesc = code, describe })
	res, err := makeStatusRes(1, NETCONNECT_CONNECT_REJECTED, LEVEL_ERROR, "nope")
	if err != nil {
		t.Fatal(err)
	}
	// makeStatusRes starts with the "onStatus" command name, replace it with _error
	body := res[3+len("onStatus"):]
	data := append(amfString("_error"), body...)
	if err := cli.handleCommandRes(data); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || gotCode != string(NETCONNECT_CONNECT_REJECTED) || gotDesc != "nope" {
		t.Fatalf("calls %d code %q desc %q", calls, gotCode, gotDesc)
	}
	if cli.GetState() != STATE_RTMP_PLAY_FAILED {
		t.Fatalf("state %d", cli.GetState())
	}
}

func TestClientErrorPaths(t *testing.T) {
	// Start without an output must report it instead of dereferencing a nil callback
	if err := NewRtmpClient().Start("rtmp://127.0.0.1/live/abc"); err == nil {
		t.Fatal("Start without SetOutput must fail")
	}
	// a failing output is reported by Start
	want := errors.New("boom")
	cli := NewRtmpClient()
	cli.SetOutput(func(b []byte) error { return want })
	if err := cli.Start("rtmp://127.0.0.1/live/abc"); !errors.Is(err, want) {
		t.Fatalf("Start: %v", err)
	}

	cli = NewRtmpClient()
	cli.SetOutput(func(b []byte) error { return nil })
	// an unsupported codec id used to leave a nil muxer behind and panic on Write
	if err := cli.WriteVideo(codec.CODECID_AUDIO_AAC, []byte{0, 0, 0, 1, 0x65}, 0, 0); err == nil {
		t.Fatal("WriteVideo with an audio codec id must fail")
	}
	if err := cli.WriteAudio(codec.CODECID_VIDEO_H264, []byte{1, 2, 3}, 0, 0); err == nil {
		t.Fatal("WriteAudio with a video codec id must fail")
	}
	// metadata that cannot be represented in amf0 is reported, not silently dropped
	if err := cli.WriteSetDataFrame(map[string]interface{}{"bad": []int{1}}); err == nil {
		t.Fatal("WriteSetDataFrame must reject an unsupported value")
	}
	if err := cli.WriteSetDataFrame(map[string]interface{}{"width": 1280, "height": 720}); err != nil {
		t.Fatalf("WriteSetDataFrame: %v", err)
	}
}

func TestServerHandshakeOutputError(t *testing.T) {
	want := errors.New("boom")
	srv := NewRtmpServerHandle()
	srv.SetOutput(func(b []byte) error { return want })
	cli := NewRtmpClient()
	var toSrv bytes.Buffer
	cli.SetOutput(func(b []byte) error { toSrv.Write(b); return nil })
	if err := cli.Start("rtmp://127.0.0.1/live/abc"); err != nil {
		t.Fatal(err)
	}
	if err := srv.Input(toSrv.Bytes()); !errors.Is(err, want) {
		t.Fatalf("the S0S1S2 write error must reach Input, got %v", err)
	}
}
