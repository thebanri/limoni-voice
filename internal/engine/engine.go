package engine

import (
	"encoding/binary"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/thebanri/limoni-voice/internal/audioio"
	"github.com/thebanri/limoni-voice/internal/dsp"
	"github.com/thebanri/limoni-voice/internal/dsp/rnnoise"
	"github.com/thebanri/limoni-voice/internal/voice"
)

const (
	AudioSampleRate   = audioio.SampleRate // 48 kHz fullband
	AudioChannels     = 1
	AudioFrameSamples = audioio.FrameSamples  // 20 ms
	AudioChunkSize    = AudioFrameSamples * 2 // bytes of 16-bit PCM per frame

	analysisRate    = 16000 // voice activity / noise analysis runs on a 3:1 decimated copy
	analysisSamples = AudioFrameSamples / 3

	echoTail = 200 * time.Millisecond
)

// Noise suppression modes.
const (
	SuppressionOff      = 0
	SuppressionStandard = 1
	SuppressionHigh     = 2
	SuppressionAI       = 3 // RNNoise neural network
	suppressionModes    = 4
)

// AudioDevice represents a system microphone or speaker output device.
type AudioDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
	IsInput   bool   `json:"is_input"`
}

func toAudioDevices(devs []audioio.Device, input bool, fallbackName string) []AudioDevice {
	if len(devs) == 0 {
		return []AudioDevice{{ID: "default", Name: fallbackName, IsDefault: true, IsInput: input}}
	}
	out := make([]AudioDevice, 0, len(devs))
	for _, d := range devs {
		out = append(out, AudioDevice{ID: d.ID, Name: d.Name, IsDefault: d.IsDefault, IsInput: input})
	}
	return out
}

// EnumerateInputDevices discovers available microphone input devices on the system.
func EnumerateInputDevices() []AudioDevice {
	return toAudioDevices(audioio.Devices(true), true, "Default System Microphone")
}

// EnumerateOutputDevices discovers available speaker/headphone playback devices on the system.
func EnumerateOutputDevices() []AudioDevice {
	return toAudioDevices(audioio.Devices(false), false, "Default System Output / Speakers")
}

// PeerJitterBuffer is a small FIFO for raw PCM streams (local loopback test, legacy PCM peers).
type PeerJitterBuffer struct {
	chunks    [][]byte
	isPlaying bool
	prebuffer int
}

func newPeerJitterBuffer(prebuffer int) *PeerJitterBuffer {
	if prebuffer < 1 {
		prebuffer = 1
	}
	return &PeerJitterBuffer{
		chunks:    make([][]byte, 0, 8),
		prebuffer: prebuffer,
	}
}

func (jb *PeerJitterBuffer) Push(chunk []byte) {
	if len(chunk) != AudioChunkSize {
		padded := make([]byte, AudioChunkSize)
		copy(padded, chunk)
		chunk = padded
	}
	// Bound queue to max 6 chunks (~120ms) to ensure low latency while absorbing jitter
	if len(jb.chunks) >= 6 {
		jb.chunks = jb.chunks[1:]
	}
	jb.chunks = append(jb.chunks, chunk)

	if !jb.isPlaying && len(jb.chunks) >= jb.prebuffer {
		jb.isPlaying = true
	}
}

func (jb *PeerJitterBuffer) Pop() ([]byte, bool) {
	if !jb.isPlaying || len(jb.chunks) == 0 {
		jb.isPlaying = false
		return nil, false
	}
	chunk := jb.chunks[0]
	jb.chunks = jb.chunks[1:]
	if len(jb.chunks) == 0 {
		jb.isPlaying = false
	}
	return chunk, true
}

func (jb *PeerJitterBuffer) Reset() {
	jb.chunks = jb.chunks[:0]
	jb.isPlaying = false
}

type AudioInputMode int

const (
	InputModeVoiceActivity AudioInputMode = 0
	InputModePushToTalk    AudioInputMode = 1
)

// peerVoice is the receive chain of one remote speaker.
type peerVoice struct {
	jitter *voice.JitterBuffer
	level  float64 // slow loudness leveler gain
}

// Receive-side leveling: quiet speakers are lifted and loud ones tamed towards a comfortable
// target, slowly and only while they speak so background noise is never pumped up.
const (
	levelTargetRMS = 0.07 // about −23 dBFS
	levelMinGain   = 0.6
	levelMaxGain   = 1.4
	levelSpeechRMS = 0.012 // frames quieter than this do not move the leveler
	levelRate      = 0.02  // per 20 ms frame (~1 s time constant)
)

// nextLevel moves a leveler gain one frame towards the target for a frame of rms level.
func nextLevel(level, rms float64) float64 {
	if level <= 0 {
		level = 1
	}
	if rms < levelSpeechRMS {
		return level
	}
	desired := math.Max(levelMinGain, math.Min(levelMaxGain, levelTargetRMS/rms))
	return level + (desired-level)*levelRate
}

type AudioEngine struct {
	mu           sync.RWMutex
	Muted        bool
	Deafened     bool
	Loopback     bool // Mic test / echo loopback
	InTestMode   bool // True when test dialog is open
	PrevMuted    bool
	PrevDeafened bool

	// Push-to-Talk (PTT)
	InputMode       AudioInputMode
	IsPTTActive     bool
	PTTReleaseTime  time.Time
	PTTKey          rune
	PTTKeyName      string
	PTTListeningKey bool
	GlobalPTT       bool   // system-wide hotkey instead of terminal key presses
	GlobalPTTStatus string // backend in use or reason unavailable

	// Suppression mode: 0 = OFF (Bypass), 1 = ON (Standard Clean), 2 = HIGH, 3 = AI (RNNoise)
	SuppressionMode  int
	EchoCancellation bool
	VoiceSmoothing   bool    // soften highs, even out loudness and limit peaks on the sent voice
	Gain             float64 // Mic Gain: 0.0 to 3.0 (1.0 = 100%, up to 300%)
	OutputVolume     float64 // Output Volume: 0.0 to 2.0 (1.0 = 100%, up to 200%)
	VADSensitivity   int     // 1 to 100% (default 65%)
	IsSpeaking       bool
	LocalRMS         float64
	LocalWave        []float64 // Last 40 samples for visualizer
	PeerWaves        map[string][]float64
	PeerVolumes      map[string]float64 // Per-user volume scaling (0.0 to 2.0, default 1.0)
	VADThreshold     float64

	// Devices
	InputDevices      []AudioDevice
	OutputDevices     []AudioDevice
	SelectedInputIdx  int
	SelectedOutputIdx int
	CaptureBackend    string
	PlaybackBackend   string

	// Analysis DSP state (16 kHz decimated copy)
	decim           decimator3
	hpPrevIn        float64
	hpPrevOut       float64
	lpPrevOut       float64
	hpMidPrevIn     float64
	hpMidPrevOut    float64
	lpMidPrevOut    float64
	hpHghPrevIn     float64
	hpHghPrevOut    float64
	noiseFloor      float64
	noiseFloorLow   float64 // 90Hz - 400Hz (fan, drone, hum)
	noiseFloorMid   float64 // 400Hz - 2500Hz (room reverb)
	noiseFloorHigh  float64 // 2500Hz - 8000Hz (mic hiss)
	gateGain        float64
	speechHangover  int     // Hangover counter (chunks) to preserve word endings and pauses
	lastPlaybackRMS float64 // Tracks speaker playback energy for echo suppression
	residualEchoRMS float64

	// Fullband reconstruction filter state (48 kHz)
	rec48 bandSplitter

	// Echo canceller & neural denoiser
	aecMu    sync.Mutex
	aec      *dsp.EchoCanceller
	aecOut   []int16
	residual []float64
	denoiser *rnnoise.State
	rnnBuf   []float32

	// Screen share audio (audio_screen.go)
	ScreenAudioVolume float64
	screen            *screenAudio
	sysMu             sync.Mutex
	sysAEC            *dsp.LoopbackCanceller
	sysOut            []int16

	// Click / clap suppression and voice smoothing (capture goroutine only)
	transient       *dsp.TransientSuppressor
	transientActive bool // the current frame contained a suppressed click / clap
	transientTail   int  // frames left in which reverb of a recent transient cannot open the gate
	smoother        *dsp.VoiceSmoother
	fxBuf           []float64

	// Live audio streams
	captureStream  audioio.Stream
	playbackStream audioio.Stream
	onFrame        func(rms float64, speaking bool, pcm []byte)

	// Receive & mixing
	peerJitterBuffers map[string]*PeerJitterBuffer
	peerVoices        map[string]*peerVoice
	mixScratch        []int16
	mixAccum          []float64
	sfxQueue          [][]byte
	lastSFXTime       map[SoundEffect]time.Time
	SFXMuted          bool
	stopChan          chan struct{}
	running           bool
}

