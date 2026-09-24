package srt

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"time"
)

// Config holds the options both sides of a connection share.
type Config struct {
	// StreamID is sent by a caller to tell the listener what it wants,
	// typically "#!::r=live/stream,m=publish" or a plain path.
	StreamID string
	// Latency is how long a packet may take to arrive, retransmissions
	// included, before it is given up. Each side asks for its own; the
	// larger one wins. 120 ms by default, as in libsrt.
	Latency time.Duration
	// Passphrase turns on AES encryption; 10 to 79 characters. Both sides
	// must use the same one.
	Passphrase string
	// PBKeyLen is the AES key length in bytes a caller asks for: 16 (the
	// default), 24 or 32.
	PBKeyLen int
	// PayloadSize is the largest message sent in one packet, 1316 bytes by
	// default: seven MPEG-TS packets. Writes larger than this are split.
	PayloadSize int
	// ConnectTimeout bounds the handshake of Dial, 3 s by default.
	ConnectTimeout time.Duration
	// PeerIdleTimeout closes a connection after this long without hearing
	// from the peer, 5 s by default.
	PeerIdleTimeout time.Duration
}

const (
	maxPayloadSize  = 1456 // 1500 byte MTU less IP, UDP and SRT headers
	maxReadyQueue   = 8192
	maxRecvWindow   = 1 << 16
	tickInterval    = 5 * time.Millisecond
	ackInterval     = 10 * time.Millisecond
	keepaliveEvery  = time.Second
	minNakInterval  = 20 * time.Millisecond
	maxLossPerNak   = 300
	receiverBufSize = 8192
)

func (c Config) withDefaults() (Config, error) {
	if c.Latency <= 0 {
		c.Latency = 120 * time.Millisecond
	}
	if c.Latency > 65535*time.Millisecond {
		return c, errors.New("srt: latency above 65535 ms")
	}
	if c.PBKeyLen == 0 {
		c.PBKeyLen = 16
	}
	if c.PBKeyLen != 16 && c.PBKeyLen != 24 && c.PBKeyLen != 32 {
		return c, errors.New("srt: PBKeyLen must be 16, 24 or 32")
	}
	if c.Passphrase != "" && (len(c.Passphrase) < 10 || len(c.Passphrase) > 79) {
		return c, errors.New("srt: passphrase must be 10 to 79 characters")
	}
	if c.PayloadSize <= 0 {
		c.PayloadSize = 1316
	}
	if c.PayloadSize > maxPayloadSize {
		return c, errors.New("srt: payload size above 1456 bytes")
	}
	if len(c.StreamID) > maxStreamID {
		return c, errors.New("srt: stream id longer than 512 bytes")
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = 3 * time.Second
	}
	if c.PeerIdleTimeout <= 0 {
		c.PeerIdleTimeout = 5 * time.Second
	}
	return c, nil
}

// Stats counts what happened on a connection.
type Stats struct {
	PacketsSent          uint64
	PacketsRetransmitted uint64
	PacketsReceived      uint64
	// PacketsLost is how many gaps the receiver saw and asked to resend.
	PacketsLost uint64
	// PacketsDropped is how many packets never arrived in time and were
	// skipped so that playback could go on.
	PacketsDropped uint64
	// PacketsSendDropped is how many packets the sender stopped holding
	// for retransmission because they were too old to arrive in time.
	PacketsSendDropped uint64
	// SendErrors is how many packets the socket refused to send (ENOBUFS,
	// a network change, ...). They are counted as lost and recovered by
	// retransmission like a packet lost on the way.
	SendErrors uint64
	// SendBuffered is how many sent packets the peer has not acknowledged
	// yet, and SendBufferDelay how long the oldest of them has waited. On a
	// healthy link they stay around one round trip's worth; growing towards
	// the latency they are the early sign of congestion that Write, which
	// never blocks, does not give. PacketsSendDropped is the late one.
	SendBuffered    int
	SendBufferDelay time.Duration
	RTT             time.Duration
}

