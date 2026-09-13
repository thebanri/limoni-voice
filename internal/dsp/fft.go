// Package dsp holds the signal processing primitives of the voice pipeline: an arbitrary
// size FFT (kiss_fft algorithm), the Speex MDF acoustic echo canceller port and small
// filter helpers.
package dsp

import "math"

// FFT is a complex FFT for arbitrary sizes (radix 4, 2, 3, 5 and generic factors).
// Forward computes X[k] = sum_j x[j]·exp(-2πi·k·j/n) without normalization.
type FFT struct {
	n       int
	factors []int
	twr     []float64
	twi     []float64
	scratch []float64 // generic radix
}

// NewFFT prepares an FFT of size n (n >= 1).
func NewFFT(n int) *FFT {
	f := &FFT{n: n, twr: make([]float64, n), twi: make([]float64, n)}
	for i := 0; i < n; i++ {
		phase := -2 * math.Pi * float64(i) / float64(n)
		f.twr[i] = math.Cos(phase)
		f.twi[i] = math.Sin(phase)
	}
	f.factors = factorize(n)
	maxP := 0
	for i := 0; i < len(f.factors); i += 2 {
		maxP = max(maxP, f.factors[i])
	}
	f.scratch = make([]float64, 2*maxP)
	return f
}

// Size returns n.
func (f *FFT) Size() int { return f.n }

func factorize(n int) []int {
	var facbuf []int
	p := 4
	floorSqrt := int(math.Floor(math.Sqrt(float64(n))))
	for n > 1 {
		for n%p != 0 {
			switch p {
			case 4:
				p = 2
			case 2:
				p = 3
			default:
				p += 2
			}
			if p > floorSqrt {
				p = n
			}
		}
		n /= p
		facbuf = append(facbuf, p, n)
	}
	return facbuf
}

// Transform computes the forward FFT of (inRe, inIm) into (outRe, outIm). Inputs and outputs
// must not alias and must have length n.
func (f *FFT) Transform(inRe, inIm, outRe, outIm []float64) {
	if f.n == 1 {
		outRe[0], outIm[0] = inRe[0], inIm[0]
		return
	}
	f.work(outRe, outIm, 0, inRe, inIm, 0, 1, 1, f.factors)
}

func (f *FFT) work(outRe, outIm []float64, out int, inRe, inIm []float64, in int, fstride, istride int, factors []int) {
	p := factors[0]
	m := factors[1]
	if m == 1 {
		for k := 0; k < p; k++ {
			outRe[out+k] = inRe[in+k*fstride*istride]
			outIm[out+k] = inIm[in+k*fstride*istride]
		}
	} else {
		for k := 0; k < p; k++ {
			f.work(outRe, outIm, out+k*m, inRe, inIm, in+k*fstride*istride, fstride*p, istride, factors[2:])
		}
	}
	switch p {
	case 2:
		f.bfly2(outRe, outIm, out, fstride, m)
	case 3:
		f.bfly3(outRe, outIm, out, fstride, m)
	case 4:
		f.bfly4(outRe, outIm, out, fstride, m)
	case 5:
		f.bfly5(outRe, outIm, out, fstride, m)
	default:
		f.bflyGeneric(outRe, outIm, out, fstride, m, p)
	}
}

func (f *FFT) bfly2(re, im []float64, o, fstride, m int) {
	for k := 0; k < m; k++ {
		tw := k * fstride
		a, b := o+k, o+k+m
		tr := re[b]*f.twr[tw] - im[b]*f.twi[tw]
		ti := re[b]*f.twi[tw] + im[b]*f.twr[tw]
		re[b], im[b] = re[a]-tr, im[a]-ti
		re[a] += tr
		im[a] += ti
	}
}

