package dsp

import "math"

// Biquad is a second-order IIR section (RBJ audio EQ cookbook, transposed direct form II).
type Biquad struct {
	b0, b1, b2, a1, a2 float64
	z1, z2             float64
}

func newBiquad(b0, b1, b2, a0, a1, a2 float64) *Biquad {
	return &Biquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: a1 / a0, a2: a2 / a0}
}

// NewLowpass returns a low-pass filter with cutoff f0 (Hz) and quality q (0.707 = Butterworth).
func NewLowpass(rate, f0, q float64) *Biquad {
	w := 2 * math.Pi * f0 / rate
	cw, alpha := math.Cos(w), math.Sin(w)/(2*q)
	return newBiquad((1-cw)/2, 1-cw, (1-cw)/2, 1+alpha, -2*cw, 1-alpha)
}

// NewHighpass returns a high-pass filter with cutoff f0 (Hz) and quality q.
func NewHighpass(rate, f0, q float64) *Biquad {
	w := 2 * math.Pi * f0 / rate
	cw, alpha := math.Cos(w), math.Sin(w)/(2*q)
	return newBiquad((1+cw)/2, -(1 + cw), (1+cw)/2, 1+alpha, -2*cw, 1-alpha)
}

// NewHighShelf returns a high-shelf filter boosting (gainDB > 0) or cutting everything above f0.
func NewHighShelf(rate, f0, gainDB float64) *Biquad {
	A := math.Pow(10, gainDB/40)
	w := 2 * math.Pi * f0 / rate
	cw := math.Cos(w)
	alpha := math.Sin(w) / 2 * math.Sqrt2 // shelf slope S = 1
	sq := 2 * math.Sqrt(A) * alpha
	return newBiquad(
		A*((A+1)+(A-1)*cw+sq),
		-2*A*((A-1)+(A+1)*cw),
		A*((A+1)+(A-1)*cw-sq),
		(A+1)-(A-1)*cw+sq,
		2*((A-1)-(A+1)*cw),
		(A+1)-(A-1)*cw-sq,
	)
}

// Process filters one sample.
func (b *Biquad) Process(x float64) float64 {
	y := b.b0*x + b.z1
	b.z1 = b.b1*x - b.a1*y + b.z2
	b.z2 = b.b2*x - b.a2*y
	return y
}

// TransientSuppressor attenuates short impulsive noises — keyboard clicks, mouse clicks, claps,
// desk taps — while leaving speech untouched. It applies gains to two bands (below and above
// 2 kHz), decided on 1 ms blocks with a short look-ahead:
//
//   - a block is a transient candidate when its energy jumps well above the band's slowly
//     rising background envelope;
//   - the candidate is only suppressed if the energy has died away again inside the
//     look-ahead window. Speech (vowels, fricatives, plosives followed by aspiration) keeps its
//     energy, clicks and claps do not.
//
// The high band gain is decided on energy above 5 kHz only: glottal pulses of voiced speech are
// impulsive too, but carry little energy that high, whereas clicks and claps are broadband.
// Because the background envelope follows ongoing speech, typing while talking is caught there
// without ducking the voice itself.
type TransientSuppressor struct {
	block int // samples per analysis block
	look  int // look-ahead blocks

	split       *Biquad
	det1, det2  *Biquad   // 4th-order 5 kHz high-pass for the high band detector
	low, high   []float64 // pending (delayed) band samples
	bands       [2]transientBand
	cur         [2]float64 // gain at the end of the previous output block
	pendingDone int        // blocks of the pending buffer that already have a gain decision

	minGain float64 // lowest band gain applied during the last Process call

	onGain func(low, high float64) // test hook: per output sample band gains
}

type transientBand struct {
	energy []float64 // per pending block
	gain   []float64 // decided gain per pending block
	env    float64
	hold   int
	hist   [transientHistory]float64 // energies of the most recently decided blocks
	histAt int
}

const (
	transientTrigger  = 4.0     // candidate: block energy ≥ 6 dB above background
	transientDecay    = 1 / 8.0 // suppressed only if energy falls ≥ 9 dB (or back to background) in the look-ahead
	transientKeep     = 1.5     // attenuate to ~2 dB above background
	transientDepth    = 0.08    // at most −22 dB
	transientHold     = 12      // blocks the tail keeps being attenuated
	transientAbsFloor = 30 * 30 // ignore blocks quieter than about −61 dBFS (int16 scale)
	transientHistory  = 14      // ms of history a burst must stand out from (longer than a pitch period)
)

