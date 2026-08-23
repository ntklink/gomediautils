package rtsp

import (
	"errors"
	"strconv"
	"strings"
)

type RtpInfo struct {
	Url     string
	Seq     uint16
	Rtptime int64 // -1 means absent
}

func NewRtpInfo(url string, seq uint16) *RtpInfo {
	return &RtpInfo{Url: url, Seq: seq, Rtptime: -1}
}

// EncodeString formats a single RTP-Info entry: url=...;seq=N[;rtptime=M]
func (info *RtpInfo) EncodeString() string {
	str := "url=" + info.Url + ";seq=" + strconv.Itoa(int(info.Seq))
	if info.Rtptime >= 0 {
		str += ";rtptime=" + strconv.FormatInt(info.Rtptime, 10)
	}
	return str
}

// Decode parses one RTP-Info entry. Items without '=' are ignored, an item
// with a value that is not a number is reported.
func (info *RtpInfo) Decode(str string) error {
	info.Rtptime = -1
	items := strings.Split(str, ";")
	for _, item := range items {
		kv := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(kv) < 2 {
			continue
		}
		value := strings.TrimSpace(kv[1])
		switch strings.TrimSpace(kv[0]) {
		case "url":
			info.Url = value
		case "seq":
			seq, err := strconv.ParseUint(value, 10, 16)
			if err != nil {
				return errors.New("rtsp: illegal seq in RTP-Info: " + item)
			}
			info.Seq = uint16(seq)
		case "rtptime":
			t, err := strconv.ParseInt(value, 10, 64)
			if err != nil || t < 0 {
				return errors.New("rtsp: illegal rtptime in RTP-Info: " + item)
			}
			info.Rtptime = t
		}
	}
	return nil
}