type SoundEffect int

const (
	SoundJoin SoundEffect = iota + 1
	SoundLeave
	SoundChat
)

var (
	cachedJoinPCM  []byte
	cachedLeavePCM []byte
	cachedChatPCM  []byte
	sfxOnce        sync.Once
)

func initSFXCache() {
	cachedJoinPCM = generateJoinSoundPCM()
	cachedLeavePCM = generateLeaveSoundPCM()
	cachedChatPCM = generateChatSoundPCM()
}

func padToChunks(pcm []byte) []byte {
	if rem := len(pcm) % AudioChunkSize; rem != 0 {
		pcm = append(pcm, make([]byte, AudioChunkSize-rem)...)
	}
	return pcm
}

type chimeNote struct {
	start, end   float64
	freq1, freq2 float64
}

func synthChime(duration float64, notes []chimeNote, decay, amplitude, harmonic float64) []byte {
	numSamples := int(duration * AudioSampleRate)
	pcm := make([]byte, numSamples*2)
	for i := 0; i < numSamples; i++ {
		t := float64(i) / AudioSampleRate
		var sample float64
		for _, n := range notes {
			if t >= n.start && t < n.end {
				noteT := t - n.start
				dur := n.end - n.start
				env := 1.0
				if noteT < 0.012 {
					env = noteT / 0.012
				} else {
					env = math.Exp(-decay * (noteT - 0.012) / dur)
				}
				val := math.Sin(2*math.Pi*n.freq1*noteT)*0.70 +
					math.Sin(2*math.Pi*n.freq2*noteT)*0.35 +
					math.Sin(4*math.Pi*n.freq1*noteT)*harmonic
				sample += val * env
			}
		}
		amp := math.Max(-32768, math.Min(32767, sample*amplitude))
		binary.LittleEndian.PutUint16(pcm[i*2:i*2+2], uint16(int16(amp)))
	}
	return padToChunks(pcm)
}

// generateJoinSoundPCM generates a rich, ascending multi-tone chime (C5 -> E5 -> G5 -> C6)
func generateJoinSoundPCM() []byte {
	return synthChime(0.40, []chimeNote{
		{start: 0.00, end: 0.18, freq1: 523.25, freq2: 659.25},
		{start: 0.08, end: 0.26, freq1: 659.25, freq2: 783.99},
		{start: 0.16, end: 0.40, freq1: 783.99, freq2: 1046.50},
	}, 7.0, 14000.0, 0.10)
}

// generateLeaveSoundPCM generates a gentle, descending multi-tone chime (G5 -> E5 -> C5)
func generateLeaveSoundPCM() []byte {
	return synthChime(0.38, []chimeNote{
		{start: 0.00, end: 0.16, freq1: 783.99, freq2: 659.25},
		{start: 0.08, end: 0.25, freq1: 659.25, freq2: 523.25},
		{start: 0.16, end: 0.38, freq1: 523.25, freq2: 392.00},
	}, 7.5, 13500.0, 0.08)
}

// generateChatSoundPCM generates a subtle, pleasant message notification blip
func generateChatSoundPCM() []byte {
	numSamples := AudioSampleRate / 10 // 100ms
	pcm := make([]byte, numSamples*2)
	for i := 0; i < numSamples; i++ {
		t := float64(i) / AudioSampleRate
		freq := 650.0 + (300.0 * (t / 0.10))
		env := 1.0
		if t < 0.004 {
			env = t / 0.004
		} else {
			env = math.Exp(-22.0 * (t - 0.004))
		}
		val := math.Sin(2*math.Pi*freq*t)*0.85 + math.Sin(4*math.Pi*freq*t)*0.15
		amp := math.Max(-32768, math.Min(32767, val*env*11000.0))
		binary.LittleEndian.PutUint16(pcm[i*2:i*2+2], uint16(int16(amp)))
	}
	return padToChunks(pcm)
}

