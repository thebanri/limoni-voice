package dsp

import "math"

// LoopbackCanceller removes a known digital signal (what this application plays) from a
// loopback capture of the system output (desktop audio). Unlike an acoustic echo path, the
// loopback path is a delay plus a gain / short filter, so the canceller estimates the delay with
// GCC-PHAT on a decimated copy and then runs a short NLMS filter around it. Continuous other
// audio (music, games) is just uncorrelated noise to it and passes untouched.
//
// Playback and Capture must be called with consecutive frames of their streams from the moment
// the canceller is created, so sample counters of both streams stay comparable.
type LoopbackCanceller struct {
	ref      []float64 // reference ring (full rate)
	refCount int

	refDec, capDec []float64 // decimated rings for delay estimation
	refAcc, capAcc float64
	refN, capN     int
	refDecCount    int
	capDecCount    int
	capCount       int

	fft           *FFT
	re, im, oRe   []float64
	oIm, specR    []float64
	specI         []float64
	lag           int // capture[c] ≈ Σ w[k]·ref[c-lag+center-k]
	haveLag       bool
	sinceEstimate int

	// Two-path filter: the background filter adapts fast (and is noisy while other audio plays),
	// the foreground filter produces the output and only takes over background coefficients
	// that proved better over a window.
	w, wb         []float64
	history       []float64
	adapted       int // samples adapted since the filter was reset (step size schedule)
	errF, errB    float64
	windowSamples int
}

const (
	loopRefRing   = 1 << 16 // 1.36 s at 48 kHz
	loopDecim     = 8
	loopDecWindow = 8192 // decimated samples (1.36 s)
	loopTaps      = 64
	loopCenter    = 32
	loopMaxLag    = 400 * 48000 / 1000 / loopDecim // decimated samples either way
)

// NewLoopbackCanceller creates a canceller for 48 kHz streams.
func NewLoopbackCanceller() *LoopbackCanceller {
	return &LoopbackCanceller{
		ref:     make([]float64, loopRefRing),
		refDec:  make([]float64, loopDecWindow),
		capDec:  make([]float64, loopDecWindow),
		fft:     NewFFT(loopDecWindow * 2),
		re:      make([]float64, loopDecWindow*2),
		im:      make([]float64, loopDecWindow*2),
		oRe:     make([]float64, loopDecWindow*2),
		oIm:     make([]float64, loopDecWindow*2),
		specR:   make([]float64, loopDecWindow*2),
		specI:   make([]float64, loopDecWindow*2),
		w:       make([]float64, loopTaps),
		wb:      make([]float64, loopTaps),
		history: make([]float64, loopTaps),
	}
}

// Playback records samples that were sent to the output device.
func (lc *LoopbackCanceller) Playback(frame []int16) {
	for _, s := range frame {
		v := float64(s)
		lc.ref[lc.refCount%loopRefRing] = v
		lc.refCount++
		lc.refAcc += v * v
		lc.refN++
		if lc.refN == loopDecim {
			lc.refDec[lc.refDecCount%loopDecWindow] = math.Sqrt(lc.refAcc / loopDecim)
			lc.refDecCount++
			lc.refAcc, lc.refN = 0, 0
		}
	}
}

// Delay reports the estimated loopback delay in samples and whether one was found.
func (lc *LoopbackCanceller) Delay() (int, bool) { return lc.lag, lc.haveLag }

func (lc *LoopbackCanceller) refAt(i int) float64 {
	if i < 0 || i >= lc.refCount || lc.refCount-i > loopRefRing {
		return 0
	}
	return lc.ref[i%loopRefRing]
}

