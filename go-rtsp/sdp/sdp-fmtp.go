package sdp

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yapingcat/gomedia/go-codec"
)

// FmtpCodecParamParser reads and writes the codec parameters of an
// "a=fmtp" attribute. Load reports malformed parameters instead of silently
// leaving the parser half configured.
type FmtpCodecParamParser interface {
	Load(fmtp string) error
	Save() string
}

// CreateFmtpParamParser returns a parser for the given rtpmap encoding name,
// or nil when this package has no parameter parser for it.
func CreateFmtpParamParser(name string) FmtpCodecParamParser {
	var (
		parser FmtpCodecParamParser
		err    error
	)
	switch strings.ToLower(name) {
	case "h264":
		parser, err = NewH264FmtpParam()
	case "h265":
		parser, err = NewH265FmtpParam()
	case "mpeg4-generic":
		parser, err = NewAACFmtpParam()
	default:
		return nil
	}
	if err != nil {
		// no option was passed, the defaults are always valid
		return nil
	}
	return parser
}

// H264ExtraOption configures a H264FmtpParam. An option that is given an
// unusable value reports it instead of panicking or storing it silently.
type H264ExtraOption func(param *H264FmtpParam) error

// stripStartCode returns a copy of nalu without its start code.
func stripStartCode(nalu []byte) ([]byte, error) {
	idx, sc := codec.FindStartCode(nalu, 0)
	if idx >= 0 {
		nalu = nalu[idx+int(sc):]
	}
	if len(nalu) == 0 {
		return nil, errors.New("sdp: empty nalu")
	}
	out := make([]byte, len(nalu))
	copy(out, nalu)
	return out, nil
}

type H264FmtpParam struct {
	packetizationMode int
	profileLevelId    []byte
	sps               []byte
	pps               []byte
}

func WithPacketizationMode(mode int) H264ExtraOption {
	return func(param *H264FmtpParam) error {
		// rfc 6184: single nalu, non interleaved, interleaved
		if mode < 0 || mode > 2 {
			return fmt.Errorf("sdp: packetization-mode %d out of range", mode)
		}
		param.packetizationMode = mode
		return nil
	}
}

func WithProfileLevelId(profileLevel []byte) H264ExtraOption {
	return func(param *H264FmtpParam) error {
		if len(profileLevel) != 3 {
			return fmt.Errorf("sdp: profile-level-id must be 3 bytes, got %d", len(profileLevel))
		}
		param.profileLevelId = make([]byte, 3)
		copy(param.profileLevelId, profileLevel)
		return nil
	}
}

func WithH264SPS(sps []byte) H264ExtraOption {
	return func(param *H264FmtpParam) (err error) {
		param.sps, err = stripStartCode(sps)
		return
	}
}

func WithH264PPS(pps []byte) H264ExtraOption {
	return func(param *H264FmtpParam) (err error) {
		param.pps, err = stripStartCode(pps)
		return
	}
}

// a=fmtp:98 profile-level-id=42A01E;
//
//	packetization-mode=1;
//	sprop-parameter-sets=<parameter sets data>
func NewH264FmtpParam(opt ...H264ExtraOption) (*H264FmtpParam, error) {
	param := &H264FmtpParam{packetizationMode: 1}
	for _, o := range opt {
		if err := o(param); err != nil {
			return nil, err
		}
	}
	return param, nil
}

func (param *H264FmtpParam) GetSpsPps() ([]byte, []byte) {
	return param.sps, param.pps
}

func (param *H264FmtpParam) Load(fmtp string) error {
	items := strings.SplitN(fmtp, " ", 2)
	if len(items) < 2 {
		return fmt.Errorf("sdp: fmtp without codec parameters: %q", fmtp)
	}

	codecParam := strings.Split(items[1], ";")
	for _, p := range codecParam {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) < 2 {
			continue
		}
		var err error
		switch kv[0] {
		case "packetization-mode":
			param.packetizationMode, err = strconv.Atoi(strings.TrimSpace(kv[1]))
		case "sprop-parameter-sets":
			err = param.loadParameterSets(kv[1])
		case "profile-level-id":
			profileLevelId := make([]byte, 3)
			_, err = fmt.Sscanf(strings.TrimSpace(kv[1]), "%02x%02x%02x", &profileLevelId[0], &profileLevelId[1], &profileLevelId[2])
			if err == nil {
				param.profileLevelId = profileLevelId
			}
		}
		if err != nil {
			return fmt.Errorf("sdp: illegal h264 fmtp parameter %q: %w", p, err)
		}
	}
	return nil
}

