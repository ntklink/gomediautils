package rtmp

import (
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
)

type RtmpConnectCmd int

const (
	CONNECT RtmpConnectCmd = iota
	CLOSE
	CREATE_STREAM
	GET_STREAM_LENGTH
)

type RtmpClient struct {
	tcurl          string
	app            string
	streamName     string
	cmdChan        *chunkStreamWriter
	userCtrlChan   *chunkStreamWriter
	sourceChan     *chunkStreamWriter
	audioChan      *chunkStreamWriter
	videoChan      *chunkStreamWriter
	metaChan       *chunkStreamWriter
	reader         *chunkStreamReader
	wndAckSize     uint32
	peerWndAckSize uint32
	lastAckBytes   uint64
	state          RtmpParserState
	streamState    int32 // RtmpState, accessed atomically
	outMu          sync.Mutex
	hs             *clientHandShake
	output         OutputCB
	onframe        OnFrame
	onstatus       OnStatus
	onerror        OnError
	onstateChange  OnStateChange
	videoDemuxer   flv.VideoTagDemuxer
	audioDemuxer   flv.AudioTagDemuxer
	videoMuxer     flv.AVTagMuxer
	audioMuxer     flv.AVTagMuxer
	timestamp      uint32
	lastMethod     RtmpConnectCmd
	lastMethodTid  int
	tid            uint32
	// streamId is assigned while handling the createStream result on the
	// Input goroutine and read by the Write* methods, which the api invites
	// callers to drive from a second goroutine
	streamId       uint32 // accessed atomically
	writeChunkSize uint32
	isPublish      bool
}

func NewRtmpClient(options ...func(*RtmpClient)) *RtmpClient {
	cli := &RtmpClient{
		hs:             newClientHandShake(),
		cmdChan:        newChunkStreamWriter(CHUNK_CHANNEL_CMD),
		userCtrlChan:   newChunkStreamWriter(CHUNK_CHANNEL_USE_CTRL),
		sourceChan:     newChunkStreamWriter(CHUNK_CHANNEL_NET_STREAM),
		reader:         newChunkStreamReader(FIX_CHUNK_SIZE),
		tid:            4,
		wndAckSize:     DEFAULT_ACK_SIZE,
		writeChunkSize: DEFAULT_CHUNK_SIZE,
		isPublish:      false,
	}

	for _, o := range options {
		o(cli)
	}
	return cli
}

func WithChunkSize(chunkSize uint32) func(*RtmpClient) {
	return func(rc *RtmpClient) {
		if rc != nil {
			rc.writeChunkSize = chunkSize
		}
	}
}

func WithComplexHandshake() func(*RtmpClient) {
	return func(rc *RtmpClient) {
		if rc != nil {
			rc.hs.simpleHs = false
		}
	}
}

func WithComplexHandshakeSchema(schema int) func(*RtmpClient) {
	return func(rc *RtmpClient) {
		if rc != nil {
			rc.hs.schema = schema
		}
	}
}

func WithWndAckSize(ackSize uint32) func(*RtmpClient) {
	return func(rc *RtmpClient) {
		if rc != nil {
			rc.wndAckSize = ackSize
		}
	}
}

func WithEnablePublish() func(*RtmpClient) {
	return func(rc *RtmpClient) {
		if rc != nil {
			rc.isPublish = true
		}
	}
}

func WithAudioMuxer(muxer flv.AVTagMuxer) func(*RtmpClient) {
	return func(rc *RtmpClient) {
		if rc != nil {
			rc.audioMuxer = muxer
		}
	}
}

func (cli *RtmpClient) SetOutput(output OutputCB) {
	cli.output = output
	cli.hs.output = cli.send
}

// send serializes every write to the output callback, Input (ping response, acknowledgement) and
// the Write* methods may be called from different goroutines
func (cli *RtmpClient) send(data []byte) error {
	cli.outMu.Lock()
	defer cli.outMu.Unlock()
	if cli.output == nil {
		return errors.New("rtmp client output is not set")
	}
	return cli.output(data)
}

