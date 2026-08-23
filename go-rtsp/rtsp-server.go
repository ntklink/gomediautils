package rtsp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/yapingcat/gomedia/go-rtsp/sdp"
)

type RtspServer struct {
	buffer      bytes.Buffer
	tracks      map[string]*RtspTrack
	userName    string
	passwd      string
	realm       string
	auth        authenticate
	output      OutPutCallBack
	handle      ServerHandle
	sessionId   string
	sdpContext  *sdp.Sdp
	interleaved int
	isRecord    bool
}

// ServerOption configures a server. An option that is given a value the
// server can not use reports it, so that a server is never silently built
// with, say, authentication disabled.
type ServerOption func(*RtspServer) error

func WithUserInfo(userName, passwd string) ServerOption {
	return func(rs *RtspServer) error {
		if userName == "" || passwd == "" {
			return errors.New("rtsp: user name and password must not be empty")
		}
		rs.userName = userName
		rs.passwd = passwd
		return nil
	}
}

// WithAuthType selects the authentication scheme, "Basic" or "Digest".
func WithAuthType(authType string) ServerOption {
	return func(rs *RtspServer) error {
		auth, err := createAuthByAuthenticate(authType)
		if err != nil {
			return err
		}
		rs.auth = auth
		return nil
	}
}

func WithRealm(realm string) ServerOption {
	return func(rs *RtspServer) error {
		rs.realm = realm
		return nil
	}
}

// NewRtspServer builds a server. handle may be nil, every request is then
// answered with the default response.
func NewRtspServer(handle ServerHandle, opt ...ServerOption) (*RtspServer, error) {
	if handle == nil {
		handle = nopServerHandle{}
	}
	server := &RtspServer{
		handle:     handle,
		auth:       nil,
		realm:      "gomedia server",
		tracks:     make(map[string]*RtspTrack),
		sdpContext: &sdp.Sdp{},
		isRecord:   false,
	}
	for _, o := range opt {
		if err := o(server); err != nil {
			return nil, err
		}
	}
	if server.auth == nil && server.userName != "" && server.passwd != "" {
		auth, err := createAuthByAuthenticate("Digest")
		if err != nil {
			return nil, err
		}
		server.auth = auth
	}
	if server.auth != nil {
		if server.userName == "" || server.passwd == "" {
			return nil, errors.New("rtsp: authentication was requested without user info")
		}
		server.auth.setUserInfo(server.userName, server.passwd)
		server.auth.setRealm(server.realm)
	}
	server.sdpContext.Attrs = make(map[string]string)
	server.sdpContext.Attrs["control"] = "*"
	return server, nil
}

// AddTrack registers a track and adds its media description to the session
// description sent in a DESCRIBE response.
func (server *RtspServer) AddTrack(track *RtspTrack) error {
	track.uri = fmt.Sprintf("track%d", len(server.tracks))
	if err := server.sdpContext.ParserSdp(track.mediaDescripe()); err != nil {
		return err
	}
	server.tracks[track.TrackName] = track
	return nil
}

func (server *RtspServer) SetOutput(output OutPutCallBack) {
	server.output = output
}

// sendToClient writes to the connection, or reports that no output is set.
func (server *RtspServer) sendToClient(data []byte) error {
	if server.output == nil {
		return errors.New("rtsp: no output set on the server")
	}
	return server.output(data)
}

func (server *RtspServer) Input(data []byte) (err error) {
	var buf []byte
	if server.buffer.Len() > 0 {
		server.buffer.Write(data)
		buf = server.buffer.Bytes()
	} else {
		buf = data
	}

	for len(buf) > 0 {
		ret := 0
		if buf[0] == '$' {
			ret, err = server.hanleRtpOverRtsp(buf)
		} else {
			ret, err = server.handleRtspMessage(buf)
		}
		if err != nil {
			break
		}
		buf = buf[ret:]
	}

	if err != nil {
		if errors.Is(err, errNeedMore) {
			err = nil
		} else {
			return
		}
	}

	if len(buf) == 0 {
		server.buffer.Reset()
	} else {
		if server.buffer.Len() > 0 {
			server.buffer.Reset()
		}
		server.buffer.Write(buf)
	}
	return nil
}

