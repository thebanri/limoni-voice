package main

import (
	"encoding/binary"
	"io"
	"math"
	"os/exec"
	"sync"
	"time"

	"github.com/thebanri/limoni/widgets"
)

const (
	AudioSampleRate = 16000
	AudioChannels   = 1
	AudioChunkSize  = 640 // 320 samples @ 16-bit (20ms)
)

// biquad is a persistent-state 2nd-order IIR filter (RBJ Audio EQ Cookbook,
// Direct Form I). Because its state carries over sample-by-sample across
// chunk boundaries, it introduces no block-edge clicking the way a
// window-and-reconstruct (STFT) approach would if used on 20ms frames
// without overlap-add.
type biquad struct {
	b0, b1, b2, a1, a2 float64
	x1, x2, y1, y2     float64
}

func newLowpass(fs, f0, q float64) biquad {
	w0 := 2 * math.Pi * f0 / fs
	alpha := math.Sin(w0) / (2 * q)
	cosw0 := math.Cos(w0)
	a0 := 1 + alpha
	return biquad{
		b0: ((1 - cosw0) / 2) / a0,
		b1: (1 - cosw0) / a0,
		b2: ((1 - cosw0) / 2) / a0,
		a1: (-2 * cosw0) / a0,
		a2: (1 - alpha) / a0,
	}
}

func newHighpass(fs, f0, q float64) biquad {
	w0 := 2 * math.Pi * f0 / fs
	alpha := math.Sin(w0) / (2 * q)
	cosw0 := math.Cos(w0)
	a0 := 1 + alpha
	return biquad{
		b0: ((1 + cosw0) / 2) / a0,
		b1: (-(1 + cosw0)) / a0,
		b2: ((1 + cosw0) / 2) / a0,
		a1: (-2 * cosw0) / a0,
		a2: (1 - alpha) / a0,
	}
}

func (f *biquad) process(x float64) float64 {
	y := f.b0*x + f.b1*f.x1 + f.b2*f.x2 - f.a1*f.y1 - f.a2*f.y2
	f.x2, f.x1 = f.x1, x
	f.y2, f.y1 = f.y1, y
	return y
}

// butterworthQ is the standard maximally-flat Q for a single-stage 2nd order filter.
const butterworthQ = 0.70710678

type AudioEngine struct {
	mu           sync.RWMutex
	Muted        bool
	Deafened     bool
	Loopback     bool // Mic test / echo loopback
	InTestMode   bool // True when test dialog is open
	PrevMuted    bool
	PrevDeafened bool

	// Suppression mode: 0 = KAPALI, 1 = ACIK (Standart), 2 = YUKSEK
	SuppressionMode int
	Gain            float64 // 0.0 to 2.0 (1.0 = 100%)
	GainSliderState *widgets.SliderState
	VADSliderState  *widgets.SliderState
	IsSpeaking      bool
	LocalRMS        float64
	LocalWave       []float64 // Last 40 samples for visualizer
	PeerWaves       map[string][]float64
	VADThreshold    float64

	// 3-band splitter: low (<150Hz rumble/hum), mid (150-3400Hz voice),
	// high (>3400Hz hiss/sibilance). Each has independent adaptive noise
	// floor tracking and gate gain, so a constant hum or fan hiss can be
	// suppressed continuously -- including while you're actively speaking --
	// instead of only during silence.
	lpLow  biquad // low band extractor: LPF @150Hz
	hpMid  biquad // mid band extractor: HPF @150Hz (stage 1)
	lpMid  biquad // mid band extractor: LPF @3400Hz (stage 2)
	hpHigh biquad // high band extractor: HPF @3400Hz

	noiseFloorLow  float64
	noiseFloorMid  float64
	noiseFloorHigh float64

	gateGainLow  float64
	gateGainMid  float64
	gateGainHigh float64

	// DSP filter & VAD state
	hpPrevIn       float64
	hpPrevOut      float64
	speechHangover int // Hangover counter (chunks) to preserve word endings and pauses

	// Live audio capture & playback processes
	captureCmd   *exec.Cmd
	capturePipe  io.ReadCloser
	playbackCmd  *exec.Cmd
	playbackPipe io.WriteCloser

	// Mixing buffer for incoming peer streams
	peerBuffers map[string][]byte
	mixChan     chan []byte
	stopChan    chan struct{}
	running     bool
}

