package es

// lookahead is how many access units are held back to place a frame in
// presentation order. It has to exceed the deepest reordering of the
// stream; x264 and x265 stay far below it.
const lookahead = 16

// orderKey sorts pictures into presentation order: the picture order count
// within a coded video sequence, sequences one after the other.
type orderKey struct {
	period int64
	poc    int64
}

func (a orderKey) less(b orderKey) bool {
	if a.period != b.period {
		return a.period < b.period
	}
	return a.poc < b.poc
}

type orderedFrame struct {
	frame    *Frame
	key      orderKey
	dtsIndex int64
	ptsIndex int64
}

// orderer turns decode order plus picture order counts into frame indexes
// for the decode and presentation times. The n-th frame decodes at frame n
// and is shown at its rank in presentation order, plus the reordering
// delay of the stream so that no frame is shown before it is decoded.
type orderer struct {
	window   []*orderedFrame
	history  []orderKey
	emitted  int64
	delay    int64
	measured bool
}

func newOrderer() *orderer {
	return &orderer{}
}

func (o *orderer) push(f *Frame, key orderKey) []*orderedFrame {
	o.window = append(o.window, &orderedFrame{frame: f, key: key})
	if len(o.window) <= lookahead {
		return nil
	}
	return []*orderedFrame{o.resolve()}
}

func (o *orderer) flush() []*orderedFrame {
	var out []*orderedFrame
	for len(o.window) > 0 {
		out = append(out, o.resolve())
	}
	return out
}

// rank is how many frames come before the window's frame i in presentation
// order: every frame decoded before it, less those shown after it, plus the
// frames decoded after it that are shown before it.
func (o *orderer) rank(i int) int64 {
	key := o.window[i].key
	r := o.emitted + int64(i)
	for _, k := range o.history {
		if key.less(k) {
			r--
		}
	}
	for j, f := range o.window {
		switch {
		case j < i && key.less(f.key):
			r--
		case j > i && f.key.less(key):
			r++
		}
	}
	return r
}

// measure finds the reordering delay on the first window: the most frames
// any of them is decoded ahead of its place in presentation order.
func (o *orderer) measure() {
	o.measured = true
	for i := range o.window {
		o.delay = max(o.delay, int64(i)-o.rank(i))
	}
}

func (o *orderer) resolve() *orderedFrame {
	if !o.measured {
		o.measure()
	}
	f := o.window[0]
	f.dtsIndex = o.emitted
	// a stream reordering deeper than the first window showed is held to
	// its decode time rather than shown before it is decoded
	f.ptsIndex = max(o.rank(0)+o.delay, f.dtsIndex)

	o.window[0] = nil
	o.window = o.window[1:]
	o.history = append(o.history, f.key)
	if len(o.history) > lookahead {
		o.history = o.history[1:]
	}
	o.emitted++
	return f
}