type sentPacket struct {
	seq    uint32
	raw    []byte
	sentAt time.Time
}

type rcvPacket struct {
	payload []byte
	ts      uint64 // microseconds, extended past the 32 bit wrap
	skip    bool   // dropped at the sender's request
}

var errPeerIdle = errors.New("srt: peer stopped responding")

// Conn is an established SRT connection in live mode. Every Read returns
// one message, every Write sends one or more. It implements net.Conn.
type Conn struct {
	mu      sync.Mutex
	cfg     Config
	send    func([]byte) error
	onClose func()
	local   net.Addr
	remote  net.Addr

	socketID     uint32
	peerSocketID uint32
	streamID     string
	start        time.Time
	crypto       *cryptoCtx
	rcvLatency   time.Duration
	sndLatency   time.Duration

	// sender
	nextSeq  uint32
	nextMsg  uint32
	sndBuf   []*sentPacket // consecutive sequence numbers, oldest first
	lastSend time.Time
	lastData time.Time // last new data packet sent
	ackMoved time.Time // last time an ACK released packets

	// receiver
	rcvNext   uint32 // next sequence number to deliver
	rcvMax    uint32 // highest sequence number received
	rcvBuf    map[uint32]*rcvPacket
	loss      map[uint32]time.Time // lost sequence number, last reported
	tsbpdBase time.Time
	tsEpoch   uint64
	lastTs    uint32
	tsSeen    bool
	ackNo     uint32
	lastAck   uint32
	ackTime   time.Time
	ackSent   map[uint32]time.Time
	rtt       time.Duration
	rttVar    time.Duration
	rttSet    bool
	lastRecv  time.Time
	rcvBytes  uint64
	rcvPkts   uint64
	rateStart time.Time

	ready        [][]byte
	readyNotify  chan struct{}
	readDeadline time.Time
	closed       bool
	closeErr     error
	done         chan struct{}
	stats        Stats

	// response is the handshake a listener answered with, sent again when
	// the caller repeats its conclusion because the answer got lost
	response []byte
}

// connParams is what the handshake settled.
type connParams struct {
	socketID, peerSocketID uint32
	isn                    uint32
	peerTimestamp          uint32
	rcvLatency, sndLatency time.Duration
	crypto                 *cryptoCtx
	streamID               string
	local, remote          net.Addr
}

func newConn(cfg Config, p connParams, send func([]byte) error, onClose func(), start time.Time) *Conn {
	now := time.Now()
	c := &Conn{
		cfg:          cfg,
		send:         send,
		onClose:      onClose,
		local:        p.local,
		remote:       p.remote,
		socketID:     p.socketID,
		peerSocketID: p.peerSocketID,
		streamID:     p.streamID,
		start:        start,
		crypto:       p.crypto,
		rcvLatency:   p.rcvLatency,
		sndLatency:   p.sndLatency,
		nextSeq:      p.isn,
		nextMsg:      1,
		rcvNext:      p.isn,
		rcvMax:       seqAdd(p.isn, -1),
		lastAck:      p.isn,
		rcvBuf:       make(map[uint32]*rcvPacket),
		loss:         make(map[uint32]time.Time),
		ackSent:      make(map[uint32]time.Time),
		rtt:          100 * time.Millisecond,
		rttVar:       50 * time.Millisecond,
		lastRecv:     now,
		lastSend:     now,
		rateStart:    now,
		// the peer's clock read p.peerTimestamp when its handshake left;
		// its packets are played relative to that
		tsbpdBase:   now.Add(-time.Duration(p.peerTimestamp) * time.Microsecond),
		readyNotify: make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	go c.run()
	return c
}

// StreamID is the stream id the caller sent.
func (c *Conn) StreamID() string { return c.streamID }

func (c *Conn) LocalAddr() net.Addr  { return c.local }
func (c *Conn) RemoteAddr() net.Addr { return c.remote }

// Latency is the receive latency the two sides agreed on.
func (c *Conn) Latency() time.Duration { return c.rcvLatency }

// Stats returns the counters of the connection.
func (c *Conn) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.RTT = c.rtt
	s.SendBuffered = len(c.sndBuf)
	if len(c.sndBuf) > 0 {
		s.SendBufferDelay = time.Since(c.sndBuf[0].sentAt)
	}
	return s
}

