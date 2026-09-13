package dsp

import "math"

// EchoCanceller is a Go port of the SpeexDSP MDF acoustic echo canceller (float build,
// two-path filter, single microphone and speaker). It removes the far-end signal played by
// the speakers from the microphone capture.
//
// Copyright notice of the original implementation (BSD-3-Clause):
// Copyright (C) 2003-2008 Jean-Marc Valin, Xiph.Org Foundation.
type EchoCanceller struct {
	frameSize   int
	windowSize  int
	m           int
	cancelCount int
	adapted     bool
	saturated   int
	screwedUp   int
	rate        int
	specAverage float64
	beta0       float64
	betaMax     float64
	sumAdapt    float64
	leak        float64

	e, x, X, input, y, lastY, Y, E []float64
	phi, W, foreground             []float64
	davg1, davg2, dvar1, dvar2     float64
	power, power1, wtmp            []float64
	rf, yf, xf, eh, yh             []float64
	pey, pyy                       float64
	window, prop                   []float64
	fft                            *RealFFT
	memX, memD, memE               float64
	preemph                        float64
	notchRadius                    float64
	notchMem                       [2]float64

	playBuf        []float64
	playBufPos     int
	playBufStarted bool
}

const (
	aecPlaybackDelay = 2
	aecMinLeak       = .005
	aecVar1Smooth    = .36
	aecVar2Smooth    = .7225
	aecVar1Update    = .5
	aecVar2Update    = .25
	aecVarBacktrack  = 4.0
)

// NewEchoCanceller creates a canceller for frameSize samples per call and an echo tail of
// filterLength samples at sampleRate.
func NewEchoCanceller(frameSize, filterLength, sampleRate int) *EchoCanceller {
	n := 2 * frameSize
	m := (filterLength + frameSize - 1) / frameSize
	if m < 2 {
		m = 2
	}
	st := &EchoCanceller{
		frameSize:  frameSize,
		windowSize: n,
		m:          m,
		fft:        NewRealFFT(n),
		e:          make([]float64, n),
		x:          make([]float64, n),
		input:      make([]float64, frameSize),
		y:          make([]float64, n),
		lastY:      make([]float64, n),
		yf:         make([]float64, frameSize+1),
		rf:         make([]float64, frameSize+1),
		xf:         make([]float64, frameSize+1),
		yh:         make([]float64, frameSize+1),
		eh:         make([]float64, frameSize+1),
		X:          make([]float64, (m+1)*n),
		Y:          make([]float64, n),
		E:          make([]float64, n),
		W:          make([]float64, m*n),
		foreground: make([]float64, m*n),
		phi:        make([]float64, n),
		power:      make([]float64, frameSize+1),
		power1:     make([]float64, frameSize+1),
		window:     make([]float64, n),
		prop:       make([]float64, m),
		wtmp:       make([]float64, n),
		preemph:    .9,
		playBuf:    make([]float64, (aecPlaybackDelay+1)*frameSize),
	}
	for i := 0; i < n; i++ {
		st.window[i] = .5 - .5*math.Cos(2*math.Pi*float64(i)/float64(n))
	}
	for i := range st.power1 {
		st.power1[i] = 1
	}
	decay := math.Exp(-2.4 / float64(m))
	st.prop[0] = .7
	sum := st.prop[0]
	for i := 1; i < m; i++ {
		st.prop[i] = st.prop[i-1] * decay
		sum += st.prop[i]
	}
	for i := m - 1; i >= 0; i-- {
		st.prop[i] = .8 * st.prop[i] / sum
	}
	st.pey, st.pyy = 1, 1
	st.playBufPos = aecPlaybackDelay * frameSize
	st.setSampleRate(sampleRate)
	return st
}

func (st *EchoCanceller) setSampleRate(rate int) {
	st.rate = rate
	st.specAverage = float64(st.frameSize) / float64(rate)
	st.beta0 = 2.0 * float64(st.frameSize) / float64(rate)
	st.betaMax = .5 * float64(st.frameSize) / float64(rate)
	switch {
	case rate < 12000:
		st.notchRadius = .9
	case rate < 24000:
		st.notchRadius = .982
	default:
		st.notchRadius = .992
	}
}

