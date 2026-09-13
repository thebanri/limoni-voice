package dsp

import (
	"math"
	"math/rand"
	"testing"
)

// runLoopback plays speech-like noise, captures it back delayed (plus a music tone) and returns
// the echo suppression and the residual echo level relative to the music (dB).
func runLoopback(t *testing.T, delay int, gain float64, filter bool, musicAmp float64) (erle, belowMusic float64, lc *LoopbackCanceller) {
	t.Helper()
	lc = NewLoopbackCanceller()
	r := rand.New(rand.NewSource(9))
	var played []int16
	var echoE, residE, musicE float64
	lp := NewLowpass(48000, 9000, 0.7)
	const frames = 300
	out := make([]int16, 960)
	for f := 0; f < frames; f++ {
		play := make([]int16, 960)
		for i := range play {
			n := f*960 + i
			// speech-like: noise with 4 Hz syllable envelope and pauses
			env := math.Max(0, math.Sin(2*math.Pi*2*float64(n)/48000))
			play[i] = int16(7000 * env * (r.Float64()*2 - 1))
		}
		lc.Playback(play)
		played = append(played, play...)
		capt := make([]int16, 960)
		music := make([]float64, 960)
		for i := range capt {
			idx := f*960 + i - delay
			var echo float64
			if idx >= 0 && idx < len(played) {
				echo = gain * float64(played[idx])
				if filter {
					echo = lp.Process(echo)
				}
			}
			music[i] = musicAmp * math.Sin(2*math.Pi*523*float64(f*960+i)/48000)
			capt[i] = int16(echo + music[i])
		}
		lc.Capture(capt, out)
		if f > frames-100 {
			for i := range out {
				e := float64(capt[i]) - music[i]
				d := float64(out[i]) - music[i]
				echoE += e * e
				residE += d * d
				musicE += music[i] * music[i]
			}
		}
	}
	return 10 * math.Log10(echoE/(residE+1)), 10 * math.Log10((musicE+1)/(residE+1)), lc
}

func TestLoopbackCancellerRemovesPlayback(t *testing.T) {
	for _, c := range []struct {
		name   string
		delay  int
		gain   float64
		filter bool
	}{
		{"40ms", 1920, 0.8, false},
		{"130ms filtered", 6240, 0.5, true},
	} {
		// Our playback alone (nothing else playing): must vanish.
		erle, _, lc := runLoopback(t, c.delay, c.gain, c.filter, 0)
		lag, ok := lc.Delay()
		t.Logf("%s: estimated delay %d (true %d), suppression alone %.1f dB", c.name, lag, c.delay, erle)
		if !ok || absInt(lag-c.delay) > loopCenter/2 {
			t.Errorf("%s: delay estimate %d, want %d", c.name, lag, c.delay)
		}
		if erle < 40 {
			t.Errorf("%s: suppression only %.1f dB", c.name, erle)
		}
		// With loud music playing at the same time the leftover voice stays well below it.
		erle, below, _ := runLoopback(t, c.delay, c.gain, c.filter, 3000)
		t.Logf("%s: with music: suppression %.1f dB, residual %.1f dB below the music", c.name, erle, below)
		if below < 15 {
			t.Errorf("%s: residual only %.1f dB below the music", c.name, below)
		}
	}
}