func (c *Conn) now32() uint32 {
	return uint32(time.Since(c.start) / time.Microsecond)
}

func (c *Conn) sendControl(typ uint16, subtype uint16, typeInfo uint32, cif []byte) {
	if cif == nil {
		// libsrt expects at least one word of control information
		cif = make([]byte, 4)
	}
	p := packet{control: true, ctrlType: typ, subtype: subtype, typeInfo: typeInfo,
		timestamp: c.now32(), dstSocket: c.peerSocketID, payload: cif}
	c.send(p.marshal())
	c.lastSend = time.Now()
}

// Write sends b as one message, or as several of PayloadSize bytes when it
// is longer. Like libsrt in live mode it never blocks and does not report
// congestion or a failed send: a packet the socket refuses counts as lost
// and is sent again when the peer asks, and a packet still unacknowledged
// after the latency is given up. Stats shows both, for a sender that wants
// to know its link is falling behind. Write only fails once the connection
// is closed.
func (c *Conn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	now := time.Now()
	for off := 0; off < len(b); off += c.cfg.PayloadSize {
		chunk := b[off:min(off+c.cfg.PayloadSize, len(b))]
		p := packet{
			seq:       c.nextSeq,
			position:  positionSolo,
			msgNo:     c.nextMsg,
			timestamp: c.now32(),
			dstSocket: c.peerSocketID,
			payload:   append([]byte(nil), chunk...),
		}
		if c.crypto != nil {
			p.keyFlags = 1
			if err := c.crypto.xorPayload(1, p.seq, p.payload); err != nil {
				return off, err
			}
		}
		raw := p.marshal()
		// the packet takes its sequence number whether or not the socket
		// takes it: the send buffer must hold consecutive numbers for NAKs
		// to find what to resend, and a packet that failed to go out is
		// just a lost one to the receiver, which asks for it again
		c.sndBuf = append(c.sndBuf, &sentPacket{seq: p.seq, raw: raw, sentAt: now})
		if err := c.send(raw); err != nil {
			c.stats.SendErrors++
		} else {
			c.stats.PacketsSent++
		}
		c.lastSend = now
		c.lastData = now
		c.nextSeq = seqNext(c.nextSeq)
		c.nextMsg = (c.nextMsg + 1) & 0x03FFFFFF
		if c.nextMsg == 0 {
			c.nextMsg = 1
		}
	}
	return len(b), nil
}

// Read returns the next message, or as much of it as fits in b; the rest
// comes with the next Read. A Read never returns parts of two messages, so
// a buffer of PayloadSize bytes reads one message per call, and anything
// consuming an io.Reader (an MPEG-TS demuxer, io.Copy) can read a stream
// of messages as one byte stream.
func (c *Conn) Read(b []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.ready) > 0 {
			msg := c.ready[0]
			n := copy(b, msg)
			if n < len(msg) {
				c.ready[0] = msg[n:]
			} else {
				c.ready[0] = nil
				c.ready = c.ready[1:]
			}
			c.mu.Unlock()
			return n, nil
		}
		if c.closed {
			err := c.closeErr
			c.mu.Unlock()
			return 0, err
		}
		deadline := c.readDeadline
		c.mu.Unlock()

		var timeout <-chan time.Time
		if !deadline.IsZero() {
			d := time.Until(deadline)
			if d <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			t := time.NewTimer(d)
			defer t.Stop()
			timeout = t.C
		}
		select {
		case <-c.readyNotify:
		case <-c.done:
		case <-timeout:
			return 0, os.ErrDeadlineExceeded
		}
	}
}

func (c *Conn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *Conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	c.notify()
	return nil
}