// sendAckIfNeeded sends an acknowledgement when the bytes received since the last one exceed the
// peer's window acknowledgement size
func (cli *RtmpClient) sendAckIfNeeded() error {
	if cli.peerWndAckSize == 0 {
		return nil
	}
	recv := cli.reader.recvBytes
	if recv-cli.lastAckBytes < uint64(cli.peerWndAckSize) {
		return nil
	}
	cli.lastAckBytes = recv
	ack := makeAcknowledgementSize(uint32(recv))
	return cli.sendBatch(new(chunkBatch).write(cli.userCtrlChan, ack, nil, ACKNOWLEDGEMENT, 0, 0))
}

// sendBatch hands the bytes accumulated in a chunkBatch to the output callback
func (cli *RtmpClient) sendBatch(b *chunkBatch) error {
	data, err := b.bytes()
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return cli.send(data)
}

func (cli *RtmpClient) OnFrame(onframe OnFrame) {
	cli.onframe = onframe
}

func (cli *RtmpClient) OnError(onerror OnError) {
	cli.onerror = onerror
}

func (cli *RtmpClient) OnStatus(onstatus OnStatus) {
	cli.onstatus = onstatus
}

func (cli *RtmpClient) OnStateChange(stateChange OnStateChange) {
	cli.onstateChange = stateChange
}

// Start begins the handshake, the url looks like "rtmp://host:port/app/stream", any scheme is
// accepted and tcUrl is always rtmp://. It reports the error of writing C0C1 to the output.
func (cli *RtmpClient) Start(url string) error {
	tmp := url
	if loc := strings.Index(url, "://"); loc >= 0 {
		tmp = url[loc+3:]
	}
	cli.tcurl = "rtmp://"
	loc := strings.Index(tmp, "/")
	if loc < 0 {
		cli.tcurl += tmp
		return cli.hs.start()
	}
	cli.tcurl += tmp[:loc]
	tmp = tmp[loc+1:]
	loc = strings.Index(tmp, "/")
	if loc < 0 {
		cli.app = tmp
	} else {
		cli.app = tmp[:loc]
		cli.streamName = tmp[loc+1:]
	}
	cli.tcurl += "/" + cli.app
	return cli.hs.start()
}

func (cli *RtmpClient) GetState() RtmpState {
	return RtmpState(atomic.LoadInt32(&cli.streamState))
}

func (cli *RtmpClient) Input(data []byte) error {
	for len(data) > 0 {
		switch cli.state {
		case HandShake:
			cli.changeState(STATE_HANDSHAKEING)
			r, err := cli.hs.input(data)
			data = data[r:]
			if err != nil {
				return err
			}
			if cli.hs.getState() != HANDSHAKE_DONE {
				return nil
			}
			cli.changeState(STATE_RTMP_CONNECTING)
			cli.state = ReadChunk
			cmd, cmdErr := makeConnect(cli.app, cli.tcurl)
			if err := cli.sendBatch(new(chunkBatch).write(cli.cmdChan, cmd, cmdErr, Command_AMF0, 0, 0)); err != nil {
				return err
			}
			cli.lastMethod = CONNECT
			cli.lastMethodTid = 1
			// bytes after S2 (if any) are the first rtmp chunks, keep looping to parse them
		case ReadChunk:
			err := cli.reader.readRtmpMessage(data, func(msg *rtmpMessage) error {
				cli.timestamp = msg.timestamp
				return cli.handleMessage(msg)
			})
			if err != nil {
				return err
			}
			return cli.sendAckIfNeeded()
		default:
			return errors.New("rtmp client in unknown state")
		}
	}
	return nil
}

func (cli *RtmpClient) WriteFrame(cid codec.CodecID, frame []byte, pts, dts uint32) error {
	if cid == codec.CODECID_AUDIO_AAC || cid == codec.CODECID_AUDIO_G711A || cid == codec.CODECID_AUDIO_G711U {
		return cli.WriteAudio(cid, frame, pts, dts)
	} else if cid == codec.CODECID_VIDEO_H264 || cid == codec.CODECID_VIDEO_H265 {
		return cli.WriteVideo(cid, frame, pts, dts)
	} else {
		return errors.New("unsupport codec id")
	}
}

