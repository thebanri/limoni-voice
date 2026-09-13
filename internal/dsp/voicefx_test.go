package dsp

import (
	"math"
	"math/rand"
	"testing"
)

const fxRate = 48000

type rng struct{ *rand.Rand }

func (r rng) noise() float64 { return r.Float64()*2 - 1 }

// synthSpeech renders voiced syllables (harmonic source shaped by two formants) with a
// fricative "s" and an aspirated plosive "t". Returns the signal and a mask of speech samples.
func synthSpeech(n int, r rng) []float64 {
	out := make([]float64, n)
	f1 := NewLowpass(fxRate, 900, 2)
	f2 := NewHighpass(fxRate, 1200, 1)
	f2b := NewLowpass(fxRate, 2600, 2)
	fric := NewHighpass(fxRate, 4500, 0.9)
	phase := 0.0
	for i := range out {
		t := float64(i) / fxRate
		// 4 Hz syllable envelope with 25 ms attack/release edges.
		syl := math.Mod(t, 0.25)
		env := 0.0
		switch {
		case syl < 0.025:
			env = syl / 0.025
		case syl < 0.18:
			env = 1
		case syl < 0.205:
			env = 1 - (syl-0.18)/0.025
		}
		f0 := 140 + 15*math.Sin(2*math.Pi*3*t)
		phase += f0 / fxRate
		src := 0.0
		for h := 1; h <= 30; h++ {
			src += math.Sin(2*math.Pi*float64(h)*phase) / float64(h)
		}
		v := f1.Process(src)*1.0 + f2b.Process(f2.Process(src))*0.6
		out[i] = 5000 * env * v
		// "s" for 120 ms every second syllable pair, "t" burst + 50 ms aspiration.
		ph := math.Mod(t, 1.0)
		if ph >= 0.5 && ph < 0.62 {
			out[i] += 1800 * fric.Process(r.noise())
		}
		if ph >= 0.75 && ph < 0.753 {
			out[i] += 6000 * r.noise()
		} else if ph >= 0.753 && ph < 0.80 {
			out[i] += 1200 * fric.Process(r.noise()) * (1 - (ph-0.753)/0.047)
		}
	}
	return out
}

// synthKeyboard renders mechanical key presses: a bright 1.5 ms click, a 400 Hz bottom-out
// thock 6 ms later and a softer release click 90 ms after, repeated every 170 ms.
func synthKeyboard(n int, r rng, amp float64) []float64 {
	out := make([]float64, n)
	bright := NewHighpass(fxRate, 2500, 0.7)
	period := int(0.17 * fxRate)
	for i := range out {
		k := i % period
		tk := float64(k) / fxRate
		s := 0.0
		if tk < 0.02 {
			s += bright.Process(r.noise()) * math.Exp(-tk/0.0015)
		}
		if tk >= 0.006 && tk < 0.04 {
			td := tk - 0.006
			s += 0.7 * math.Sin(2*math.Pi*400*td) * math.Exp(-td/0.005)
		}
		if tk >= 0.09 && tk < 0.11 {
			s += 0.5 * bright.Process(r.noise()) * math.Exp(-(tk-0.09)/0.001)
		}
		out[i] = amp * s
	}
	return out
}

// synthClap renders hand claps (broadband burst, 5 ms decay, 60 ms room tail) every 400 ms.
func synthClap(n int, r rng, amp float64) []float64 {
	out := make([]float64, n)
	period := int(0.4 * fxRate)
	for i := range out {
		tk := float64(i%period) / fxRate
		if tk < 0.2 {
			out[i] = amp * r.noise() * (math.Exp(-tk/0.005) + 0.08*math.Exp(-tk/0.06))
		}
	}
	return out
}

func runSuppressor(in []float64) []float64 {
	ts := NewTransientSuppressor(fxRate)
	out := append([]float64(nil), in...)
	for off := 0; off+960 <= len(out); off += 960 {
		ts.Process(out[off : off+960])
	}
	lat := ts.LatencySamples()
	// Re-align for comparisons.
	return append(out[lat:], make([]float64, lat)...)
}

func energyDB(x []float64) float64 {
	var p float64
	for _, v := range x {
		p += v * v
	}
	return 10 * math.Log10(p/float64(len(x))+1e-9)
}

func TestTransientSuppressorKeepsSpeech(t *testing.T) {
	r := rng{rand.New(rand.NewSource(1))}
	n := 3 * fxRate
	speech := synthSpeech(n, r)
	floor := make([]float64, n)
	for i := range floor {
		floor[i] = 12 * r.noise()
	}
	in := make([]float64, n)
	for i := range in {
		in[i] = speech[i] + floor[i]
	}
	out := runSuppressor(in)
	skip := fxRate / 2 // envelope warm-up
	lost := energyDB(in[skip:n-2000]) - energyDB(out[skip:n-2000])
	t.Logf("speech energy change: %.2f dB", -lost)
	if lost > 1.0 {
		t.Fatalf("speech lost %.2f dB, want < 1 dB", lost)
	}
	// Fricative "s" and plosive "t" segments specifically.
	for _, seg := range [][2]float64{{1.5, 1.62}, {1.75, 1.80}, {2.5, 2.62}, {2.75, 2.8}} {
		a, b := int(seg[0]*fxRate), int(seg[1]*fxRate)
		d := energyDB(in[a:b]) - energyDB(out[a:b])
		t.Logf("consonant %.2f-%.2fs change: %.2f dB", seg[0], seg[1], -d)
		if d > 2.0 {
			t.Fatalf("consonant at %.2fs lost %.2f dB", seg[0], d)
		}
	}
}

