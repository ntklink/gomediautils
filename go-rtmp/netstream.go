package rtmp

type NetStreamStatusCode string

func makePlay(transactionId int, streamName string, start float64, duration float64, reset bool) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("play")).
		item(makeNumberItem(float64(transactionId))).
		null().
		item(makeStringItem(streamName)).
		item(makeNumberItem(start)).
		item(makeNumberItem(duration)).
		item(makeBoolItem(reset)).
		bytes()
}

func makeLivePlay(transactionId int, streamName string) ([]byte, error) {
	return makePlay(transactionId, streamName, -1, -1, true)
}

func makeDeleteStream(streamId int) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("deleteStream")).
		item(makeNumberItem(0)).
		null().
		item(makeNumberItem(float64(streamId))).
		bytes()
}

func makeReceiveAudio(flag bool) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("receiveAudio")).
		item(makeNumberItem(0)).
		null().
		item(makeBoolItem(flag)).
		bytes()
}

func makeReceiveVideo(flag bool) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("receiveVideo")).
		item(makeNumberItem(0)).
		null().
		item(makeBoolItem(flag)).
		bytes()
}

func makePublish(pubName, pubType string) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("publish")).
		item(makeNumberItem(0)).
		null().
		item(makeStringItem(pubName)).
		item(makeStringItem(pubType)).
		bytes()
}

func makeSeek(milliSeconds float64) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("seek")).
		item(makeNumberItem(0)).
		null().
		item(makeNumberItem(milliSeconds)).
		bytes()
}

func makePause(pause bool, milliSeconds float64) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("pause")).
		item(makeNumberItem(0)).
		null().
		item(makeBoolItem(pause)).
		item(makeNumberItem(milliSeconds)).
		bytes()
}

func makeReleaseStream(streamName string) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("releaseStream")).
		item(makeNumberItem(0)).
		null().
		item(makeStringItem(streamName)).
		bytes()
}

func makeFcPublish(streamName string) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("FCPublish")).
		item(makeNumberItem(0)).
		null().
		item(makeStringItem(streamName)).
		bytes()
}

func makeFcUnPublish(streamName string) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("FCUnpublish")).
		item(makeNumberItem(0)).
		null().
		item(makeStringItem(streamName)).
		bytes()
}

func makeStatusRes(transactionId int, code StatusCode, level StatusLevel, description string) ([]byte, error) {
	des := amfObject{
		items: []*amfObjectItem{
			{name: "level", value: makeStringItem(string(level))},
			{name: "code", value: makeStringItem(string(code))},
			{name: "description", value: makeStringItem(description)},
		},
	}
	var b amf0Buf
	return b.item(makeStringItem("onStatus")).
		item(makeNumberItem(float64(transactionId))).
		null().
		object(&des).
		bytes()
}
