package video

import (
	"sync"
	"time"
)

// History keeps the most recent sealed chunks by sequence number for retransmission and for
// starting new viewers at the last keyframe (GOP cache).
type History struct {
	mu      sync.Mutex
	size    uint32
	seqs    []uint32
	data    [][]byte
	lastKey uint32
	haveKey bool
	last    uint32
	have    bool
}

// NewHistory creates a history holding up to size chunks.
func NewHistory(size int) *History {
	return &History{size: uint32(size), seqs: make([]uint32, size), data: make([][]byte, size)}
}

// Put stores a sealed chunk.
func (h *History) Put(seq uint32, sealed []byte, keyframe bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := seq % h.size
	h.seqs[i] = seq
	h.data[i] = sealed
	h.last, h.have = seq, true
	if keyframe {
		h.lastKey, h.haveKey = seq, true
	}
}

// Get returns the sealed chunk for seq if it is still held.
func (h *History) Get(seq uint32) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := seq % h.size
	if h.data[i] == nil || h.seqs[i] != seq {
		return nil
	}
	return h.data[i]
}

// SinceKeyframe returns the chunks from the most recent keyframe up to the newest chunk, or nil
// when that range is no longer complete in the history.
func (h *History) SinceKeyframe() (first uint32, chunks [][]byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.haveKey || !h.have || h.last-h.lastKey >= h.size {
		return 0, nil
	}
	for s := h.lastKey; ; s++ {
		i := s % h.size
		if h.data[i] == nil || h.seqs[i] != s {
			return 0, nil
		}
		chunks = append(chunks, h.data[i])
		if s == h.last {
			break
		}
	}
	return h.lastKey, chunks
}

// Pacer smooths bursts (keyframes) with a token bucket. The rate never goes below the
// configured floor and follows the measured live input rate with headroom, so a keyframe is
// spread over a few tens of milliseconds while a sustained encoder overshoot (fast scrolling,
// games) is not held back for long.
//
// Three kinds of traffic are queued in order of priority:
//   - retransmissions (Retransmit): sent first; dropped when older than RetransmitMaxAge
//     because the viewer has given up on them by then;
//   - live video (Live): token bucket; dropped when older than MaxDelay;
//   - GOP cache replays for a new viewer (Replay) share the live queue so the viewer receives
//     them before newer live chunks, but skip the token bucket (up to replayPerStep chunks per
//     step) and are never dropped for age.
type Pacer struct {
	MaxDelay         time.Duration
	RetransmitMaxAge time.Duration

	mu      sync.Mutex
	live    []pacerItem
	retx    []pacerItem
	rate    float64 // effective bytes per second
	floor   float64 // configured minimum rate
	inBytes float64 // live input bytes since the last rate update
	inRate  float64 // smoothed live input byte rate
	inAt    time.Time
	queued  float64 // paced live bytes waiting
	burst   float64
	tokens  float64
	last    time.Time
	dropped uint64
	wake    chan struct{}
	stop    chan struct{}
	send    func(data []byte, to string)
}

// Traffic kinds for Enqueue.
type Kind int

const (
	Live Kind = iota
	Replay
	Retransmit
)

const (
	replayPerStep = 4                      // ≈ 2000 chunks/s
	maxBacklog    = 100 * time.Millisecond // live data never waits much longer than this
)

type pacerItem struct {
	data []byte
	to   []string
	at   time.Time
	free bool // replay: outside the token bucket
}

// NewPacer creates a pacer delivering through send at bytesPerSec (minimum).
func NewPacer(bytesPerSec float64, send func(data []byte, to string)) *Pacer {
	p := &Pacer{
		MaxDelay:         time.Second,
		RetransmitMaxAge: 150 * time.Millisecond,
		wake:             make(chan struct{}, 1),
		stop:             make(chan struct{}),
		send:             send,
	}
	p.SetRate(bytesPerSec)
	return p
}

// SetRate changes the minimum pacing rate.
func (p *Pacer) SetRate(bytesPerSec float64) {
	p.mu.Lock()
	p.floor = max(bytesPerSec, 16_000)
	p.updateRateLocked()
	p.mu.Unlock()
}

// updateRateLocked applies max(floor, 1.3 × live input rate); the bucket holds 30 ms of data.
func (p *Pacer) updateRateLocked() {
	p.rate = max(p.floor, p.inRate*1.3)
	p.burst = max(p.rate*0.03, 4*ChunkSize)
}

// Enqueue schedules data for each recipient.
func (p *Pacer) Enqueue(data []byte, to []string, kind Kind) {
	if len(to) == 0 {
		return
	}
	item := pacerItem{data: data, to: to, at: time.Now()}
	p.mu.Lock()
	switch kind {
	case Retransmit:
		p.retx = append(p.retx, item)
	case Replay:
		item.free = true
		p.live = append(p.live, item)
	default:
		p.live = append(p.live, item)
		p.inBytes += float64(len(data) * len(to))
		p.queued += float64(len(data) * len(to))
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// QueueDelay returns how long the oldest live item has waited.
func (p *Pacer) QueueDelay() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.live) == 0 {
		return 0
	}
	return time.Since(p.live[0].at)
}