func (server *RtspServer) hanleRtpOverRtsp(packet []byte) (int, error) {
	if len(packet) < 4 {
		return 0, errNeedMore
	}
	channel := packet[1]
	length := binary.BigEndian.Uint16(packet[2:])
	if len(packet)-4 < int(length) {
		return 0, errNeedMore
	}
	for _, track := range server.tracks {
		if track.transport == nil {
			// track not SETUP yet
			continue
		}
		isRtcp := false
		if track.transport.Interleaved[1] == int(channel) {
			isRtcp = true
		}
		if track.transport.Interleaved[0] == int(channel) || isRtcp {
			return 4 + int(length), track.Input(packet[4:4+length], isRtcp)
		}
	}
	//improve compatibility
	return 4 + int(length), nil
}

func (server *RtspServer) handleRtspMessage(msg []byte) (int, error) {
	idx := bytes.IndexFunc(msg, func(r rune) bool {
		return r != ' ' && r != '\r' && r != '\n'
	})
	if idx == -1 {
		// only whitespace so far, consume it
		return len(msg), nil
	}
	skipped := idx
	msg = msg[idx:]
	if bytes.HasPrefix(msg, []byte{'R', 'T', 'S', 'P'}) {
		ret, err := server.handleResponse(msg)
		return ret + skipped, err
	} else {
		ret, err := server.handleRequest(msg)
		return ret + skipped, err
	}
}

// TODO
// server send request to client
func (server *RtspServer) handleResponse(res []byte) (ret int, err error) {
	response := RtspResponse{Fileds: make(HeadFiled)}
	ret, err = response.parseBytes(res)
	if err != nil {
		return
	}
	server.handle.HandleResponse(server, response)
	return
}

