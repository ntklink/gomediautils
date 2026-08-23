package rtmp

import (
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
)

//example
//1. rtmp 推流服务端
//
//listen, _ := net.Listen("tcp4", "0.0.0.0:1935")
//conn, _ := listen.Accept()
//
// handle := NewRtmpServerHandle()
// handle.OnPublish(func(app, streamName string) StatusCode {
//     return NETSTREAM_PUBLISH_START
// })
//
// handle.SetOutput(func(b []byte) error {
//     _, err := conn.Write(b)
//     return err
// })

// handle.OnFrame(func(cid codec.CodecID, pts, dts uint32, frame []byte) {
//     if cid == codec.CODECID_VIDEO_H264 {
//        //do something
//     }
//     ........
// })

//
// 把从网络中接收到的数据，input到rtmp句柄当中
// buf := make([]byte, 60000)
// for {
//     n, err := conn.Read(buf)
//     if err != nil {
//         fmt.Println(err)
//         break
//     }
//     err = handle.Input(buf[0:n])
//     if err != nil {
//         fmt.Println(err)
//         break
//     }
// }

// rtmp播放服务端
// listen, _ := net.Listen("tcp4", "0.0.0.0:1935")
// conn, _ := listen.Accept()

// ready := make(chan struct{})
// handle := NewRtmpServerHandle()
// handle.onPlay = func(app, streamName string, start, duration float64, reset bool) StatusCode {
//        return NETSTREAM_PLAY_START
//  }
//
// handle.OnStateChange(func(newstate RtmpState) {
//    if newstate == STATE_RTMP_PLAY_START {
//        close(ready) //关闭这个通道，通知推流协程可以向客户端推流了
//    }
//  })
//
//  handle.SetOutput(func(b []byte) error {
//       _, err := conn.Write(b)
//      return err
//  })
//
//  go func() {
//
//      等待推流
//      <-ready
//
//      开始推流
//      handle.WriteVideo(cid, frame, pts, dts)
//      handle.WriteAudio(cid, frame, pts, dts)
//
//  }()
//
//  把从网络中接收到的数据，input到rtmp句柄当中
//  buf := make([]byte, 60000)
//  for {
//      n, err := conn.Read(buf)
//      if err != nil {
//          fmt.Println(err)
//          break
//      }
//      err = handle.Input(buf[0:n])
//      if err != nil {
//          fmt.Println(err)
//          break
//      }
//  }
//  conn.Close()

type RtmpServerHandle struct {
	app            string
	streamName     string
	tcUrl          string
	state          RtmpParserState
	streamState    int32 // RtmpState, accessed atomically
	outMu          sync.Mutex
	lastAckBytes   uint64
	cmdChan        *chunkStreamWriter
	userCtrlChan   *chunkStreamWriter
	audioChan      *chunkStreamWriter
	videoChan      *chunkStreamWriter
	reader         *chunkStreamReader
	writeChunkSize uint32
	hs             *serverHandShake
	wndAckSize     uint32
	peerWndAckSize uint32
	videoDemuxer   flv.VideoTagDemuxer
	audioDemuxer   flv.AudioTagDemuxer
	videoMuxer     flv.AVTagMuxer
	audioMuxer     flv.AVTagMuxer
	onframe        OnFrame
	output         OutputCB
	onRelease      OnReleaseStream
	onChangeState  OnStateChange
	onPlay         OnPlay
	onPublish      OnPublish
	timestamp      uint32
	streamId       uint32
}

func NewRtmpServerHandle(options ...func(*RtmpServerHandle)) *RtmpServerHandle {
	server := &RtmpServerHandle{
		hs:             newServerHandShake(),
		cmdChan:        newChunkStreamWriter(CHUNK_CHANNEL_CMD),
		userCtrlChan:   newChunkStreamWriter(CHUNK_CHANNEL_USE_CTRL),
		reader:         newChunkStreamReader(FIX_CHUNK_SIZE),
		wndAckSize:     DEFAULT_ACK_SIZE,
		writeChunkSize: DEFAULT_CHUNK_SIZE,
		streamId:       1,
	}

	for _, o := range options {
		o(server)
	}

	return server
}