func (param *H264FmtpParam) loadParameterSets(value string) error {
	spspps := strings.Split(value, ",")
	sps, err := base64.StdEncoding.DecodeString(strings.TrimSpace(spspps[0]))
	if err != nil {
		return err
	}
	param.sps = sps
	if len(spspps) > 1 {
		pps, err := base64.StdEncoding.DecodeString(strings.TrimSpace(spspps[1]))
		if err != nil {
			return err
		}
		param.pps = pps
	}
	return nil
}

func (param *H264FmtpParam) Save() string {
	paramStr := ""
	if len(param.profileLevelId) == 3 {
		paramStr += fmt.Sprintf("profile-level-id=%02x%02x%02x;", param.profileLevelId[0], param.profileLevelId[1], param.profileLevelId[2])
	}
	paramStr += fmt.Sprintf("packetization-mode=%d", param.packetizationMode)
	if len(param.sps) > 0 && len(param.pps) > 0 {
		paramStr += fmt.Sprintf(";sprop-parameter-sets=%s,%s", base64.StdEncoding.EncodeToString(param.sps), base64.StdEncoding.EncodeToString(param.pps))
	}
	return paramStr
}

type H265FmtpParam struct {
	sps []byte
	pps []byte
	vps []byte
}

// H265FmtpPramOption configures a H265FmtpParam and reports a value it can
// not use.
type H265FmtpPramOption func(extra *H265FmtpParam) error

func WithH265SPS(sps []byte) H265FmtpPramOption {
	return func(extra *H265FmtpParam) (err error) {
		extra.sps, err = stripStartCode(sps)
		return
	}
}

func WithH265PPS(pps []byte) H265FmtpPramOption {
	return func(extra *H265FmtpParam) (err error) {
		extra.pps, err = stripStartCode(pps)
		return
	}
}

func WithH265VPS(vps []byte) H265FmtpPramOption {
	return func(extra *H265FmtpParam) (err error) {
		extra.vps, err = stripStartCode(vps)
		return
	}
}

// a=fmtp:96 sprop-vps=QAEMAfAIAAAAMAAAMAAAMAAAMAALUCQA==;sprop-sps=QgEBAIAAAAMAAAMAAAMAAAMAAKACgIAtH+W1kkbQzkkktySqSfKSyA==;sprop-pps=RAHBpVgeSA==
func NewH265FmtpParam(opt ...H265FmtpPramOption) (*H265FmtpParam, error) {
	param := &H265FmtpParam{}
	for _, o := range opt {
		if err := o(param); err != nil {
			return nil, err
		}
	}
	return param, nil
}

func (param *H265FmtpParam) GetVpsSpsPps() ([]byte, []byte, []byte) {
	return param.vps, param.sps, param.pps
}

func (param *H265FmtpParam) Load(fmtp string) error {
	items := strings.SplitN(fmtp, " ", 2)
	if len(items) < 2 {
		return fmt.Errorf("sdp: fmtp without codec parameters: %q", fmtp)
	}

	codecParams := strings.Split(items[1], ";")
	for _, p := range codecParams {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) < 2 {
			continue
		}
		var (
			nalu []byte
			err  error
		)
		switch kv[0] {
		case "sprop-vps", "sprop-sps", "sprop-pps":
			nalu, err = base64.StdEncoding.DecodeString(strings.TrimSpace(kv[1]))
		default:
			continue
		}
		if err != nil {
			return fmt.Errorf("sdp: illegal h265 fmtp parameter %q: %w", p, err)
		}
		switch kv[0] {
		case "sprop-vps":
			param.vps = nalu
		case "sprop-sps":
			param.sps = nalu
		case "sprop-pps":
			param.pps = nalu
		}
	}
	return nil
}