func TestTransientSuppressorRemovesKeyboardAndClaps(t *testing.T) {
	r := rng{rand.New(rand.NewSource(2))}
	n := 3 * fxRate
	floor := make([]float64, n)
	for i := range floor {
		floor[i] = 12 * r.noise()
	}
	cases := []struct {
		name  string
		sig   []float64
		minDB float64
	}{
		{"keyboard -20 dBFS", synthKeyboard(n, r, 8000), 12},
		{"keyboard -35 dBFS", synthKeyboard(n, r, 1200), 10},
		{"clap -6 dBFS", synthClap(n, r, 26000), 10},
	}
	for _, c := range cases {
		in := make([]float64, n)
		for i := range in {
			in[i] = c.sig[i] + floor[i]
		}
		out := runSuppressor(in)
		skip := fxRate / 2
		red := energyDB(in[skip:n-2000]) - energyDB(out[skip:n-2000])
		t.Logf("%s: reduced %.1f dB", c.name, red)
		if red < c.minDB {
			t.Errorf("%s: reduced only %.1f dB, want ≥ %.0f dB", c.name, red, c.minDB)
		}
	}
}

func TestTransientSuppressorTypingWhileTalking(t *testing.T) {
	r := rng{rand.New(rand.NewSource(3))}
	n := 3 * fxRate
	speech := synthSpeech(n, r)
	keys := synthKeyboard(n, r, 5000)
	in := make([]float64, n)
	for i := range in {
		in[i] = speech[i] + keys[i]
	}
	// Run the mixture and replay its exact gain trajectory on the keyboard alone, so the
	// keyboard residual can be measured separately from the voice.
	ts := NewTransientSuppressor(fxRate)
	split := NewLowpass(fxRate, 2000, math.Sqrt2/2)
	lat := ts.LatencySamples()
	res := make([]float64, 0, n)
	pos := -lat
	ts.onGain = func(gl, gh float64) {
		if pos >= 0 {
			lo := split.Process(keys[pos])
			res = append(res, lo*gl+(keys[pos]-lo)*gh)
		}
		pos++
	}
	buf := append([]float64(nil), in...)
	for off := 0; off+960 <= n; off += 960 {
		ts.Process(buf[off : off+960])
	}
	skip := fxRate / 2
	// Under speech the low "thock" is masked by the voice; the bright click (above 2.5 kHz,
	// clearly audible over the voice) must go.
	hr1, hr2 := NewHighpass(fxRate, 2500, 0.54), NewHighpass(fxRate, 2500, 1.31)
	hk1, hk2 := NewHighpass(fxRate, 2500, 0.54), NewHighpass(fxRate, 2500, 1.31)
	keyHF := make([]float64, len(res))
	for i := range res {
		keyHF[i] = hk2.Process(hk1.Process(keys[i]))
		res[i] = hr2.Process(hr1.Process(res[i]))
	}
	end := len(res) - 2000
	red := energyDB(keyHF[skip:end]) - energyDB(res[skip:end])
	t.Logf("keyboard clicks (>2.5 kHz) under speech reduced %.1f dB", red)
	if red < 5 {
		t.Fatalf("keyboard clicks while talking reduced only %.1f dB, want ≥ 5 dB", red)
	}
}

func TestVoiceSmootherLevelsAndLimits(t *testing.T) {
	r := rng{rand.New(rand.NewSource(4))}
	n := 2 * fxRate
	loud := synthSpeech(n, r)
	for i := range loud {
		loud[i] *= 4 // very hot microphone, clipping territory
	}
	quiet := synthSpeech(n, r)
	for i := range quiet {
		quiet[i] *= 0.25
	}
	s1, s2 := NewVoiceSmoother(fxRate), NewVoiceSmoother(fxRate)
	lo, qo := append([]float64(nil), loud...), append([]float64(nil), quiet...)
	for off := 0; off+960 <= n; off += 960 {
		s1.Process(lo[off : off+960])
		s2.Process(qo[off : off+960])
	}
	skip := fxRate / 2
	var peak float64
	for _, v := range lo[skip:] {
		peak = math.Max(peak, math.Abs(v))
	}
	spreadIn := energyDB(loud[skip:]) - energyDB(quiet[skip:])
	spreadOut := energyDB(lo[skip:]) - energyDB(qo[skip:])
	t.Logf("loudness spread in %.1f dB → out %.1f dB, loud peak %.0f", spreadIn, spreadOut, peak)
	if peak > 32768*math.Pow(10, -2.5/20) {
		t.Fatalf("limiter let peak %.0f through", peak)
	}
	if spreadOut > spreadIn*0.6 {
		t.Fatalf("compressor spread %.1f dB, want ≤ %.1f dB", spreadOut, spreadIn*0.6)
	}
}

func BenchmarkVoiceFXFrame(b *testing.B) {
	r := rng{rand.New(rand.NewSource(5))}
	speech := synthSpeech(fxRate, r)
	ts, sm := NewTransientSuppressor(fxRate), NewVoiceSmoother(fxRate)
	frame := make([]float64, 960)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		off := (i % 49) * 960
		copy(frame, speech[off:off+960])
		ts.Process(frame)
		sm.Process(frame)
	}
}