// PlaySound enqueues a synthesized sound effect for real-time playback
func (a *AudioEngine) PlaySound(sfx SoundEffect) {
	if a == nil {
		return
	}
	sfxOnce.Do(initSFXCache)

	var raw []byte
	switch sfx {
	case SoundJoin:
		raw = cachedJoinPCM
	case SoundLeave:
		raw = cachedLeavePCM
	case SoundChat:
		raw = cachedChatPCM
	default:
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Deafened || a.SFXMuted {
		return
	}
	if a.lastSFXTime == nil {
		a.lastSFXTime = make(map[SoundEffect]time.Time)
	}
	// Debounce identical sound effects triggered within 400ms to prevent double audio playback
	if last, exists := a.lastSFXTime[sfx]; exists && time.Since(last) < 400*time.Millisecond {
		return
	}
	a.lastSFXTime[sfx] = time.Now()

	for i := 0; i+AudioChunkSize <= len(raw); i += AudioChunkSize {
		a.sfxQueue = append(a.sfxQueue, raw[i:i+AudioChunkSize])
	}
}

func NewAudioEngine() *AudioEngine {
	inputDevs := EnumerateInputDevices()
	outputDevs := EnumerateOutputDevices()

	return &AudioEngine{
		InputMode:         InputModeVoiceActivity,
		PTTKey:            ' ',
		PTTKeyName:        "Space",
		SuppressionMode:   SuppressionStandard,
		EchoCancellation:  true,
		VoiceSmoothing:    true,
		ScreenAudioVolume: 1.0,
		Gain:              1.0,
		OutputVolume:      1.0,
		VADSensitivity:    65,
		VADThreshold:      0.0031, // Natural default voice detection threshold (~65% sensitivity, -50dB)
		LocalWave:         make([]float64, 40),
		PeerWaves:         make(map[string][]float64),
		PeerVolumes:       make(map[string]float64),
		peerJitterBuffers: make(map[string]*PeerJitterBuffer),
		peerVoices:        make(map[string]*peerVoice),
		mixScratch:        make([]int16, AudioFrameSamples),
		mixAccum:          make([]float64, AudioFrameSamples),
		lastSFXTime:       make(map[SoundEffect]time.Time),
		stopChan:          make(chan struct{}),
		InputDevices:      inputDevs,
		OutputDevices:     outputDevs,
		noiseFloor:        0.001,
		noiseFloorLow:     0.0008,
		noiseFloorMid:     0.0005,
		noiseFloorHigh:    0.0004,
		gateGain:          1.0,
		rec48:             newBandSplitter(AudioSampleRate),
	}
}

func (a *AudioEngine) RefreshDevices() {
	inDevs := EnumerateInputDevices()
	outDevs := EnumerateOutputDevices()

	a.mu.Lock()
	a.InputDevices = inDevs
	a.OutputDevices = outDevs
	if a.SelectedInputIdx >= len(inDevs) {
		a.SelectedInputIdx = 0
	}
	if a.SelectedOutputIdx >= len(outDevs) {
		a.SelectedOutputIdx = 0
	}
	a.mu.Unlock()
}

func (a *AudioEngine) SetInputDevice(idx int) {
	a.mu.Lock()
	if len(a.InputDevices) == 0 {
		a.mu.Unlock()
		return
	}
	idx = max(0, min(idx, len(a.InputDevices)-1))
	if a.SelectedInputIdx == idx {
		a.mu.Unlock()
		return
	}
	a.SelectedInputIdx = idx
	isRunning := a.running
	a.mu.Unlock()

	if isRunning {
		a.restartCapture()
	}
}

func (a *AudioEngine) CycleInputDevice(delta int) int {
	a.mu.Lock()
	n := len(a.InputDevices)
	if n <= 1 {
		a.mu.Unlock()
		return 0
	}
	newIdx := (a.SelectedInputIdx + delta%n + n) % n
	a.mu.Unlock()

	a.SetInputDevice(newIdx)
	return newIdx
}

func (a *AudioEngine) SetOutputDevice(idx int) {
	a.mu.Lock()
	if len(a.OutputDevices) == 0 {
		a.mu.Unlock()
		return
	}
	idx = max(0, min(idx, len(a.OutputDevices)-1))
	if a.SelectedOutputIdx == idx {
		a.mu.Unlock()
		return
	}
	a.SelectedOutputIdx = idx
	isRunning := a.running
	a.mu.Unlock()

	if isRunning {
		a.restartPlayback()
	}
}

func (a *AudioEngine) CycleOutputDevice(delta int) int {
	a.mu.Lock()
	n := len(a.OutputDevices)
	if n <= 1 {
		a.mu.Unlock()
		return 0
	}
	newIdx := (a.SelectedOutputIdx + delta%n + n) % n
	a.mu.Unlock()

	a.SetOutputDevice(newIdx)
	return newIdx
}

func (a *AudioEngine) GetSelectedInputName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.InputDevices) == 0 {
		return "Default Microphone"
	}
	idx := a.SelectedInputIdx
	if idx < 0 || idx >= len(a.InputDevices) {
		idx = 0
	}
	return a.InputDevices[idx].Name
}

func (a *AudioEngine) GetSelectedOutputName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.OutputDevices) == 0 {
		return "Default Speakers"
	}
	idx := a.SelectedOutputIdx
	if idx < 0 || idx >= len(a.OutputDevices) {
		idx = 0
	}
	return a.OutputDevices[idx].Name
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
	delete(a.peerJitterBuffers, "local_loopback")
}

// LeaveTestMode restores prior room audio state and disables loopback.
func (a *AudioEngine) LeaveTestMode() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.InTestMode = false
	a.Muted = a.PrevMuted
	a.Deafened = a.PrevDeafened
	a.Loopback = false
	delete(a.peerJitterBuffers, "local_loopback")
}

func (a *AudioEngine) CycleSuppressionMode() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SuppressionMode = (a.SuppressionMode + 1) % suppressionModes
	return a.SuppressionMode
}

func (a *AudioEngine) SetSuppressionMode(mode int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SuppressionMode = max(0, min(mode, suppressionModes-1))
	return a.SuppressionMode
}

// ToggleEchoCancellation switches the acoustic echo canceller on or off.
func (a *AudioEngine) ToggleEchoCancellation() bool {
	a.mu.Lock()
	a.EchoCancellation = !a.EchoCancellation
	enabled := a.EchoCancellation
	a.mu.Unlock()
	if !enabled {
		a.aecMu.Lock()
		if a.aec != nil {
			a.aec.Reset()
		}
		a.aecMu.Unlock()
	}
	return enabled
}

func (a *AudioEngine) SetInputMode(mode AudioInputMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.InputMode = mode
}

func (a *AudioEngine) CycleInputMode() AudioInputMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.InputMode == InputModeVoiceActivity {
		a.InputMode = InputModePushToTalk
	} else {
		a.InputMode = InputModeVoiceActivity
	}
	return a.InputMode
}

func (a *AudioEngine) InputModeString() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.InputMode == InputModePushToTalk {
		if a.GlobalPTT {
			return "Push-to-Talk (Global)"
		}
		return "Push-to-Talk"
	}
	return "Voice Activity"
}

func (a *AudioEngine) SetPTT(active bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.IsPTTActive && !active {
		a.PTTReleaseTime = time.Now().Add(250 * time.Millisecond)
	}
	a.IsPTTActive = active
}

func (a *AudioEngine) PulsePTT(duration time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.PTTReleaseTime = time.Now().Add(duration)
}

func (a *AudioEngine) SetPTTKey(key rune, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.PTTKey = key
	if name == "" {
		if key == ' ' {
			name = "Space"
		} else {
			name = strings.ToUpper(string(key))
		}
	}
	a.PTTKeyName = name
	a.PTTListeningKey = false
}

func (a *AudioEngine) GetPTTKeyName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.PTTKeyName != "" {
		return a.PTTKeyName
	}
	if a.PTTKey == ' ' {
		return "Space"
	}
	if a.PTTKey > 0 {
		return strings.ToUpper(string(a.PTTKey))
	}
	return "Space"
}

func (a *AudioEngine) IsTransmitting() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.Muted {
		return false
	}
	if a.InputMode == InputModeVoiceActivity {
		return true
	}
	return a.IsPTTActive || time.Now().Before(a.PTTReleaseTime)
}

func (a *AudioEngine) SuppressionModeString() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch a.SuppressionMode {
	case SuppressionOff:
		return "OFF"
	case SuppressionHigh:
		return "HIGH"
	case SuppressionAI:
		return "AI"
	default:
		return "ON"
	}
}

// Start opens the audio devices and begins capture, processing and playback.
func (a *AudioEngine) Start(onFrame func(rms float64, speaking bool, pcm []byte)) {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return
	}
	a.running = true
	a.onFrame = onFrame
	a.stopChan = make(chan struct{})
	a.mu.Unlock()

	a.startPlayback()
	a.startCapture()
}

// Stop shuts down the audio engine and closes device streams.
func (a *AudioEngine) Stop() {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return
	}
	a.running = false
	close(a.stopChan)
	capture, playback := a.captureStream, a.playbackStream
	a.captureStream, a.playbackStream = nil, nil
	a.mu.Unlock()

	if capture != nil {
		_ = capture.Close()
	}
	if playback != nil {
		_ = playback.Close()
	}
}

func (a *AudioEngine) restartCapture() {
	a.mu.Lock()
	old := a.captureStream
	a.captureStream = nil
	a.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	a.startCapture()
}