// NewTransientSuppressor creates a suppressor for rate Hz audio (1 ms blocks, 12 ms look-ahead).
// frame lengths passed to Process must be multiples of rate/1000.
func NewTransientSuppressor(rate int) *TransientSuppressor {
	t := &TransientSuppressor{
		block: rate / 1000,
		look:  12,
		split: NewLowpass(float64(rate), 2000, math.Sqrt2/2),
		det1:  NewHighpass(float64(rate), 5000, 0.54),
		det2:  NewHighpass(float64(rate), 5000, 1.31),
		cur:   [2]float64{1, 1},
	}
	// Prime the delay line so output is exactly LatencySamples() behind the input.
	pad := (t.look + 1) * t.block
	t.low = make([]float64, pad)
	t.high = make([]float64, pad)
	for b := range t.bands {
		t.bands[b].energy = make([]float64, t.look+1)
		t.bands[b].env = transientAbsFloor
	}
	return t
}

// MinGain reports the strongest attenuation (lowest gain, 1 = untouched) applied to the audio
// returned by the last Process call. Callers use it to keep clicks from opening a voice gate.
func (t *TransientSuppressor) MinGain() float64 { return t.minGain }

// LatencySamples is the constant delay Process adds.
func (t *TransientSuppressor) LatencySamples() int { return (t.look + 1) * t.block }

// Process suppresses transients in frame (int16-scaled samples) in place; the output is delayed
// by LatencySamples().
func (t *TransientSuppressor) Process(frame []float64) {
	n := len(frame) - len(frame)%t.block
	for off := 0; off < n; off += t.block {
		var eLow, eHigh float64
		for _, x := range frame[off : off+t.block] {
			lo := t.split.Process(x)
			t.low = append(t.low, lo)
			t.high = append(t.high, x-lo)
			eLow += lo * lo
			d := t.det2.Process(t.det1.Process(x))
			eHigh += d * d
		}
		t.bands[0].energy = append(t.bands[0].energy, eLow/float64(t.block))
		t.bands[1].energy = append(t.bands[1].energy, eHigh/float64(t.block))
	}

	blocks := len(t.bands[0].energy)
	for j := t.pendingDone; j+t.look < blocks; j++ {
		for b := range t.bands {
			t.bands[b].decide(j, t.look)
		}
		t.pendingDone = j + 1
	}

	outBlocks := n / t.block
	t.minGain = 1
	var gains [256]float64
	for j := 0; j < outBlocks; j++ {
		start := j * t.block
		for b := range t.bands {
			band := &t.bands[b]
			target := band.gain[j]
			if j+1 < len(band.gain) {
				target = min(target, band.gain[j+1]) // ramp down before the click arrives
			}
			from := t.cur[b]
			src := t.low
			if b == 1 {
				src = t.high
			}
			for i := 0; i < t.block; i++ {
				g := from + (target-from)*float64(i+1)/float64(t.block)
				if b == 0 {
					frame[start+i] = src[start+i] * g
					gains[i] = g
				} else {
					frame[start+i] += src[start+i] * g
					if t.onGain != nil {
						t.onGain(gains[i], g)
					}
				}
			}
			t.cur[b] = target
			t.minGain = math.Min(t.minGain, target)
		}
	}

	// Drop the emitted samples and block decisions.
	consumed := outBlocks * t.block
	t.low = append(t.low[:0], t.low[consumed:]...)
	t.high = append(t.high[:0], t.high[consumed:]...)
	for b := range t.bands {
		band := &t.bands[b]
		band.energy = append(band.energy[:0], band.energy[outBlocks:]...)
		band.gain = append(band.gain[:0], band.gain[outBlocks:]...)
	}
	t.pendingDone -= outBlocks
}

