package video

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tsPacket builds one TS packet; keyframe sets the random access indicator.
func tsPacket(pid uint16, pusi, keyframe bool, fill byte) []byte {
	p := bytes.Repeat([]byte{fill}, TSPacketSize)
	p[0] = tsSync
	p[1] = byte(pid>>8) & 0x1F
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	if keyframe {
		p[3] = 0x30 // adaptation + payload
		p[4] = 7
		p[5] = 0x40 // random_access_indicator
	} else {
		p[3] = 0x10
	}
	return p
}

func TestChunkerSplitsAndDetectsKeyframes(t *testing.T) {
	var stream []byte
	for i := 0; i < 14; i++ {
		stream = append(stream, tsPacket(0x100, i == 7, i == 7, byte(i))...)
	}
	var c Chunker
	var chunks [][]byte
	var keys []bool
	emit := func(chunk []byte, key bool) {
		chunks = append(chunks, chunk)
		keys = append(keys, key)
	}
	// Feed in odd sized pieces with garbage in front.
	c.Push(append([]byte{1, 2, 3}, stream[:500]...), emit)
	c.Push(stream[500:1200], emit)
	c.Push(stream[1200:], emit)
	total := 0
	for i, ch := range chunks {
		if !ValidTS(ch) || len(ch) > ChunkSize {
			t.Fatalf("chunk %d invalid (%d bytes)", i, len(ch))
		}
		total += len(ch) / TSPacketSize
	}
	if total != 14 {
		t.Fatalf("got %d packets, want 14", total)
	}
	var sawKey bool
	for i, k := range keys {
		if k {
			sawKey = true
			if !bytes.Contains(chunks[i], tsPacket(0x100, true, true, 7)) {
				t.Fatalf("keyframe flag on wrong chunk %d", i)
			}
		}
	}
	if !sawKey {
		t.Fatal("keyframe not detected")
	}
}

func TestKeyframeFromH264NAL(t *testing.T) {
	p := tsPacket(0x100, true, false, 0xFF)
	pes := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 5, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0x67}
	copy(p[4:], pes)
	if !IsKeyframeChunk(p) {
		t.Fatal("SPS start not detected as keyframe")
	}
	pes[len(pes)-1] = 0x41 // non-IDR slice
	copy(p[4:], pes)
	if IsKeyframeChunk(p) {
		t.Fatal("P-slice detected as keyframe")
	}
}

func TestReorderWaitsNacksAndSkips(t *testing.T) {
	r := NewReorder(80 * time.Millisecond)
	t0 := time.Unix(1000, 0)
	got := func(ch [][]byte) []byte {
		var out []byte
		for _, c := range ch {
			out = append(out, c[0])
		}
		return out
	}
	if out := got(r.Push(10, []byte{10}, t0)); !bytes.Equal(out, []byte{10}) {
		t.Fatalf("first: %v", out)
	}
	// 11 and 12 lost, 13 arrives: held.
	if out := r.Push(13, []byte{13}, t0.Add(time.Millisecond)); len(out) != 0 {
		t.Fatalf("released across a gap: %v", got(out))
	}
	if due := r.NackDue(t0.Add(5*time.Millisecond), 40*time.Millisecond); len(due) != 0 {
		t.Fatalf("NACK before reorder grace: %v", due)
	}
	due := r.NackDue(t0.Add(15*time.Millisecond), 40*time.Millisecond)
	if len(due) != 2 || due[0] != 11 || due[1] != 12 {
		t.Fatalf("NACK list %v", due)
	}
	if again := r.NackDue(t0.Add(20*time.Millisecond), 40*time.Millisecond); len(again) != 0 {
		t.Fatalf("NACK retried too early: %v", again)
	}
	// Retransmission of 11 arrives, 12 never does.
	if out := got(r.Push(11, []byte{11}, t0.Add(30*time.Millisecond))); !bytes.Equal(out, []byte{11}) {
		t.Fatalf("recovered: %v", out)
	}
	if out := r.Tick(t0.Add(60 * time.Millisecond)); len(out) != 0 {
		t.Fatal("skipped before MaxWait")
	}
	if out := got(r.Tick(t0.Add(90 * time.Millisecond))); !bytes.Equal(out, []byte{13}) {
		t.Fatalf("after skip: %v", out)
	}
	if out := r.Push(12, []byte{12}, t0.Add(100*time.Millisecond)); len(out) != 0 {
		t.Fatal("late chunk released out of order")
	}
	rec, lost, recovered := r.Stats()
	if rec != 3 || lost != 1 || recovered != 1 {
		t.Fatalf("stats rec=%d lost=%d recovered=%d", rec, lost, recovered)
	}
}

