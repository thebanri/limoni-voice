package rnnoise

import (
	"math"
	"sync"

	"github.com/thebanri/limoni-voice/internal/dsp"
)

const (
	frameSizeShift = 2
	// FrameSize is the number of 48 kHz samples processed per call (10 ms).
	FrameSize      = 120 << frameSizeShift
	windowSize     = 2 * FrameSize
	freqSize       = FrameSize + 1
	pitchMinPeriod = 60
	pitchMaxPeriod = 768
	pitchFrameSize = 960
	pitchBufSize   = pitchMaxPeriod + pitchFrameSize
	nbBands        = 22
	cepsMem        = 8
	nbDeltaCeps    = 6
	nbFeatures     = nbBands + 3*nbDeltaCeps + 2
)

var eband5ms = [nbBands]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 10, 12, 14, 16, 20, 24, 28, 34, 40, 48, 60, 78, 100}

type commonState struct {
	fft        *dsp.FFT
	halfWindow [FrameSize]float32
	dctTable   [nbBands * nbBands]float32
}

var (
	common     commonState
	commonOnce sync.Once
)

func initCommon() {
	commonOnce.Do(func() {
		common.fft = dsp.NewFFT(windowSize)
		for i := 0; i < FrameSize; i++ {
			s := math.Sin(.5 * math.Pi * (float64(i) + .5) / FrameSize)
			common.halfWindow[i] = float32(math.Sin(.5 * math.Pi * s * s))
		}
		for i := 0; i < nbBands; i++ {
			for j := 0; j < nbBands; j++ {
				v := math.Cos((float64(i) + .5) * float64(j) * math.Pi / nbBands)
				if j == 0 {
					v *= math.Sqrt(.5)
				}
				common.dctTable[i*nbBands+j] = float32(v)
			}
		}
	})
}

type cpx struct{ r, i float32 }

// State is one RNNoise denoiser instance (not safe for concurrent use).
type State struct {
	analysisMem   [FrameSize]float32
	cepstralMem   [cepsMem][nbBands]float32
	memID         int
	synthesisMem  [FrameSize]float32
	pitchBuf      [pitchBufSize]float32
	lastGain      float32
	lastPeriod    int
	memHPX        [2]float32
	rnn           rnnState
	fftInRe       []float64
	fftInIm       []float64
	fftOutRe      []float64
	fftOutIm      []float64
	downsampleBuf [pitchBufSize >> 1]float32
}

// New creates a denoiser.
func New() *State {
	initCommon()
	return &State{
		fftInRe:  make([]float64, windowSize),
		fftInIm:  make([]float64, windowSize),
		fftOutRe: make([]float64, windowSize),
		fftOutIm: make([]float64, windowSize),
	}
}

func computeBandEnergy(bandE []float32, X []cpx) {
	var sum [nbBands]float32
	for i := 0; i < nbBands-1; i++ {
		bandSize := (eband5ms[i+1] - eband5ms[i]) << frameSizeShift
		for j := 0; j < bandSize; j++ {
			frac := float32(j) / float32(bandSize)
			x := X[(eband5ms[i]<<frameSizeShift)+j]
			tmp := x.r*x.r + x.i*x.i
			sum[i] += (1 - frac) * tmp
			sum[i+1] += frac * tmp
		}
	}
	sum[0] *= 2
	sum[nbBands-1] *= 2
	copy(bandE, sum[:])
}

func computeBandCorr(bandE []float32, X, P []cpx) {
	var sum [nbBands]float32
	for i := 0; i < nbBands-1; i++ {
		bandSize := (eband5ms[i+1] - eband5ms[i]) << frameSizeShift
		for j := 0; j < bandSize; j++ {
			frac := float32(j) / float32(bandSize)
			idx := (eband5ms[i] << frameSizeShift) + j
			tmp := X[idx].r*P[idx].r + X[idx].i*P[idx].i
			sum[i] += (1 - frac) * tmp
			sum[i+1] += frac * tmp
		}
	}
	sum[0] *= 2
	sum[nbBands-1] *= 2
	copy(bandE, sum[:])
}

func interpBandGain(g []float32, bandE []float32) {
	for i := range g[:freqSize] {
		g[i] = 0
	}
	for i := 0; i < nbBands-1; i++ {
		bandSize := (eband5ms[i+1] - eband5ms[i]) << frameSizeShift
		for j := 0; j < bandSize; j++ {
			frac := float32(j) / float32(bandSize)
			g[(eband5ms[i]<<frameSizeShift)+j] = (1-frac)*bandE[i] + frac*bandE[i+1]
		}
	}
}

