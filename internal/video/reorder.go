package video

import (
	"sort"
	"time"
)

// Reorder is the viewer's per-sharer receive buffer. Chunks are released strictly in sequence
// order. A gap is held for at most MaxWait while the missing chunks are NACKed; after that the
// buffer skips ahead so a lost chunk costs a short glitch instead of a freeze.
type Reorder struct {
	MaxWait time.Duration

	started bool
	next    uint32 // next sequence to release
	high    uint32 // highest sequence seen
	pending map[uint32][]byte
	missing map[uint32]*missState

	received, lost, recovered uint64
}

type missState struct {
	since    time.Time
	lastNack time.Time
	nacks    int
}

const (
	maxGap      = 1024 // a larger jump is treated as a new stream
	maxPending  = 4096
	maxNacks    = 3
	defaultWait = 80 * time.Millisecond
)

// NewReorder creates a buffer that waits up to maxWait for missing chunks.
func NewReorder(maxWait time.Duration) *Reorder {
	if maxWait <= 0 {
		maxWait = defaultWait
	}
	return &Reorder{MaxWait: maxWait, pending: map[uint32][]byte{}, missing: map[uint32]*missState{}}
}

// Reset forgets all state (a new stream starts with the next Push).
func (r *Reorder) Reset() {
	r.started = false
	clear(r.pending)
	clear(r.missing)
}

// Push adds a chunk and returns the chunks that became deliverable, in order.
func (r *Reorder) Push(seq uint32, data []byte, now time.Time) [][]byte {
	if len(data) == 0 {
		return nil
	}
	if !r.started {
		r.started = true
		r.next, r.high = seq, seq-1
	}
	d := int64(int32(seq - r.next))
	switch {
	case d < 0:
		return nil // duplicate or too late
	case d > maxGap:
		// Sender restarted or we fell far behind: begin a new stream at seq.
		r.lost += uint64(len(r.missing))
		r.Reset()
		r.started = true
		r.next, r.high = seq, seq-1
	}
	if _, dup := r.pending[seq]; dup {
		return nil
	}
	r.received++
	if _, wasMissing := r.missing[seq]; wasMissing {
		r.recovered++
		delete(r.missing, seq)
	}
	r.pending[seq] = data
	// Everything between the previous highest sequence and seq is missing (for now).
	if int32(seq-r.high) > 0 {
		for s := r.high + 1; s != seq; s++ {
			r.missing[s] = &missState{since: now}
		}
		r.high = seq
	}
	if len(r.pending) > maxPending {
		return r.skip(now, true)
	}
	return r.drain()
}

// Tick releases chunks held behind a gap whose wait time expired.
func (r *Reorder) Tick(now time.Time) [][]byte {
	if !r.started {
		return nil
	}
	return r.skip(now, false)
}

func (r *Reorder) skip(now time.Time, force bool) [][]byte {
	out := r.drain()
	for len(r.pending) > 0 {
		ms := r.missing[r.next]
		if ms == nil {
			ms = &missState{since: now}
			r.missing[r.next] = ms
		}
		if !force && now.Sub(ms.since) < r.MaxWait {
			break
		}
		r.lost++
		delete(r.missing, r.next)
		r.next++
		out = append(out, r.drain()...)
		force = force && len(r.pending) > maxPending/2
	}
	return out
}

func (r *Reorder) drain() [][]byte {
	var out [][]byte
	for {
		data, ok := r.pending[r.next]
		if !ok {
			return out
		}
		delete(r.pending, r.next)
		out = append(out, data)
		r.next++
	}
}

// NackDue returns missing sequence numbers that should be (re)requested now. A chunk is first
// requested after reorderGrace (it may just be out of order), then every retry interval.
func (r *Reorder) NackDue(now time.Time, retry time.Duration) []uint32 {
	const reorderGrace = 10 * time.Millisecond
	if retry < 20*time.Millisecond {
		retry = 20 * time.Millisecond
	}
	var due []uint32
	for s, ms := range r.missing {
		if ms.nacks >= maxNacks || now.Sub(ms.since) < reorderGrace {
			continue
		}
		if ms.nacks > 0 && now.Sub(ms.lastNack) < retry {
			continue
		}
		ms.nacks++
		ms.lastNack = now
		due = append(due, s)
	}
	base := r.next
	sort.Slice(due, func(i, j int) bool { return due[i]-base < due[j]-base })
	return due
}

// Stats returns and resets the received / lost / recovered counters.
func (r *Reorder) Stats() (received, lost, recovered uint64) {
	received, lost, recovered = r.received, r.lost, r.recovered
	r.received, r.lost, r.recovered = 0, 0, 0
	return
}