func (a *AudioEngine) restartPlayback() {
	a.mu.Lock()
	old := a.playbackStream
	a.playbackStream = nil
	a.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	a.startPlayback()
}

func (a *AudioEngine) selectedDeviceID(input bool) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if input {
		if a.SelectedInputIdx >= 0 && a.SelectedInputIdx < len(a.InputDevices) {
			return a.InputDevices[a.SelectedInputIdx].ID
		}
	} else if a.SelectedOutputIdx >= 0 && a.SelectedOutputIdx < len(a.OutputDevices) {
		return a.OutputDevices[a.SelectedOutputIdx].ID
	}
	return "default"
}

func (a *AudioEngine) startCapture() {
	stream, err := audioio.OpenCapture(a.selectedDeviceID(true), a.processCaptureFrame)
	a.mu.Lock()
	if err != nil {
		a.CaptureBackend = "unavailable"
		stop := a.stopChan
		a.mu.Unlock()
		go a.fallbackSimulatedLoop(stop)
		return
	}
	if !a.running {
		a.mu.Unlock()
		_ = stream.Close()
		return
	}
	a.captureStream = stream
	a.CaptureBackend = stream.Backend()
	a.mu.Unlock()
}

func (a *AudioEngine) startPlayback() {
	stream, err := audioio.OpenPlayback(a.selectedDeviceID(false), a.renderFrame)
	a.mu.Lock()
	if err != nil {
		// No output device: keep consuming the jitter buffers so receive state stays fresh.
		a.PlaybackBackend = "unavailable"
		stop := a.stopChan
		a.mu.Unlock()
		go a.renderWithoutDevice(stop)
		return
	}
	if !a.running {
		a.mu.Unlock()
		_ = stream.Close()
		return
	}
	a.playbackStream = stream
	a.PlaybackBackend = stream.Backend()
	a.mu.Unlock()
}

func (a *AudioEngine) renderWithoutDevice(stop chan struct{}) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	frame := make([]int16, AudioFrameSamples)
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			a.renderFrame(frame)
		}
	}
}

func pcmFromInt16(frame []int16) []byte {
	out := make([]byte, len(frame)*2)
	for i, s := range frame {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(s))
	}
	return out
}

