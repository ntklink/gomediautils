package rtsp

import (
	"strings"
	"testing"

	"github.com/yapingcat/gomedia/go-rtsp/sdp"
)

// A session level "a=control" is optional (rfc2326 C.1.1): without one the
// base url is itself the aggregate control url. gortsplib and ffmpeg's rtsp
// muxer both leave it out, so a client that insists on one cannot play the
// streams most servers actually serve.
func TestDescribeWithoutSessionControlAttribute(t *testing.T) {
	body := strings.Join([]string{
		"v=0",
		"o=- 0 0 IN IP4 127.0.0.1",
		"s=No Name",
		"c=IN IP4 0.0.0.0",
		"t=0 0",
		"m=video 0 RTP/AVP 96",
		"a=control:trackID=0",
		"a=rtpmap:96 H264/90000",
		"a=fmtp:96 packetization-mode=1; profile-level-id=42C00D; sprop-parameter-sets=Z0LADdoFB+wEQAAAAwBAAAAMg8UKqA==,aM4Ecg==",
		"m=audio 0 RTP/AVP 97",
		"a=control:trackID=1",
		"a=rtpmap:97 mpeg4-generic/48000/1",
		"a=fmtp:97 config=1188; indexdeltalength=3; indexlength=3; mode=AAC-hbr; profile-level-id=1; sizelength=13; streamtype=5",
		"",
	}, "\r\n")

	handle := &describeCapture{}
	client, err := NewRtspClient("rtsp://127.0.0.1:8554/live/test", handle)
	if err != nil {
		t.Fatal(err)
	}
	client.SetOutput(func([]byte) error { return nil })

	res := RtspResponse{StatusCode: 200, Reason: "OK", Fileds: make(HeadFiled)}
	res.Fileds[ContentBase] = "rtsp://127.0.0.1:8554/live/test/"
	res.Body = body

	if err := client.handleDescribe(&res); err != nil {
		t.Fatalf("describe without a session level control url: %v", err)
	}
	if len(handle.tracks) != 2 {
		t.Fatalf("got %d tracks, want video and audio", len(handle.tracks))
	}
	if got := client.sdpContext.ControlUrl; got != "rtsp://127.0.0.1:8554/live/test" {
		t.Errorf("aggregate control url %q, want the base url", got)
	}
	for _, name := range []string{"video", "audio"} {
		if handle.tracks[name] == nil {
			t.Errorf("no %s track", name)
		}
	}
}

// describeCapture records the tracks a describe response produced and ignores
// everything else the interface requires.
type describeCapture struct {
	tracks map[string]*RtspTrack
}

func (h *describeCapture) HandleDescribe(cli *RtspClient, res RtspResponse, s *sdp.Sdp, tracks map[string]*RtspTrack) error {
	h.tracks = tracks
	return nil
}

func (h *describeCapture) HandleOption(*RtspClient, RtspResponse, []string) error { return nil }
func (h *describeCapture) HandleSetup(*RtspClient, RtspResponse, *RtspTrack, map[string]*RtspTrack, string, int) error {
	return nil
}
func (h *describeCapture) HandleAnnounce(*RtspClient, RtspResponse) error { return nil }
func (h *describeCapture) HandlePlay(*RtspClient, RtspResponse, *RangeTime, *RtpInfo) error {
	return nil
}
func (h *describeCapture) HandlePause(*RtspClient, RtspResponse) error        { return nil }
func (h *describeCapture) HandleTeardown(*RtspClient, RtspResponse) error     { return nil }
func (h *describeCapture) HandleGetParameter(*RtspClient, RtspResponse) error { return nil }
func (h *describeCapture) HandleSetParameter(*RtspClient, RtspResponse) error { return nil }
func (h *describeCapture) HandleRedirect(*RtspClient, RtspRequest, string, *RangeTime) error {
	return nil
}
func (h *describeCapture) HandleRecord(*RtspClient, RtspResponse, *RangeTime, *RtpInfo) error {
	return nil
}
func (h *describeCapture) HandleRequest(*RtspClient, RtspRequest) error { return nil }

// ANNOUNCE carries an sdp body, and rfc2326 12.16 makes Content-Type
// mandatory on a request that has one. gortsplib, and so mediamtx, answers
// 400 Bad Request without it, which used to make publishing over rtsp
// impossible against either.
func TestAnnounceCarriesContentType(t *testing.T) {
	req := makeAnnounce("rtsp://127.0.0.1/live/test", 2)
	if got := req.Fileds[ContentType]; got != "application/sdp" {
		t.Errorf("announce Content-Type is %q, want application/sdp", got)
	}
	if !strings.Contains(req.Encode(), "Content-Type: application/sdp\r\n") {
		t.Errorf("the encoded request has no Content-Type header:\n%s", req.Encode())
	}
}

// A track that was not given a parameter handler still has to describe how
// it packetises. Without an "a=fmtp" line a receiver falls back on
// packetization-mode=0, single nalu per packet, while the packer happily
// fragments anything over the mtu; mediamtx rejects the mismatch outright.
func TestVideoTrackDescribesItsPacketizationMode(t *testing.T) {
	cases := []struct {
		name  string
		codec RtspCodec
		want  string
	}{
		{"h264", RtspCodec{Cid: RTSP_CODEC_H264, PayloadType: 96, SampleRate: 90000}, "packetization-mode=1"},
		{"h265", RtspCodec{Cid: RTSP_CODEC_H265, PayloadType: 96, SampleRate: 90000}, "a=fmtp:96"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			track := NewVideoTrack(tc.codec)
			track.uri = "track0"
			md := track.mediaDescripe()
			if !strings.Contains(md, tc.want) {
				t.Errorf("media description does not mention %q:\n%s", tc.want, md)
			}
		})
	}
}

// A codec with no fmtp parameters must not grow an empty "a=fmtp" line.
func TestTrackWithoutFmtpParametersHasNoFmtpLine(t *testing.T) {
	track := NewAudioTrack(RtspCodec{Cid: RTSP_CODEC_G711A, PayloadType: 8, SampleRate: 8000, ChannelCount: 1})
	track.uri = "track0"
	if md := track.mediaDescripe(); strings.Contains(md, "a=fmtp") {
		t.Errorf("g711 grew an fmtp line with nothing to say:\n%s", md)
	}
}
