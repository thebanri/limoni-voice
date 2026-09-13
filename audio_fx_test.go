package main

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

// keyboardFrames renders broadband 48 kHz mechanical key presses (bright click + low thock)
// every 170 ms over a quiet room floor.
func keyboardFrames(frames int, amp float64, seed int64) [][]byte {
	r := rand.New(rand.NewSource(seed))
	period := int(0.17 * AudioSampleRate)
	var hpIn, hpOut float64
	out := make([][]byte, frames)
	for f := range out {
		pcm := make([]byte, AudioChunkSize)
		for i := 0; i < AudioFrameSamples; i++ {
			n := f*AudioFrameSamples + i
			tk := float64(n%period) / AudioSampleRate
			noise := r.Float64()*2 - 1
			hp := 0.72 * (hpOut + noise - hpIn) // ~2.5 kHz one-pole high-pass
			hpIn, hpOut = noise, hp
			s := 10 * (r.Float64()*2 - 1)
			if tk < 0.02 {
				s += amp * hp * math.Exp(-tk/0.0015)
			}
			if tk >= 0.006 && tk < 0.04 {
				s += amp * 0.7 * math.Sin(2*math.Pi*400*(tk-0.006)) * math.Exp(-(tk-0.006)/0.005)
			}
			binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(s)))
		}
		out[f] = pcm
	}
	return out
}

func voicedFrames(frames int, amp float64) [][]byte { return pulsyVoice(frames, amp, 140, 12) }

// pulsyVoice renders a voiced source with f0 Hz and harmonics partials (1/h roll-off); many
// partials at a low pitch give strongly impulsive glottal pulses.
func pulsyVoice(frames int, amp, f0 float64, harmonics int) [][]byte {
	out := make([][]byte, frames)
	phase := 0.0
	for f := range out {
		pcm := make([]byte, AudioChunkSize)
		for i := 0; i < AudioFrameSamples; i++ {
			t := float64(f*AudioFrameSamples+i) / AudioSampleRate
			phase += (f0 + 0.07*f0*math.Sin(2*math.Pi*3*t)) / AudioSampleRate
			var s float64
			for h := 1; h <= harmonics; h++ {
				s += math.Sin(2*math.Pi*float64(h)*phase) / float64(h)
			}
			binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(amp*s)))
		}
		out[f] = pcm
	}
	return out
}