// SetWriteDeadline is accepted for net.Conn; writes never block.
func (c *Conn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (c *Conn) notify() {
	select {
	case c.readyNotify <- struct{}{}:
	default:
	}
}

// Close tells the peer and releases the connection. It first gives the
// last packets written the time the latency allows: to be acknowledged,
// and to be played out, since a libsrt receiver throws away what it still
// holds when the shutdown arrives. A connection idle for longer than the
// latency closes at once.
func (c *Conn) Close() error {
	c.mu.Lock()
	linger := time.Now().Add(c.sndLatency + max(3*c.rtt, 100*time.Millisecond))
	for !c.closed && time.Now().Before(linger) &&
		(len(c.sndBuf) > 0 || time.Since(c.lastData) < c.sndLatency) {
		c.mu.Unlock()
		time.Sleep(tickInterval)
		c.mu.Lock()
	}
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.sendControl(ctrlShutdown, 0, 0, nil)
	c.closeLocked(net.ErrClosed)
	c.mu.Unlock()
	return nil
}

func (c *Conn) closeLocked(err error) {
	if c.closed {
		return
	}
	c.closed = true
	c.closeErr = err
	close(c.done)
	if c.onClose != nil {
		go c.onClose()
	}
}

func (c *Conn) run() {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case now := <-t.C:
			c.mu.Lock()
			c.tick(now)
			c.mu.Unlock()
		}
	}
}

func (c *Conn) tick(now time.Time) {
	if now.Sub(c.lastRecv) > c.cfg.PeerIdleTimeout {
		c.closeLocked(errPeerIdle)
		return
	}
	c.deliver(now)
	if now.Sub(c.ackTime) >= ackInterval {
		c.sendACK(now)
	}
	c.periodicNAK(now)
	c.dropOldSent(now)
	c.resendTail(now)
	if now.Sub(c.lastSend) >= keepaliveEvery {
		c.sendControl(ctrlKeepalive, 0, 0, nil)
	}
	for no, at := range c.ackSent {
		if now.Sub(at) > time.Second {
			delete(c.ackSent, no)
		}
	}
}

// handlePacket processes one packet from the peer.
func (c *Conn) handlePacket(p *packet) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.lastRecv = now
	if !p.control {
		c.handleData(p, now)
		return
	}
	switch p.ctrlType {
	case ctrlACK:
		c.handleACK(p)
	case ctrlNAK:
		c.handleNAK(p)
	case ctrlACKACK:
		if at, ok := c.ackSent[p.typeInfo]; ok {
			delete(c.ackSent, p.typeInfo)
			sample := now.Sub(at)
			if !c.rttSet {
				// the first measurement replaces the 100 ms guess outright,
				// as in libsrt; smoothing towards it would keep loss
				// reports slow for seconds
				c.rttSet = true
				c.rtt, c.rttVar = sample, sample/2
				break
			}
			diff := c.rtt - sample
			if diff < 0 {
				diff = -diff
			}
			c.rttVar = (3*c.rttVar + diff) / 4
			c.rtt = (7*c.rtt + sample) / 8
		}
	case ctrlDropReq:
		if len(p.payload) >= 8 {
			first := binary.BigEndian.Uint32(p.payload) & seqMask
			last := binary.BigEndian.Uint32(p.payload[4:]) & seqMask
			c.skipRange(first, last)
		}
	case ctrlShutdown:
		// the peer is done; what already arrived is still the stream, so
		// it is handed over without waiting out the latency
		c.flushReceived()
		c.closeLocked(io.EOF)
	case ctrlUserDefine:
		// an in band key refresh: install the new key and confirm it
		if p.subtype == extKMReq && c.crypto != nil {
			if c.crypto.applyKM(p.payload) == nil {
				c.sendControl(ctrlUserDefine, extKMRsp, 0, p.payload)
			}
		}
	}
}

