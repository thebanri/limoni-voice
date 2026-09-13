package dsp

import (
	"math"
	"math/rand"
	"testing"
)

func naiveFFT(re, im []float64) ([]float64, []float64) {
	n := len(re)
	outRe, outIm := make([]float64, n), make([]float64, n)
	for k := 0; k < n; k++ {
		for j := 0; j < n; j++ {
			ph := -2 * math.Pi * float64(k*j) / float64(n)
			c, s := math.Cos(ph), math.Sin(ph)
			outRe[k] += re[j]*c - im[j]*s
			outIm[k] += re[j]*s + im[j]*c
		}
	}
	return outRe, outIm
}

func TestFFTMatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, n := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 15, 16, 20, 25, 30, 49, 60, 96, 120, 240, 480, 960} {
		re, im := make([]float64, n), make([]float64, n)
		for i := range re {
			re[i], im[i] = rng.NormFloat64(), rng.NormFloat64()
		}
		wantRe, wantIm := naiveFFT(re, im)
		gotRe, gotIm := make([]float64, n), make([]float64, n)
		NewFFT(n).Transform(re, im, gotRe, gotIm)
		for k := 0; k < n; k++ {
			if math.Abs(gotRe[k]-wantRe[k]) > 1e-9*float64(n) || math.Abs(gotIm[k]-wantIm[k]) > 1e-9*float64(n) {
				t.Fatalf("n=%d k=%d: got (%g,%g) want (%g,%g)", n, k, gotRe[k], gotIm[k], wantRe[k], wantIm[k])
			}
		}
	}
}

func TestRealFFTMatchesSpeexConvention(t *testing.T) {
	// Values produced by speexdsp spx_drft_forward for x[i]=sin(.7i)+.3cos(2.1i)+.1i, n=16
	// (the packed output before spx_fft's 1/n scaling).
	want := []float64{13.887974, 1.614240, 5.263246, -4.433188, -3.926917, -0.626536, 0.259701, -0.199990,
		0.628011, 0.568965, 1.927684, -0.771997, -0.730045, -0.478788, -0.167648, -0.433386}
	n := 16
	x := make([]float64, n)
	for i := range x {
		x[i] = math.Sin(0.7*float64(i)) + 0.3*math.Cos(2.1*float64(i)) + 0.1*float64(i)
	}
	r := NewRealFFT(n)
	packed := make([]float64, n)
	r.Forward(x, packed)
	for i := range want {
		if math.Abs(packed[i]*float64(n)-want[i]) > 1e-4 {
			t.Fatalf("packed[%d]=%g want %g", i, packed[i]*float64(n), want[i])
		}
	}
	back := make([]float64, n)
	r.Backward(packed, back)
	for i := range x {
		if math.Abs(back[i]-x[i]) > 1e-9 {
			t.Fatalf("round trip [%d]=%g want %g", i, back[i], x[i])
		}
	}
}

func BenchmarkFFT960(b *testing.B) {
	f := NewFFT(960)
	re, im := make([]float64, 960), make([]float64, 960)
	or, oi := make([]float64, 960), make([]float64, 960)
	for i := range re {
		re[i] = float64(i % 17)
	}
	for b.Loop() {
		f.Transform(re, im, or, oi)
	}
}