func (server *RtspServer) handleRequest(req []byte) (ret int, err error) {
	request := RtspRequest{}
	request.Fileds = make(HeadFiled)
	ret, err = request.parseBytes(req)
	if err != nil {
		return
	}
	if server.auth != nil && server.userName != "" && server.passwd != "" {
		server.auth.setMethod(request.Method)
		if !request.Fileds.Has(Authorization) || !server.auth.check(request.Fileds[Authorization]) {
			return ret, server.handleUnAuth(request)
		}
	}

	res := RtspResponse{}
	res.Fileds = make(HeadFiled)
	res.StatusCode = 200
	res.Version = RTSP_1_0
	if server.sessionId != "" {
		if !request.Fileds.Has(Session) || request.Fileds[Session] != server.sessionId {
			res.StatusCode = Session_Not_Found
			return ret, server.sendRespones(request, res)
		}
	}
	switch request.Method {
	case OPTIONS:
		methods := []string{OPTIONS, SET_PARAMETER, GET_PARAMETER, SETUP, DESCRIBE, PLAY, ANNOUNCE, RECORD, TEARDOWN, PAUSE}
		public := ""
		for _, m := range methods {
			public += m + ","
		}
		public = public[:len(public)-1]
		server.handle.HandleOption(server, request, &res)
		if res.StatusCode == 200 {
			res.Fileds[Public] = public
		}
	case DESCRIBE:
		server.handle.HandleDescribe(server, request, &res)
		if res.StatusCode == OK {
			res.Body = server.sdpContext.Encode()
			res.Fileds[ContentType] = "application/sdp"
		}
	case SETUP:
		foundTrack := false
		for _, track := range server.tracks {
			if !strings.Contains(request.Uri, track.uri) {
				continue
			}
			foundTrack = true
			track.uri = request.Uri
			transport := NewRtspTransport()
			if err := transport.DecodeString(request.Fileds[Transport]); err != nil {
				res.StatusCode = Unsupported_Transport
				break
			}
			server.handle.HandleSetup(server, request, &res, transport, track)
			if res.StatusCode == 200 {
				if server.sessionId == "" {
					number := []byte("0123456789")
					b := make([]byte, 10)
					for i := range b {
						b[i] = number[rand.Intn(len(number))]
					}
					server.sessionId = string(b)
				}
				if transport.Proto == TCP {
					transport.Interleaved[0] = server.interleaved
					transport.Interleaved[1] = server.interleaved + 1
					server.interleaved = server.interleaved + 2
					track.OnPacket(func(b []byte, isRtcp bool) error {
						interleavedPacket := make([]byte, 4+len(b))
						interleavedPacket[0] = '$'
						if isRtcp {
							interleavedPacket[1] = byte(transport.Interleaved[1])
						} else {
							interleavedPacket[1] = byte(transport.Interleaved[0])
						}
						binary.BigEndian.PutUint16(interleavedPacket[2:], uint16(len(b)))
						copy(interleavedPacket[4:], b)
						return server.sendToClient(interleavedPacket)
					})
				}
				res.Fileds[Transport] = transport.EncodeString()
				res.Fileds[Session] = server.sessionId
				track.SetTransport(transport)
			}
			break
		}
		if !foundTrack {
			res.StatusCode = BAD_REQUEST
		}
	case ANNOUNCE:
		if err = server.sdpContext.ParserSdp(request.Body); err != nil {
			return
		}
		server.isRecord = true
		for _, media := range server.sdpContext.Medias {
			fmtpHandle := sdp.CreateFmtpParamParser(media.EncodeName)
			if fmtpHandle != nil {
				if fmtp, found := media.Attrs["fmtp"]; found {
					if err := fmtpHandle.Load(fmtp); err != nil {
						return ret, err
					}
				}
			}
			if _, ok := GetCodecIdByEncodeName(media.EncodeName); !ok {
				// unsupported codec (jpeg, opus, application/...), skip this track
				continue
			}
			var track *RtspTrack = nil
			switch media.MediaType {
			case "audio":
				track = NewAudioTrack(NewAudioCodec(media.EncodeName, uint8(media.PayloadType), uint32(media.ClockRate), media.ChannelCount), WithCodecParamHandler(fmtpHandle))
			case "video":
				track = NewVideoTrack(NewVideoCodec(media.EncodeName, uint8(media.PayloadType), uint32(media.ClockRate)), WithCodecParamHandler(fmtpHandle))
			default:
				track = NewMetaTrack(NewApplicatioCodec(media.EncodeName, uint8(media.PayloadType)))
			}
			track.uri = media.ControlUrl
			server.tracks[media.MediaType] = track
		}
		server.handle.HandleAnnounce(server, request, server.tracks)
	case PLAY:
		var tr *RangeTime = nil
		var info []*RtpInfo
		if request.Fileds.Has(Range) {
			if tr, err = parseRange(request.Fileds[Range]); err != nil {
				res.StatusCode = BAD_REQUEST
				return ret, server.sendRespones(request, res)
			}
		}
		for _, t := range server.tracks {
			info = append(info, NewRtpInfo(t.uri, t.initSequence))
		}
		server.handle.HandlePlay(server, request, &res, tr, info)
		if res.StatusCode == 200 {
			if tr != nil {
				res.Fileds[Range] = tr.EncodeString()
			}
			if len(info) > 0 {
				var infostr strings.Builder
				for _, i := range info {
					infostr.WriteString(i.EncodeString())
					infostr.WriteString(",")
				}
				res.Fileds[RTPInfo] = infostr.String()[:len(infostr.String())-1]
			}
		}
	case RECORD:
		var tr *RangeTime = nil
		var info []*RtpInfo
		if request.Fileds.Has(Range) {
			if tr, err = parseRange(request.Fileds[Range]); err != nil {
				res.StatusCode = BAD_REQUEST
				return ret, server.sendRespones(request, res)
			}
		}
		for _, t := range server.tracks {
			info = append(info, NewRtpInfo(t.uri, t.initSequence))
		}
		server.handle.HandleRecord(server, request, &res, tr, info)
		if res.StatusCode == 200 {
			if tr != nil {
				res.Fileds[Range] = tr.EncodeString()
			}
			if len(info) > 0 {
				var infostr strings.Builder
				for _, i := range info {
					infostr.WriteString(i.EncodeString())
					infostr.WriteString(",")
				}
				res.Fileds[RTPInfo] = infostr.String()[:len(infostr.String())-1]
			}
		}
	case TEARDOWN:
		server.handle.HandleTeardown(server, request, &res)
	case PAUSE:
		server.handle.HandlePause(server, request, &res)
	case SET_PARAMETER:
		server.handle.HandleSetParameter(server, request, &res)
	case GET_PARAMETER:
		server.handle.HandleGetParameter(server, request, &res)
	}
	return ret, server.sendRespones(request, res)
}

func (server *RtspServer) handleUnAuth(request RtspRequest) error {
	response := RtspResponse{Fileds: make(HeadFiled)}
	response.StatusCode = 401
	if server.auth != nil {
		response.Fileds[WWWAuthenticate] = server.auth.wwwAuthenticate()
	}
	return server.sendRespones(request, response)
}

func (server *RtspServer) sendRespones(req RtspRequest, res RtspResponse) error {
	res.Fileds[CSeq] = req.Fileds[CSeq]
	res.Fileds[Date] = time.Now().UTC().Format("02 Jan 06 15:04:05 GMT")
	if server.output == nil {
		// nothing is connected yet, dropping the response is not an error
		return nil
	}
	return server.output([]byte(res.Encode()))
}