func NewAudioEngine() *AudioEngine {
	engine := &AudioEngine{
		Muted:           false,
		Deafened:        false,
		Loopback:        false,
		InTestMode:      false,
		SuppressionMode: 1, // Default: ACIK (Standart)
		Gain:            1.0,
		GainSliderState: widgets.NewSliderState(100),
		VADSliderState:  widgets.NewSliderState(2),
		VADThreshold:    0.002, // Ultra-sensitive: relaxed conversational voice is detected without shouting
		LocalWave:       make([]float64, 40),
		PeerWaves:       make(map[string][]float64),
		peerBuffers:     make(map[string][]byte),
		mixChan:         make(chan []byte, 64),
		stopChan:        make(chan struct{}),

		lpLow:  newLowpass(AudioSampleRate, 150.0, butterworthQ),
		hpMid:  newHighpass(AudioSampleRate, 150.0, butterworthQ),
		lpMid:  newLowpass(AudioSampleRate, 3400.0, butterworthQ),
		hpHigh: newHighpass(AudioSampleRate, 3400.0, butterworthQ),

		noiseFloorLow:  0.001,
		noiseFloorMid:  0.001,
		noiseFloorHigh: 0.001,
	}

	return engine
}

// EnterTestMode sets up isolated microphone test state:
// Mutes outbound room audio, deafens incoming peer audio, and enables live loopback.
func (a *AudioEngine) EnterTestMode() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.PrevMuted = a.Muted
	a.PrevDeafened = a.Deafened
	a.InTestMode = true
	a.Muted = false
	a.Deafened = true
	a.Loopback = true
	delete(a.peerBuffers, "local_loopback")
}

// LeaveTestMode restores prior room audio state and disables loopback.
func (a *AudioEngine) LeaveTestMode() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.InTestMode = false
	a.Muted = a.PrevMuted
	a.Deafened = a.PrevDeafened
	a.Loopback = false
	delete(a.peerBuffers, "local_loopback")
}

func (a *AudioEngine) CycleSuppressionMode() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SuppressionMode = (a.SuppressionMode + 1) % 3
	return a.SuppressionMode
}

func (a *AudioEngine) SetSuppressionMode(mode int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if mode < 0 {
		mode = 0
	}
	if mode > 2 {
		mode = 2
	}
	a.SuppressionMode = mode
	return a.SuppressionMode
}

func (a *AudioEngine) SuppressionModeString() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch a.SuppressionMode {
	case 0:
		return "KAPALI"
	case 1:
		return "ACIK"
	case 2:
		return "YUKSEK"
	default:
		return "ACIK"
	}
}

// Start launches the real audio capture and playback background workers.
func (a *AudioEngine) Start(onFrame func(rms float64, speaking bool, pcm []byte)) {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return
	}
	a.running = true
	a.stopChan = make(chan struct{})
	a.mu.Unlock()

	a.startPlayback()
	a.startCapture(onFrame)
	go a.playbackMixerLoop()
}

// Stop shuts down the audio engine and terminates background audio processes.
func (a *AudioEngine) Stop() {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return
	}
	a.running = false
	close(a.stopChan)
	a.mu.Unlock()

	if a.capturePipe != nil {
		_ = a.capturePipe.Close()
	}
	if a.captureCmd != nil && a.captureCmd.Process != nil {
		_ = a.captureCmd.Process.Kill()
	}

	if a.playbackPipe != nil {
		_ = a.playbackPipe.Close()
	}
	if a.playbackCmd != nil && a.playbackCmd.Process != nil {
		_ = a.playbackCmd.Process.Kill()
	}
}

func (a *AudioEngine) startCapture(onFrame func(rms float64, speaking bool, pcm []byte)) {
	var cmd *exec.Cmd
	if p, err := exec.LookPath("parec"); err == nil {
		cmd = exec.Command(p, "--rate=16000", "--channels=1", "--format=s16le", "--latency-msec=20")
	} else if p, err := exec.LookPath("arecord"); err == nil {
		cmd = exec.Command(p, "-q", "-r", "16000", "-f", "S16_LE", "-c", "1", "-t", "raw")
	} else if p, err := exec.LookPath("pw-record"); err == nil {
		cmd = exec.Command(p, "--rate", "16000", "--channels", "1", "--format", "s16", "-")
	}

	if cmd != nil {
		stdout, err := cmd.StdoutPipe()
		if err == nil && cmd.Start() == nil {
			a.captureCmd = cmd
			a.capturePipe = stdout
			go a.readCaptureLoop(stdout, onFrame)
			return
		}
	}

	go a.fallbackSimulatedLoop(onFrame)
}