func (server *RtmpServerHandle) SetOutput(output OutputCB) {
	server.output = output
	server.hs.output = server.send
}

// send serializes every write to the output callback, Input and the Write* methods may be called
// from different goroutines
func (server *RtmpServerHandle) send(data []byte) error {
	server.outMu.Lock()
	defer server.outMu.Unlock()
	if server.output == nil {
		return errors.New("rtmp server output is not set")
	}
	return server.output(data)
}

// sendAckIfNeeded sends an acknowledgement when the bytes received since the last one exceed the
// peer's window acknowledgement size
func (server *RtmpServerHandle) sendAckIfNeeded() error {
	if server.peerWndAckSize == 0 {
		return nil
	}
	recv := server.reader.recvBytes
	if recv-server.lastAckBytes < uint64(server.peerWndAckSize) {
		return nil
	}
	server.lastAckBytes = recv
	ack := makeAcknowledgementSize(uint32(recv))
	return server.sendBatch(new(chunkBatch).write(server.userCtrlChan, ack, nil, ACKNOWLEDGEMENT, 0, 0))
}

// sendBatch hands the bytes accumulated in a chunkBatch to the output callback
func (server *RtmpServerHandle) sendBatch(b *chunkBatch) error {
	data, err := b.bytes()
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return server.send(data)
}

func (server *RtmpServerHandle) OnFrame(onframe OnFrame) {
	server.onframe = onframe
}

func (server *RtmpServerHandle) OnPlay(onPlay OnPlay) {
	server.onPlay = onPlay
}

func (server *RtmpServerHandle) OnPublish(onPub OnPublish) {
	server.onPublish = onPub
}

func (server *RtmpServerHandle) OnRelease(onRelease OnReleaseStream) {
	server.onRelease = onRelease
}

// 状态变更，回调函数，
// 服务端在STATE_RTMP_PLAY_START状态下，开始发流
// 客户端在STATE_RTMP_PUBLISH_START状态，开始推流
func (server *RtmpServerHandle) OnStateChange(stateChange OnStateChange) {
	server.onChangeState = stateChange
}

func (server *RtmpServerHandle) GetStreamName() string {
	return server.streamName
}

func (server *RtmpServerHandle) GetApp() string {
	return server.app
}

func (server *RtmpServerHandle) GetState() RtmpState {
	return RtmpState(atomic.LoadInt32(&server.streamState))
}

func (server *RtmpServerHandle) Input(data []byte) error {
	for len(data) > 0 {
		switch server.state {
		case HandShake:
			server.changeState(STATE_HANDSHAKEING)
			r, err := server.hs.input(data)
			data = data[r:]
			if err != nil {
				return err
			}
			if server.hs.getState() == HANDSHAKE_DONE {
				server.changeState(STATE_HANDSHAKE_DONE)
				server.state = ReadChunk
			}
		case ReadChunk:

			err := server.reader.readRtmpMessage(data, func(msg *rtmpMessage) error {
				server.timestamp = msg.timestamp
				return server.handleMessage(msg)
			})
			if err != nil {
				return err
			}
			return server.sendAckIfNeeded()
		default:
			return errors.New("rtmp server in unknown state")
		}
	}
	return nil
}

func (server *RtmpServerHandle) WriteFrame(cid codec.CodecID, frame []byte, pts, dts uint32) error {
	if cid == codec.CODECID_AUDIO_AAC || cid == codec.CODECID_AUDIO_G711A || cid == codec.CODECID_AUDIO_G711U {
		return server.WriteAudio(cid, frame, pts, dts)
	} else if cid == codec.CODECID_VIDEO_H264 || cid == codec.CODECID_VIDEO_H265 {
		return server.WriteVideo(cid, frame, pts, dts)
	} else {
		return errors.New("unsupport codec id")
	}
}

