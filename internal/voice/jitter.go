package voice

import (
	"math"
	"sync"
	"time"
)

// FrameKind reports how a pulled frame was produced.
type FrameKind int

const (
	FrameNone      FrameKind = iota // nothing to play (silence / buffering)
	FrameDecoded                    // regular packet
	FrameFEC                        // recovered from the next packet's in-band FEC
	FrameConcealed                  // packet loss concealment
)

// JitterStats summarises receive quality.
type JitterStats struct {
	Received    uint64
	Lost        uint64 // frames never received before their playout time
	Late        uint64 // arrived after playout
	Recovered   uint64 // FEC recoveries
	Concealed   uint64 // PLC frames
	Dropped     uint64 // frames discarded to reduce latency
	JitterMs    float64
	TargetDelay time.Duration
	Buffered    int
}

type bufferedPacket struct {
	data     []byte
	speaking bool
}

const (
	minTargetFrames = 2
	maxTargetFrames = 10
	maxConceal      = 3
	resyncGap       = 50
	maxBuffered     = 64
)

// JitterBuffer reorders Opus packets by sequence number and plays them out at a delay that
// adapts to measured network jitter (RFC 3550 estimator). Missing frames are recovered with
// in-band FEC when the following packet is available, otherwise concealed.
type JitterBuffer struct {
	mu           sync.Mutex
	dec          *Decoder
	frameMs      float64
	packets      map[uint32]bufferedPacket
	started      bool
	next         uint32
	target       int
	jitter       float64
	lastTransit  float64
	haveTransit  bool
	concealRun   int
	lastSpeaking bool
	highWater    int
	stats        JitterStats

	// loss accounting window for receiver reports
	windowExpected uint64
	windowLost     uint64
	lossPct        float64
}

// NewJitterBuffer creates a buffer decoding with dec; frameDuration is the codec frame length.
func NewJitterBuffer(dec *Decoder, frameDuration time.Duration) *JitterBuffer {
	return &JitterBuffer{
		dec:     dec,
		frameMs: float64(frameDuration) / float64(time.Millisecond),
		packets: make(map[uint32]bufferedPacket),
		target:  minTargetFrames,
	}
}