// Capture removes the reference from in and writes the result to out (len(out) ≥ len(in)).
func (lc *LoopbackCanceller) Capture(in, out []int16) {
	for i, s := range in {
		v := float64(s)
		lc.capAcc += v * v
		lc.capN++
		if lc.capN == loopDecim {
			lc.capDec[lc.capDecCount%loopDecWindow] = math.Sqrt(lc.capAcc / loopDecim)
			lc.capDecCount++
			lc.capAcc, lc.capN = 0, 0
		}

		c := lc.capCount
		lc.capCount++
		if !lc.haveLag {
			out[i] = s
			continue
		}
		var y, yb, power float64
		base := c - lc.lag + loopCenter
		for k := 0; k < loopTaps; k++ {
			x := lc.refAt(base - k)
			lc.history[k] = x
			y += lc.w[k] * x
			yb += lc.wb[k] * x
			power += x * x
		}
		e := v - y
		if power > loopTaps*50*50 { // adapt only while we actually play something
			eb := v - yb
			step := math.Max(0.02, 0.5*math.Exp(-float64(lc.adapted)/12000))
			lc.adapted++
			mu := step / (power + 1e3)
			for k := range lc.wb {
				lc.wb[k] += mu * eb * lc.history[k]
			}
			lc.errF += e * e
			lc.errB += eb * eb
			lc.windowSamples++
			if lc.windowSamples >= 2400 {
				if lc.errB < 0.8*lc.errF {
					copy(lc.w, lc.wb)
				}
				lc.errF, lc.errB, lc.windowSamples = 0, 0, 0
			}
		}
		out[i] = int16(math.Max(-32768, math.Min(32767, math.Round(e))))
	}

	lc.sinceEstimate += len(in)
	if lc.sinceEstimate >= 48000 && lc.capDecCount >= loopDecWindow && lc.refDecCount >= loopDecWindow {
		lc.sinceEstimate = 0
		lc.estimateLag()
	}
}

// estimateLag runs GCC-PHAT between the decimated energy envelopes of capture and reference.
func (lc *LoopbackCanceller) estimateLag() {
	n := len(lc.re)
	end := min(lc.capDecCount, lc.refDecCount) // common decimated index window
	start := end - loopDecWindow
	var refE float64
	var refMean, capMean float64
	for j := start; j < end; j++ {
		refMean += lc.refDec[j%loopDecWindow]
		capMean += lc.capDec[j%loopDecWindow]
	}
	refMean /= loopDecWindow
	capMean /= loopDecWindow
	// Spectrum of capture and reference (zero padded to 2×window).
	clear(lc.re)
	clear(lc.im)
	for j := start; j < end; j++ {
		r := lc.refDec[j%loopDecWindow] - refMean
		lc.re[j-start] = r
		refE += r * r
	}
	if refE < float64(loopDecWindow)*20*20 {
		return // nothing played recently: keep the previous estimate
	}
	lc.fft.Transform(lc.re, lc.im, lc.specR, lc.specI)
	clear(lc.re)
	clear(lc.im)
	for j := start; j < end; j++ {
		lc.re[j-start] = lc.capDec[j%loopDecWindow] - capMean
	}
	lc.fft.Transform(lc.re, lc.im, lc.oRe, lc.oIm)
	// Cross spectrum C = Cap · conj(Ref), PHAT weighted.
	for k := 0; k < n; k++ {
		cr := lc.oRe[k]*lc.specR[k] + lc.oIm[k]*lc.specI[k]
		ci := lc.oIm[k]*lc.specR[k] - lc.oRe[k]*lc.specI[k]
		mag := math.Hypot(cr, ci) + 1e-9
		lc.re[k], lc.im[k] = cr/mag, ci/mag
	}
	lc.fft.Transform(lc.re, lc.im, lc.oRe, lc.oIm)
	// forward transform of the spectrum yields the correlation time reversed.
	best, bestLag, sum := -1.0, 0, 0.0
	for lag := -loopMaxLag / 4; lag <= loopMaxLag; lag++ {
		v := lc.oRe[((n-lag)%n+n)%n]
		sum += math.Abs(v)
		if v > best {
			best, bestLag = v, lag
		}
	}
	mean := sum / float64(loopMaxLag+loopMaxLag/4+1)
	if best < 6*mean {
		return // no clear peak
	}
	lag := bestLag * loopDecim
	if !lc.haveLag || absInt(lag-lc.lag) > loopCenter/2 {
		lc.lag = lag
		clear(lc.w)
		clear(lc.wb)
		lc.adapted = 0
		lc.errF, lc.errB, lc.windowSamples = 0, 0, 0
		lc.haveLag = true
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