func TestReorderWrapAndRestart(t *testing.T) {
	r := NewReorder(0)
	now := time.Now()
	n := 0
	for s := uint32(0xFFFFFFFE); s != 3; s++ {
		n += len(r.Push(s, []byte{1}, now))
	}
	if n != 5 {
		t.Fatalf("wrap delivered %d", n)
	}
	if out := r.Push(90000, []byte{2}, now); len(out) != 1 {
		t.Fatal("restart jump not treated as a new stream")
	}
}

func TestHistoryGOPCache(t *testing.T) {
	h := NewHistory(8)
	for s := uint32(1); s <= 5; s++ {
		h.Put(s, []byte{byte(s)}, s == 3)
	}
	first, chunks := h.SinceKeyframe()
	if first != 3 || len(chunks) != 3 || chunks[2][0] != 5 {
		t.Fatalf("gop cache first=%d chunks=%v", first, chunks)
	}
	if h.Get(2) == nil || h.Get(9) != nil {
		t.Fatal("history lookup")
	}
	for s := uint32(6); s <= 12; s++ { // keyframe 3 falls out of the ring
		h.Put(s, []byte{byte(s)}, false)
	}
	if _, chunks := h.SinceKeyframe(); chunks != nil {
		t.Fatal("incomplete GOP returned")
	}
	if h.Get(3) != nil {
		t.Fatal("evicted chunk returned")
	}
}

func TestPacerSmoothsBursts(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	p := NewPacer(100_000, func(data []byte, to string) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
	})
	go p.Run()
	defer p.Stop()
	start := time.Now()
	chunk := make([]byte, 1000)
	for i := 0; i < 50; i++ { // 50 kB burst at 100 kB/s ≈ 0.5 s
		p.Enqueue(chunk, []string{"a"}, Live)
	}
	p.Enqueue([]byte("nack"), []string{"a"}, Retransmit)
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(times)
		mu.Unlock()
		if n == 51 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d items delivered", n)
		case <-time.After(10 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if spread := times[50].Sub(start); spread < 150*time.Millisecond {
		t.Fatalf("burst not paced: all delivered within %v", spread)
	}
}

func TestPacerFollowsSustainedLoad(t *testing.T) {
	var delivered atomic.Int64
	p := NewPacer(50_000, func(data []byte, to string) { delivered.Add(int64(len(data))) })
	go p.Run()
	defer p.Stop()
	chunk := make([]byte, ChunkSize)
	var worst time.Duration
	// 1.5 s of input at ~4× the configured rate (fast scrolling at a high bitrate).
	for i := 0; i < 150; i++ {
		for k := 0; k < 18; k++ {
			p.Enqueue(chunk, []string{"a"}, Live)
		}
		time.Sleep(10 * time.Millisecond)
		if d := p.QueueDelay(); i > 30 && d > worst {
			worst = d
		}
	}
	t.Logf("worst queue delay under sustained load: %v", worst)
	if worst > 100*time.Millisecond {
		t.Fatalf("pacer builds latency under sustained load: %v", worst)
	}
}

func TestControllerStepsDownFastAndUpSlowly(t *testing.T) {
	c := NewController(800, 4000)
	now := time.Unix(0, 0)
	step := func(loss float64, q time.Duration) (int, bool) {
		now = now.Add(2 * time.Second)
		return c.Report(loss, q, now)
	}
	for i := 0; i < 4; i++ {
		step(0, 0)
	}
	if _, ch := step(12, 0); ch {
		t.Fatal("stepped down on a single bad report")
	}
	kbps, ch := step(12, 0)
	if !ch || kbps != 2800 {
		t.Fatalf("expected step down to 2800, got %d %v", kbps, ch)
	}
	if _, ch := step(0, 1500*time.Millisecond); ch {
		t.Fatal("queue delay alone changed immediately")
	}
	for i := 0; i < 14; i++ {
		if _, ch := step(0, 0); ch {
			t.Fatalf("stepped up too early (report %d)", i)
		}
	}
	kbps, ch = step(0, 0)
	for !ch {
		kbps, ch = step(0, 0)
	}
	if kbps != 3500 {
		t.Fatalf("step up to 3500, got %d", kbps)
	}
	for i := 0; i < 60; i++ {
		step(50, 0)
	}
	if c.Current != 800 {
		t.Fatalf("floor not respected: %d", c.Current)
	}
}