// processCaptureFrame runs the capture chain on one device frame:
// gain → echo cancellation → noise suppression / VAD → loopback and network callback.
func (a *AudioEngine) processCaptureFrame(frame []int16) {
	a.mu.RLock()
	aecEnabled := a.EchoCancellation && !a.InTestMode && a.playbackStream != nil
	a.mu.RUnlock()

	input := frame
	if aecEnabled {
		input = a.cancelEcho(frame)
	}
	buf := pcmFromInt16(input)

	a.mu.Lock()
	muted := a.Muted
	gain := a.Gain
	loopback := a.Loopback
	suppressMode := a.SuppressionMode
	inputMode := a.InputMode
	isPTT := a.IsPTTActive || time.Now().Before(a.PTTReleaseTime)
	onFrame := a.onFrame

	var chunk []byte
	var finalRMS float64
	var speaking bool

	if muted || (inputMode == InputModePushToTalk && !isPTT) {
		chunk = make([]byte, AudioChunkSize)
		a.LocalRMS = 0
		a.IsSpeaking = false
		a.shiftWave(0)
	} else {
		processed := applyGain(buf, gain)
		a.transientActive = false
		if suppressMode != SuppressionOff {
			// Remove keyboard / mouse clicks and claps before voice detection so they neither
			// open the gate nor ride along with speech.
			processed = a.suppressTransients(processed)
		}
		switch suppressMode {
		case SuppressionOff:
			rawRMS := calculateRMS(processed)
			speaking = rawRMS > a.VADThreshold
			finalRMS = rawRMS
			chunk = append([]byte(nil), processed...)
			if inputMode == InputModePushToTalk && isPTT {
				speaking = true
			}
			// Smooth gate in bypass mode: close gently when silent to avoid background hiss
			targetGain := 0.0
			if speaking {
				targetGain = 1.0
			}
			if targetGain > a.gateGain {
				a.gateGain += (targetGain - a.gateGain) * 0.85
			} else {
				a.gateGain += (targetGain - a.gateGain) * 0.15
			}
			if a.gateGain < 0.99 && inputMode != InputModePushToTalk {
				chunk = applyGain(chunk, a.gateGain)
			}
		case SuppressionAI:
			speaking, finalRMS, chunk = a.processNeuralSuppression(processed)
		default:
			speaking, finalRMS, chunk = a.processNoiseCancellation(processed, suppressMode)
		}
		if inputMode == InputModePushToTalk && isPTT {
			speaking = true
		}
		if a.VoiceSmoothing {
			chunk = a.smoothVoice(chunk)
			if finalRMS > 0 {
				finalRMS = calculateRMS(chunk)
			}
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

// pcmToFx converts 16-bit PCM into the float scratch buffer used by the voice effects.
func (a *AudioEngine) pcmToFx(pcm []byte) []float64 {
	n := len(pcm) / 2
	if cap(a.fxBuf) < n {
		a.fxBuf = make([]float64, n)
	}
	buf := a.fxBuf[:n]
	for i := range buf {
		buf[i] = float64(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
	}
	return buf
}

func fxToPCM(buf []float64) []byte {
	out := make([]byte, len(buf)*2)
	for i, v := range buf {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(math.Max(-32768, math.Min(32767, math.Round(v))))))
	}
	return out
}

// suppressTransients attenuates clicks and claps (adds 13 ms of delay). Caller holds a.mu.
func (a *AudioEngine) suppressTransients(pcm []byte) []byte {
	if a.transient == nil {
		a.transient = dsp.NewTransientSuppressor(AudioSampleRate)
	}
	buf := a.pcmToFx(pcm)
	a.transient.Process(buf)
	a.transientActive = a.transient.MinGain() < 0.5
	if a.transientActive {
		a.transientTail = 15 // 300 ms
	}
	return fxToPCM(buf)
}

// smoothVoice applies the de-harsh EQ, compressor and limiter. Caller holds a.mu.
func (a *AudioEngine) smoothVoice(pcm []byte) []byte {
	if a.smoother == nil {
		a.smoother = dsp.NewVoiceSmoother(AudioSampleRate)
	}
	buf := a.pcmToFx(pcm)
	a.smoother.Process(buf)
	return fxToPCM(buf)
}

// ToggleVoiceSmoothing switches voice smoothing on or off.
func (a *AudioEngine) ToggleVoiceSmoothing() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.VoiceSmoothing = !a.VoiceSmoothing
	return a.VoiceSmoothing
}

// cancelEcho removes the speaker signal from the microphone frame (two 10 ms sub-frames).
func (a *AudioEngine) cancelEcho(frame []int16) []int16 {
	a.aecMu.Lock()
	defer a.aecMu.Unlock()
	if a.aec == nil {
		sub := AudioFrameSamples / 2
		a.aec = dsp.NewEchoCanceller(sub, int(echoTail.Seconds()*AudioSampleRate), AudioSampleRate)
		a.aecOut = make([]int16, AudioFrameSamples)
		a.residual = make([]float64, sub+1)
	}
	sub := a.aec.FrameSize()
	var residualPower float64
	for off := 0; off+sub <= len(frame); off += sub {
		a.aec.Capture(frame[off:off+sub], a.aecOut[off:off+sub])
		if a.aec.Adapted() {
			a.aec.Residual(a.residual)
			for _, p := range a.residual {
				residualPower += p
			}
		}
	}
	// Residual echo level (normalized RMS) raises the VAD threshold so leftover echo does not
	// open the gate.
	res := math.Sqrt(residualPower/float64(AudioFrameSamples)) / 32768.0
	a.mu.Lock()
	a.residualEchoRMS = res
	a.mu.Unlock()
	return a.aecOut
}

func (a *AudioEngine) processNeuralSuppression(pcm []byte) (bool, float64, []byte) {
	if a.denoiser == nil {
		a.denoiser = rnnoise.New()
		a.rnnBuf = make([]float32, rnnoise.FrameSize)
	}
	out := make([]byte, len(pcm))
	var vad float32
	var sumSquares float64
	for off := 0; off+rnnoise.FrameSize <= AudioFrameSamples; off += rnnoise.FrameSize {
		for i := 0; i < rnnoise.FrameSize; i++ {
			a.rnnBuf[i] = float32(int16(binary.LittleEndian.Uint16(pcm[2*(off+i):])))
		}
		vad = max(vad, a.denoiser.ProcessFrame(a.rnnBuf, a.rnnBuf))
		for i, v := range a.rnnBuf {
			s := math.Max(-32768, math.Min(32767, float64(v)))
			binary.LittleEndian.PutUint16(out[2*(off+i):], uint16(int16(s)))
		}
	}

	rawRMS := calculateRMS(out)
	threshold := a.VADThreshold
	if a.residualEchoRMS > 0 {
		threshold = math.Max(threshold, a.residualEchoRMS*1.5)
	}
	isSpeech := vad > 0.6 && rawRMS > threshold*0.5 && !a.transientActive
	speaking := isSpeech
	if isSpeech {
		a.speechHangover = 15
	} else if a.speechHangover > 0 {
		a.speechHangover--
		speaking = true
	}

	target := 0.0
	if speaking {
		target = 1.0
	}
	if target > a.gateGain {
		a.gateGain += (target - a.gateGain) * 0.85
	} else {
		a.gateGain += (target - a.gateGain) * 0.12
	}
	if a.gateGain < 0.99 {
		out = applyGain(out, a.gateGain)
	}
	for i := 0; i < AudioFrameSamples; i++ {
		norm := float64(int16(binary.LittleEndian.Uint16(out[2*i:]))) / 32768.0
		sumSquares += norm * norm
	}
	finalRMS := math.Sqrt(sumSquares / AudioFrameSamples)
	if !speaking && a.gateGain < 0.05 {
		finalRMS = 0
	}
	return speaking, finalRMS, out
}

// calculatePitchHarmonicity computes the maximum normalized autocorrelation across the
// fundamental human vocal pitch range (85 Hz to 330 Hz, lag 48 to 188 samples at 16kHz).
// Voiced human speech produces peaks between 0.35 and 0.90+.
// Claps, keyboard typing, coughs, fan hum, and room noise produce values < 0.22.
func calculatePitchHarmonicity(samples []float64) float64 {
	n := len(samples)
	if n < 240 {
		return 0
	}

	const minLag = 48  // ~333 Hz
	const maxLag = 188 // ~85 Hz
	const corrLen = 128

	var sumZero float64
	for i := 0; i < corrLen; i++ {
		sumZero += samples[i] * samples[i]
	}
	if sumZero < 500.0 { // Silence / noise floor
		return 0
	}

	var maxNormCorr float64
	for lag := minLag; lag <= maxLag; lag += 2 {
		var crossSum, lagSum float64
		for i := 0; i < corrLen; i++ {
			s0 := samples[i]
			sLag := samples[i+lag]
			crossSum += s0 * sLag
			lagSum += sLag * sLag
		}
		if lagSum > 0 {
			if normCorr := crossSum / (math.Sqrt(sumZero*lagSum) + 1e-6); normCorr > maxNormCorr {
				maxNormCorr = normCorr
			}
		}
	}
	return maxNormCorr
}

// processNoiseCancellation performs multi-band voice enhancement and noise suppression.
// Decisions (VAD, noise floors, clap / keyboard / cough rejection) are made on a 16 kHz
// decimated copy; the gains are applied to the fullband 48 kHz signal:
//  1. High-Pass Filter (85 Hz) to eliminate desk thumps, AC 50/60 Hz hum, and DC offset
//  2. Multi-Band Decomposition: Low (<280Hz fan/drone), Mid (280-2500Hz voice), High (>2500Hz hiss/air)
//  3. Pitch Harmonicity & Voiced/Unvoiced Speech Tracking
//  4. Impulsive Transient & Slew-Rate Limiting for hand claps and mechanical keyboard clicks
//  5. Explosive Turbulence Filtering for coughs and throat clearing
//  6. Adaptive per-band stationary noise floor subtraction (-20dB to -36dB)
//  7. Transparent soft-knee vocal peak limiter preventing digital clipping
func (a *AudioEngine) processNoiseCancellation(pcm []byte, mode int) (bool, float64, []byte) {
	if len(pcm) != AudioChunkSize {
		return false, 0, pcm
	}

	full := make([]float64, AudioFrameSamples)
	for i := range full {
		full[i] = float64(int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2])))
	}
	decimated := a.decim.process(full)

	filtered := make([]float64, analysisSamples)

	const hpAlpha = 0.968    // 85Hz High-Pass (removes sub-bass DC & table bumps)
	const lpAlpha = 0.099    // 280Hz Low-Pass (captures fan, drone, power hum)
	const hpMidAlpha = 0.901 // 280Hz High-Pass for mid-band
	const lpMidAlpha = 0.495 // 2500Hz Low-Pass for mid-band (vocal formant core)
	const hpHghAlpha = 0.505 // 2500Hz High-Pass (captures hiss, clicks, consonant air)

	var frameEnergy, lowEnergy, midEnergy, highEnergy, maxPeak float64

	for i := 0; i < analysisSamples; i++ {
		s := decimated[i]

		hpOut := hpAlpha * (a.hpPrevOut + s - a.hpPrevIn)
		a.hpPrevIn = s
		a.hpPrevOut = hpOut
		filtered[i] = hpOut

		lpOut := a.lpPrevOut + lpAlpha*(hpOut-a.lpPrevOut)
		a.lpPrevOut = lpOut
		lowEnergy += lpOut * lpOut

		midHP := hpMidAlpha * (a.hpMidPrevOut + hpOut - a.hpMidPrevIn)
		a.hpMidPrevIn = hpOut
		a.hpMidPrevOut = midHP
		midLP := a.lpMidPrevOut + lpMidAlpha*(midHP-a.lpMidPrevOut)
		a.lpMidPrevOut = midLP
		midEnergy += midLP * midLP

		hghHP := hpHghAlpha * (a.hpHghPrevOut + hpOut - a.hpHghPrevIn)
		a.hpHghPrevIn = hpOut
		a.hpHghPrevOut = hghHP
		highEnergy += hghHP * hghHP

		if absVal := math.Abs(hpOut); absVal > maxPeak {
			maxPeak = absVal
		}
		frameEnergy += hpOut * hpOut
	}

	frameRMS := math.Sqrt(frameEnergy/analysisSamples) / 32768.0
	lowRMS := math.Sqrt(lowEnergy/analysisSamples) / 32768.0
	midRMS := math.Sqrt(midEnergy/analysisSamples) / 32768.0
	highRMS := math.Sqrt(highEnergy/analysisSamples) / 32768.0

	harmonicity := calculatePitchHarmonicity(filtered)

	var maxSlew float64
	for i := 1; i < analysisSamples; i++ {
		if slew := math.Abs(filtered[i] - filtered[i-1]); slew > maxSlew {
			maxSlew = slew
		}
	}

	peakToRMS := maxPeak / ((frameRMS * 32768.0) + 1.0)
	hfRatio := highEnergy / (midEnergy + lowEnergy + 1.0)

	// Non-vocal sound discrimination (Claps, Keyboards, Coughs, Fan/AC low-drone)
	isImpulsiveClap := (peakToRMS > 4.6 || maxSlew > 5500.0) && harmonicity < 0.22
	// Mechanical keyboard clicks are sharp micro-impulses with extreme crest factor, high slew, and zero harmonicity.
	isKeyboardClick := peakToRMS > 4.2 && (hfRatio > 1.2 || maxSlew > 3800.0) && harmonicity < 0.18
	// Coughs and throat clearing are explosive, low-frequency guttural turbulence
	isCoughBurst := frameRMS > a.VADThreshold*1.40 && harmonicity < 0.20 && lowEnergy > midEnergy*1.10
	isLowDrone := (lowEnergy > (midEnergy*2.5 + 1.0)) && (midRMS < 0.003 || midRMS < a.VADThreshold*0.50)
	// A frame in which the transient suppressor just removed a click never counts as speech on
	// its own (an ongoing word is carried over it by the hangover).
	isNonVocalNoise := isImpulsiveClap || isKeyboardClick || isCoughBurst || isLowDrone || a.transientActive

	// Adaptive Noise Floor Tracking
	if a.noiseFloor <= 0 || math.IsNaN(a.noiseFloor) {
		a.noiseFloor = 0.001
	}
	if a.noiseFloorLow <= 0 || math.IsNaN(a.noiseFloorLow) {
		a.noiseFloorLow = 0.0008
	}
	if a.noiseFloorMid <= 0 || math.IsNaN(a.noiseFloorMid) {
		a.noiseFloorMid = 0.0005
	}
	if a.noiseFloorHigh <= 0 || math.IsNaN(a.noiseFloorHigh) {
		a.noiseFloorHigh = 0.0004
	}
	track := func(floor *float64, level float64) {
		switch {
		case level < *floor:
			*floor = *floor*0.70 + level*0.30
		case !a.IsSpeaking:
			*floor = *floor*0.85 + level*0.15
		default:
			*floor = *floor*0.998 + level*0.002
		}
	}
	track(&a.noiseFloor, frameRMS)
	track(&a.noiseFloorLow, lowRMS)
	track(&a.noiseFloorMid, midRMS)
	track(&a.noiseFloorHigh, highRMS)

	// Voice Activity Detection with Pitch Harmonicity & echo-aware thresholds
	threshold := a.VADThreshold
	if a.lastPlaybackRMS > 0.015 {
		echoDucker := math.Min(a.VADThreshold+(a.lastPlaybackRMS*0.10), a.VADThreshold*1.30)
		threshold = math.Max(threshold, echoDucker)
		a.lastPlaybackRMS *= 0.80
	} else {
		a.lastPlaybackRMS = 0
	}
	if a.residualEchoRMS > 0 {
		threshold = math.Max(threshold, math.Min(a.residualEchoRMS*1.5, a.VADThreshold*3))
	}

	snr := frameRMS / math.Max(a.noiseFloor, 0.0004)
	midSNR := midRMS / math.Max(a.noiseFloorMid, 0.0003)
	highSNR := highRMS / math.Max(a.noiseFloorHigh, 0.0003)

	var isSpeech bool
	if mode == SuppressionHigh {
		isVoiced := harmonicity >= 0.20 || midSNR > 1.25
		isUnvoicedConsonant := (snr > 1.25 || highSNR > 1.25) && highRMS > threshold*0.25
		isSpeech = frameRMS > threshold && snr > 1.25 && (isVoiced || isUnvoicedConsonant) && !isNonVocalNoise
	} else {
		isVoiced := harmonicity >= 0.18 && (snr > 1.15 || midSNR > 1.15) && midRMS > threshold*0.25
		isUnvoicedConsonant := (snr > 1.20 || midSNR > 1.20 || highSNR > 1.20) && highRMS > threshold*0.25
		isSpeech = frameRMS > threshold && (isVoiced || isUnvoicedConsonant) && !isNonVocalNoise
	}
	// Room reverb after a clap or key press sounds like a fricative. Shortly after a transient,
	// only clearly voiced sound may open a closed gate (an ongoing word is unaffected).
	if a.transientTail > 0 {
		a.transientTail--
		if a.speechHangover == 0 && harmonicity < 0.3 {
			isSpeech = false
		}
	}

	speaking := false
	if isSpeech {
		if mode == SuppressionHigh {
			a.speechHangover = 12 // ~240ms hangover in HIGH mode
		} else {
			a.speechHangover = 18 // ~360ms hangover in STANDARD mode
		}
		speaking = true
	} else if a.speechHangover > 0 {
		a.speechHangover--
		speaking = true
	}

	// Multi-Band Spectral Subtraction & Expander Gain Calculation
	var lowGain, highGain float64
	if speaking {
		lowSNR := lowRMS / math.Max(a.noiseFloorLow, 0.0002)
		bandHighSNR := highRMS / math.Max(a.noiseFloorHigh, 0.0002)
		if mode == SuppressionHigh {
			lowGain = 1.0
			if lowSNR <= 2.5 {
				lowGain = math.Max(0.08, (lowSNR-1.0)/1.5*0.92+0.08)
			}
			highGain = 1.0
			if bandHighSNR <= 2.8 {
				highGain = math.Max(0.12, (bandHighSNR-1.0)/1.8*0.88+0.12)
			}
			if isNonVocalNoise {
				highGain *= 0.20
				lowGain *= 0.40
			}
		} else {
			lowGain = 1.0
			if lowSNR <= 2.0 {
				lowGain = math.Max(0.25, (lowSNR-1.0)/1.0*0.75+0.25)
			}
			highGain = 1.0
			if bandHighSNR <= 2.2 {
				highGain = math.Max(0.35, (bandHighSNR-1.0)/1.2*0.65+0.35)
			}
		}
	}

	// Master Gate Ramping
	targetGain := 0.0
	if speaking {
		targetGain = 1.0
	}
	alpha := 0.12 // smooth release (~120ms fadeout)
	if targetGain > a.gateGain {
		alpha = 0.85 // fast attack (~3ms)
	}
	a.gateGain += (targetGain - a.gateGain) * alpha
	g := a.gateGain

	// Reconstruct the fullband signal with the band gains and a soft-knee limiter.
	outBytes := make([]byte, AudioChunkSize)
	var sumSquares float64
	clampSlew := isImpulsiveClap || isKeyboardClick
	prevOut := a.rec48.lastHP
	for i := 0; i < AudioFrameSamples; i++ {
		hp, low, high := a.rec48.split(full[i])
		if clampSlew {
			// Soften raw impulsive clicks/claps (limit per-sample slew at 48 kHz)
			const slewLimit = 2000.0 / 3
			if diff := hp - prevOut; diff > slewLimit {
				hp = prevOut + slewLimit + (diff-slewLimit)*0.05
			} else if diff < -slewLimit {
				hp = prevOut - slewLimit + (diff+slewLimit)*0.05
			}
		}
		prevOut = hp
		sample := (hp - low*(1.0-lowGain) - high*(1.0-highGain)) * g

		if sample > 28000.0 {
			sample = 28000.0 + (sample-28000.0)*0.25
		} else if sample < -28000.0 {
			sample = -28000.0 + (sample+28000.0)*0.25
		}
		norm := sample / 32768.0
		sumSquares += norm * norm
		sample = math.Max(-32768, math.Min(32767, sample))
		binary.LittleEndian.PutUint16(outBytes[i*2:i*2+2], uint16(int16(sample)))
	}

	finalRMS := math.Sqrt(sumSquares / AudioFrameSamples)
	if !speaking && a.gateGain < 0.05 {
		finalRMS = 0
	}
	return speaking, finalRMS, outBytes
}