// extendTs widens a 32 bit microsecond timestamp across its wrap, which
// comes every 71 minutes.
func (c *Conn) extendTs(ts uint32) uint64 {
	if !c.tsSeen {
		c.tsSeen = true
		c.lastTs = ts
		return uint64(ts)
	}
	switch {
	case ts < c.lastTs && c.lastTs-ts > 1<<31:
		c.tsEpoch += 1 << 32
		c.lastTs = ts
	case ts > c.lastTs && ts-c.lastTs > 1<<31:
		// a late packet from before the wrap
		if c.tsEpoch > 0 {
			return c.tsEpoch - 1<<32 + uint64(ts)
		}
	case ts > c.lastTs:
		c.lastTs = ts
	}
	return c.tsEpoch + uint64(ts)
}

func (c *Conn) handleData(p *packet, now time.Time) {
	d := seqDiff(c.rcvNext, p.seq)
	if d < 0 || d > maxRecvWindow {
		return
	}
	if _, dup := c.rcvBuf[p.seq]; dup {
		return
	}
	payload := append([]byte(nil), p.payload...)
	// an encrypted connection takes encrypted packets only and a clear one
	// clear packets only: libsrt encrypts every data packet once keys are
	// agreed, so anything else is not from the peer
	if (c.crypto != nil) != (p.keyFlags != 0) {
		return
	}
	if p.keyFlags != 0 && c.crypto.xorPayload(p.keyFlags, p.seq, payload) != nil {
		return
	}
	c.stats.PacketsReceived++
	c.rcvPkts++
	c.rcvBytes += uint64(len(payload))
	c.rcvBuf[p.seq] = &rcvPacket{payload: payload, ts: c.extendTs(p.timestamp)}
	delete(c.loss, p.seq)
	if seqLess(c.rcvMax, p.seq) {
		if p.seq != seqNext(c.rcvMax) {
			// a gap: everything between was lost on the way, report it
			// straight away
			var lost []uint32
			for s := seqNext(c.rcvMax); s != p.seq && len(lost) < maxLossRange; s = seqNext(s) {
				c.loss[s] = now
				lost = append(lost, s)
			}
			c.stats.PacketsLost += uint64(len(lost))
			c.sendNAK(lost)
		}
		c.rcvMax = p.seq
	}
	c.deliver(now)
}

func (c *Conn) sendNAK(seqs []uint32) {
	for len(seqs) > 0 {
		n := min(len(seqs), maxLossPerNak)
		c.sendControl(ctrlNAK, 0, 0, encodeLossList(seqs[:n]))
		seqs = seqs[n:]
	}
}

// periodicNAK reports again the losses a retransmission should have
// filled by now.
func (c *Conn) periodicNAK(now time.Time) {
	if len(c.loss) == 0 {
		return
	}
	interval := c.nakInterval()
	var due []uint32
	for s, at := range c.loss {
		if now.Sub(at) >= interval {
			due = append(due, s)
		}
	}
	slices.SortFunc(due, func(a, b uint32) int { return -int(seqDiff(a, b)) })
	if len(due) > maxLossPerNak {
		due = due[:maxLossPerNak]
	}
	for _, s := range due {
		c.loss[s] = now
	}
	c.sendNAK(due)
}

func (c *Conn) playTime(ts uint64) time.Time {
	return c.tsbpdBase.Add(time.Duration(ts)*time.Microsecond + c.rcvLatency)
}

// deliver hands over the packets whose play time has come, in order. A
// packet still missing when a later one is due is given up (too late
// packet drop), so a loss costs a glitch instead of stalling the stream.
func (c *Conn) deliver(now time.Time) {
	delivered := false
	for {
		if pkt, ok := c.rcvBuf[c.rcvNext]; ok {
			if !pkt.skip && now.Before(c.playTime(pkt.ts)) {
				break
			}
			delete(c.rcvBuf, c.rcvNext)
			c.rcvNext = seqNext(c.rcvNext)
			if !pkt.skip {
				if len(c.ready) >= maxReadyQueue {
					c.stats.PacketsDropped++
				} else {
					c.ready = append(c.ready, pkt.payload)
					delivered = true
				}
			}
			continue
		}
		if !seqLess(c.rcvNext, seqNext(c.rcvMax)) {
			break
		}
		next, found := c.rcvNext, false
		for s := seqNext(c.rcvNext); seqLess(s, seqNext(c.rcvMax)); s = seqNext(s) {
			if _, ok := c.rcvBuf[s]; ok {
				next, found = s, true
				break
			}
		}
		if !found || (!c.rcvBuf[next].skip && now.Before(c.playTime(c.rcvBuf[next].ts))) {
			break
		}
		for s := c.rcvNext; s != next; s = seqNext(s) {
			delete(c.loss, s)
			c.stats.PacketsDropped++
		}
		c.rcvNext = next
	}
	if delivered {
		c.notify()
	}
}

