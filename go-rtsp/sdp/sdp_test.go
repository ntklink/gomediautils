package sdp

import (
	"fmt"
	"strings"
	"testing"
)

var sdpstr string = `v=0
o=- 0 0 IN IP6 ::1
s=No Name
c=IN IP6 ::1
t=0 0
a=tool:libavformat 56.40.101
m=video 0 RTP/AVP 96
a=rtpmap:96 H264/90000
a=fmtp:96 packetization-mode=1; sprop-parameter-sets=Z2QAH6zZQFAFuhAASRsQDqYAAPGDGWA=,aOvjyyLA; profile-level-id=64001F
a=control:streamid=0
m=audio 0 RTP/AVP 97
b=AS:34
a=rtpmap:97 MPEG4-GENERIC/16000/1
a=fmtp:97 profile-level-id=1;mode=AAC-hbr;sizelength=13;indexlength=3;indexdeltalength=3; config=1408
a=control:streamid=1`

func TestParserSdp(t *testing.T) {
	t.Run("parse_sdp", func(t *testing.T) {
		sdp := &Sdp{}
		err := sdp.ParserSdp(sdpstr)
		fmt.Println(err)
		fmt.Printf("%+v\n", sdp)
		fmt.Printf("%+v\n", sdp.Medias[0])
		fmt.Printf("%+v\n", sdp.Medias[1])
	})
}

func TestSdpRobustness(t *testing.T) {
	var r RtpMap
	if err := r.Decode("96 H264"); err == nil {
		t.Fatalf("rtpmap without clock rate must error")
	}
	var m Media
	if err := m.ParseMLine("video 0 RTP/AVP"); err == nil {
		t.Fatalf("short m-line must error")
	}
	var s Sdp
	if err := s.ParserSdp("=value\r\n"); err == nil {
		t.Fatalf("empty key must error")
	}
	h := &H264FmtpParam{}
	h.Load("96 packetization-mode=1;sprop-parameter-sets=Z0IAHpWoFAFuQA==")
	if len(h.sps) == 0 || h.pps != nil {
		t.Fatalf("sprop without comma: sps=%d pps=%v", len(h.sps), h.pps)
	}
}

func TestFmtpOptionsReportBadValues(t *testing.T) {
	if _, err := NewAACFmtpParam(WithAudioSpecificConfig([]byte{0x11})); err == nil {
		t.Fatalf("a one byte audio specific config must be rejected")
	}
	aac, err := NewAACFmtpParam(WithAudioSpecificConfig([]byte{0x14, 0x08}))
	if err != nil {
		t.Fatalf("valid asc rejected: %v", err)
	}
	if len(aac.AudioSpecificConfig()) != 2 {
		t.Fatalf("asc not stored")
	}
	if _, err := NewH264FmtpParam(WithProfileLevelId([]byte{0x42, 0x00})); err == nil {
		t.Fatalf("a two byte profile-level-id must be rejected")
	}
	if _, err := NewH264FmtpParam(WithPacketizationMode(7)); err == nil {
		t.Fatalf("packetization-mode 7 must be rejected")
	}
	if _, err := NewH264FmtpParam(WithH264SPS([]byte{0x00, 0x00, 0x00, 0x01})); err == nil {
		t.Fatalf("a start code without a nalu must be rejected")
	}
	h264, err := NewH264FmtpParam(WithProfileLevelId([]byte{0x42, 0xA0, 0x1E}),
		WithH264SPS([]byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42}), WithH264PPS([]byte{0x68, 0xCE}))
	if err != nil {
		t.Fatalf("valid h264 params rejected: %v", err)
	}
	sps, pps := h264.GetSpsPps()
	if len(sps) != 2 || sps[0] != 0x67 || len(pps) != 2 {
		t.Fatalf("sps/pps not stored: % x % x", sps, pps)
	}
	if _, err := NewH265FmtpParam(WithH265VPS(nil)); err == nil {
		t.Fatalf("an empty vps must be rejected")
	}
}