// Dropped returns and resets the number of live items dropped for exceeding MaxDelay.
func (p *Pacer) Dropped() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := p.dropped
	p.dropped = 0
	return d
}

// Run delivers queued items until Stop is called.
func (p *Pacer) Run() {
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		idle := p.step(time.Now())
		if idle {
			select {
			case <-p.stop:
				return
			case <-p.wake:
			}
			continue
		}
		select {
		case <-p.stop:
			return
		case <-ticker.C:
		}
	}
}

// Stop ends Run.
func (p *Pacer) Stop() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
}

// step sends what the bucket allows; it reports whether the queues are empty.
func (p *Pacer) step(now time.Time) bool {
	p.mu.Lock()
	if p.last.IsZero() {
		p.last = now
		p.tokens = p.burst
	}
	if p.inAt.IsZero() {
		p.inAt = now
	}
	if dt := now.Sub(p.inAt); dt >= 100*time.Millisecond {
		// ~300 ms smoothing: a keyframe burst stays paced, a sustained overshoot is followed.
		p.inRate += (p.inBytes/dt.Seconds() - p.inRate) * 0.3
		p.inBytes, p.inAt = 0, now
		p.updateRateLocked()
	}
	// Latency bound: whatever is queued must drain within maxBacklog, whatever the rate.
	rate := max(p.rate, p.queued/maxBacklog.Seconds())
	p.tokens = min(max(p.burst, rate*0.03), p.tokens+now.Sub(p.last).Seconds()*rate)
	p.last = now
	for len(p.live) > 0 && !p.live[0].free && now.Sub(p.live[0].at) > p.MaxDelay {
		p.queued -= float64(len(p.live[0].data) * len(p.live[0].to))
		p.live = p.live[1:]
		p.dropped++
	}
	for len(p.retx) > 0 && now.Sub(p.retx[0].at) > p.RetransmitMaxAge {
		p.retx = p.retx[1:]
	}

	var batch []pacerItem
	take := func(q *[]pacerItem, paced bool) bool {
		item := (*q)[0]
		if paced {
			cost := float64(len(item.data) * len(item.to))
			if cost > p.tokens && (len(batch) > 0 || p.tokens < p.burst) {
				return false
			}
			p.tokens -= cost
			if !item.free && q == &p.live {
				p.queued -= cost
			}
		}
		*q = (*q)[1:]
		batch = append(batch, item)
		return true
	}
	for len(p.retx) > 0 && take(&p.retx, true) {
	}
	if len(p.retx) == 0 {
		free := replayPerStep
		for len(p.live) > 0 {
			if p.live[0].free {
				if free == 0 {
					break
				}
				free--
				take(&p.live, false)
				continue
			}
			if !take(&p.live, true) {
				break
			}
		}
	}
	idle := len(p.live) == 0 && len(p.retx) == 0
	p.mu.Unlock()
	for _, item := range batch {
		for _, to := range item.to {
			p.send(item.data, to)
		}
	}
	return idle
}

// Controller adapts the encoder bitrate to what viewers actually receive. Steps are coarse and
// rate limited because every change restarts the encoder.
type Controller struct {
	Floor, Ceiling, Current int // kbps

	lastChange time.Time
	badRuns    int
	goodSince  time.Time
}

const (
	lossHigh        = 5.0 // % video loss that indicates congestion
	lossLow         = 1.0
	queueHigh       = time.Second // the pacer follows the input, so only a stuck uplink queues this long
	queueLow        = 150 * time.Millisecond
	minDownInterval = 10 * time.Second
	upAfterGood     = 30 * time.Second
)

// NewController starts at ceiling kbps.
func NewController(floor, ceiling int) *Controller {
	return &Controller{Floor: floor, Ceiling: ceiling, Current: ceiling}
}

// Report feeds one evaluation (every ~2 s) and returns the new bitrate when it should change.
func (c *Controller) Report(lossPct float64, queueDelay time.Duration, now time.Time) (int, bool) {
	if c.lastChange.IsZero() {
		c.lastChange = now
	}
	congested := lossPct >= lossHigh || queueDelay >= queueHigh
	good := lossPct < lossLow && queueDelay < queueLow
	switch {
	case congested:
		c.goodSince = time.Time{}
		c.badRuns++
		if c.badRuns >= 2 && now.Sub(c.lastChange) >= minDownInterval && c.Current > c.Floor {
			c.Current = max(c.Floor, c.Current*7/10)
			c.lastChange, c.badRuns = now, 0
			return c.Current, true
		}
	case good:
		c.badRuns = 0
		if c.goodSince.IsZero() {
			c.goodSince = now
		}
		if now.Sub(c.goodSince) >= upAfterGood && now.Sub(c.lastChange) >= upAfterGood && c.Current < c.Ceiling {
			c.Current = min(c.Ceiling, c.Current*5/4)
			c.lastChange, c.goodSince = now, now
			return c.Current, true
		}
	default:
		c.badRuns = 0
		c.goodSince = time.Time{}
	}
	return c.Current, false
}
