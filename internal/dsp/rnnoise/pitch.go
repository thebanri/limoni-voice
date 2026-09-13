package rnnoise

import "math"

func innerProd(x, y []float32, n int) float32 {
	var xy float32
	for i := 0; i < n; i++ {
		xy += x[i] * y[i]
	}
	return xy
}

func dualInnerProd(x, y1, y2 []float32, n int) (float32, float32) {
	var a, b float32
	for i := 0; i < n; i++ {
		a += x[i] * y1[i]
		b += x[i] * y2[i]
	}
	return a, b
}

func pitchXcorr(x, y, xcorr []float32, length, maxPitch int) {
	for i := 0; i < maxPitch; i++ {
		xcorr[i] = innerProd(x, y[i:], length)
	}
}

func findBestPitch(xcorr, y []float32, length, maxPitch int, bestPitch *[2]int) {
	syy := float32(1)
	bestNum := [2]float32{-1, -1}
	bestDen := [2]float32{0, 0}
	bestPitch[0], bestPitch[1] = 0, 1
	for j := 0; j < length; j++ {
		syy += y[j] * y[j]
	}
	for i := 0; i < maxPitch; i++ {
		if xcorr[i] > 0 {
			xcorr16 := xcorr[i] * 1e-12
			num := xcorr16 * xcorr16
			if num*bestDen[1] > bestNum[1]*syy {
				if num*bestDen[0] > bestNum[0]*syy {
					bestNum[1], bestDen[1], bestPitch[1] = bestNum[0], bestDen[0], bestPitch[0]
					bestNum[0], bestDen[0], bestPitch[0] = num, syy, i
				} else {
					bestNum[1], bestDen[1], bestPitch[1] = num, syy, i
				}
			}
		}
		syy += y[i+length]*y[i+length] - y[i]*y[i]
		if syy < 1 {
			syy = 1
		}
	}
}

func celtFir5(x, num, y []float32, n int, mem *[5]float32) {
	num0, num1, num2, num3, num4 := num[0], num[1], num[2], num[3], num[4]
	mem0, mem1, mem2, mem3, mem4 := mem[0], mem[1], mem[2], mem[3], mem[4]
	for i := 0; i < n; i++ {
		sum := x[i] + num0*mem0 + num1*mem1 + num2*mem2 + num3*mem3 + num4*mem4
		mem4, mem3, mem2, mem1 = mem3, mem2, mem1, mem0
		mem0 = x[i]
		y[i] = sum
	}
	*mem = [5]float32{mem0, mem1, mem2, mem3, mem4}
}

func celtAutocorr(x []float32, ac []float32, lag, n int) {
	fastN := n - lag
	pitchXcorr(x, x, ac, fastN, lag+1)
	for k := 0; k <= lag; k++ {
		var d float32
		for i := k + fastN; i < n; i++ {
			d += x[i] * x[i-k]
		}
		ac[k] += d
	}
}

func celtLPC(lpc []float32, ac []float32, p int) {
	errv := ac[0]
	for i := range lpc[:p] {
		lpc[i] = 0
	}
	if ac[0] == 0 {
		return
	}
	for i := 0; i < p; i++ {
		var rr float32
		for j := 0; j < i; j++ {
			rr += lpc[j] * ac[i-j]
		}
		rr += ac[i+1]
		r := -rr / errv
		lpc[i] = r
		for j := 0; j < (i+1)>>1; j++ {
			tmp1 := lpc[j]
			tmp2 := lpc[i-1-j]
			lpc[j] = tmp1 + r*tmp2
			lpc[i-1-j] = tmp2 + r*tmp1
		}
		errv = errv - r*r*errv
		if errv < .001*ac[0] {
			break
		}
	}
}

func pitchDownsample(x []float32, xLP []float32, length int) {
	var ac [5]float32
	tmp := float32(1)
	var lpc [4]float32
	var mem [5]float32
	var lpc2 [5]float32
	const c1 = float32(.8)
	for i := 1; i < length>>1; i++ {
		xLP[i] = .5 * (.5*(x[2*i-1]+x[2*i+1]) + x[2*i])
	}
	xLP[0] = .5 * (.5*x[1] + x[0])
	celtAutocorr(xLP, ac[:], 4, length>>1)
	ac[0] *= 1.0001
	for i := 1; i <= 4; i++ {
		f := .008 * float32(i)
		ac[i] -= ac[i] * f * f
	}
	celtLPC(lpc[:], ac[:], 4)
	for i := 0; i < 4; i++ {
		tmp = .9 * tmp
		lpc[i] = lpc[i] * tmp
	}
	lpc2[0] = lpc[0] + .8
	lpc2[1] = lpc[1] + c1*lpc[0]
	lpc2[2] = lpc[2] + c1*lpc[1]
	lpc2[3] = lpc[3] + c1*lpc[2]
	lpc2[4] = c1 * lpc[3]
	celtFir5(xLP, lpc2[:], xLP, length>>1, &mem)
}