// FrameSize returns the number of samples processed per call.
func (st *EchoCanceller) FrameSize() int { return st.frameSize }

// Reset clears the adaptive state.
func (st *EchoCanceller) Reset() {
	st.cancelCount = 0
	st.screwedUp = 0
	clear(st.W)
	clear(st.foreground)
	clear(st.X)
	for i := 0; i <= st.frameSize; i++ {
		st.power[i] = 0
		st.power1[i] = 1
		st.eh[i] = 0
		st.yh[i] = 0
	}
	clear(st.lastY)
	clear(st.E)
	clear(st.x)
	st.notchMem = [2]float64{}
	st.memD, st.memE, st.memX = 0, 0, 0
	st.saturated = 0
	st.adapted = false
	st.sumAdapt = 0
	st.pey, st.pyy = 1, 1
	st.davg1, st.davg2, st.dvar1, st.dvar2 = 0, 0, 0, 0
	clear(st.playBuf)
	st.playBufPos = aecPlaybackDelay * st.frameSize
	st.playBufStarted = false
}

// Playback queues a far-end frame that has just been sent to the speakers.
func (st *EchoCanceller) Playback(play []int16) {
	if !st.playBufStarted {
		return
	}
	fs := st.frameSize
	if st.playBufPos <= aecPlaybackDelay*fs {
		for i := 0; i < fs; i++ {
			st.playBuf[st.playBufPos+i] = float64(play[i])
		}
		st.playBufPos += fs
		if st.playBufPos <= (aecPlaybackDelay-1)*fs {
			for i := 0; i < fs; i++ {
				st.playBuf[st.playBufPos+i] = float64(play[i])
			}
			st.playBufPos += fs
		}
	}
}

// Capture removes echo from a microphone frame using the queued playback frames.
func (st *EchoCanceller) Capture(rec, out []int16) {
	st.playBufStarted = true
	fs := st.frameSize
	if st.playBufPos >= fs {
		far := st.playBuf[:fs]
		st.cancel(rec, far, out)
		st.playBufPos -= fs
		copy(st.playBuf, st.playBuf[fs:fs+st.playBufPos])
	} else {
		st.playBufPos = 0
		copy(out[:fs], rec[:fs])
	}
}

// Cancel removes the echo of farEnd from rec (both frameSize samples) into out.
func (st *EchoCanceller) Cancel(rec, farEnd, out []int16) {
	far := make([]float64, st.frameSize)
	for i := range far {
		far[i] = float64(farEnd[i])
	}
	st.cancel(rec, far, out)
}

func innerProd(x, y []float64) float64 {
	var sum float64
	for i := range x {
		sum += x[i] * y[i]
	}
	return sum
}

func powerSpectrumAccum(X, ps []float64, n int) {
	ps[0] += X[0] * X[0]
	j := 1
	i := 1
	for ; i < n-1; i, j = i+2, j+1 {
		ps[j] += X[i]*X[i] + X[i+1]*X[i+1]
	}
	ps[j] += X[i] * X[i]
}

func powerSpectrum(X, ps []float64, n int) {
	ps[0] = X[0] * X[0]
	j := 1
	i := 1
	for ; i < n-1; i, j = i+2, j+1 {
		ps[j] = X[i]*X[i] + X[i+1]*X[i+1]
	}
	ps[j] = X[i] * X[i]
}

// spectralMulAccum multiplies packed spectra of the M stacked frames of X with W and sums.
func spectralMulAccum(X, Y, acc []float64, n, m int) {
	clear(acc[:n])
	for j := 0; j < m; j++ {
		xo, yo := j*n, j*n
		acc[0] += X[xo] * Y[yo]
		i := 1
		for ; i < n-1; i += 2 {
			acc[i] += X[xo+i]*Y[yo+i] - X[xo+i+1]*Y[yo+i+1]
			acc[i+1] += X[xo+i+1]*Y[yo+i] + X[xo+i]*Y[yo+i+1]
		}
		acc[i] += X[xo+i] * Y[yo+i]
	}
}

