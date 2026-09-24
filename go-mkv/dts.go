package mkv

import (
	"container/heap"
	"sort"
)

// reorderWindow is how many frames of a track are looked at before the
// oldest one gets its decode time. It has to exceed the deepest frame
// reordering of the stream; x264 and x265 stay well below it.
const reorderWindow = 16

// dtsReorder derives decode times for a video track. Matroska only stores
// presentation times, but every muxer of this module wants a decode time
// that never goes backwards and never passes the presentation time.
//
// In decode order the n-th frame decodes at the n-th smallest presentation
// time, shifted back by the stream's reordering depth: with I0 P3 B1 B2 the
// P frame has to be decoded one frame before the B frame shown first after
// the I frame. The depth is measured on the first window of frames.
//
// The first frames of a stream that starts at zero decode before zero, so
// decode times are signed here; the demuxer moves the whole file forward
// by shift() to keep them representable.
type dtsReorder struct {
	pending  []*queued
	pts      ptsHeap
	emitted  int
	measured bool
	delay    int
	step     int64
	minPts   int64
	started  bool
	lastDts  int64
}

func newDtsReorder() *dtsReorder {
	return &dtsReorder{}
}

func (r *dtsReorder) push(q *queued) {
	r.pending = append(r.pending, q)
	heap.Push(&r.pts, q.pkt.Pts)
	if len(r.pending) >= reorderWindow {
		r.resolveOldest()
	}
}

func (r *dtsReorder) flush() {
	for len(r.pending) > 0 {
		r.resolveOldest()
	}
}

// shift is how far the track's timeline has to move forward for its first
// decode time to be zero or later.
func (r *dtsReorder) shift() uint64 {
	if !r.measured {
		return 0
	}
	return uint64(max(int64(r.delay)*r.step-r.minPts, 0))
}

// measure finds the reordering depth and frame spacing of the frames
// pending so far. It runs once, on the first window.
func (r *dtsReorder) measure() {
	n := len(r.pending)
	if r.measured || n == 0 {
		return
	}
	r.measured = true
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return r.pending[order[a]].pkt.Pts < r.pending[order[b]].pkt.Pts
	})
	for rank, i := range order {
		// a frame decoded later than its place in presentation order needs
		// that many frames of head start
		r.delay = max(r.delay, i-rank)
	}
	r.minPts = int64(r.pending[order[0]].pkt.Pts)
	if n > 1 {
		r.step = (int64(r.pending[order[n-1]].pkt.Pts) - r.minPts) / int64(n-1)
	}
}

func (r *dtsReorder) resolveOldest() {
	r.measure()
	q := r.pending[0]
	r.pending[0] = nil
	r.pending = r.pending[1:]

	var dts int64
	if r.emitted < r.delay {
		// frames decoded ahead of the first presentation time
		dts = int64(r.pts[0]) - int64(r.delay-r.emitted)*r.step
	} else {
		dts = int64(heap.Pop(&r.pts).(uint64))
	}
	r.emitted++

	pts := int64(q.pkt.Pts)
	dts = min(dts, pts)
	if r.started && dts <= r.lastDts {
		// keep decode times strictly increasing where the presentation time
		// leaves room, and never let them go backwards
		dts = max(min(r.lastDts+1, pts), r.lastDts)
	}
	r.started = true
	r.lastDts = dts
	q.dts = dts
	q.resolved = true
}

type ptsHeap []uint64

func (h ptsHeap) Len() int           { return len(h) }
func (h ptsHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h ptsHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *ptsHeap) Push(x any)        { *h = append(*h, x.(uint64)) }
func (h *ptsHeap) Pop() any {
	old := *h
	v := old[len(old)-1]
	*h = old[:len(old)-1]
	return v
}