func (server *RtmpServerHandle) WriteAudio(cid codec.CodecID, frame []byte, pts, dts uint32) error {

	if server.audioMuxer == nil {
		muxer, err := flv.CreateAudioMuxer(flv.CovertCodecId2SoundFromat(cid))
		if err != nil {
			return err
		}
		server.audioMuxer = muxer
	}
	tags, err := server.audioMuxer.Write(frame, pts, dts)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		if err := server.WriteAudioTag(tag, dts); err != nil {
			return err
		}
	}
	return nil
}

// WriteAudioTag sends an already encoded flv audio tag body (AudioTag header + payload)
func (server *RtmpServerHandle) WriteAudioTag(tag []byte, dts uint32) error {
	if server.audioChan == nil {
		server.audioChan = newChunkStreamWriter(CHUNK_CHANNEL_AUDIO)
		server.audioChan.chunkSize = server.writeChunkSize
	}
	return server.sendBatch(new(chunkBatch).write(server.audioChan, tag, nil, AUDIO, server.streamId, dts))
}

func (server *RtmpServerHandle) WriteVideo(cid codec.CodecID, frame []byte, pts, dts uint32) error {
	if server.videoMuxer == nil {
		muxer, err := flv.CreateVideoMuxer(flv.CovertCodecId2FlvVideoCodecId(cid))
		if err != nil {
			return err
		}
		server.videoMuxer = muxer
	}
	tags, err := server.videoMuxer.Write(frame, pts, dts)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		if err := server.WriteVideoTag(tag, dts); err != nil {
			return err
		}
	}
	return nil
}

// WriteVideoTag sends an already encoded flv video tag body (VideoTag header + payload)
func (server *RtmpServerHandle) WriteVideoTag(tag []byte, dts uint32) error {
	if server.videoChan == nil {
		server.videoChan = newChunkStreamWriter(CHUNK_CHANNEL_VIDEO)
		server.videoChan.chunkSize = server.writeChunkSize
	}
	return server.sendBatch(new(chunkBatch).write(server.videoChan, tag, nil, VIDEO, server.streamId, dts))
}

func (server *RtmpServerHandle) changeState(newState RtmpState) {
	if atomic.SwapInt32(&server.streamState, int32(newState)) != int32(newState) {
		if server.onChangeState != nil {
			server.onChangeState(newState)
		}
	}
}

func (server *RtmpServerHandle) handleMessage(msg *rtmpMessage) error {
	switch msg.msgtype {
	case SET_CHUNK_SIZE:
		if len(msg.msg) < 4 {
			return errors.New("bytes of \"set chunk size\"  < 4")
		}
		size := binary.BigEndian.Uint32(msg.msg)
		if size == 0 || size > 0x7FFFFFFF {
			return errors.New("invalid chunk size")
		}
		server.reader.chunkSize = size
	case ABORT_MESSAGE:
		//TODO
	case ACKNOWLEDGEMENT:
		// peer acknowledges bytes we sent, nothing to do
	case USER_CONTROL:
		return server.handleUserEvent(msg.msg)
	case WND_ACK_SIZE:
		if len(msg.msg) < 4 {
			return errors.New("bytes of \"window acknowledgement size\"  < 4")
		}
		server.peerWndAckSize = binary.BigEndian.Uint32(msg.msg)
	case SET_PEER_BW:
		//TODO
	case AUDIO:
		return server.handleAudioMessage(msg)
	case VIDEO:
		return server.handleVideoMessage(msg)
	case Command_AMF0:
		return server.handleCommand(msg.msg)
	case Command_AMF3:
	case Metadata_AMF0:
	case Metadata_AMF3:
	case SharedObject_AMF0:
	case SharedObject_AMF3:
	case Aggregate:
	default:
		return errors.New("unkown message type")
	}
	return nil
}

func (server *RtmpServerHandle) handleUserEvent(data []byte) error {
	event, err := decodeUserControlMsg(data)
	if err != nil {
		return err
	}
	switch event.code {
	case PingRequest:
		pong := makeUserControlMessage(PingResponse, int(event.data[0]))
		return server.sendBatch(new(chunkBatch).write(server.userCtrlChan, pong, nil, USER_CONTROL, 0, 0))
	default:
		// other events are informational, unknown ones are ignored
	}
	return nil
}