func (cli *RtmpClient) WriteAudio(cid codec.CodecID, frame []byte, pts, dts uint32) error {
	if cli.audioMuxer == nil {
		muxer, err := flv.CreateAudioMuxer(flv.CovertCodecId2SoundFromat(cid))
		if err != nil {
			return err
		}
		cli.audioMuxer = muxer
	}
	tags, err := cli.audioMuxer.Write(frame, pts, dts)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		if err := cli.WriteAudioTag(tag, dts); err != nil {
			return err
		}
	}
	return nil
}

func (cli *RtmpClient) WriteVideo(cid codec.CodecID, frame []byte, pts, dts uint32) error {
	if cli.videoMuxer == nil {
		muxer, err := flv.CreateVideoMuxer(flv.CovertCodecId2FlvVideoCodecId(cid))
		if err != nil {
			return err
		}
		cli.videoMuxer = muxer
	}
	tags, err := cli.videoMuxer.Write(frame, pts, dts)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		if err := cli.WriteVideoTag(tag, dts); err != nil {
			return err
		}
	}
	return nil
}

// WriteVideoTag sends an already encoded flv video tag body (VideoTag header + payload)
func (cli *RtmpClient) WriteVideoTag(tag []byte, dts uint32) error {
	if cli.videoChan == nil {
		cli.videoChan = newChunkStreamWriter(CHUNK_CHANNEL_VIDEO)
		cli.videoChan.chunkSize = cli.writeChunkSize
	}
	return cli.sendBatch(new(chunkBatch).write(cli.videoChan, tag, nil, VIDEO, atomic.LoadUint32(&cli.streamId), dts))
}

// WriteAudioTag sends an already encoded flv audio tag body (AudioTag header + payload)
func (cli *RtmpClient) WriteAudioTag(tag []byte, dts uint32) error {
	if cli.audioChan == nil {
		cli.audioChan = newChunkStreamWriter(CHUNK_CHANNEL_AUDIO)
		cli.audioChan.chunkSize = cli.writeChunkSize
	}
	return cli.sendBatch(new(chunkBatch).write(cli.audioChan, tag, nil, AUDIO, atomic.LoadUint32(&cli.streamId), dts))
}

// WriteSetDataFrame sends @setDataFrame onMetaData, call after STATE_RTMP_PUBLISH_START
func (cli *RtmpClient) WriteSetDataFrame(values map[string]interface{}) error {
	if cli.metaChan == nil {
		cli.metaChan = newChunkStreamWriter(CHUNK_CHANNEL_META)
		cli.metaChan.chunkSize = cli.writeChunkSize
	}
	meta, err := flv.EncodeOnMetaData(values)
	if err != nil {
		return err
	}
	data := flv.EncodeAmf0String(nil, "@setDataFrame")
	data = append(data, meta...)
	return cli.sendBatch(new(chunkBatch).write(cli.metaChan, data, nil, Metadata_AMF0, atomic.LoadUint32(&cli.streamId), 0))
}

func (cli *RtmpClient) changeState(newState RtmpState) {
	if atomic.SwapInt32(&cli.streamState, int32(newState)) != int32(newState) {
		if cli.onstateChange != nil {
			cli.onstateChange(newState)
		}
	}
}

func (cli *RtmpClient) handleMessage(msg *rtmpMessage) error {
	switch msg.msgtype {
	case SET_CHUNK_SIZE:
		if len(msg.msg) < 4 {
			return errors.New("bytes of \"set chunk size\"  < 4")
		}
		size := binary.BigEndian.Uint32(msg.msg)
		if size == 0 || size > 0x7FFFFFFF {
			return errors.New("invalid chunk size")
		}
		cli.reader.chunkSize = size
	case ABORT_MESSAGE:
		//TODO
	case ACKNOWLEDGEMENT:
		// peer acknowledges bytes we sent, nothing to do
	case USER_CONTROL:
		return cli.handleUserEvent(msg.msg)
	case WND_ACK_SIZE:
		if len(msg.msg) < 4 {
			return errors.New("bytes of \"window acknowledgement size\"  < 4")
		}
		cli.peerWndAckSize = binary.BigEndian.Uint32(msg.msg)
	case SET_PEER_BW:
		//TODO
	case AUDIO:
		return cli.handleAudioMessage(msg)
	case VIDEO:
		return cli.handleVideoMessage(msg)
	case Command_AMF0:
		return cli.handleCommandRes(msg.msg)
	case Command_AMF3:
	case Metadata_AMF0:
	case Metadata_AMF3:
	case SharedObject_AMF0:
	case SharedObject_AMF3:
	case Aggregate:
	default:
		return errors.New("unkow message type")
	}
	return nil
}