func (param *H265FmtpParam) Save() string {
	if len(param.pps) == 0 || len(param.vps) == 0 || len(param.sps) == 0 {
		return ""
	}
	return fmt.Sprintf("sprop-vps=%s; sprop-sps=%s; sprop-pps=%s", base64.StdEncoding.EncodeToString(param.vps),
		base64.StdEncoding.EncodeToString(param.sps), base64.StdEncoding.EncodeToString(param.pps))
}

// m=audio 49230 RTP/AVP 96
// a=rtpmap:96 mpeg4-generic/48000/6
// a=fmtp:96 streamtype=5; profile-level-id=16; mode=AAC-hbr;
// config=11B0; sizeLength=13; indexLength=3;indexDeltaLength=3
type AACFmtpParam struct {
	asc              []byte
	profileLevelId   int
	mode             string
	sizeLength       int
	indexLength      int
	indexDeltaLength int
}

// AACFmtpParamOption configures an AACFmtpParam and reports a value it can
// not use.
type AACFmtpParamOption func(extra *AACFmtpParam) error

// WithAudioSpecificConfig sets the "config" parameter. The audio specific
// config of an AAC stream is at least 2 bytes long.
func WithAudioSpecificConfig(asc []byte) AACFmtpParamOption {
	return func(extra *AACFmtpParam) error {
		if len(asc) < 2 {
			return fmt.Errorf("sdp: audio specific config must be at least 2 bytes, got %d", len(asc))
		}
		extra.asc = make([]byte, len(asc))
		copy(extra.asc, asc)
		return nil
	}
}

func NewAACFmtpParam(opt ...AACFmtpParamOption) (*AACFmtpParam, error) {
	param := &AACFmtpParam{
		mode:             "AAC-hbr",
		sizeLength:       13,
		indexLength:      3,
		indexDeltaLength: 3,
	}
	for _, o := range opt {
		if err := o(param); err != nil {
			return nil, err
		}
	}
	return param, nil
}

func (param *AACFmtpParam) SizeLength() int {
	return param.sizeLength
}

func (param *AACFmtpParam) IndexLength() int {
	return param.indexLength
}

func (param *AACFmtpParam) IndexDeltaLength() int {
	return param.indexDeltaLength
}

func (param *AACFmtpParam) AudioSpecificConfig() []byte {
	return param.asc
}

func (param *AACFmtpParam) Load(fmtp string) error {
	items := strings.SplitN(fmtp, " ", 2)
	if len(items) < 2 {
		return fmt.Errorf("sdp: fmtp without codec parameters: %q", fmtp)
	}

	codecParams := strings.Split(items[1], ";")
	for _, p := range codecParams {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) < 2 {
			continue
		}
		value := strings.TrimSpace(kv[1])
		var err error
		// rfc 3640 names the mode parameters in mixed case but they are
		// compared case insensitively
		switch strings.ToLower(kv[0]) {
		case "profile-level-id":
			param.profileLevelId, err = strconv.Atoi(value)
		case "mode":
			param.mode = value
		case "config":
			param.asc, err = hex.DecodeString(value)
		case "sizelength":
			param.sizeLength, err = strconv.Atoi(value)
		case "indexlength":
			param.indexLength, err = strconv.Atoi(value)
		case "indexdeltalength":
			param.indexDeltaLength, err = strconv.Atoi(value)
		}
		if err != nil {
			return fmt.Errorf("sdp: illegal aac fmtp parameter %q: %w", p, err)
		}
	}
	return nil
}

func (param *AACFmtpParam) Save() string {

	paramstr := fmt.Sprintf("streamtype=5;mode=%s;sizeLength=%d;indexLength=%d;indexDeltaLength=%d",
		param.mode, param.sizeLength, param.indexLength, param.indexDeltaLength)

	if param.profileLevelId > 0 {
		paramstr += ";profile-level-id=" + strconv.Itoa(param.profileLevelId)
	}

	if len(param.asc) > 0 {
		paramstr += ";config=" + hex.EncodeToString(param.asc)
	}

	return paramstr
}