func (server *RtmpServerHandle) handleCommand(data []byte) error {
	item := amf0Item{}
	l, err := item.decode(data)
	if err != nil {
		return err
	}
	name, ok := item.value.([]byte)
	if !ok {
		return errors.New("command name is not an amf0 string")
	}
	data = data[l:]
	cmd := string(name)
	switch cmd {
	case "connect":
		server.changeState(STATE_RTMP_CONNECTING)
		return server.handleConnect(data)
	case "releaseStream":
		return server.handleReleaseStream(data)
	case "FCPublish":
	case "createStream":
		return server.handleCreateStream(data)
	case "play":
		return server.handlePlay(data)
	case "publish":
		return server.handlePublish(data)
	default:
	}
	return nil
}

func (server *RtmpServerHandle) handleConnect(data []byte) error {
	_, objs, err := decodeAmf0(data)
	if err != nil {
		return err
	}
	if len(objs) > 0 {
		for _, item := range objs[0].items {
			if item.name == "app" {
				server.app = amf0String(item.value)
			} else if item.name == "tcUrl" {
				server.tcUrl = amf0String(item.value)
			}
		}
	}

	batch := new(chunkBatch)
	batch.write(server.userCtrlChan, makeSetChunkSize(server.writeChunkSize), nil, SET_CHUNK_SIZE, 0, 0)
	server.userCtrlChan.chunkSize = server.writeChunkSize
	server.cmdChan.chunkSize = server.writeChunkSize
	batch.write(server.userCtrlChan, makeAcknowledgementSize(server.wndAckSize), nil, WND_ACK_SIZE, 0, 0)
	batch.write(server.userCtrlChan, makeSetPeerBandwidth(server.wndAckSize, LimitType_DYNAMIC), nil, SET_PEER_BW, 0, 0)
	res, err := makeConnectRes()
	batch.write(server.cmdChan, res, err, Command_AMF0, 0, 0)
	return server.sendBatch(batch)
}

func (server *RtmpServerHandle) handleReleaseStream(data []byte) error {
	items, _, err := decodeAmf0(data)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	streamName, ok := items[len(items)-1].value.([]byte)
	if !ok {
		return errors.New("releaseStream: stream name is not an amf0 string")
	}
	if server.onRelease != nil {
		server.onRelease(server.app, string(streamName))
	}
	return nil
}

func (server *RtmpServerHandle) handleCreateStream(data []byte) error {
	items, _, err := decodeAmf0(data)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	tidf, ok := items[0].value.(float64)
	if !ok {
		return errors.New("createStream: transaction id is not a number")
	}
	tid := uint32(tidf)
	res, err := makeCreateStreamRes(tid, server.streamId)
	return server.sendBatch(new(chunkBatch).write(server.cmdChan, res, err, Command_AMF0, 0, 0))
}

// parseTidAndStreamName extracts the transaction id (items[0]) and stream name (items[2]) of a
// play/publish command, the null command object in between is items[1]
func parseTidAndStreamName(cmd string, items []amf0Item) (int, string, error) {
	if len(items) < 3 {
		return 0, "", errors.New(cmd + ": too few arguments")
	}
	tid, ok := items[0].value.(float64)
	if !ok {
		return 0, "", errors.New(cmd + ": transaction id is not a number")
	}
	name, ok := items[2].value.([]byte)
	if !ok {
		return 0, "", errors.New(cmd + ": stream name is not an amf0 string")
	}
	return int(tid), string(name), nil
}

