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

// Pacer smooths bursts (keyframes, GOP cache replays) to a byte rate with a token bucket.
// Retransmissions jump the queue. Items older than MaxDelay are dropped: at that point the
// link is congested and the bitrate controller is expected to step down.
type Pacer struct {
	MaxDelay time.Duration

	mu      sync.Mutex
	queue   []pacerItem
	urgent  []pacerItem
	rate    float64 // bytes per second
	burst   float64
	tokens  float64
	last    time.Time
	dropped uint64
	wake    chan struct{}
	stop    chan struct{}
	send    func(data []byte, to string)
}

type pacerItem struct {
	data []byte
	to   []string
	at   time.Time
}

// NewPacer creates a pacer delivering through send at bytesPerSec.
func NewPacer(bytesPerSec float64, send func(data []byte, to string)) *Pacer {
	p := &Pacer{MaxDelay: time.Second, wake: make(chan struct{}, 1), stop: make(chan struct{}), send: send}
	p.SetRate(bytesPerSec)
	return p
}

// SetRate changes the pacing rate; the bucket holds 20 ms of data (at least two chunks).
func (p *Pacer) SetRate(bytesPerSec float64) {
	p.mu.Lock()
	p.rate = max(bytesPerSec, 16_000)
	p.burst = max(p.rate*0.02, 2*ChunkSize+256)
	p.mu.Unlock()
}

// Enqueue schedules data for each recipient.
func (p *Pacer) Enqueue(data []byte, to []string, urgent bool) {
	if len(to) == 0 {
		return
	}
	item := pacerItem{data: data, to: to, at: time.Now()}
	p.mu.Lock()
	if urgent {
		p.urgent = append(p.urgent, item)
	} else {
		p.queue = append(p.queue, item)
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// QueueDelay returns how long the oldest queued item has waited.
func (p *Pacer) QueueDelay() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return 0
	}
	return time.Since(p.queue[0].at)
}

// Dropped returns and resets the number of items dropped for exceeding MaxDelay.
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
	p.tokens = min(p.burst, p.tokens+now.Sub(p.last).Seconds()*p.rate)
	p.last = now
	for len(p.queue) > 0 && now.Sub(p.queue[0].at) > p.MaxDelay {
		p.queue = p.queue[1:]
		p.dropped++
	}
	var batch []pacerItem
	for {
		q := &p.urgent
		if len(*q) == 0 {
			q = &p.queue
		}
		if len(*q) == 0 {
			break
		}
		item := (*q)[0]
		cost := float64(len(item.data) * len(item.to))
		if cost > p.tokens && len(batch) > 0 {
			break
		}
		if cost > p.tokens && p.tokens < p.burst {
			break
		}
		p.tokens -= cost
		*q = (*q)[1:]
		batch = append(batch, item)
	}
	idle := len(p.queue) == 0 && len(p.urgent) == 0
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
	queueHigh       = 400 * time.Millisecond
	queueLow        = 100 * time.Millisecond
	minDownInterval = 6 * time.Second
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
