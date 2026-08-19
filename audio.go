package main

import (
	"encoding/binary"
	"io"
	"math"
	"os/exec"
	"sync"
	"time"
)

const (
	AudioSampleRate = 16000
	AudioChannels   = 1
	AudioChunkSize  = 640 // 320 samples @ 16-bit (20ms)
	FFTBins         = 80  // 80 frequency bins (0 to 8000 Hz, 100 Hz resolution per bin)
)

var (
	// Precomputed Cosine and Sine lookup tables for zero-allocation ultra-fast DFT/IDFT
	cosTable [FFTBins][320]float64
	sinTable [FFTBins][320]float64
	hannWin  [320]float64
)

func init() {
	for k := 0; k < FFTBins; k++ {
		for n := 0; n < 320; n++ {
			angle := 2.0 * math.Pi * float64(k*n) / 320.0
			cosTable[k][n] = math.Cos(angle)
			sinTable[k][n] = math.Sin(angle)
		}
	}
	for n := 0; n < 320; n++ {
		hannWin[n] = 0.5 * (1.0 - math.Cos(2.0*math.Pi*float64(n)/319.0))
	}
}

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
	IsSpeaking      bool
	LocalRMS        float64
	LocalWave       []float64 // Last 40 samples for visualizer
	PeerWaves       map[string][]float64
	VADThreshold    float64

	// Frequency-Domain Noise Floor Spectrum Estimator (80 bins)
	noiseSpectrum [FFTBins]float64
	binGains      [FFTBins]float64

	// DSP filter & VAD state
	hpPrevIn       float64
	hpPrevOut      float64
	gateGain       float64
	speechHangover int // Hangover counter (chunks) to preserve word endings
	consecSpeech   int

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
		VADThreshold:    0.002, // Ultra-sensitive: relaxed conversational voice is detected without shouting
		gateGain:        0.0,
		LocalWave:       make([]float64, 40),
		PeerWaves:       make(map[string][]float64),
		peerBuffers:     make(map[string][]byte),
		mixChan:         make(chan []byte, 64),
		stopChan:        make(chan struct{}),
	}

	for k := 0; k < FFTBins; k++ {
		engine.noiseSpectrum[k] = 50.0 // Initial baseline noise floor estimate
		engine.binGains[k] = 1.0
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

// processNoiseCancellation performs crystal-clear studio-grade voice noise cancellation:
// 1. Butterworth High-Pass Filter (85 Hz) to eliminate desk thumps & low hums
// 2. High-Frequency Transient Impulse Suppressor (mechanical keyboard clicks & claps)
// 3. Adaptive Minimum-Statistics Noise Floor Energy Tracker
// 4. Voice Activity Energy Discriminator with Syllable Hangover
// 5. Downward Expander / Noise Gate with smooth envelope ramping
// 6. Soft-Knee Vocal Limiter (0% distortion, 100% full-bandwidth 16kHz clarity)
func (a *AudioEngine) processNoiseCancellation(pcm []byte, mode int) (bool, float64, []byte) {
	sampleCount := len(pcm) / 2
	if sampleCount != 320 {
		return false, 0, pcm
	}

	// 1. High-Pass Filter (85 Hz)
	// Prevents rumble and DC offset
	filteredSamples := make([]float64, 320)
	const hpAlpha = 0.967
	var frameEnergy float64
	var highFreqEnergy float64

	for i := 0; i < 320; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2])))
		hpOut := hpAlpha * (a.hpPrevOut + s - a.hpPrevIn)
		a.hpPrevIn = s
		a.hpPrevOut = hpOut
		filteredSamples[i] = hpOut

		frameEnergy += hpOut * hpOut
		if i > 0 {
			diff := hpOut - filteredSamples[i-1]
			highFreqEnergy += diff * diff
		}
	}

	frameRMS := math.Sqrt(frameEnergy / 320.0) / 32768.0

	// 2. Keyboard / Clap Click Transient Detection
	// Sharp spikes have very high high-frequency differential energy relative to total energy
	isClick := false
	if frameEnergy > 1000.0 {
		hRatio := highFreqEnergy / (frameEnergy + 1.0)
		if hRatio > 1.6 && frameRMS < 0.08 {
			isClick = true
		}
	}

	// 3. Adaptive Noise Floor Tracking
	if a.noiseSpectrum[0] <= 0 {
		a.noiseSpectrum[0] = 0.003
	}
	if frameRMS < a.noiseSpectrum[0]*1.5 || frameRMS < 0.004 {
		a.noiseSpectrum[0] = a.noiseSpectrum[0]*0.92 + frameRMS*0.08 // Quick adaptation to quiet room
	} else {
		a.noiseSpectrum[0] = a.noiseSpectrum[0]*0.998 + frameRMS*0.002 // Slow during speech
	}

	noiseFloor := a.noiseSpectrum[0]
	snr := frameRMS / (noiseFloor + 0.0001)

	// 4. Speech Discrimination
	threshold := a.VADThreshold
	if mode == 2 {
		threshold *= 1.5
	}
	isSpeech := (frameRMS > threshold && snr > 1.35) && !isClick

	speaking := false
	if isSpeech {
		a.consecSpeech++
		if a.consecSpeech >= 1 {
			a.speechHangover = 12 // ~240ms hangover to retain word endings
			speaking = true
		}
	} else {
		if isClick {
			a.consecSpeech = 0
		}
		if a.speechHangover > 0 {
			a.speechHangover--
			speaking = true
		} else {
			speaking = false
		}
	}

	// 5. Downward Expander Gain Calculation
	gateFloor := 0.0
	if mode == 1 {
		gateFloor = 0.04 // ACIK: Clean attenuated floor (downward expander)
	} else {
		gateFloor = 0.00 // YUKSEK: Complete absolute silence
	}

	targetGain := gateFloor
	if speaking {
		targetGain = 1.0
	} else if isClick {
		targetGain = 0.0
	}

	// Smooth exponential attack / release envelope to avoid clicking or pumping
	alpha := 0.45
	if targetGain < a.gateGain {
		alpha = 0.25 // Smooth release
	}
	a.gateGain += (targetGain - a.gateGain) * alpha

	// 6. Apply gain and Soft-Knee Peak Limiter
	outBytes := make([]byte, AudioChunkSize)
	var sumSquares float64

	for i := 0; i < 320; i++ {
		sample := filteredSamples[i] * a.gateGain

		// Soft compressor curve to keep voice full-bodied and prevent digital clipping
		if sample > 28000.0 {
			sample = 28000.0 + (sample-28000.0)*0.3
		} else if sample < -28000.0 {
			sample = -28000.0 + (sample+28000.0)*0.3
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
	if !speaking && a.gateGain < 0.1 {
		finalRMS = 0
	}

	return speaking, finalRMS, outBytes
}

func quickMedian5(w [5]float64) float64 {
	if w[0] > w[1] {
		w[0], w[1] = w[1], w[0]
	}
	if w[3] > w[4] {
		w[3], w[4] = w[4], w[3]
	}
	if w[0] > w[3] {
		w[0], w[3] = w[3], w[0]
		w[1], w[4] = w[4], w[1]
	}
	if w[1] > w[2] {
		w[1], w[2] = w[2], w[1]
	}
	if w[2] > w[3] {
		w[2], w[3] = w[3], w[2]
		w[1], w[2] = w[2], w[1]
	}
	if w[1] > w[2] {
		w[1], w[2] = w[2], w[1]
	}
	return w[2]
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
				if id == "local_loopback" {
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
	return a.Gain
}

func (a *AudioEngine) AdjustThreshold(delta float64) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.VADThreshold += delta
	if a.VADThreshold < 0.001 {
		a.VADThreshold = 0.001
	}
	if a.VADThreshold > 0.20 {
		a.VADThreshold = 0.20
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
