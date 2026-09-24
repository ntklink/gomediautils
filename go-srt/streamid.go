package srt

import "strings"

// StreamIDInfo is a stream id in the SRT access control syntax,
// "#!::r=live/cam1,m=publish,u=alice". A stream id without the "#!::"
// prefix is taken whole as the resource name, which is how most servers
// treat a plain one.
type StreamIDInfo struct {
	Resource string // r
	Mode     string // m: request (the default), publish or bidirectional
	User     string // u
	Session  string // s
	Type     string // t: stream (the default), file or auth
	Host     string // h
	// Params holds every key, the standard ones above included.
	Params map[string]string
}

// ParseStreamID splits a stream id into its keys.
func ParseStreamID(sid string) StreamIDInfo {
	info := StreamIDInfo{Params: map[string]string{}}
	body, ok := strings.CutPrefix(sid, "#!::")
	if !ok {
		info.Resource = sid
		info.Mode = "request"
		return info
	}
	for _, kv := range strings.Split(body, ",") {
		k, v, _ := strings.Cut(kv, "=")
		info.Params[k] = v
	}
	info.Resource = info.Params["r"]
	info.Mode = info.Params["m"]
	info.User = info.Params["u"]
	info.Session = info.Params["s"]
	info.Type = info.Params["t"]
	info.Host = info.Params["h"]
	if info.Mode == "" {
		info.Mode = "request"
	}
	return info
}
