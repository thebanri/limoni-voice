package voice

import (
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