func (a *AudioEngine) fallbackSimulatedLoop(stop chan struct{}) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	frame := make([]int16, AudioFrameSamples)
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			a.processCaptureFrame(frame)
		}
	}
}

// renderFrame mixes everything that should be heard into out (one device frame).
func (a *AudioEngine) renderFrame(out []int16) {
	a.mu.Lock()
	outputVol := a.OutputVolume
	accum := a.mixAccum
	clear(accum)
	active := false

	addPCM := func(chunk []byte) {
		for i := 0; i < AudioFrameSamples && 2*i+1 < len(chunk); i++ {
			accum[i] += float64(int16(binary.LittleEndian.Uint16(chunk[2*i:])))
		}
		active = true
	}

	switch {
	case a.InTestMode:
		if jb := a.peerJitterBuffers["local_loopback"]; a.Loopback && jb != nil {
			if chunk, ok := jb.Pop(); ok {
				addPCM(chunk)
			}
		}
	case a.Deafened:
		// Drain receive buffers so audio does not pile up while deafened.
		for _, pv := range a.peerVoices {
			pv.jitter.Pull(a.mixScratch)
		}
		a.mixScreenAudioLocked(accum, false)
	default:
		for id, pv := range a.peerVoices {
			if pv.jitter.Pull(a.mixScratch) == voice.FrameNone {
				continue
			}
			var energy float64
			for _, s := range a.mixScratch {
				energy += float64(s) * float64(s)
			}
			pv.level = nextLevel(pv.level, math.Sqrt(energy/AudioFrameSamples)/32768.0)
			gain := pv.level
			if vol, ok := a.PeerVolumes[id]; ok {
				gain *= vol
			}
			for i, s := range a.mixScratch {
				accum[i] += float64(s) * gain
			}
			active = true
		}
		for id, jb := range a.peerJitterBuffers {
			if id == "local_loopback" {
				delete(a.peerJitterBuffers, id)
				continue
			}
			if chunk, ok := jb.Pop(); ok {
				addPCM(chunk)
			}
		}
		if a.mixScreenAudioLocked(accum, true) {
			active = true
		}
		if len(a.sfxQueue) > 0 {
			addPCM(a.sfxQueue[0])
			a.sfxQueue = a.sfxQueue[1:]
		}
	}

	var energy float64
	for i := range out {
		sum := accum[i]
		if outputVol != 1.0 {
			sum *= outputVol
		}
		// Soft saturation limiter for multi-speaker mix
		if sum > 29000.0 {
			sum = 29000.0 + (sum-29000.0)*0.30
		} else if sum < -29000.0 {
			sum = -29000.0 + (sum+29000.0)*0.30
		}
		sum = math.Max(-32768, math.Min(32767, sum))
		out[i] = int16(sum)
		energy += sum * sum
	}
	if active {
		a.lastPlaybackRMS = math.Sqrt(energy/float64(len(out))) / 32768.0
	}
	aecEnabled := a.EchoCancellation && !a.InTestMode
	a.mu.Unlock()
	a.feedLoopbackReference(out)

	if aecEnabled {
		a.aecMu.Lock()
		if a.aec != nil {
			sub := a.aec.FrameSize()
			for off := 0; off+sub <= len(out); off += sub {
				a.aec.Playback(out[off : off+sub])
			}
		}
		a.aecMu.Unlock()
	}
}