func weightedSpectralMulConj(w []float64, p float64, X, Y, prod []float64, n int) {
	W := p * w[0]
	prod[0] = W * X[0] * Y[0]
	i, j := 1, 1
	for ; i < n-1; i, j = i+2, j+1 {
		W = p * w[j]
		prod[i] = W * (X[i]*Y[i] + X[i+1]*Y[i+1])
		prod[i+1] = W * (-X[i+1]*Y[i] + X[i]*Y[i+1])
	}
	W = p * w[j]
	prod[i] = W * X[i] * Y[i]
}

func (st *EchoCanceller) adjustProp() {
	n, m := st.windowSize, st.m
	maxSum := 1.0
	propSum := 1.0
	for i := 0; i < m; i++ {
		tmp := 1.0
		for j := 0; j < n; j++ {
			v := st.W[i*n+j]
			tmp += v * v
		}
		st.prop[i] = math.Sqrt(tmp)
		if st.prop[i] > maxSum {
			maxSum = st.prop[i]
		}
	}
	for i := 0; i < m; i++ {
		st.prop[i] += .1 * maxSum
		propSum += st.prop[i]
	}
	for i := 0; i < m; i++ {
		st.prop[i] = .99 * st.prop[i] / propSum
	}
}

func word2int(x float64) int16 {
	switch {
	case x < -32767.5:
		return -32768
	case x > 32766.5:
		return 32767
	default:
		return int16(math.Floor(.5 + x))
	}
}