func dct(out, in []float32) {
	scale := float32(math.Sqrt(2. / 22))
	for i := 0; i < nbBands; i++ {
		var sum float32
		for j := 0; j < nbBands; j++ {
			sum += in[j] * common.dctTable[j*nbBands+i]
		}
		out[i] = sum * scale
	}
}

func (st *State) forwardTransform(out []cpx, in []float32) {
	scale := 1.0 / float64(windowSize)
	for i := 0; i < windowSize; i++ {
		st.fftInRe[i] = float64(in[i]) * scale
		st.fftInIm[i] = 0
	}
	common.fft.Transform(st.fftInRe, st.fftInIm, st.fftOutRe, st.fftOutIm)
	for i := range out {
		out[i] = cpx{float32(st.fftOutRe[i]), float32(st.fftOutIm[i])}
	}
}

func (st *State) inverseTransform(out []float32, in []cpx) {
	scale := 1.0 / float64(windowSize)
	for i := 0; i < freqSize; i++ {
		st.fftInRe[i] = float64(in[i].r) * scale
		st.fftInIm[i] = float64(in[i].i) * scale
	}
	for i := freqSize; i < windowSize; i++ {
		st.fftInRe[i] = st.fftInRe[windowSize-i]
		st.fftInIm[i] = -st.fftInIm[windowSize-i]
	}
	common.fft.Transform(st.fftInRe, st.fftInIm, st.fftOutRe, st.fftOutIm)
	out[0] = float32(windowSize * st.fftOutRe[0])
	for i := 1; i < windowSize; i++ {
		out[i] = float32(windowSize * st.fftOutRe[windowSize-i])
	}
}

func applyWindow(x []float32) {
	for i := 0; i < FrameSize; i++ {
		x[i] *= common.halfWindow[i]
		x[windowSize-1-i] *= common.halfWindow[i]
	}
}

func (st *State) frameAnalysis(X []cpx, Ex []float32, in []float32) {
	var x [windowSize]float32
	copy(x[:FrameSize], st.analysisMem[:])
	copy(x[FrameSize:], in[:FrameSize])
	copy(st.analysisMem[:], in[:FrameSize])
	applyWindow(x[:])
	st.forwardTransform(X, x[:])
	computeBandEnergy(Ex, X)
}

func (st *State) computeFrameFeatures(X, P []cpx, Ex, Ep, Exp, features []float32, in []float32) bool {
	var E float32
	var specVariability float32
	var Ly, tmp [nbBands]float32
	var p [windowSize]float32

	st.frameAnalysis(X, Ex, in)
	copy(st.pitchBuf[:], st.pitchBuf[FrameSize:])
	copy(st.pitchBuf[pitchBufSize-FrameSize:], in[:FrameSize])
	pitchDownsample(st.pitchBuf[:], st.downsampleBuf[:], pitchBufSize)
	pitchIndex := pitchSearch(st.downsampleBuf[pitchMaxPeriod>>1:], st.downsampleBuf[:], pitchFrameSize, pitchMaxPeriod-3*pitchMinPeriod)
	pitchIndex = pitchMaxPeriod - pitchIndex
	gain := removeDoubling(st.downsampleBuf[:], pitchMaxPeriod, pitchMinPeriod, pitchFrameSize, &pitchIndex, st.lastPeriod, st.lastGain)
	st.lastPeriod = pitchIndex
	st.lastGain = gain
	for i := 0; i < windowSize; i++ {
		p[i] = st.pitchBuf[pitchBufSize-windowSize-pitchIndex+i]
	}
	applyWindow(p[:])
	st.forwardTransform(P, p[:])
	computeBandEnergy(Ep, P)
	computeBandCorr(Exp, X, P)
	for i := 0; i < nbBands; i++ {
		Exp[i] = float32(float64(Exp[i]) / math.Sqrt(.001+float64(Ex[i])*float64(Ep[i])))
	}
	dct(tmp[:], Exp)
	for i := 0; i < nbDeltaCeps; i++ {
		features[nbBands+2*nbDeltaCeps+i] = tmp[i]
	}
	features[nbBands+2*nbDeltaCeps] -= 1.3
	features[nbBands+2*nbDeltaCeps+1] -= 0.9
	features[nbBands+3*nbDeltaCeps] = .01 * float32(pitchIndex-300)
	logMax := float32(-2)
	follow := float32(-2)
	for i := 0; i < nbBands; i++ {
		Ly[i] = float32(math.Log10(1e-2 + float64(Ex[i])))
		Ly[i] = max(logMax-7, max(follow-1.5, Ly[i]))
		logMax = max(logMax, Ly[i])
		follow = max(follow-1.5, Ly[i])
		E += Ex[i]
	}
	if E < 0.04 {
		// No audio: avoid messing up the state.
		for i := range features[:nbFeatures] {
			features[i] = 0
		}
		return true
	}
	dct(features, Ly[:])
	features[0] -= 12
	features[1] -= 4
	ceps0 := &st.cepstralMem[st.memID]
	ceps1 := &st.cepstralMem[(st.memID+cepsMem-1)%cepsMem]
	ceps2 := &st.cepstralMem[(st.memID+cepsMem-2)%cepsMem]
	copy(ceps0[:], features[:nbBands])
	st.memID++
	for i := 0; i < nbDeltaCeps; i++ {
		features[i] = ceps0[i] + ceps1[i] + ceps2[i]
		features[nbBands+i] = ceps0[i] - ceps2[i]
		features[nbBands+nbDeltaCeps+i] = ceps0[i] - 2*ceps1[i] + ceps2[i]
	}
	if st.memID == cepsMem {
		st.memID = 0
	}
	for i := 0; i < cepsMem; i++ {
		mindist := float32(1e15)
		for j := 0; j < cepsMem; j++ {
			var dist float32
			for k := 0; k < nbBands; k++ {
				d := st.cepstralMem[i][k] - st.cepstralMem[j][k]
				dist += d * d
			}
			if j != i {
				mindist = min(mindist, dist)
			}
		}
		specVariability += mindist
	}
	features[nbBands+3*nbDeltaCeps+1] = specVariability/cepsMem - 2.1
	return false
}