func (a *AudioEngine) queueLoopbackPCM(pcm []byte, rms float64, speaking bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	jb, exists := a.peerJitterBuffers["local_loopback"]
	if !exists {
		jb = newPeerJitterBuffer(1) // 1 chunk (20ms) for instant local mic test response
		a.peerJitterBuffers["local_loopback"] = jb
	}
	jb.Push(pcm)
}

func (a *AudioEngine) recordPeerWaveLocked(peerID string, rms float64) {
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
}

// PlayPeerOpus queues an Opus packet from a peer into its adaptive jitter buffer.
func (a *AudioEngine) PlayPeerOpus(peerID string, seq uint32, timestampMs int64, frame []byte, rms float64, speaking bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recordPeerWaveLocked(peerID, rms)
	if len(frame) == 0 {
		return
	}
	pv, ok := a.peerVoices[peerID]
	if !ok {
		dec, err := voice.NewDecoder(AudioSampleRate, AudioFrameSamples)
		if err != nil {
			return
		}
		pv = &peerVoice{jitter: voice.NewJitterBuffer(dec, 20*time.Millisecond), level: 1}
		a.peerVoices[peerID] = pv
	}
	pv.jitter.Push(seq, timestampMs, frame, speaking, time.Now())
}

// PeerReceiveQuality returns the audio loss percentage and jitter (ms) observed from a peer.
func (a *AudioEngine) PeerReceiveQuality(peerID string) (lossPct, jitterMs float64) {
	a.mu.RLock()
	pv, ok := a.peerVoices[peerID]
	a.mu.RUnlock()
	if !ok {
		return 0, 0
	}
	return pv.jitter.LossPercent(), pv.jitter.Stats().JitterMs
}

// PlayPeerPCM queues raw PCM from a peer (loopback / tests) with per-peer volume and AGC.
func (a *AudioEngine) PlayPeerPCM(peerID string, pcm []byte, rms float64, speaking bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.recordPeerWaveLocked(peerID, rms)
	if a.Deafened || a.InTestMode || len(pcm) == 0 {
		return
	}

	// Gentle leveling towards the same target as the Opus receive path (speech frames only).
	if rms >= levelSpeechRMS {
		pcm = applyGain(pcm, math.Max(levelMinGain, math.Min(levelMaxGain, levelTargetRMS/rms)))
	}
	if vol, ok := a.PeerVolumes[peerID]; ok && vol != 1.0 {
		pcm = applyGain(pcm, vol)
	}

	jb, exists := a.peerJitterBuffers[peerID]
	if !exists {
		jb = newPeerJitterBuffer(2) // 2 chunks (40ms) jitter cushion
		a.peerJitterBuffers[peerID] = jb
	}
	jb.Push(pcm)
}

// SetPeerVolume sets the volume multiplier for a specific peer (0.0 = 0% to 2.0 = 200%).
func (a *AudioEngine) SetPeerVolume(peerID string, vol float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.PeerVolumes == nil {
		a.PeerVolumes = make(map[string]float64)
	}
	a.PeerVolumes[peerID] = math.Max(0, math.Min(vol, 2.0))
}

// GetPeerVolume returns the volume multiplier for a specific peer (defaults to 1.0).
func (a *AudioEngine) GetPeerVolume(peerID string) float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if vol, ok := a.PeerVolumes[peerID]; ok {
		return vol
	}
	return 1.0
}