func (band *transientBand) decide(j, look int) {
	e := band.energy[j]
	bg := band.env
	g := 1.0
	if band.hold > 0 {
		band.hold--
	}
	// A new burst must also stand out from the last ~14 ms: glottal pulses of voiced speech
	// repeat within one pitch period, a click comes out of (relative) quiet.
	var recent float64
	for _, h := range band.hist {
		recent = math.Max(recent, h)
	}
	onset := e > bg*transientTrigger && e > recent*transientTrigger
	candidate := e > transientAbsFloor && (onset || (band.hold > 0 && e > bg*transientKeep))
	if candidate {
		// Only a burst that dies away inside the look-ahead window is a transient; sustained
		// energy (vowels, fricatives, aspiration) is speech.
		var later float64
		for k := j + look - 2; k <= j+look; k++ {
			later += band.energy[k]
		}
		if later/3 < math.Max(e*transientDecay, bg*transientKeep) {
			g = math.Max(transientDepth, math.Min(1, math.Sqrt(bg*transientKeep/e)))
			if onset {
				band.hold = transientHold
			}
		}
	}
	band.gain = append(band.gain, g)
	band.hist[band.histAt] = e
	band.histAt = (band.histAt + 1) % transientHistory

	// Background envelope: rises at most ~4 %/ms (sustained sounds such as speech lift it within
	// ~150 ms, a click barely moves it) and falls with a ~100 ms time constant.
	if e > band.env {
		band.env += math.Min(e-band.env, band.env*0.04)
	} else {
		band.env += (e - band.env) * 0.01
	}
	band.env = math.Max(band.env, transientAbsFloor)
}

// VoiceSmoother gently shapes the transmitted voice: it tames harsh highs and sibilance
// (high shelf −4 dB from 6 kHz), removes the hardly audible but fatiguing air band (12 kHz
// low-pass), evens out loudness with a soft compressor and catches peaks with a limiter.
type VoiceSmoother struct {
	shelf, lowpass *Biquad
	block          int

	env       float64 // RMS envelope (linear, int16 scale)
	gainDB    float64
	limGain   float64
	limRel    float64
	threshold float64 // dBFS
	ratio     float64
	makeupDB  float64
	ceiling   float64 // int16 scale
}

// NewVoiceSmoother creates a smoother for rate Hz audio.
func NewVoiceSmoother(rate int) *VoiceSmoother {
	fs := float64(rate)
	return &VoiceSmoother{
		shelf:     NewHighShelf(fs, 6000, -4),
		lowpass:   NewLowpass(fs, 12000, math.Sqrt2/2),
		block:     rate / 1000,
		limGain:   1,
		limRel:    1 - math.Exp(-1/(0.08*fs)),
		threshold: -26,
		ratio:     3,
		makeupDB:  3,
		ceiling:   32768 * math.Pow(10, -3.0/20), // −3 dBFS
	}
}

// Process shapes frame (int16-scaled samples) in place.
func (s *VoiceSmoother) Process(frame []float64) {
	for i, x := range frame {
		frame[i] = s.lowpass.Process(s.shelf.Process(x))
	}
	for off := 0; off < len(frame); off += s.block {
		end := min(off+s.block, len(frame))
		var p float64
		for _, x := range frame[off:end] {
			p += x * x
		}
		rms := math.Sqrt(p / float64(end-off))
		// 5 ms attack, 150 ms release RMS detector.
		if rms > s.env {
			s.env += (rms - s.env) * 0.18
		} else {
			s.env += (rms - s.env) * 0.0066
		}
		level := 20 * math.Log10(math.Max(s.env, 1)/32768)
		target := s.makeupDB
		if level > s.threshold {
			target -= (level - s.threshold) * (1 - 1/s.ratio)
		}
		from := s.gainDB
		// Gain smoothing: 10 ms towards less gain, 250 ms towards more.
		if target < s.gainDB {
			s.gainDB += (target - s.gainDB) * 0.1
		} else {
			s.gainDB += (target - s.gainDB) * 0.004
		}
		for i := off; i < end; i++ {
			gdb := from + (s.gainDB-from)*float64(i-off+1)/float64(end-off)
			y := frame[i] * math.Pow(10, gdb/20)
			if a := math.Abs(y) * s.limGain; a > s.ceiling {
				s.limGain = s.ceiling / math.Abs(y)
			}
			frame[i] = y * s.limGain
			s.limGain += (1 - s.limGain) * s.limRel
		}
	}
}