// flushReceived delivers every buffered packet in order, skipping the
// gaps, whatever their play time.
func (c *Conn) flushReceived() {
	for s := c.rcvNext; seqLess(s, seqNext(c.rcvMax)); s = seqNext(s) {
		if pkt, ok := c.rcvBuf[s]; ok && !pkt.skip && len(c.ready) < maxReadyQueue {
			c.ready = append(c.ready, pkt.payload)
		}
		delete(c.rcvBuf, s)
	}
	c.rcvNext = seqNext(c.rcvMax)
	c.notify()
}

// skipRange marks packets the sender will never send again.
func (c *Conn) skipRange(first, last uint32) {
	n := seqDiff(first, last)
	if n < 0 || n > maxLossRange {
		return
	}
	for s, i := first, int32(0); i <= n; s, i = seqNext(s), i+1 {
		if seqDiff(c.rcvNext, s) < 0 {
			continue
		}
		if _, ok := c.rcvBuf[s]; !ok {
			c.rcvBuf[s] = &rcvPacket{skip: true}
			delete(c.loss, s)
			if seqLess(c.rcvMax, s) {
				c.rcvMax = s
			}
		}
	}
}

// sendACK acknowledges everything received without a gap. The ACK number
// comes back in an ACKACK, which times the round trip.
func (c *Conn) sendACK(now time.Time) {
	ack := c.rcvNext
	for seqLess(ack, seqNext(c.rcvMax)) {
		if _, ok := c.rcvBuf[ack]; !ok {
			break
		}
		ack = seqNext(ack)
	}
	if ack == c.lastAck && now.Sub(c.ackTime) < 100*time.Millisecond {
		return
	}
	c.ackNo++
	if c.ackNo == 0 {
		c.ackNo = 1
	}
	elapsed := max(now.Sub(c.rateStart), time.Millisecond)
	pktRate := uint32(float64(c.rcvPkts) / elapsed.Seconds())
	byteRate := uint32(float64(c.rcvBytes) / elapsed.Seconds())
	if elapsed > time.Second {
		c.rcvPkts, c.rcvBytes, c.rateStart = 0, 0, now
	}
	cif := make([]byte, 28)
	binary.BigEndian.PutUint32(cif[0:], ack)
	binary.BigEndian.PutUint32(cif[4:], uint32(c.rtt/time.Microsecond))
	binary.BigEndian.PutUint32(cif[8:], uint32(c.rttVar/time.Microsecond))
	binary.BigEndian.PutUint32(cif[12:], uint32(max(receiverBufSize-len(c.rcvBuf), 2)))
	binary.BigEndian.PutUint32(cif[16:], pktRate)
	binary.BigEndian.PutUint32(cif[20:], pktRate)
	binary.BigEndian.PutUint32(cif[24:], byteRate)
	c.sendControl(ctrlACK, 0, c.ackNo, cif)
	c.ackSent[c.ackNo] = now
	c.lastAck = ack
	c.ackTime = now
}