// AdjustPeerVolume adjusts a peer's volume by delta and clamps between 0.0 and 2.0.
func (a *AudioEngine) AdjustPeerVolume(peerID string, delta float64) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.PeerVolumes == nil {
		a.PeerVolumes = make(map[string]float64)
	}
	vol, ok := a.PeerVolumes[peerID]
	if !ok {
		vol = 1.0
	}
	vol = math.Max(0, math.Min(vol+delta, 2.0))
	a.PeerVolumes[peerID] = vol
	return vol
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

func (a *AudioEngine) ToggleSFXMute() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SFXMuted = !a.SFXMuted
	if a.SFXMuted {
		a.sfxQueue = a.sfxQueue[:0]
	}
	return a.SFXMuted
}

func (a *AudioEngine) SetSFXMuted(val bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SFXMuted = val
	if a.SFXMuted {
		a.sfxQueue = a.sfxQueue[:0]
	}
}

func (a *AudioEngine) ToggleLoopback() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Loopback = !a.Loopback
	if !a.Loopback {
		delete(a.peerJitterBuffers, "local_loopback")
	}
	return a.Loopback
}

func (a *AudioEngine) SetLoopback(val bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Loopback = val
	if !a.Loopback {
		delete(a.peerJitterBuffers, "local_loopback")
	}
}

func (a *AudioEngine) AdjustGain(delta float64) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Gain = math.Max(0, math.Min(a.Gain+delta, 3.0))
	return a.Gain
}

func (a *AudioEngine) AdjustOutputVolume(delta float64) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.OutputVolume = math.Max(0, math.Min(a.OutputVolume+delta, 2.0))
	return a.OutputVolume
}

func thresholdToSensitivity(threshold float64) int {
	norm := math.Pow(math.Max(0, (threshold-0.001)/0.049), 1.0/3.0)
	return max(1, min(100, int(math.Round(100.0-norm*99.0))))
}

func (a *AudioEngine) GetVADSensitivity() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.VADSensitivity > 0 {
		return a.VADSensitivity
	}
	return thresholdToSensitivity(a.VADThreshold)
}

func (a *AudioEngine) SetVADSensitivity(pct int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	pct = max(1, min(pct, 100))
	a.VADSensitivity = pct
	norm := float64(100-pct) / 99.0
	// Sensitivity mapping: 100% -> 0.001 (-60dB), 65% -> 0.0031 (-50dB), 1% -> 0.050 (-26dB)
	a.VADThreshold = 0.001 + 0.049*math.Pow(norm, 3.0)
	return a.VADSensitivity
}

func (a *AudioEngine) AdjustThreshold(delta float64) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.VADThreshold = math.Max(0.001, math.Min(a.VADThreshold+delta, 0.050))
	a.VADSensitivity = thresholdToSensitivity(a.VADThreshold)
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
	return append([]float64(nil), wave...)
}

func (a *AudioEngine) GetLocalWave() []float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]float64(nil), a.LocalWave...)
}

func (a *AudioEngine) shiftWave(val float64) {
	copy(a.LocalWave[0:], a.LocalWave[1:])
	a.LocalWave[len(a.LocalWave)-1] = val
}

// RemovePeer cleans up audio buffers, visualizer waves, and volume settings when a peer disconnects
func (a *AudioEngine) RemovePeer(peerID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.peerJitterBuffers, peerID)
	delete(a.peerVoices, peerID)
	delete(a.PeerWaves, peerID)
	delete(a.PeerVolumes, peerID)
}

// ClearAllPeers resets all peer audio buffers when leaving or resetting a room
func (a *AudioEngine) ClearAllPeers() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.peerJitterBuffers = make(map[string]*PeerJitterBuffer)
	a.peerVoices = make(map[string]*peerVoice)
	a.PeerWaves = make(map[string][]float64)
}

// Helpers for PCM processing
func calculateRMS(pcm []byte) float64 {
	if len(pcm) < 2 {
		return 0
	}
	sampleCount := len(pcm) / 2
	var sumSquares float64
	for i := 0; i < sampleCount; i++ {
		norm := float64(int16(binary.LittleEndian.Uint16(pcm[i*2:i*2+2]))) / 32768.0
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
		amplified := float64(int16(binary.LittleEndian.Uint16(pcm[i*2:i*2+2]))) * gain
		// Soft-knee saturation above 28000 to prevent harsh digital clipping
		if amplified > 28000.0 {
			amplified = 28000.0 + (amplified-28000.0)*0.35
		} else if amplified < -28000.0 {
			amplified = -28000.0 + (amplified+28000.0)*0.35
		}
		amplified = math.Max(-32768, math.Min(32767, amplified))
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(int16(amplified)))
	}
	return out
}

func mixPCM(streams [][]byte, numSamples int, outputVolume float64) []byte {
	out := make([]byte, numSamples*2)
	if len(streams) == 0 {
		return out
	}
	for i := 0; i < numSamples; i++ {
		var sum float64
		for _, stream := range streams {
			if len(stream) >= (i+1)*2 {
				sum += float64(int16(binary.LittleEndian.Uint16(stream[i*2 : i*2+2])))
			}
		}
		if outputVolume != 1.0 && outputVolume > 0 {
			sum *= outputVolume
		}
		// Soft saturation limiter for multi-speaker mix
		if sum > 29000.0 {
			sum = 29000.0 + (sum-29000.0)*0.30
		} else if sum < -29000.0 {
			sum = -29000.0 + (sum+29000.0)*0.30
		}
		sum = math.Max(-32768, math.Min(32767, sum))
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(int16(sum)))
	}
	return out
}

// --- fullband helpers ---

// decimator3 reduces 48 kHz frames to the 16 kHz analysis rate by averaging sample triples.
// The boxcar has no look-ahead (the VAD reacts without added latency) and its -4 dB at 8 kHz
// is harmless for level / harmonicity analysis; the audible output stays fullband.
type decimator3 struct{}

func (decimator3) process(in []float64) []float64 {
	out := make([]float64, len(in)/3)
	for i := range out {
		out[i] = (in[3*i] + in[3*i+1] + in[3*i+2]) / 3
	}
	return out
}

// bandSplitter reproduces the analysis band split at the output sample rate:
// 85 Hz high-pass, then a 280 Hz low band and a 2.5 kHz high band.
type bandSplitter struct {
	hpA, lpA, hghA        float64
	hpIn, hpOut, lpOut    float64
	hghIn, hghOut, lastHP float64
}

// polesAt converts a one-pole coefficient tuned at 16 kHz to rate, keeping the time constant.
func poleAt(pole16k, rate float64) float64 {
	return math.Pow(pole16k, analysisRate/rate)
}

func newBandSplitter(rate float64) bandSplitter {
	// Same filters as the tuned 16 kHz analysis chain (hp 0.968, lp 0.099, high 0.505).
	return bandSplitter{hpA: poleAt(0.968, rate), lpA: 1 - poleAt(1-0.099, rate), hghA: poleAt(0.505, rate)}
}

func (b *bandSplitter) split(s float64) (hp, low, high float64) {
	hp = b.hpA * (b.hpOut + s - b.hpIn)
	b.hpIn, b.hpOut = s, hp
	b.lpOut += b.lpA * (hp - b.lpOut)
	high = b.hghA * (b.hghOut + hp - b.hghIn)
	b.hghIn, b.hghOut = hp, high
	b.lastHP = hp
	return hp, b.lpOut, high
}
