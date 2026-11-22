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

func makePublish(pubName, pubType string) ([]byte, error) {
	var b amf0Buf
	return b.item(makeStringItem("publish")).
		item(makeNumberItem(0)).
		null().
		item(makeStringItem(pubName)).
		item(makeStringItem(pubType)).
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
