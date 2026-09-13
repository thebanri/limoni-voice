package rnnoise

import (
	"bufio"
	"encoding/binary"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestAgainstCOracle(t *testing.T) {
	dir := os.Getenv("RNNOISE_ORACLE_DIR")
	if dir == "" {
		t.Skip("RNNOISE_ORACLE_DIR not set")
	}
	inRaw, err1 := os.ReadFile(dir + "/rnn_in.raw")
	outRaw, err2 := os.ReadFile(dir + "/rnn_out.raw")
	vadFile, err3 := os.Open(dir + "/rnn_vad.txt")
	if err1 != nil || err2 != nil || err3 != nil {
		t.Skip("oracle files missing")
	}
	defer vadFile.Close()
	var vads []float64
	sc := bufio.NewScanner(vadFile)
	for sc.Scan() {
		v, _ := strconv.ParseFloat(strings.TrimSpace(sc.Text()), 64)
		vads = append(vads, v)
	}
	st := New()
	in := make([]float32, FrameSize)
	out := make([]float32, FrameSize)
	var diff, ref, maxVadDiff float64
	for f := 0; f*FrameSize*2 < len(inRaw); f++ {
		for i := 0; i < FrameSize; i++ {
			in[i] = float32(int16(binary.LittleEndian.Uint16(inRaw[2*(f*FrameSize+i):])))
		}
		vad := st.ProcessFrame(out, in)
		maxVadDiff = math.Max(maxVadDiff, math.Abs(float64(vad)-vads[f]))
		for i := 0; i < FrameSize; i++ {
			want := float64(int16(binary.LittleEndian.Uint16(outRaw[2*(f*FrameSize+i):])))
			got := math.Floor(math.Max(-32768, math.Min(32767, float64(out[i]))) + .5)
			diff += (got - want) * (got - want)
			ref += want * want
		}
	}
	snr := 10 * math.Log10(ref/(diff+1e-9))
	t.Logf("output SNR vs C oracle %.1f dB, max VAD difference %.4f", snr, maxVadDiff)
	if snr < 40 || maxVadDiff > 0.02 {
		t.Fatalf("port diverges from reference (SNR %.1f dB, VAD diff %.4f)", snr, maxVadDiff)
	}
}

func synthFrame(f int, voiced bool, seed *uint32, ph *float64) []float32 {
	out := make([]float32, FrameSize)
	for i := range out {
		tm := float64(f*FrameSize+i) / 48000
		*ph += 2 * math.Pi * (140 + 30*math.Sin(tm*3)) / 48000
		v := 0.0
		if voiced {
			v = 5000 * (math.Sin(*ph) + .6*math.Sin(2**ph) + .4*math.Sin(3**ph) + .2*math.Sin(5**ph)) * (.6 + .4*math.Sin(tm*9))
		}
		*seed = *seed*1664525 + 1013904223
		n := 1500*(float64(*seed>>8)/float64(1<<24)-.5) + 800*math.Sin(2*math.Pi*60*tm)
		out[i] = float32(int16(v + n))
	}
	return out
}

// TestSuppressesNoiseKeepsSpeech checks the behaviour on synthetic noisy "speech".
func TestSuppressesNoiseKeepsSpeech(t *testing.T) {
	st := New()
	seed := uint32(1)
	ph := 0.0
	out := make([]float32, FrameSize)
	var noiseIn, noiseOut, speechIn, speechOut float64
	var vadNoise, vadSpeech float64
	var nNoise, nSpeech int
	for f := 0; f < 400; f++ {
		voiced := f%50 < 30 && f > 40
		in := synthFrame(f, voiced, &seed, &ph)
		vad := st.ProcessFrame(out, in)
		if f < 100 {
			continue // let the recurrent state settle
		}
		var ein, eout float64
		for i := range in {
			ein += float64(in[i]) * float64(in[i])
			eout += float64(out[i]) * float64(out[i])
		}
		if voiced && f%50 > 3 {
			speechIn += ein
			speechOut += eout
			vadSpeech += float64(vad)
			nSpeech++
		} else if !voiced && f%50 > 33 {
			noiseIn += ein
			noiseOut += eout
			vadNoise += float64(vad)
			nNoise++
		}
	}
	noiseReduction := 10 * math.Log10(noiseIn/noiseOut)
	speechLoss := 10 * math.Log10(speechIn/speechOut)
	t.Logf("noise reduction %.1f dB, speech loss %.1f dB, VAD speech %.2f noise %.2f", noiseReduction, speechLoss, vadSpeech/float64(nSpeech), vadNoise/float64(nNoise))
	if noiseReduction < 10 {
		t.Fatalf("noise only reduced by %.1f dB", noiseReduction)
	}
	if speechLoss > 6 {
		t.Fatalf("speech attenuated by %.1f dB", speechLoss)
	}
	if vadSpeech/float64(nSpeech) <= vadNoise/float64(nNoise) {
		t.Fatal("VAD does not separate speech from noise")
	}
}

func BenchmarkProcessFrame(b *testing.B) {
	st := New()
	seed := uint32(3)
	ph := 0.0
	in := synthFrame(0, true, &seed, &ph)
	out := make([]float32, FrameSize)
	for b.Loop() {
		st.ProcessFrame(out, in)
	}
}
