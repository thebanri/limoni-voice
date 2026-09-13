package sysaudio

import (
	"math"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestFramerAndResampler(t *testing.T) {
	var frames int
	f := newFramer(func(fr []int16) {
		if len(fr) != FrameSamples {
			t.Fatalf("frame of %d samples", len(fr))
		}
		frames++
	})
	f.push(make([]int16, 500))
	f.push(make([]int16, 1500))
	if frames != 2 {
		t.Fatalf("got %d frames", frames)
	}

	rs := newResampler(44100)
	in := make([]float64, 44100)
	for i := range in {
		in[i] = 0.5 * math.Sin(2*math.Pi*1000*float64(i)/44100)
	}
	out := rs.process(in, nil)
	if d := len(out) - SampleRate; d < -2 || d > 2 {
		t.Fatalf("44.1 kHz second resampled to %d samples", len(out))
	}
	var peak int16
	for _, v := range out[100:] {
		peak = max(peak, v)
	}
	if peak < 15000 || peak > 16500 {
		t.Fatalf("resampled amplitude %d", peak)
	}
}

// Captures the real system monitor for a moment (opt-in: needs a running sound server).
func TestOpenSystemMonitor(t *testing.T) {
	if os.Getenv("LIMONI_SYSAUDIO_TEST") == "" {
		t.Skip("set LIMONI_SYSAUDIO_TEST=1 to capture the real system audio monitor")
	}
	var n atomic.Int32
	s, err := Open(func(fr []int16) { n.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(500 * time.Millisecond)
	t.Logf("%s delivered %d frames in 500 ms", s.Backend(), n.Load())
	if n.Load() < 10 {
		t.Fatal("no audio frames from the monitor")
	}
}