func TestFmtpLoadReportsMalformedParameters(t *testing.T) {
	h264, _ := NewH264FmtpParam()
	if err := h264.Load("96 sprop-parameter-sets=!!!notbase64"); err == nil {
		t.Fatalf("illegal base64 parameter sets must be reported")
	}
	if err := h264.Load("96 packetization-mode=x"); err == nil {
		t.Fatalf("illegal packetization-mode must be reported")
	}
	if err := h264.Load("96"); err == nil {
		t.Fatalf("fmtp without parameters must be reported")
	}
	if err := h264.Load("96 packetization-mode=1;profile-level-id=64001F"); err != nil {
		t.Fatalf("valid fmtp rejected: %v", err)
	}
	// a profile-level-id that was never set must not be written out
	h265, _ := NewH265FmtpParam()
	if err := h265.Load("96 sprop-vps=###"); err == nil {
		t.Fatalf("illegal base64 vps must be reported")
	}
	aac, _ := NewAACFmtpParam()
	if err := aac.Load("97 config=zz"); err == nil {
		t.Fatalf("illegal hex config must be reported")
	}
	if err := aac.Load("97 sizelength=13;indexlength=3;config=1408"); err != nil {
		t.Fatalf("valid aac fmtp rejected: %v", err)
	}
	if aac.SizeLength() != 13 || len(aac.AudioSpecificConfig()) != 2 {
		t.Fatalf("aac fmtp not loaded: %+v", aac)
	}
}

func TestH264SaveWithoutProfileLevelId(t *testing.T) {
	param := &H264FmtpParam{packetizationMode: 1, profileLevelId: []byte{0x42}}
	// a short profile-level-id must not be written out (it used to panic)
	if s := param.Save(); s != "packetization-mode=1" {
		t.Fatalf("save: %s", s)
	}
}

func TestParserSdpRejectsSessionLevelRtpmap(t *testing.T) {
	var s Sdp
	// an "a=rtpmap" before any "m=" line used to index Medias[-1]
	if err := s.ParserSdp("v=0\r\na=rtpmap:96 H264/90000\r\n"); err == nil {
		t.Fatalf("session level rtpmap must be reported")
	}
	var s2 Sdp
	if err := s2.ParserSdp("v=0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264\r\n"); err == nil {
		t.Fatalf("rtpmap without clock rate must be reported")
	}
	var s3 Sdp
	if err := s3.ParserSdp("v=0\r\nm=video xx RTP/AVP 96\r\n"); err == nil {
		t.Fatalf("illegal port must be reported")
	}
	var s4 Sdp
	if err := s4.ParserSdp("v=0\r\nm=audio 0 RTP/AVP 97\r\na=rtpmap:97 MPEG4-GENERIC/16000/x\r\n"); err == nil {
		t.Fatalf("illegal channel count must be reported")
	}
}

// rfc4566 defines "c=" as three fields. An encoder that leaves the address
// empty writes a line every parser rejects, and a description that cannot be
// parsed is a publish that never starts.
func TestEncodeConnectionAddressIsNeverEmpty(t *testing.T) {
	s := &Sdp{Attrs: map[string]string{"control": "*"}}
	out := s.Encode()

	var connection string
	for _, line := range strings.Split(out, "\r\n") {
		if strings.HasPrefix(line, "c=") {
			connection = line
		}
	}
	if connection == "" {
		t.Fatalf("no connection line in:\n%s", out)
	}
	fields := strings.Fields(strings.TrimPrefix(connection, "c="))
	if len(fields) != 3 {
		t.Fatalf("connection line %q has %d fields, want 3", connection, len(fields))
	}

	// and it has to survive a round trip through the parser
	back := &Sdp{}
	if err := back.ParserSdp(out); err != nil {
		t.Fatalf("the encoder produced a description it cannot parse: %v\n%s", err, out)
	}
	if back.ConnectionData.Address != "0.0.0.0" {
		t.Errorf("connection address came back as %q, want 0.0.0.0", back.ConnectionData.Address)
	}
}

// A caller that sets the connection data or the session name has to see it
// in the output rather than the hardcoded default.
func TestEncodeUsesTheSessionItWasGiven(t *testing.T) {
	s := &Sdp{
		SessionName:    "camera 3",
		ConnectionData: Connection{Nettype: "IN", Addrtype: "IP4", Address: "192.0.2.10"},
	}
	out := s.Encode()
	if !strings.Contains(out, "s=camera 3\r\n") {
		t.Errorf("session name was dropped:\n%s", out)
	}
	if !strings.Contains(out, "c=IN IP4 192.0.2.10\r\n") {
		t.Errorf("connection data was dropped:\n%s", out)
	}
}