func (a *AudioEngine) readCaptureLoop(r io.Reader, onFrame func(rms float64, speaking bool, pcm []byte)) {
	buf := make([]byte, AudioChunkSize)
	for {
		select {
		case <-a.stopChan:
			return
		default:
		}

		_, err := io.ReadFull(r, buf)
		if err != nil {
			return
		}

		a.mu.Lock()
		muted := a.Muted
		gain := a.Gain
		loopback := a.Loopback
		suppressMode := a.SuppressionMode

		var chunk []byte
		var finalRMS float64
		var speaking bool

		if muted {
			chunk = make([]byte, AudioChunkSize)
			finalRMS = 0
			speaking = false
			a.LocalRMS = 0
			a.IsSpeaking = false
			a.shiftWave(0)
		} else {
			// 1. Apply Volume Gain
			processed := applyGain(buf, gain)

			if suppressMode == 0 {
				// KAPALI (Bypass Mode): Direct raw audio with basic VAD
				rawRMS := calculateRMS(processed)
				speaking = rawRMS > a.VADThreshold
				finalRMS = rawRMS
				chunk = make([]byte, len(processed))
				copy(chunk, processed)
			} else {
				// ACIK / YUKSEK (True Spectral Gating & Transient Subband Noise Reduction)
				var cleaned []byte
				speaking, finalRMS, cleaned = a.processNoiseCancellation(processed, suppressMode)
				chunk = cleaned
			}

			a.LocalRMS = finalRMS
			a.IsSpeaking = speaking
			a.shiftWave(finalRMS)
		}
		a.mu.Unlock()

		if loopback && len(chunk) > 0 && !muted {
			a.queueLoopbackPCM(chunk, finalRMS, speaking)
		}

		if onFrame != nil {
			onFrame(finalRMS, speaking, chunk)
		}
	}
}