func (f *FFT) bfly4(re, im []float64, o, fstride, m int) {
	m2, m3 := 2*m, 3*m
	for k := 0; k < m; k++ {
		t1, t2, t3 := k*fstride, k*2*fstride, k*3*fstride
		i0, i1, i2, i3 := o+k, o+k+m, o+k+m2, o+k+m3

		c2r := re[i2]*f.twr[t2] - im[i2]*f.twi[t2]
		c2i := re[i2]*f.twi[t2] + im[i2]*f.twr[t2]
		c1r := re[i1]*f.twr[t1] - im[i1]*f.twi[t1]
		c1i := re[i1]*f.twi[t1] + im[i1]*f.twr[t1]
		c3r := re[i3]*f.twr[t3] - im[i3]*f.twi[t3]
		c3i := re[i3]*f.twi[t3] + im[i3]*f.twr[t3]

		c0r, c0i := re[i0]-c2r, im[i0]-c2i
		re[i0] += c2r
		im[i0] += c2i
		sr, si := c1r+c3r, c1i+c3i
		dr, di := c1r-c3r, c1i-c3i
		re[i2], im[i2] = re[i0]-sr, im[i0]-si
		re[i0] += sr
		im[i0] += si
		re[i1], im[i1] = c0r+di, c0i-dr
		re[i3], im[i3] = c0r-di, c0i+dr
	}
}

func (f *FFT) bfly3(re, im []float64, o, fstride, m int) {
	m2 := 2 * m
	epr, epi := f.twr[fstride*m], f.twi[fstride*m]
	for k := 0; k < m; k++ {
		t1, t2 := k*fstride, k*2*fstride
		i0, i1, i2 := o+k, o+k+m, o+k+m2

		c1r := re[i1]*f.twr[t1] - im[i1]*f.twi[t1]
		c1i := re[i1]*f.twi[t1] + im[i1]*f.twr[t1]
		c2r := re[i2]*f.twr[t2] - im[i2]*f.twi[t2]
		c2i := re[i2]*f.twi[t2] + im[i2]*f.twr[t2]

		s3r, s3i := c1r+c2r, c1i+c2i
		s0r, s0i := c1r-c2r, c1i-c2i

		re[i1] = re[i0] - s3r*0.5
		im[i1] = im[i0] - s3i*0.5
		s0r, s0i = s0r*epi, s0i*epi

		re[i0] += s3r
		im[i0] += s3i

		re[i2] = re[i1] + s0i
		im[i2] = im[i1] - s0r

		re[i1] -= s0i
		im[i1] += s0r
		_ = epr
	}
}

func (f *FFT) bfly5(re, im []float64, o, fstride, m int) {
	yar, yai := f.twr[fstride*m], f.twi[fstride*m]
	ybr, ybi := f.twr[fstride*2*m], f.twi[fstride*2*m]
	for u := 0; u < m; u++ {
		i0, i1, i2, i3, i4 := o+u, o+u+m, o+u+2*m, o+u+3*m, o+u+4*m
		t1, t2, t3, t4 := u*fstride, 2*u*fstride, 3*u*fstride, 4*u*fstride

		s0r, s0i := re[i0], im[i0]
		s1r := re[i1]*f.twr[t1] - im[i1]*f.twi[t1]
		s1i := re[i1]*f.twi[t1] + im[i1]*f.twr[t1]
		s2r := re[i2]*f.twr[t2] - im[i2]*f.twi[t2]
		s2i := re[i2]*f.twi[t2] + im[i2]*f.twr[t2]
		s3r := re[i3]*f.twr[t3] - im[i3]*f.twi[t3]
		s3i := re[i3]*f.twi[t3] + im[i3]*f.twr[t3]
		s4r := re[i4]*f.twr[t4] - im[i4]*f.twi[t4]
		s4i := re[i4]*f.twi[t4] + im[i4]*f.twr[t4]

		s7r, s7i := s1r+s4r, s1i+s4i
		s10r, s10i := s1r-s4r, s1i-s4i
		s8r, s8i := s2r+s3r, s2i+s3i
		s9r, s9i := s2r-s3r, s2i-s3i

		re[i0] = s0r + s7r + s8r
		im[i0] = s0i + s7i + s8i

		s5r := s0r + (s7r*yar + s8r*ybr)
		s5i := s0i + (s7i*yar + s8i*ybr)
		s6r := s10i*yai + s9i*ybi
		s6i := -(s10r*yai + s9r*ybi)

		re[i1], im[i1] = s5r-s6r, s5i-s6i
		re[i4], im[i4] = s5r+s6r, s5i+s6i

		s5r = s0r + (s7r*ybr + s8r*yar)
		s5i = s0i + (s7i*ybr + s8i*yar)
		s6r = s9i*yai - s10i*ybi
		s6i = s10r*ybi - s9r*yai

		re[i2], im[i2] = s5r+s6r, s5i+s6i
		re[i3], im[i3] = s5r-s6r, s5i-s6i
	}
}