// Push adds a packet. senderTimestampMs is the sender clock capture time; arrival is local time.
func (jb *JitterBuffer) Push(seq uint32, senderTimestampMs int64, data []byte, speaking bool, arrival time.Time) {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	jb.stats.Received++

	transit := float64(arrival.UnixMilli() - senderTimestampMs)
	if jb.haveTransit {
		d := math.Abs(transit - jb.lastTransit)
		if d < 2000 { // ignore clock jumps / talk-spurt restarts
			jb.jitter += (d - jb.jitter) / 16
		}
	}
	jb.lastTransit, jb.haveTransit = transit, true
	jb.target = max(minTargetFrames, min(maxTargetFrames, int(math.Ceil((2.5*jb.jitter+jb.frameMs/2)/jb.frameMs))))

	if jb.started {
		diff := int32(seq - jb.next)
		if diff < 0 {
			if diff > -resyncGap {
				jb.stats.Late++
				return
			}
			// sender restarted its sequence
			jb.started = false
			clear(jb.packets)
		} else if diff > resyncGap {
			jb.started = false
			clear(jb.packets)
		}
	}
	if len(jb.packets) >= maxBuffered {
		jb.dropOldestLocked()
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	jb.packets[seq] = bufferedPacket{data: cp, speaking: speaking}
}

func (jb *JitterBuffer) dropOldestLocked() {
	var oldest uint32
	found := false
	for s := range jb.packets {
		if !found || int32(s-oldest) < 0 {
			oldest, found = s, true
		}
	}
	if found {
		delete(jb.packets, oldest)
		jb.stats.Dropped++
		if jb.started && oldest == jb.next {
			jb.next++
		}
	}
}

func (jb *JitterBuffer) minSeqLocked() (uint32, bool) {
	var m uint32
	found := false
	for s := range jb.packets {
		if !found || int32(s-m) < 0 {
			m, found = s, true
		}
	}
	return m, found
}

// Pull produces the next playout frame into out.
func (jb *JitterBuffer) Pull(out []int16) FrameKind {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if !jb.started {
		if len(jb.packets) < jb.target {
			return FrameNone
		}
		first, _ := jb.minSeqLocked()
		jb.next, jb.started, jb.concealRun = first, true, 0
	}

	if pkt, ok := jb.packets[jb.next]; ok {
		delete(jb.packets, jb.next)
		_ = jb.dec.Decode(pkt.data, out)
		jb.next++
		jb.concealRun = 0
		jb.lastSpeaking = pkt.speaking
		jb.accountLocked(false)
		jb.trimLatencyLocked()
		return FrameDecoded
	}

	if len(jb.packets) == 0 {
		// End of a talk spurt (sender stopped transmitting) or a real underrun.
		if !jb.lastSpeaking || jb.concealRun >= maxConceal {
			jb.started = false
			return FrameNone
		}
		_ = jb.dec.Decode(nil, out)
		jb.concealRun++
		jb.stats.Concealed++
		return FrameConcealed
	}

	if first, _ := jb.minSeqLocked(); int32(first-jb.next) > resyncGap {
		jb.next = first
		return jb.pullResyncedLocked(out)
	}

	jb.stats.Lost++
	jb.accountLocked(true)
	if nextPkt, ok := jb.packets[jb.next+1]; ok {
		_ = jb.dec.DecodeFEC(nextPkt.data, out)
		jb.next++
		jb.concealRun = 0
		jb.stats.Recovered++
		return FrameFEC
	}
	_ = jb.dec.Decode(nil, out)
	jb.next++
	jb.concealRun++
	jb.stats.Concealed++
	return FrameConcealed
}

func (jb *JitterBuffer) pullResyncedLocked(out []int16) FrameKind {
	pkt := jb.packets[jb.next]
	delete(jb.packets, jb.next)
	_ = jb.dec.Decode(pkt.data, out)
	jb.next++
	jb.lastSpeaking = pkt.speaking
	return FrameDecoded
}

// trimLatencyLocked drops a frame when the buffer stays well above target, preferring silence.
func (jb *JitterBuffer) trimLatencyLocked() {
	if len(jb.packets) > jb.target+2 {
		jb.highWater++
	} else {
		jb.highWater = 0
	}
	if jb.highWater >= 25 {
		if pkt, ok := jb.packets[jb.next]; ok && (!pkt.speaking || jb.highWater >= 50) {
			delete(jb.packets, jb.next)
			jb.next++
			jb.stats.Dropped++
			jb.highWater = 0
		}
	}
}

func (jb *JitterBuffer) accountLocked(lost bool) {
	jb.windowExpected++
	if lost {
		jb.windowLost++
	}
	if jb.windowExpected >= 250 { // ~5 s of speech
		jb.lossPct = 100 * float64(jb.windowLost) / float64(jb.windowExpected)
		jb.windowExpected, jb.windowLost = 0, 0
	}
}

// Stats returns a snapshot of receive statistics.
func (jb *JitterBuffer) Stats() JitterStats {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	s := jb.stats
	s.JitterMs = jb.jitter
	s.TargetDelay = time.Duration(float64(jb.target) * jb.frameMs * float64(time.Millisecond))
	s.Buffered = len(jb.packets)
	return s
}

// LossPercent returns the loss rate of the last completed accounting window (or the running one).
func (jb *JitterBuffer) LossPercent() float64 {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	if jb.lossPct == 0 && jb.windowExpected >= 50 {
		return 100 * float64(jb.windowLost) / float64(jb.windowExpected)
	}
	return jb.lossPct
}

// Reset discards buffered packets.
func (jb *JitterBuffer) Reset() {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	clear(jb.packets)
	jb.started = false
	jb.haveTransit = false
}