// processNoiseCancellation performs 3-band spectral gating designed to eliminate
// ambient hum, desk vibrations, fan hiss, and isolated sharp keystrokes without
// cutting off the user's voice or muffling natural vocal harmonics:
// 1. High-Pass Filter (85 Hz) to eliminate desk thumps & DC offset
// 2. 3-band splitting: low (<150Hz voice depth/hum), mid (150-3400Hz vocal fundamentals),
//    high (>3400Hz sibilance & consonants) via persistent-state IIR biquads
// 3. Transient click protection that preserves voice attacks without muting speech onset
// 4. Per-band noise floor tracking that protects against speech pollution
// 5. Intelligent VAD with generous hangover and zero attack delay so the start of words is never cut off
// 6. Natural band gain balancing and soft-knee peak limiting
func (a *AudioEngine) processNoiseCancellation(pcm []byte, mode int) (bool, float64, []byte) {
	sampleCount := len(pcm) / 2
	if sampleCount != 320 {
		return false, 0, pcm
	}

	// 1. High-Pass Filter (85 Hz) - removes sub-audible DC offset / desk rumble
	filtered := make([]float64, 320)
	const hpAlpha = 0.967
	var frameEnergy float64
	var highFreqEnergy float64
	var maxPeak float64

	for i := 0; i < 320; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2])))
		hpOut := hpAlpha * (a.hpPrevOut + s - a.hpPrevIn)
		a.hpPrevIn = s
		a.hpPrevOut = hpOut
		filtered[i] = hpOut

		absVal := math.Abs(hpOut)
		if absVal > maxPeak {
			maxPeak = absVal
		}

		frameEnergy += hpOut * hpOut
		if i > 0 {
			diff := hpOut - filtered[i-1]
			highFreqEnergy += diff * diff
		}
	}
	frameRMS := math.Sqrt(frameEnergy/320.0) / 32768.0

	// 2. 3-Band Splitter via persistent-state RBJ biquads
	lowBand := make([]float64, 320)
	midBand := make([]float64, 320)
	highBand := make([]float64, 320)

	var lowEnergy, midEnergy, highEnergy float64
	for i := 0; i < 320; i++ {
		x := filtered[i]

		lo := a.lpLow.process(x)
		hpStage := a.hpMid.process(x)
		mid := a.lpMid.process(hpStage)
		hi := a.hpHigh.process(x)

		lowBand[i] = lo
		midBand[i] = mid
		highBand[i] = hi

		lowEnergy += lo * lo
		midEnergy += mid * mid
		highEnergy += hi * hi
	}

	lowRMS := math.Sqrt(lowEnergy/320.0) / 32768.0
	midRMS := math.Sqrt(midEnergy/320.0) / 32768.0
	highRMS := math.Sqrt(highEnergy/320.0) / 32768.0

	// 3. Transient Click Isolation (sharp mechanical clicks with no vocal energy)
	hRatio := highFreqEnergy / (frameEnergy + 1.0)
	peakToRMS := maxPeak / ((frameRMS * 32768.0) + 1.0)
	isTransientClick := (hRatio > 2.5 && peakToRMS > 6.0 && midRMS < a.VADThreshold*1.5)

	// 4. Adaptive Per-Band Noise Floor Tracking
	// Crucial rule: do NOT allow active human speech to raise the noise floor.
	if !a.IsSpeaking {
		a.noiseFloorLow = adaptNoiseFloor(a.noiseFloorLow, lowRMS)
		a.noiseFloorMid = adaptNoiseFloor(a.noiseFloorMid, midRMS)
		a.noiseFloorHigh = adaptNoiseFloor(a.noiseFloorHigh, highRMS)
	} else {
		if lowRMS < a.noiseFloorLow {
			a.noiseFloorLow = lowRMS
		}
		if midRMS < a.noiseFloorMid {
			a.noiseFloorMid = midRMS
		}
		if highRMS < a.noiseFloorHigh {
			a.noiseFloorHigh = highRMS
		}
	}

	// 5. Voice Activity Detection (VAD)
	threshold := a.VADThreshold
	snrFloor := math.Max(a.noiseFloorMid, 0.0005)
	midSNR := midRMS / snrFloor

	var isSpeech bool
	if mode == 2 { // YUKSEK (Aggressive mode for noisy rooms)
		isSpeech = (midRMS > threshold*1.3 && midSNR > 1.25 && !isTransientClick)
	} else { // ACIK (Standart mode - natural, warm, highly responsive)
		isSpeech = (midRMS > threshold && midSNR > 1.10 && !isTransientClick) || (frameRMS > threshold*2.0 && !isTransientClick)
	}

	speaking := false
	if isSpeech {
		a.speechHangover = 18 // ~360ms hangover to retain word endings and breathing pauses
		speaking = true
	} else {
		if a.speechHangover > 0 {
			a.speechHangover--
			speaking = true
		} else {
			speaking = false
		}
	}

	// 6. Target Gains Per Band
	var targetLow, targetMid, targetHigh float64
	if speaking {
		// When speaking: pass vocal warmth (low), core voice (mid), and crisp consonants (high)
		targetLow = 0.90
		targetMid = 1.0
		targetHigh = 0.90
	} else {
		// When silent: suppress room hum, ambient noise, and fan hiss
		if mode == 1 { // ACIK (Standart): gentle attenuation
			targetLow = 0.04
			targetMid = 0.05
			targetHigh = 0.04
		} else { // YUKSEK: near-total gate cutoff
			targetLow = 0.0
			targetMid = 0.01
			targetHigh = 0.0
		}
	}

	// Smooth gain envelope ramping
	a.gateGainLow = smoothGain(a.gateGainLow, targetLow)
	a.gateGainMid = smoothGain(a.gateGainMid, targetMid)
	a.gateGainHigh = smoothGain(a.gateGainHigh, targetHigh)

	// 7. Recombine Bands & Soft-Knee Peak Vocal Limiter
	outBytes := make([]byte, AudioChunkSize)
	var sumSquares float64

	for i := 0; i < 320; i++ {
		sample := lowBand[i]*a.gateGainLow + midBand[i]*a.gateGainMid + highBand[i]*a.gateGainHigh

		// Soft compressor curve to keep voice full-bodied and prevent digital clipping
		if sample > 28000.0 {
			sample = 28000.0 + (sample-28000.0)*0.35
		} else if sample < -28000.0 {
			sample = -28000.0 + (sample+28000.0)*0.35
		}

		norm := sample / 32768.0
		sumSquares += norm * norm

		if sample > 32767 {
			sample = 32767
		} else if sample < -32768 {
			sample = -32768
		}

		binary.LittleEndian.PutUint16(outBytes[i*2:i*2+2], uint16(int16(sample)))
	}

	finalRMS := math.Sqrt(sumSquares / 320.0)
	if !speaking && a.gateGainMid < 0.06 {
		finalRMS = 0
	}

	return speaking, finalRMS, outBytes
}