// handleACK releases the packets the peer has, answers a full ACK with an
// ACKACK and takes the round trip time the peer measured.
func (c *Conn) handleACK(p *packet) {
	if len(p.payload) < 4 {
		return
	}
	ack := binary.BigEndian.Uint32(p.payload) & seqMask
	for len(c.sndBuf) > 0 && seqLess(c.sndBuf[0].seq, ack) {
		c.sndBuf[0] = nil
		c.sndBuf = c.sndBuf[1:]
		c.ackMoved = time.Now()
	}
	if len(p.payload) >= 16 {
		// a light ACK carries only the sequence number and needs no reply
		c.sendControl(ctrlACKACK, 0, p.typeInfo, nil)
		if rtt := binary.BigEndian.Uint32(p.payload[4:]); rtt > 0 && !c.rttSet {
			// a pure sender has no ACKACKs of its own to time; it takes
			// the receiver's measurement
			c.rtt = time.Duration(rtt) * time.Microsecond
			c.rttVar = time.Duration(binary.BigEndian.Uint32(p.payload[8:])) * time.Microsecond
		}
	}
}

// handleNAK resends what the peer lost, and tells it to stop waiting for
// packets no longer held.
func (c *Conn) handleNAK(p *packet) {
	var gone []uint32
	decodeLossList(p.payload, func(seq uint32) {
		if len(c.sndBuf) == 0 {
			gone = append(gone, seq)
			return
		}
		i := seqDiff(c.sndBuf[0].seq, seq)
		if i < 0 || int(i) >= len(c.sndBuf) {
			if i < 0 {
				gone = append(gone, seq)
			}
			return
		}
		raw := append([]byte(nil), c.sndBuf[i].raw...)
		markRetransmitted(raw)
		if c.send(raw) == nil {
			c.stats.PacketsRetransmitted++
			c.lastSend = time.Now()
		}
	})
	for i := 0; i < len(gone); {
		j := i
		for j+1 < len(gone) && gone[j+1] == seqNext(gone[j]) {
			j++
		}
		cif := binary.BigEndian.AppendUint32(nil, gone[i])
		cif = binary.BigEndian.AppendUint32(cif, gone[j])
		c.sendControl(ctrlDropReq, 0, 0, cif)
		i = j + 1
	}
}

// nakInterval is how long a reported loss waits before it is reported
// again. It follows the round trip time, but never lets a loss wait so
// long that a second report could not beat the latency; before the first
// measurement the round trip is only a guess.
func (c *Conn) nakInterval() time.Duration {
	return max(min(c.rtt+4*c.rttVar, c.rcvLatency/4), minNakInterval)
}

// resendTail recovers the loss a receiver cannot see: when the last
// packets sent went missing, nothing after them reveals the gap. Once ACKs
// stop moving while packets are still unacknowledged and nothing new has
// gone out, the unacknowledged packets are sent again.
func (c *Conn) resendTail(now time.Time) {
	if len(c.sndBuf) == 0 {
		return
	}
	// the retry has to come well before dropOldSent lets go of the packets,
	// even while the round trip is still the initial 100 ms guess
	wait := min(max(c.rtt+4*c.rttVar, minNakInterval), c.sendHoldLimit()/2)
	if now.Sub(c.ackMoved) < wait || now.Sub(c.lastData) < wait {
		return
	}
	for i, sp := range c.sndBuf {
		if i >= 64 || now.Sub(sp.sentAt) < wait {
			break
		}
		raw := append([]byte(nil), sp.raw...)
		markRetransmitted(raw)
		if c.send(raw) == nil {
			c.stats.PacketsRetransmitted++
		}
	}
	// wait a full round again before the next attempt
	c.ackMoved = now
}

// sendHoldLimit is how long a sent packet is kept for retransmission.
func (c *Conn) sendHoldLimit() time.Duration {
	return max(c.sndLatency+c.sndLatency/4+20*time.Millisecond, 100*time.Millisecond)
}

// dropOldSent stops holding packets that could no longer arrive before
// the peer plays past them.
func (c *Conn) dropOldSent(now time.Time) {
	limit := c.sendHoldLimit()
	for len(c.sndBuf) > 0 && now.Sub(c.sndBuf[0].sentAt) > limit {
		c.sndBuf[0] = nil
		c.sndBuf = c.sndBuf[1:]
		c.stats.PacketsSendDropped++
	}
}