func captureChain(t *testing.T, audio *AudioEngine, frames [][]byte) (speakingFrames int, outRMS float64) {
	t.Helper()
	var sum float64
	var n int
	audio.onFrame = func(rms float64, speaking bool, pcm []byte) {
		if speaking {
			speakingFrames++
		}
		r := calculateRMS(pcm)
		sum += r * r
		n++
	}
	frame := make([]int16, AudioFrameSamples)
	for _, pcm := range frames {
		for i := range frame {
			frame[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
		}
		audio.processCaptureFrame(frame)
	}
	return speakingFrames, math.Sqrt(sum / float64(max(n, 1)))
}

func TestCaptureChainRejectsKeyboardTyping(t *testing.T) {
	keys := keyboardFrames(150, 9000, 7) // 3 s of typing, clicks around −12 dBFS peak
	for _, mode := range []int{SuppressionStandard, SuppressionHigh} {
		audio := NewAudioEngine()
		audio.EchoCancellation = false
		audio.SetSuppressionMode(mode)
		speaking, rms := captureChain(t, audio, keys)
		inRMS := 0.0
		for _, k := range keys {
			inRMS += calculateRMS(k) * calculateRMS(k)
		}
		inRMS = math.Sqrt(inRMS / float64(len(keys)))
		t.Logf("mode %d: speaking frames %d/150, output %.1f dBFS (input %.1f dBFS)", mode, speaking, 20*math.Log10(rms+1e-9), 20*math.Log10(inRMS))
		if speaking > 8 {
			t.Errorf("mode %d: typing opened the voice gate in %d frames", mode, speaking)
		}
		if rms > inRMS*0.1 {
			t.Errorf("mode %d: typing output %.4f, want ≤ 10%% of input %.4f", mode, rms, inRMS)
		}
	}
}

func TestCaptureChainKeepsSpeechAndSmooths(t *testing.T) {
	speech := voicedFrames(100, 6000)
	for _, smoothing := range []bool{false, true} {
		audio := NewAudioEngine()
		audio.EchoCancellation = false
		audio.VoiceSmoothing = smoothing
		speaking, rms := captureChain(t, audio, speech)
		t.Logf("smoothing=%v: speaking %d/100, output %.1f dBFS", smoothing, speaking, 20*math.Log10(rms+1e-9))
		if speaking < 90 || rms < 0.03 {
			t.Fatalf("smoothing=%v: speech lost (speaking %d, rms %.4f)", smoothing, speaking, rms)
		}
	}
}

func TestCaptureChainLowPulsyVoicesAndTypingWhileTalking(t *testing.T) {
	for _, v := range []struct{ f0 float64 }{{85}, {110}, {220}} {
		audio := NewAudioEngine()
		audio.EchoCancellation = false
		speaking, rms := captureChain(t, audio, pulsyVoice(100, 5000, v.f0, 60))
		t.Logf("f0 %.0f Hz (60 partials): speaking %d/100, output %.1f dBFS", v.f0, speaking, 20*math.Log10(rms+1e-9))
		if speaking < 90 {
			t.Errorf("f0 %.0f Hz voice gated: speaking %d/100", v.f0, speaking)
		}
	}

	voiceFrames := voicedFrames(150, 5000)
	keys := keyboardFrames(150, 6000, 9)
	mixed := make([][]byte, 150)
	for f := range mixed {
		pcm := make([]byte, AudioChunkSize)
		for i := 0; i < AudioFrameSamples; i++ {
			a := float64(int16(binary.LittleEndian.Uint16(voiceFrames[f][2*i:])))
			b := float64(int16(binary.LittleEndian.Uint16(keys[f][2*i:])))
			binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(math.Max(-32768, math.Min(32767, a+b)))))
		}
		mixed[f] = pcm
	}
	audio := NewAudioEngine()
	audio.EchoCancellation = false
	speaking, _ := captureChain(t, audio, mixed)
	t.Logf("typing while talking: speaking %d/150", speaking)
	if speaking < 140 {
		t.Fatalf("typing interrupted speech: speaking %d/150", speaking)
	}
}

func TestReceiveLevelerIsGentle(t *testing.T) {
	level := 1.0
	for i := 0; i < 500; i++ {
		level = nextLevel(level, 0.004) // background noise between words
	}
	if level != 1 {
		t.Fatalf("leveler moved on noise: %.3f", level)
	}
	for i := 0; i < 500; i++ {
		level = nextLevel(level, 0.02) // quiet talker
	}
	if level > levelMaxGain+1e-9 || level < 1.3 {
		t.Fatalf("quiet talker gain %.2f, want ~%.1f", level, levelMaxGain)
	}
	for i := 0; i < 500; i++ {
		level = nextLevel(level, 0.3) // very loud talker
	}
	if level > 0.62 {
		t.Fatalf("loud talker gain %.2f, want ~%.1f", level, levelMinGain)
	}
}

func TestCaptureChainRejectsClaps(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	period := int(0.4 * AudioSampleRate)
	frames := make([][]byte, 150)
	for f := range frames {
		pcm := make([]byte, AudioChunkSize)
		for i := 0; i < AudioFrameSamples; i++ {
			tk := float64((f*AudioFrameSamples+i)%period) / AudioSampleRate
			s := 10 * (r.Float64()*2 - 1)
			if tk < 0.2 {
				s += 22000 * (r.Float64()*2 - 1) * (math.Exp(-tk/0.005) + 0.05*math.Exp(-tk/0.05))
			}
			binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(math.Max(-32768, math.Min(32767, s)))))
		}
		frames[f] = pcm
	}
	audio := NewAudioEngine()
	audio.EchoCancellation = false
	speaking, rms := captureChain(t, audio, frames)
	t.Logf("claps: speaking %d/150, output %.1f dBFS", speaking, 20*math.Log10(rms+1e-9))
	if speaking > 8 {
		t.Fatalf("claps opened the voice gate in %d frames", speaking)
	}
}
