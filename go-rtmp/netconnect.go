package rtmp

// The makeXXX helpers build amf0 command messages. They report an error when a
// value cannot be represented in amf0 (in practice an app name, stream name or
// description longer than 65535 bytes) instead of writing a truncated message.

func makeConnect(app, tcurl string) ([]byte, error) {
	obj := amfObject{
		items: []*amfObjectItem{
			{name: "app", value: makeStringItem(app)},
			{name: "flashVer", value: makeStringItem("FMSc/1.0")},
			{name: "tcUrl", value: makeStringItem(tcurl)},
			{name: "fpad", value: makeBoolItem(false)},
			{name: "capabilities", value: makeNumberItem(15)},
			{name: "audioCodecs", value: makeNumberItem(4071)},
			{name: "videoCodecs", value: makeNumberItem(252)},
		},
	}
	var b amf0Buf
	return b.item(makeStringItem("connect")).
		item(makeNumberItem(1)).
		object(&obj).
		bytes()
}

func makeConnectRes() ([]byte, error) {
	properties := amfObject{
		items: []*amfObjectItem{
			{name: "fmsVer", value: makeStringItem("FMS/3,0,1,123")},
			{name: "capabilities", value: makeNumberItem(15)},
		},
	}
	information := amfObject{
		items: []*amfObjectItem{
			{name: "level", value: makeStringItem("status")},
			{name: "code", value: makeStringItem("NetConnection.Connect.Success")},
			{name: "description", value: makeStringItem("Connection Succeeded")},
			{name: "objectEncoding", value: makeNumberItem(0)},
		},
	}
	var b amf0Buf
	return b.item(makeStringItem("_result")).
		item(makeNumberItem(1)).
		object(&properties).
		object(&information).
		bytes()
}

func makeCreateStream(streamName string, tid int) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("createStream")).
		item(makeNumberItem(float64(tid))).
		null().
		bytes()
}

func makeCreateStreamRes(transactionId uint32, streamId uint32) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("_result")).
		item(makeNumberItem(float64(transactionId))).
		null().
		item(makeNumberItem(float64(streamId))).
		bytes()
}

func makeGetStreamLength(transactionId int, streamName string) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("getStreamLength")).
		item(makeNumberItem(float64(transactionId))).
		null().
		item(makeStringItem(streamName)).
		bytes()
}

func makeGetStreamLengthRes(transactionId int, duration float64) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("_result")).
		item(makeNumberItem(float64(transactionId))).
		null().
		item(makeNumberItem(duration)).
		bytes()
}

func makeErrorRes(transactionId int, level, code, description string) ([]byte, error) {
	des := amfObject{
		items: []*amfObjectItem{
			{name: "level", value: makeStringItem(level)},
			{name: "code", value: makeStringItem(code)},
			{name: "description", value: makeStringItem(description)},
		},
	}
	var b amf0Buf
	return b.item(makeStringItem("_error")).
		item(makeNumberItem(float64(transactionId))).
		null().
		object(&des).
		bytes()
}