// adaptNoiseFloor tracks a slowly-adapting noise floor estimate: fast-tracks
// downward in quiet stretches, creeps upward slowly during non-speech ambient noise.
func adaptNoiseFloor(floor, rms float64) float64 {
	if floor <= 0 || math.IsNaN(floor) {
		floor = 0.001
	}
	if rms < floor {
		return floor*0.85 + rms*0.15
	}
	return floor*0.98 + rms*0.02
}

// smoothGain applies an exponential attack/release envelope to avoid
// clicking or pumping when a band's gate opens or closes.
func smoothGain(current, target float64) float64 {
	alpha := 0.60 // Fast attack (~10-20ms) so speech is immediately audible
	if target < current {
		alpha = 0.15 // Smooth release (~150-200ms) to avoid clicking or unnatural pumping
	}
	return current + (target-current)*alpha
}

func (a *AudioEngine) fallbackSimulatedLoop(onFrame func(rms float64, speaking bool, pcm []byte)) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopChan:
			return
		case <-ticker.C:
			a.mu.Lock()
			rms := 0.0
			speaking := false
			chunk := make([]byte, AudioChunkSize)
			a.LocalRMS = rms
			a.IsSpeaking = speaking
			a.shiftWave(0)
			a.mu.Unlock()

			if onFrame != nil {
				onFrame(rms, speaking, chunk)
			}
		}
	}
}

func (a *AudioEngine) startPlayback() {
	var cmd *exec.Cmd
	if p, err := exec.LookPath("pacat"); err == nil {
		cmd = exec.Command(p, "--playback", "--rate=16000", "--channels=1", "--format=s16le", "--latency-msec=20")
	} else if p, err := exec.LookPath("aplay"); err == nil {
		cmd = exec.Command(p, "-q", "-r", "16000", "-f", "S16_LE", "-c", "1", "-t", "raw")
	}

	if cmd != nil {
		stdin, err := cmd.StdinPipe()
		if err == nil && cmd.Start() == nil {
			a.playbackCmd = cmd
			a.playbackPipe = stdin
		}
	}
}

func (a *AudioEngine) playbackMixerLoop() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopChan:
			return
		case <-ticker.C:
			a.mu.Lock()
			if a.playbackPipe == nil {
				a.mu.Unlock()
				continue
			}

			// In test mode: only play local_loopback stream
			if a.InTestMode {
				buf := a.peerBuffers["local_loopback"]
				if !a.Loopback || len(buf) == 0 {
					a.mu.Unlock()
					continue
				}
				var streams [][]byte
				if len(buf) >= AudioChunkSize {
					streams = append(streams, buf[:AudioChunkSize])
					a.peerBuffers["local_loopback"] = buf[AudioChunkSize:]
				} else if len(buf) > 0 {
					streams = append(streams, buf)
					delete(a.peerBuffers, "local_loopback")
				}
				a.mu.Unlock()

				if len(streams) > 0 && a.playbackPipe != nil {
					mixed := mixPCM(streams, AudioChunkSize/2)
					_, _ = a.playbackPipe.Write(mixed)
				}
				continue
			}

			// Normal room mode: check deafened
			if a.Deafened || len(a.peerBuffers) == 0 {
				a.mu.Unlock()
				continue
			}

			var streams [][]byte
			for id, buf := range a.peerBuffers {
				if id == "local_loopback" && !a.Loopback {
					continue
				}
				if len(buf) >= AudioChunkSize {
					streams = append(streams, buf[:AudioChunkSize])
					a.peerBuffers[id] = buf[AudioChunkSize:]
				} else if len(buf) > 0 {
					streams = append(streams, buf)
					delete(a.peerBuffers, id)
				}
			}
			a.mu.Unlock()

			if len(streams) > 0 && a.playbackPipe != nil {
				mixed := mixPCM(streams, AudioChunkSize/2)
				_, _ = a.playbackPipe.Write(mixed)
			}
		}
	}
}

func (a *AudioEngine) queueLoopbackPCM(pcm []byte, rms float64, speaking bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur := a.peerBuffers["local_loopback"]
	if len(cur) > 4096 {
		cur = cur[len(cur)-2048:]
	}
	a.peerBuffers["local_loopback"] = append(cur, pcm...)
}