func (cli *RtmpClient) handleUserEvent(data []byte) error {
	event, err := decodeUserControlMsg(data)
	if err != nil {
		return err
	}
	switch event.code {
	case PingRequest:
		pong := makeUserControlMessage(PingResponse, int(event.data[0]))
		return cli.sendBatch(new(chunkBatch).write(cli.userCtrlChan, pong, nil, USER_CONTROL, 0, 0))
	default:
		// other events are informational, unknown ones are ignored
	}
	return nil
}

func (cli *RtmpClient) handleCommandRes(data []byte) error {
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
	case "_result":
		return cli.handleResult(data)
	case "_error":
		return cli.handleError(data)
	case "onStatus":
		return cli.handleStatus(data)
	default:
	}
	return nil
}

func (cli *RtmpClient) handleVideoMessage(msg *rtmpMessage) error {
	if cli.onframe == nil || len(msg.msg) < 1 {
		return nil
	}
	if msg.msg[0]&0x80 != 0 && len(msg.msg) < 5 {
		return nil
	}
	if cli.videoDemuxer == nil {
		demuxer, err := flv.CreateFlvVideoTagHandle(flv.GetFLVVideoCodecId(msg.msg))
		if err != nil {
			return err
		}
		cli.videoDemuxer = demuxer
		cli.videoDemuxer.OnFrame(func(codecid codec.CodecID, frame []byte, cts int) {
			dts := cli.timestamp
			pts := dts + uint32(cts)
			cli.onframe(codecid, pts, dts, frame)
		})
	}
	return cli.videoDemuxer.Decode(msg.msg)
}

func (cli *RtmpClient) handleAudioMessage(msg *rtmpMessage) error {
	if cli.onframe == nil || len(msg.msg) == 0 {
		return nil
	}
	if cli.audioDemuxer == nil {
		demuxer, err := flv.CreateAudioTagDemuxer(flv.FLV_SOUND_FORMAT((msg.msg[0] >> 4) & 0x0F))
		if err != nil {
			return err
		}
		cli.audioDemuxer = demuxer
		cli.audioDemuxer.OnFrame(func(codecid codec.CodecID, frame []byte) {
			dts := cli.timestamp
			pts := dts
			cli.onframe(codecid, pts, dts, frame)
		})
	}
	return cli.audioDemuxer.Decode(msg.msg)
}

func (cli *RtmpClient) handleResult(data []byte) error {
	switch cli.lastMethod {

	case CONNECT:
		return cli.handleConnectResponse(data)
	case CREATE_STREAM:
		return cli.handleCreateStreamResponse(data)
	case GET_STREAM_LENGTH:
		//TODO
	}
	return nil
}

func (cli *RtmpClient) handleConnectResponse(data []byte) error {

	items, _, err := decodeAmf0(data)
	if err != nil {
		return err
	}
	if len(items) > 0 {
		if tid, ok := items[0].value.(float64); ok {
			if cli.lastMethodTid != int(tid) {
				return nil
			}
		}
	}

	cli.lastMethod = CREATE_STREAM
	cli.lastMethodTid = 2
	batch := new(chunkBatch)
	if !cli.isPublish {
		ack := makeAcknowledgementSize(cli.wndAckSize)
		batch.write(cli.userCtrlChan, ack, nil, WND_ACK_SIZE, 0, 0)
		cmd, err := makeCreateStream(cli.streamName, 2)
		batch.write(cli.cmdChan, cmd, err, Command_AMF0, 0, 0)
		return cli.sendBatch(batch)
	}

	batch.write(cli.userCtrlChan, makeSetChunkSize(cli.writeChunkSize), nil, SET_CHUNK_SIZE, 0, 0)
	cli.cmdChan.chunkSize = cli.writeChunkSize
	cli.userCtrlChan.chunkSize = cli.writeChunkSize
	cli.sourceChan.chunkSize = cli.writeChunkSize
	buf, err := makeReleaseStream(cli.streamName)
	batch.write(cli.cmdChan, buf, err, Command_AMF0, 0, 0)
	buf, err = makeFcPublish(cli.streamName)
	batch.write(cli.cmdChan, buf, err, Command_AMF0, 0, 0)
	buf, err = makeCreateStream(cli.streamName, 2)
	batch.write(cli.cmdChan, buf, err, Command_AMF0, 0, 0)
	return cli.sendBatch(batch)
}

