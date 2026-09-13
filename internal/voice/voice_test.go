package voice

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

const (
	testRate    = 48000
	testSamples = 960
)

func sineFrame(n int, freq float64) []byte {
	out := make([]byte, 2*testSamples)
	for i := 0; i < testSamples; i++ {
		t := float64(n*testSamples+i) / testRate
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(9000*math.Sin(2*math.Pi*freq*t))))
	}
	return out
}

func rms(pcm []int16) float64 {
	var e float64
	for _, s := range pcm {
		e += float64(s) * float64(s)
	}
	return math.Sqrt(e / float64(len(pcm)))
}

func newPair(t *testing.T) (*Encoder, *JitterBuffer) {
	t.Helper()
	enc, err := NewEncoder(testRate, testSamples)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDecoder(testRate, testSamples)
	if err != nil {
		t.Fatal(err)
	}
	return enc, NewJitterBuffer(dec, 20*time.Millisecond)
}

func TestEncoderIsCBR(t *testing.T) {
	enc, _ := newPair(t)
	sizes := map[int]bool{}
	for n := 0; n < 50; n++ {
		pkt, err := enc.EncodePCM16LE(sineFrame(n, 200+float64(n*37)))
		if err != nil {
			t.Fatal(err)
		}
		sizes[len(pkt)] = true
	}
	if len(sizes) != 1 {
		t.Fatalf("packet sizes vary with content (VBR leak): %v", sizes)
	}
}

func TestJitterBufferReorderAndConceal(t *testing.T) {
	enc, jb := newPair(t)
	now := time.Now()
	var packets [][]byte
	for n := 0; n < 12; n++ {
		p, _ := enc.EncodePCM16LE(sineFrame(n, 440))
		packets = append(packets, p)
	}
	// deliver out of order, drop seq 105
	order := []int{0, 2, 1, 3, 4, 6, 7, 8, 9, 10, 11}
	for _, i := range order {
		jb.Push(uint32(100+i), now.UnixMilli()+int64(i*20), packets[i], true, now.Add(time.Duration(i*20)*time.Millisecond))
	}
	out := make([]int16, testSamples)
	kinds := map[FrameKind]int{}
	for i := 0; i < 12; i++ {
		k := jb.Pull(out)
		kinds[k]++
		if k != FrameNone && i > 3 && rms(out) < 500 {
			t.Fatalf("frame %d (kind %d) is silent", i, k)
		}
	}
	if kinds[FrameFEC]+kinds[FrameConcealed] != 1 {
		t.Fatalf("expected exactly one recovered frame, got %v", kinds)
	}
	if kinds[FrameDecoded] != 11 {
		t.Fatalf("expected 11 decoded frames, got %v", kinds)
	}
	st := jb.Stats()
	if st.Lost != 1 || st.Received != 11 {
		t.Fatalf("unexpected stats %+v", st)
	}
}

func TestJitterBufferTalkSpurtEnd(t *testing.T) {
	enc, jb := newPair(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		p, _ := enc.EncodePCM16LE(sineFrame(i, 300))
		jb.Push(uint32(i+1), now.UnixMilli()+int64(i*20), p, i < 2, now)
	}
	out := make([]int16, testSamples)
	for i := 0; i < 3; i++ {
		if jb.Pull(out) != FrameDecoded {
			t.Fatalf("frame %d not decoded", i)
		}
	}
	// Sender went silent (last packet not speaking): no concealment noise.
	if k := jb.Pull(out); k != FrameNone {
		t.Fatalf("expected silence after talk spurt, got %d", k)
	}
}

func TestJitterBufferAdaptsTarget(t *testing.T) {
	enc, jb := newPair(t)
	base := time.Now()
	p, _ := enc.EncodePCM16LE(sineFrame(0, 300))
	for i := 0; i < 100; i++ {
		spread := time.Duration((i%5)*25) * time.Millisecond // up to 100ms arrival jitter
		jb.Push(uint32(i), base.UnixMilli()+int64(i*20), p, true, base.Add(time.Duration(i*20)*time.Millisecond+spread))
	}
	if st := jb.Stats(); st.TargetDelay <= 40*time.Millisecond || st.JitterMs < 10 {
		t.Fatalf("target did not grow with jitter: %+v", st)
	}
}