// PlayPeerPCM queues incoming audio data from a peer for real-time mixing and playback.
func (a *AudioEngine) PlayPeerPCM(peerID string, pcm []byte, rms float64, speaking bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Update visualizer wave
	wave, exists := a.PeerWaves[peerID]
	if !exists || len(wave) != 40 {
		wave = make([]float64, 40)
	}
	copy(wave[0:], wave[1:])
	if a.Deafened {
		wave[len(wave)-1] = 0
	} else {
		wave[len(wave)-1] = rms
	}
	a.PeerWaves[peerID] = wave

	if a.Deafened || a.InTestMode || len(pcm) == 0 {
		return
	}

	cur := a.peerBuffers[peerID]
	if len(cur) > 4096 {
		cur = cur[len(cur)-2048:]
	}
	a.peerBuffers[peerID] = append(cur, pcm...)
}

func (a *AudioEngine) ToggleMute() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Muted = !a.Muted
	if a.Muted {
		a.IsSpeaking = false
		a.LocalRMS = 0
	}
	return a.Muted
}

func (a *AudioEngine) ToggleDeafen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Deafened = !a.Deafened
	if a.Deafened {
		a.Muted = true
		a.IsSpeaking = false
	}
	return a.Deafened
}

func (a *AudioEngine) ToggleLoopback() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Loopback = !a.Loopback
	if !a.Loopback {
		delete(a.peerBuffers, "local_loopback")
	}
	return a.Loopback
}

func (a *AudioEngine) SetLoopback(val bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Loopback = val
	if !a.Loopback {
		delete(a.peerBuffers, "local_loopback")
	}
}

func (a *AudioEngine) AdjustGain(delta float64) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Gain += delta
	if a.Gain < 0.0 {
		a.Gain = 0.0
	}
	if a.Gain > 2.0 {
		a.Gain = 2.0
	}
	if a.GainSliderState != nil {
		a.GainSliderState.Set(int(math.Round(a.Gain*100)), 0, 200)
	}
	return a.Gain
}

func (a *AudioEngine) AdjustThreshold(delta float64) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.VADThreshold += delta
	if a.VADThreshold < 0.001 {
		a.VADThreshold = 0.001
	}
	if a.VADThreshold > 0.050 {
		a.VADThreshold = 0.050
	}
	if a.VADSliderState != nil {
		a.VADSliderState.Set(int(math.Round(a.VADThreshold*1000)), 1, 50)
	}
	return a.VADThreshold
}

func (a *AudioEngine) RecordPeerAudio(peerID string, rms float64, isSpeaking bool) {
	a.PlayPeerPCM(peerID, nil, rms, isSpeaking)
}

func (a *AudioEngine) GetPeerWave(peerID string) []float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	wave, exists := a.PeerWaves[peerID]
	if !exists {
		return make([]float64, 40)
	}
	res := make([]float64, len(wave))
	copy(res, wave)
	return res
}

func (a *AudioEngine) GetLocalWave() []float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	res := make([]float64, len(a.LocalWave))
	copy(res, a.LocalWave)
	return res
}

func (a *AudioEngine) shiftWave(val float64) {
	copy(a.LocalWave[0:], a.LocalWave[1:])
	a.LocalWave[len(a.LocalWave)-1] = val
}

// Helpers for PCM processing
func calculateRMS(pcm []byte) float64 {
	if len(pcm) < 2 {
		return 0
	}
	sampleCount := len(pcm) / 2
	var sumSquares float64
	for i := 0; i < sampleCount; i++ {
		s := int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2]))
		norm := float64(s) / 32768.0
		sumSquares += norm * norm
	}
	return math.Sqrt(sumSquares / float64(sampleCount))
}

func applyGain(pcm []byte, gain float64) []byte {
	if gain == 1.0 || len(pcm) < 2 {
		return pcm
	}
	out := make([]byte, len(pcm))
	sampleCount := len(pcm) / 2
	for i := 0; i < sampleCount; i++ {
		s := int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2]))
		amplified := float64(s) * gain
		if amplified > 32767 {
			amplified = 32767
		} else if amplified < -32768 {
			amplified = -32768
		}
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(int16(amplified)))
	}
	return out
}

func mixPCM(streams [][]byte, numSamples int) []byte {
	out := make([]byte, numSamples*2)
	for i := 0; i < numSamples; i++ {
		var sum int32
		for _, stream := range streams {
			if len(stream) >= (i+1)*2 {
				s := int16(binary.LittleEndian.Uint16(stream[i*2 : i*2+2]))
				sum += int32(s)
			}
		}
		if sum > 32767 {
			sum = 32767
		} else if sum < -32768 {
			sum = -32768
		}
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(int16(sum)))
	}
	return out
}