func (cli *RtmpClient) handleCreateStreamResponse(data []byte) error {

	items, _, err := decodeAmf0(data)
	if err != nil {
		return err
	}
	if len(items) > 0 {
		if tid, ok := items[0].value.(float64); ok {
			if cli.lastMethodTid != int(tid) {
				return nil
			}
		}
		if sid, ok := items[len(items)-1].value.(float64); ok {
			atomic.StoreUint32(&cli.streamId, uint32(sid))
		}
	}

	batch := new(chunkBatch)
	if !cli.isPublish {
		cli.lastMethod = GET_STREAM_LENGTH
		cli.lastMethodTid = 3
		cmd, err := makeGetStreamLength(3, cli.streamName)
		batch.write(cli.cmdChan, cmd, err, Command_AMF0, atomic.LoadUint32(&cli.streamId), 0)
		req, err := makePlay(int(cli.tid), cli.streamName, -1, -1, true)
		batch.write(cli.sourceChan, req, err, Command_AMF0, atomic.LoadUint32(&cli.streamId), 0)
		return cli.sendBatch(batch)
	}

	pub, err := makePublish(cli.streamName, PUBLISHING_LIVE)
	batch.write(cli.cmdChan, pub, err, Command_AMF0, atomic.LoadUint32(&cli.streamId), 0)
	return cli.sendBatch(batch)
}

func (cli *RtmpClient) handleError(data []byte) error {
	code := ""
	describe := ""
	_, objs, err := decodeAmf0(data)
	if err != nil {
		return err
	}
	for _, obj := range objs {
		for _, item := range obj.items {
			if item.name == "code" {
				code = amf0String(item.value)
			} else if item.name == "description" {
				describe = amf0String(item.value)
			}
		}
	}
	if cli.onerror != nil {
		cli.onerror(code, describe)
	}
	if cli.isPublish {
		cli.changeState(STATE_RTMP_PUBLISH_FAILED)
	} else {
		cli.changeState(STATE_RTMP_PLAY_FAILED)
	}
	return nil
}

func (cli *RtmpClient) handleStatus(data []byte) error {
	code := ""
	level := ""
	describe := ""

	foundInfoObj := false
	_, objs, err := decodeAmf0(data)
	if err != nil {
		return err
	}
	for _, obj := range objs {
		for _, item := range obj.items {
			if item.name == "code" {
				foundInfoObj = true
				code = amf0String(item.value)
			} else if item.name == "level" {
				level = amf0String(item.value)
			} else if item.name == "description" {
				describe = amf0String(item.value)
			}
		}
	}

	if cli.onstatus != nil && foundInfoObj {
		cli.onstatus(code, level, describe)
	}

	if code == string(NETSTREAM_PUBLISH_START) {
		cli.changeState(STATE_RTMP_PUBLISH_START)
	} else if code == string(NETSTREAM_PLAY_START) {
		cli.changeState(STATE_RTMP_PLAY_START)
	} else if level == string(LEVEL_ERROR) {
		if cli.isPublish {
			cli.changeState(STATE_RTMP_PUBLISH_FAILED)
		} else {
			cli.changeState(STATE_RTMP_PLAY_FAILED)
		}
	}
	return nil
}

// amf0String returns the string value of an amf0 string item, or "" for any other type
func amf0String(item amf0Item) string {
	if v, ok := item.value.([]byte); ok {
		return string(v)
	}
	return ""
}