func pitchSearch(xLP, y []float32, length, maxPitch int) int {
	lag := length + maxPitch
	xLP4 := make([]float32, length>>2)
	yLP4 := make([]float32, lag>>2)
	xcorr := make([]float32, maxPitch>>1)
	var bestPitch [2]int

	for j := 0; j < length>>2; j++ {
		xLP4[j] = xLP[2*j]
	}
	for j := 0; j < lag>>2; j++ {
		yLP4[j] = y[2*j]
	}
	pitchXcorr(xLP4, yLP4, xcorr, length>>2, maxPitch>>2)
	findBestPitch(xcorr, yLP4, length>>2, maxPitch>>2, &bestPitch)

	for i := 0; i < maxPitch>>1; i++ {
		xcorr[i] = 0
		if absInt(i-2*bestPitch[0]) > 2 && absInt(i-2*bestPitch[1]) > 2 {
			continue
		}
		sum := innerProd(xLP, y[i:], length>>1)
		if sum < -1 {
			sum = -1
		}
		xcorr[i] = sum
	}
	findBestPitch(xcorr, y, length>>1, maxPitch>>1, &bestPitch)

	offset := 0
	if bestPitch[0] > 0 && bestPitch[0] < (maxPitch>>1)-1 {
		a := xcorr[bestPitch[0]-1]
		b := xcorr[bestPitch[0]]
		c := xcorr[bestPitch[0]+1]
		if (c - a) > .7*(b-a) {
			offset = 1
		} else if (a - c) > .7*(b-c) {
			offset = -1
		}
	}
	return 2*bestPitch[0] - offset
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func computePitchGain(xy, xx, yy float32) float32 {
	return float32(float64(xy) / math.Sqrt(1+float64(xx)*float64(yy)))
}

var secondCheck = [16]int{0, 0, 3, 2, 3, 2, 5, 2, 3, 2, 3, 2, 5, 2, 3, 2}

// removeDoubling refines the pitch period. x holds the downsampled pitch buffer; the analysed
// frame starts at x[maxPeriod/2].
func removeDoubling(xBuf []float32, maxPeriod, minPeriod, n int, t0 *int, prevPeriod int, prevGain float32) float32 {
	minPeriod0 := minPeriod
	maxPeriod /= 2
	minPeriod /= 2
	*t0 /= 2
	prevPeriod /= 2
	n /= 2
	base := maxPeriod
	x := func(i int) float32 { return xBuf[base+i] }
	if *t0 >= maxPeriod {
		*t0 = maxPeriod - 1
	}
	T0 := *t0
	T := T0
	yyLookup := make([]float32, maxPeriod+1)
	xx, xy := dualInnerProd(xBuf[base:], xBuf[base:], xBuf[base-T0:], n)
	yyLookup[0] = xx
	yy := xx
	for i := 1; i <= maxPeriod; i++ {
		yy = yy + x(-i)*x(-i) - x(n-i)*x(n-i)
		if yy < 0 {
			yyLookup[i] = 0
		} else {
			yyLookup[i] = yy
		}
	}
	yy = yyLookup[T0]
	bestXY := xy
	bestYY := yy
	g0 := computePitchGain(xy, xx, yy)
	g := g0
	for k := 2; k <= 15; k++ {
		T1 := (2*T0 + k) / (2 * k)
		if T1 < minPeriod {
			break
		}
		var T1b int
		if k == 2 {
			if T1+T0 > maxPeriod {
				T1b = T0
			} else {
				T1b = T0 + T1
			}
		} else {
			T1b = (2*secondCheck[k]*T0 + k) / (2 * k)
		}
		xy1, xy2 := dualInnerProd(xBuf[base:], xBuf[base-T1:], xBuf[base-T1b:], n)
		xyk := .5 * (xy1 + xy2)
		yyk := .5 * (yyLookup[T1] + yyLookup[T1b])
		g1 := computePitchGain(xyk, xx, yyk)
		var cont float32
		if absInt(T1-prevPeriod) <= 1 {
			cont = prevGain
		} else if absInt(T1-prevPeriod) <= 2 && 5*k*k < T0 {
			cont = .5 * prevGain
		}
		thresh := max(float32(.3), .7*g0-cont)
		if T1 < 3*minPeriod {
			thresh = max(float32(.4), .85*g0-cont)
		} else if T1 < 2*minPeriod {
			thresh = max(float32(.5), .9*g0-cont)
		}
		if g1 > thresh {
			bestXY, bestYY, T, g = xyk, yyk, T1, g1
		}
	}
	if bestXY < 0 {
		bestXY = 0
	}
	var pg float32
	if bestYY <= bestXY {
		pg = 1
	} else {
		pg = bestXY / (bestYY + 1)
	}
	var xcorr [3]float32
	for k := 0; k < 3; k++ {
		xcorr[k] = innerProd(xBuf[base:], xBuf[base-(T+k-1):], n)
	}
	offset := 0
	if (xcorr[2] - xcorr[0]) > .7*(xcorr[1]-xcorr[0]) {
		offset = 1
	} else if (xcorr[0] - xcorr[2]) > .7*(xcorr[1]-xcorr[2]) {
		offset = -1
	}
	if pg > g {
		pg = g
	}
	*t0 = 2*T + offset
	if *t0 < minPeriod0 {
		*t0 = minPeriod0
	}
	return pg
}