func (st *EchoCanceller) cancel(in []int16, farEnd []float64, out []int16) {
	N, M, fs := st.windowSize, st.m, st.frameSize
	st.cancelCount++
	ss := .35 / float64(M)
	ss1 := 1 - ss

	// DC notch filter + pre-emphasis on the near end
	r := st.notchRadius
	den2 := r*r + .7*(1-r)*(1-r)
	for i := 0; i < fs; i++ {
		vin := float64(in[i])
		vout := st.notchMem[0] + vin
		st.notchMem[0] = st.notchMem[1] + 2*(-vin+r*vout)
		st.notchMem[1] = vin - den2*vout
		v := r * vout
		if v > 32767 {
			v = 32767
		} else if v < -32767 {
			v = -32767
		}
		tmp := v - st.preemph*st.memD
		st.memD = v
		st.input[i] = tmp
	}

	// Far end pre-emphasis and sliding window
	for i := 0; i < fs; i++ {
		st.x[i] = st.x[i+fs]
		st.x[i+fs] = farEnd[i] - st.preemph*st.memX
		st.memX = farEnd[i]
	}

	// Shift the far-end spectra history and transform the new window
	for j := M - 1; j >= 0; j-- {
		copy(st.X[(j+1)*N:(j+2)*N], st.X[j*N:(j+1)*N])
	}
	st.fft.Forward(st.x, st.X[:N])

	sxx := innerProd(st.x[fs:], st.x[fs:])
	powerSpectrumAccum(st.X, st.xf, N)

	// Foreground filter output
	spectralMulAccum(st.X, st.foreground, st.Y, N, M)
	st.fft.Backward(st.Y, st.e)
	for i := 0; i < fs; i++ {
		st.e[i] = st.input[i] - st.e[i+fs]
	}
	sff := innerProd(st.e[:fs], st.e[:fs])

	if st.adapted {
		st.adjustProp()
	}
	if st.saturated == 0 {
		for j := M - 1; j >= 0; j-- {
			weightedSpectralMulConj(st.power1, st.prop[j], st.X[(j+1)*N:], st.E, st.phi, N)
			w := st.W[j*N : (j+1)*N]
			for i := 0; i < N; i++ {
				w[i] += st.phi[i]
			}
		}
	} else {
		st.saturated--
	}

	// Keep the filter length constrained to the frame (weight normalization)
	for j := 0; j < M; j++ {
		if j == 0 || st.cancelCount%(M-1) == j-1 {
			w := st.W[j*N : (j+1)*N]
			st.fft.Backward(w, st.wtmp)
			for i := fs; i < N; i++ {
				st.wtmp[i] = 0
			}
			st.fft.Forward(st.wtmp, w)
		}
	}

	for i := 0; i <= fs; i++ {
		st.rf[i], st.yf[i], st.xf[i] = 0, 0, 0
	}

	// Background filter output
	spectralMulAccum(st.X, st.W, st.Y, N, M)
	st.fft.Backward(st.Y, st.y)
	for i := 0; i < fs; i++ {
		st.e[i] = st.e[i+fs] - st.y[i+fs]
	}
	dbf := 10 + innerProd(st.e[:fs], st.e[:fs])
	for i := 0; i < fs; i++ {
		st.e[i] = st.input[i] - st.y[i+fs]
	}
	see := innerProd(st.e[:fs], st.e[:fs])

	// Two-path logic: decide whether to update the foreground or backtrack the background
	st.davg1 = .6*st.davg1 + .4*(sff-see)
	st.davg2 = .85*st.davg2 + .15*(sff-see)
	st.dvar1 = aecVar1Smooth*st.dvar1 + .16*sff*dbf
	st.dvar2 = aecVar2Smooth*st.dvar2 + .0225*sff*dbf

	updateForeground := (sff-see)*math.Abs(sff-see) > sff*dbf ||
		st.davg1*math.Abs(st.davg1) > aecVar1Update*st.dvar1 ||
		st.davg2*math.Abs(st.davg2) > aecVar2Update*st.dvar2

	if updateForeground {
		st.davg1, st.davg2, st.dvar1, st.dvar2 = 0, 0, 0, 0
		copy(st.foreground, st.W)
		for i := 0; i < fs; i++ {
			st.e[i+fs] = st.window[i+fs]*st.e[i+fs] + st.window[i]*st.y[i+fs]
		}
	} else {
		resetBackground := -(sff-see)*math.Abs(sff-see) > aecVarBacktrack*sff*dbf ||
			-st.davg1*math.Abs(st.davg1) > aecVarBacktrack*st.dvar1 ||
			-st.davg2*math.Abs(st.davg2) > aecVarBacktrack*st.dvar2
		if resetBackground {
			copy(st.W, st.foreground)
			for i := 0; i < fs; i++ {
				st.y[i+fs] = st.e[i+fs]
			}
			for i := 0; i < fs; i++ {
				st.e[i] = st.input[i] - st.y[i+fs]
			}
			see = sff
			st.davg1, st.davg2, st.dvar1, st.dvar2 = 0, 0, 0, 0
		}
	}

	// Output with de-emphasis
	for i := 0; i < fs; i++ {
		tmpOut := st.input[i] - st.e[i+fs]
		tmpOut += st.preemph * st.memE
		if in[i] <= -32000 || in[i] >= 32000 {
			if st.saturated == 0 {
				st.saturated = 1
			}
		}
		out[i] = word2int(tmpOut)
		st.memE = tmpOut
	}

	for i := 0; i < fs; i++ {
		st.e[i+fs] = st.e[i]
		st.e[i] = 0
	}

	sey := innerProd(st.e[fs:], st.y[fs:])
	syy := innerProd(st.y[fs:], st.y[fs:])
	sdd := innerProd(st.input, st.input)

	st.fft.Forward(st.e, st.E)
	for i := 0; i < fs; i++ {
		st.y[i] = 0
	}
	st.fft.Forward(st.y, st.Y)
	powerSpectrumAccum(st.E, st.rf, N)
	powerSpectrumAccum(st.Y, st.yf, N)

	if !(syy >= 0 && sxx >= 0 && see >= 0) || !(sff < float64(N)*1e9 && syy < float64(N)*1e9 && sxx < float64(N)*1e9) {
		st.screwedUp += 50
		for i := 0; i < fs; i++ {
			out[i] = 0
		}
	} else if sff > sdd+float64(N)*10000 {
		st.screwedUp++
	} else {
		st.screwedUp = 0
	}
	if st.screwedUp >= 50 {
		st.Reset()
		return
	}

	see = math.Max(see, float64(N)*100)

	sxx += innerProd(st.x[fs:], st.x[fs:])
	powerSpectrumAccum(st.X, st.xf, N)

	for j := 0; j <= fs; j++ {
		st.power[j] = ss1*st.power[j] + 1 + ss*st.xf[j]
	}

	pey, pyy := 1.0, 1.0
	for j := fs; j >= 0; j-- {
		eh := st.rf[j] - st.eh[j]
		yh := st.yf[j] - st.yh[j]
		pey += eh * yh
		pyy += yh * yh
		st.eh[j] = (1-st.specAverage)*st.eh[j] + st.specAverage*st.rf[j]
		st.yh[j] = (1-st.specAverage)*st.yh[j] + st.specAverage*st.yf[j]
	}
	pyy = math.Sqrt(pyy)
	pey = pey / pyy

	tmp32 := st.beta0 * syy
	if tmp32 > st.betaMax*see {
		tmp32 = st.betaMax * see
	}
	alpha := tmp32 / see
	alpha1 := 1 - alpha
	st.pey = alpha1*st.pey + alpha*pey
	st.pyy = alpha1*st.pyy + alpha*pyy
	if st.pyy < 1 {
		st.pyy = 1
	}
	if st.pey < aecMinLeak*st.pyy {
		st.pey = aecMinLeak * st.pyy
	}
	if st.pey > st.pyy {
		st.pey = st.pyy
	}
	st.leak = st.pey / st.pyy

	rer := (.0001*sxx + 3.*st.leak*syy) / see
	if bound := sey * sey / (1 + see*syy); rer < bound {
		rer = bound
	}
	if rer > .5 {
		rer = .5
	}

	if !st.adapted && st.sumAdapt > float64(M) && st.leak*syy > .03*syy {
		st.adapted = true
	}

	if st.adapted {
		for i := 0; i <= fs; i++ {
			r := st.leak * st.yf[i]
			e := st.rf[i] + 1
			if r > .5*e {
				r = .5 * e
			}
			r = .7*r + .3*(rer*e)
			st.power1[i] = r / (e * (st.power[i] + 10))
		}
	} else {
		adaptRate := 0.0
		if sxx > float64(N)*1000 {
			tmp := .25 * sxx
			if tmp > .25*see {
				tmp = .25 * see
			}
			adaptRate = tmp / see
		}
		for i := 0; i <= fs; i++ {
			st.power1[i] = adaptRate / (st.power[i] + 10)
		}
		st.sumAdapt += adaptRate
	}

	copy(st.lastY[:fs], st.lastY[fs:])
	if st.adapted {
		for i := 0; i < fs; i++ {
			st.lastY[fs+i] = float64(in[i]) - float64(out[i])
		}
	}
}

// Residual estimates the residual echo power spectrum (frameSize+1 bins) of the last frame,
// for use by a post-filter.
func (st *EchoCanceller) Residual(residual []float64) {
	N := st.windowSize
	for i := 0; i < N; i++ {
		st.y[i] = st.window[i] * st.lastY[i]
	}
	st.fft.Forward(st.y, st.Y)
	powerSpectrum(st.Y, residual, N)
	leak2 := 2 * st.leak
	if st.leak > .5 {
		leak2 = 1
	}
	for i := 0; i <= st.frameSize; i++ {
		residual[i] *= leak2
	}
}

// Adapted reports whether the filter has converged at least once.
func (st *EchoCanceller) Adapted() bool { return st.adapted }

// LeakEstimate returns the current echo leak estimate (0..1).
func (st *EchoCanceller) LeakEstimate() float64 { return st.leak }