func (server *RtmpServerHandle) handlePlay(data []byte) error {
	items, _, err := decodeAmf0(data)
	if err != nil {
		return err
	}
	tid, streamName, err := parseTidAndStreamName("play", items)
	if err != nil {
		return err
	}
	server.streamName = streamName
	start := float64(-2)
	duration := float64(-1)
	reset := false

	if len(items) > 3 {
		if v, ok := items[3].value.(float64); ok {
			start = v
		}
	}
	if len(items) > 4 {
		if v, ok := items[4].value.(float64); ok {
			duration = v
		}
	}
	if len(items) > 5 {
		if v, ok := items[5].value.(bool); ok {
			reset = v
		}
	}

	code := NETSTREAM_PLAY_START
	if server.onPlay != nil {
		code = server.onPlay(server.app, streamName, start, duration, reset)
	}
	batch := new(chunkBatch)
	if code != NETSTREAM_PLAY_START {
		res, err := makeStatusRes(tid, code, code.Level(), string(code.Description()))
		batch.write(server.cmdChan, res, err, Command_AMF0, server.streamId, 0)
		if err := server.sendBatch(batch); err != nil {
			return err
		}
		server.changeState(STATE_RTMP_PLAY_FAILED)
		return nil
	}

	begin := makeUserControlMessage(StreamBegin, int(server.streamId))
	batch.write(server.userCtrlChan, begin, nil, USER_CONTROL, 0, 0)
	res, err := makeStatusRes(tid, NETSTREAM_PLAY_RESET, NETSTREAM_PLAY_RESET.Level(), string(NETSTREAM_PLAY_RESET.Description()))
	batch.write(server.cmdChan, res, err, Command_AMF0, server.streamId, 0)
	res, err = makeStatusRes(tid, NETSTREAM_PLAY_START, NETSTREAM_PLAY_START.Level(), string(NETSTREAM_PLAY_START.Description()))
	batch.write(server.cmdChan, res, err, Command_AMF0, server.streamId, 0)
	if err := server.sendBatch(batch); err != nil {
		return err
	}
	server.changeState(STATE_RTMP_PLAY_START)
	return nil
}

func (server *RtmpServerHandle) handlePublish(data []byte) error {
	items, _, err := decodeAmf0(data)
	if err != nil {
		return err
	}
	tid, streamName, err := parseTidAndStreamName("publish", items)
	if err != nil {
		return err
	}
	server.streamName = streamName
	code := NETSTREAM_PUBLISH_START
	if server.onPublish != nil {
		code = server.onPublish(server.app, streamName)
	}
	res, resErr := makeStatusRes(tid, code, code.Level(), string(code.Description()))
	if err := server.sendBatch(new(chunkBatch).write(server.cmdChan, res, resErr, Command_AMF0, server.streamId, 0)); err != nil {
		return err
	}
	if code == NETSTREAM_PUBLISH_START {
		server.changeState(STATE_RTMP_PUBLISH_START)
	} else {
		server.changeState(STATE_RTMP_PUBLISH_FAILED)
	}
	return nil
}

func (server *RtmpServerHandle) handleVideoMessage(msg *rtmpMessage) error {
	if server.onframe == nil || len(msg.msg) < 1 {
		return nil
	}
	if msg.msg[0]&0x80 != 0 && len(msg.msg) < 5 {
		return nil
	}
	if server.videoDemuxer == nil {
		demuxer, err := flv.CreateFlvVideoTagHandle(flv.GetFLVVideoCodecId(msg.msg))
		if err != nil {
			return err
		}
		server.videoDemuxer = demuxer
		server.videoDemuxer.OnFrame(func(codecid codec.CodecID, frame []byte, cts int) {
			dts := server.timestamp
			pts := dts + uint32(cts)
			server.onframe(codecid, pts, dts, frame)
		})
	}
	return server.videoDemuxer.Decode(msg.msg)
}

func (server *RtmpServerHandle) handleAudioMessage(msg *rtmpMessage) error {
	if server.onframe == nil || len(msg.msg) < 1 {
		return nil
	}
	if server.audioDemuxer == nil {
		demuxer, err := flv.CreateAudioTagDemuxer(flv.FLV_SOUND_FORMAT((msg.msg[0] >> 4) & 0x0F))
		if err != nil {
			return err
		}
		server.audioDemuxer = demuxer
		server.audioDemuxer.OnFrame(func(codecid codec.CodecID, frame []byte) {
			dts := server.timestamp
			pts := dts
			server.onframe(codecid, pts, dts, frame)
		})
	}
	return server.audioDemuxer.Decode(msg.msg)
}