func (st *State) frameSynthesis(out []float32, y []cpx) {
	var x [windowSize]float32
	st.inverseTransform(x[:], y)
	applyWindow(x[:])
	for i := 0; i < FrameSize; i++ {
		out[i] = x[i] + st.synthesisMem[i]
	}
	copy(st.synthesisMem[:], x[FrameSize:])
}

func biquad(y []float32, mem *[2]float32, x []float32, b, a [2]float32, n int) {
	for i := 0; i < n; i++ {
		xi := x[i]
		yi := x[i] + mem[0]
		mem[0] = float32(float64(mem[1]) + (float64(b[0])*float64(xi) - float64(a[0])*float64(yi)))
		mem[1] = float32(float64(b[1])*float64(xi) - float64(a[1])*float64(yi))
		y[i] = yi
	}
}

func pitchFilter(X, P []cpx, Ex, Ep, Exp, g []float32) {
	var r [nbBands]float32
	var rf [freqSize]float32
	for i := 0; i < nbBands; i++ {
		if Exp[i] > g[i] {
			r[i] = 1
		} else {
			r[i] = Exp[i] * Exp[i] * (1 - g[i]*g[i]) / (.001 + g[i]*g[i]*(1-Exp[i]*Exp[i]))
		}
		r[i] = float32(math.Sqrt(float64(min(1, max(0, r[i])))))
		r[i] *= float32(math.Sqrt(float64(Ex[i]) / (1e-8 + float64(Ep[i]))))
	}
	interpBandGain(rf[:], r[:])
	for i := 0; i < freqSize; i++ {
		X[i].r += rf[i] * P[i].r
		X[i].i += rf[i] * P[i].i
	}
	var newE, norm [nbBands]float32
	var normf [freqSize]float32
	computeBandEnergy(newE[:], X)
	for i := 0; i < nbBands; i++ {
		norm[i] = float32(math.Sqrt(float64(Ex[i]) / (1e-8 + float64(newE[i]))))
	}
	interpBandGain(normf[:], norm[:])
	for i := 0; i < freqSize; i++ {
		X[i].r *= normf[i]
		X[i].i *= normf[i]
	}
}

var (
	aHP = [2]float32{-1.99599, 0.99600}
	bHP = [2]float32{-2, 1}
)

// ProcessFrame denoises FrameSize samples (int16 scale floats) from in into out and returns
// the voice activity probability (0..1). in and out may alias.
func (st *State) ProcessFrame(out, in []float32) float32 {
	var X [freqSize]cpx
	var P [windowSize]cpx
	var x [FrameSize]float32
	var Ex, Ep, Exp, g [nbBands]float32
	var features [nbFeatures]float32
	var gf [freqSize]float32
	var vadProb float32

	biquad(x[:], &st.memHPX, in, bHP, aHP, FrameSize)
	silence := st.computeFrameFeatures(X[:], P[:freqSize], Ex[:], Ep[:], Exp[:], features[:], x[:])
	if !silence {
		computeRNN(&st.rnn, g[:], &vadProb, features[:])
		pitchFilter(X[:], P[:], Ex[:], Ep[:], Exp[:], g[:])
		interpBandGain(gf[:], g[:])
		for i := 0; i < freqSize; i++ {
			X[i].r *= gf[i]
			X[i].i *= gf[i]
		}
	}
	st.frameSynthesis(out, X[:])
	return vadProb
}