func (f *FFT) bflyGeneric(re, im []float64, o, fstride, m, p int) {
	sr := f.scratch[:p]
	si := f.scratch[p : 2*p]
	norig := f.n
	for u := 0; u < m; u++ {
		k := o + u
		for q1 := 0; q1 < p; q1++ {
			sr[q1], si[q1] = re[k+q1*m], im[k+q1*m]
		}
		for q1 := 0; q1 < p; q1++ {
			// sum_q s[q] * tw[(fstride*(q1*m+u)*q) mod n]
			var tr, ti float64
			for q := 0; q < p; q++ {
				idx := (fstride * (q1*m + u) * q) % norig
				tr += sr[q]*f.twr[idx] - si[q]*f.twi[idx]
				ti += sr[q]*f.twi[idx] + si[q]*f.twr[idx]
			}
			re[k+q1*m], im[k+q1*m] = tr, ti
		}
	}
}

// RealFFT implements the Speex "spx_fft" pair on a packed half-complex representation:
// packed[0]=Re X0, packed[2k-1]=Re Xk, packed[2k]=Im Xk, packed[n-1]=Re X(n/2) (n even).
type RealFFT struct {
	fft                *FFT
	inRe, inIm, re, im []float64
}

// NewRealFFT creates a packed real FFT of even size n.
func NewRealFFT(n int) *RealFFT {
	return &RealFFT{fft: NewFFT(n), inRe: make([]float64, n), inIm: make([]float64, n), re: make([]float64, n), im: make([]float64, n)}
}

// Forward computes spx_fft: out = packed(FFT(in)/n).
func (r *RealFFT) Forward(in, out []float64) {
	n := r.fft.n
	scale := 1.0 / float64(n)
	for i := 0; i < n; i++ {
		r.inRe[i] = in[i] * scale
		r.inIm[i] = 0
	}
	r.fft.Transform(r.inRe, r.inIm, r.re, r.im)
	out[0] = r.re[0]
	for k := 1; k < n/2; k++ {
		out[2*k-1] = r.re[k]
		out[2*k] = r.im[k]
	}
	out[n-1] = r.re[n/2]
}

// Backward computes spx_ifft: synthesis without normalization (Backward(Forward(x)) == x).
func (r *RealFFT) Backward(in, out []float64) {
	n := r.fft.n
	// Build the full spectrum with conjugate symmetry, then use the forward kernel on the
	// time-reversed spectrum: sum_k X_k e^{+iθ} = FFT_forward(X)[n - j].
	r.inRe[0], r.inIm[0] = in[0], 0
	for k := 1; k < n/2; k++ {
		r.inRe[k], r.inIm[k] = in[2*k-1], in[2*k]
		r.inRe[n-k], r.inIm[n-k] = in[2*k-1], -in[2*k]
	}
	r.inRe[n/2], r.inIm[n/2] = in[n-1], 0
	r.fft.Transform(r.inRe, r.inIm, r.re, r.im)
	out[0] = r.re[0]
	for j := 1; j < n; j++ {
		out[j] = r.re[n-j]
	}
}
