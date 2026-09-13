package dsp

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

func readRaw(t *testing.T, path string) []int16 {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("oracle data unavailable: %v", err)
	}
	out := make([]int16, len(data)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(data[2*i:]))
	}
	return out
}

func TestAECAgainstSpeexOracle(t *testing.T) {
	dir := os.Getenv("AEC_ORACLE_DIR")
	if dir == "" {
		t.Skip("AEC_ORACLE_DIR not set")
	}
	far := readRaw(t, dir+"/aec_far.raw")
	near := readRaw(t, dir+"/aec_near.raw")
	want := readRaw(t, dir+"/aec_out.raw")
	const fs = 480
	aec := NewEchoCanceller(fs, 4800, 48000)
	got := make([]int16, len(near))
	for f := 0; f+fs <= len(near); f += fs {
		aec.Cancel(near[f:f+fs], far[f:f+fs], got[f:f+fs])
	}
	var diff, ref, maxAbs float64
	firstBad := -1
	for i := range got {
		d := float64(got[i]) - float64(want[i])
		diff += d * d
		ref += float64(want[i]) * float64(want[i])
		if math.Abs(d) > maxAbs {
			maxAbs = math.Abs(d)
		}
		if firstBad < 0 && math.Abs(d) > 2 {
			firstBad = i
		}
	}
	t.Logf("SNR vs oracle %.1f dB, max abs diff %.0f, first sample diff>2 at %d (frame %d)", 10*math.Log10(ref/(diff+1e-9)), maxAbs, firstBad, firstBad/fs)
	// ERLE over frames 150..300 (converged, no local talker)
	var en, eo, eg float64
	for i := 150 * fs; i < 300*fs; i++ {
		en += float64(near[i]) * float64(near[i])
		eo += float64(want[i]) * float64(want[i])
		eg += float64(got[i]) * float64(got[i])
	}
	t.Logf("ERLE oracle %.1f dB, port %.1f dB", 10*math.Log10(en/eo), 10*math.Log10(en/eg))
}

// TestAECCancelsSyntheticEcho checks convergence and double-talk preservation on a
// synthetic room (1200 + 2100 sample reflections) without external data.
func TestAECCancelsSyntheticEcho(t *testing.T) {
	const fs, rate, frames = 480, 48000, 600
	aec := NewEchoCanceller(fs, 4800, rate)
	hist := make([]float64, 4800)
	hp := 0
	far := make([]int16, fs)
	near := make([]int16, fs)
	out := make([]int16, fs)
	seed := uint32(1)
	noise := func() float64 {
		seed = seed*1664525 + 1013904223
		return float64(seed>>8)/float64(1<<24) - .5
	}
	var ph, ph2 float64
	var nearE, outE, localIn, localOut float64
	for f := 0; f < frames; f++ {
		for i := 0; i < fs; i++ {
			env := .5 + .5*math.Sin(2*math.Pi*float64(f)/37)
			ph += 2 * math.Pi * (180 + 60*math.Sin(float64(f)*.1)) / rate
			ph2 += 2 * math.Pi * 1200 / rate
			v := env*(6000*math.Sin(ph)+2500*math.Sin(3*ph)+1200*math.Sin(ph2)) + 800*noise()
			far[i] = int16(v)
			hist[hp] = v
			echo := .35*hist[(hp-1200+4800)%4800] + .12*hist[(hp-2100+4800)%4800]
			hp = (hp + 1) % 4800
			local := 0.0
			if f > 300 && f < 400 {
				local = 4000 * math.Sin(2*math.Pi*300*float64(f*fs+i)/rate)
			}
			near[i] = int16(math.Max(-32768, math.Min(32767, echo+local+50*noise())))
		}
		aec.Cancel(near, far, out)
		for i := 0; i < fs; i++ {
			switch {
			case f >= 150 && f < 300:
				nearE += float64(near[i]) * float64(near[i])
				outE += float64(out[i]) * float64(out[i])
			case f >= 320 && f < 400:
				localIn += float64(near[i]) * float64(near[i])
				localOut += float64(out[i]) * float64(out[i])
			}
		}
	}
	erle := 10 * math.Log10(nearE/outE)
	if erle < 12 {
		t.Fatalf("echo return loss enhancement too low: %.1f dB", erle)
	}
	// During double talk the local talker must survive (output keeps most of the energy).
	if keep := 10 * math.Log10(localOut/localIn); keep < -4 {
		t.Fatalf("local speech attenuated by %.1f dB during double talk", -keep)
	}
	if !aec.Adapted() {
		t.Fatal("filter never adapted")
	}
}

func BenchmarkAEC480(b *testing.B) {
	aec := NewEchoCanceller(480, 9600, 48000)
	far := make([]int16, 480)
	near := make([]int16, 480)
	out := make([]int16, 480)
	for i := range far {
		far[i] = int16(3000 * math.Sin(float64(i)*.05))
		near[i] = far[i] / 3
	}
	for b.Loop() {
		aec.Cancel(near, far, out)
	}
}
