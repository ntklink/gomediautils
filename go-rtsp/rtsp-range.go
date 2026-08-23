package rtsp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type RangeType int

const (
	RANGE_NPT RangeType = iota
	RANGE_UTC
)

type RangeTime struct {
	rangeType RangeType
	begin     int64 // -1 means now
	end       int64 // -1 meas has no end
}

func (rt RangeTime) EncodeString() string {
	switch rt.rangeType {
	case RANGE_NPT:
		npt := "npt="
		if rt.begin == -1 {
			npt += "now-"
		} else {
			npt += fmt.Sprintf("%d.%03d-", rt.begin/1000, rt.begin%1000)
			if rt.end != -1 {
				npt += fmt.Sprintf("%d.%03d", rt.end/1000, rt.end%1000)
			}
		}
		return npt
	case RANGE_UTC:
		clock := "clock="
		beg := time.Unix(rt.begin/1000, rt.begin%1000*1000000)
		clock += beg.UTC().Format("20060102T150405.999Z-")
		if rt.end != -1 {
			end := time.Unix(rt.end/1000, rt.end%1000*1000000)
			clock += end.UTC().Format("20060102T150405.999Z")
		}
		return clock
	default:
		return ""
	}
}

// parseNPT converts a normal play time, either "h:mm:ss.mmm" or a number of
// seconds, into milliseconds. A time it can not read is reported instead of
// being turned into 0, which is a valid play time.
func parseNPT(npt string) (int64, error) {
	npt = strings.TrimSpace(npt)
	if strings.Contains(npt, ":") {
		var h, m, s, mill int
		// the fraction is optional, three fields are enough
		r, err := fmt.Sscanf(npt, "%d:%d:%d.%d", &h, &m, &s, &mill)
		if r < 3 {
			return 0, fmt.Errorf("illegal npt time %q: %v", npt, err)
		}
		timeInMilliseconds := (h*3600+m*60+s)*1000 + mill
		if r == 3 {
			timeInMilliseconds = (h*3600 + m*60 + s) * 1000
		}
		return int64(timeInMilliseconds), nil
	}
	t, err := strconv.ParseFloat(npt, 64)
	if err != nil {
		return 0, fmt.Errorf("illegal npt time %q", npt)
	}
	return int64(t * 1000), nil
}

func parseClock(str string) (int64, error) {
	str = strings.TrimSpace(str)
	if str == "" {
		return -1, nil
	}
	for _, layout := range []string{"20060102T150405.999Z", "20060102T150405Z"} {
		if t, err := time.Parse(layout, str); err == nil {
			return t.UTC().UnixNano() / 1000000, nil
		}
	}
	return 0, errors.New("illegal clock time " + str)
}

func parseRange(str string) (*RangeTime, error) {
	strs := strings.Split(str, ";")
	rt := &RangeTime{begin: -1, end: -1}
	timestr := strings.SplitN(strings.TrimSpace(strs[0]), "=", 2)
	if len(timestr) < 2 {
		return rt, errors.New("illegal Range header: " + str)
	}
	switch timestr[0] {
	case "npt":
		rt.rangeType = RANGE_NPT
		tp := strings.SplitN(timestr[1], "-", 2)
		var err error
		if strings.TrimSpace(tp[0]) == "now" || strings.TrimSpace(tp[0]) == "" {
			rt.begin = -1
		} else if rt.begin, err = parseNPT(tp[0]); err != nil {
			return rt, err
		}
		rt.end = -1
		if len(tp) > 1 && strings.TrimSpace(tp[1]) != "" {
			if rt.end, err = parseNPT(tp[1]); err != nil {
				return rt, err
			}
		}
		return rt, nil
	case "clock":
		rt.rangeType = RANGE_UTC
		tp := strings.SplitN(timestr[1], "-", 2)
		var err error
		if rt.begin, err = parseClock(tp[0]); err != nil {
			return rt, err
		}
		rt.end = -1
		if len(tp) > 1 {
			if rt.end, err = parseClock(tp[1]); err != nil {
				return rt, err
			}
		}
		return rt, nil
	default:
		return rt, errors.New("unsupport " + timestr[0] + " Range type")
	}
}
