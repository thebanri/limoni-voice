package voice

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestBitrateStepsDownOnSustainedLossAndBackUp(t *testing.T) {
	c := NewBitrateController()
	now := time.Unix(1000, 0)
	tick := func(loss float64, rtt time.Duration, d time.Duration) (int, bool) {
		now = now.Add(d)
		return c.Update(loss, rtt, now)
	}

	if br, changed := tick(15, 50*time.Millisecond, time.Second); changed || br != DefaultBitrate {
		t.Fatal("a single bad report changed the bitrate")
	}
	var br int
	var changed bool
	for i := 0; i < 3 && !changed; i++ {
		br, changed = tick(15, 50*time.Millisecond, time.Second)
	}
	if !changed || br != 24000 {
		t.Fatalf("sustained loss: got %d changed=%v", br, changed)
	}

	// High latency alone also steps down, but never below the floor.
	for i := 0; i < 60; i++ {
		br, _ = tick(0, 600*time.Millisecond, time.Second)
	}
	if br != BitrateLadder[0] {
		t.Fatalf("expected the floor, got %d", br)
	}

	// A mediocre link holds; a clean one climbs back one step at a time.
	for i := 0; i < 30; i++ {
		if _, changed := tick(4, 300*time.Millisecond, time.Second); changed {
			t.Fatal("a middling link changed the bitrate")
		}
	}
	steps := 0
	for i := 0; i < 120; i++ {
		if _, changed := tick(0, 40*time.Millisecond, time.Second); changed {
			steps++
		}
	}
	if steps != len(BitrateLadder)-1 || c.Bitrate() != DefaultBitrate {
		t.Fatalf("recovered %d steps to %d", steps, c.Bitrate())
	}
}

func TestBitrateRecoveryNeedsAStretchOfGoodReports(t *testing.T) {
	var c BitrateController // the zero value is ready to use
	if c.Bitrate() != DefaultBitrate {
		t.Fatalf("zero value starts at %d", c.Bitrate())
	}
	now := time.Unix(0, 0)
	for i := 0; i < 5; i++ {
		now = now.Add(time.Second)
		c.Update(20, 0, now)
	}
	low := c.Bitrate()
	// Good and bad reports alternating never climb.
	for i := 0; i < 40; i++ {
		now = now.Add(time.Second)
		loss := 0.0
		if i%5 == 4 {
			loss = 20
		}
		c.Update(loss, 0, now)
	}
	if c.Bitrate() > low {
		t.Fatalf("flaky link climbed from %d to %d", low, c.Bitrate())
	}
}

func TestEncoderPacketsFollowTheBitrate(t *testing.T) {
	enc, err := NewEncoder(48000, 960)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]int16, 960)
	for i := range frame {
		frame[i] = int16((i * 97 % 2000) - 1000)
	}
	size := func() int {
		pkt, err := enc.EncodeInt16(frame)
		if err != nil {
			t.Fatal(err)
		}
		return len(pkt)
	}
	for i := 0; i < 5; i++ {
		size()
	}
	high := size()
	enc.SetBitrate(12000)
	if enc.Bitrate() != 12000 {
		t.Fatalf("bitrate not applied: %d", enc.Bitrate())
	}
	for i := 0; i < 5; i++ {
		size()
	}
	if low := size(); low >= high {
		t.Fatalf("12 kbps packets (%d bytes) are not smaller than 32 kbps ones (%d)", low, high)
	}
}

// Walking the ladder as a flaky link does, with the loss hint changing too, never leaves the
// encoder sending silence while someone talks. With a 12 kbps step this seed went silent at
// frame 2711 and stayed so until the bitrate rose again.
func TestBitrateLadderNeverGoesSilent(t *testing.T) {
	enc, _ := NewEncoder(48000, 960)
	dec, _ := NewDecoder(48000, 960)
	walk, noise := rand.New(rand.NewSource(77)), rand.New(rand.NewSource(1))
	frame, out := make([]int16, 960), make([]int16, 960)
	idx, phase := len(BitrateLadder)-1, 0.0
	for f := 0; f < 3200; f++ {
		if f%150 == 0 && walk.Intn(3) == 0 {
			idx = max(0, min(len(BitrateLadder)-1, idx+walk.Intn(3)-1))
			enc.SetBitrate(BitrateLadder[idx])
		}
		if f%250 == 0 {
			enc.SetPacketLoss(walk.Intn(30))
		}
		talking := (f/150)%3 != 2 // 3 s of speech, 3 s more, 3 s of room noise
		pitch := 110 + 60*math.Sin(float64(f)/40)
		for i := range frame {
			v := noise.NormFloat64() * 200
			if talking {
				phase += 2 * math.Pi * pitch / 48000
				v += 6000*math.Sin(phase) + 3000*math.Sin(3*phase) + 1500*math.Sin(5*phase)
			}
			frame[i] = int16(max(-32768, min(32767, v)))
		}
		pkt, err := enc.EncodeInt16(frame)
		if err != nil {
			t.Fatal(err)
		}
		_ = dec.Decode(pkt, out)
		var e float64
		for _, s := range out {
			e += float64(s) * float64(s)
		}
		if talking && f%150 > 10 && math.Sqrt(e/960) < 50 {
			t.Fatalf("frame %d at %d bps decoded to silence while talking", f, enc.Bitrate())
		}
	}
}
