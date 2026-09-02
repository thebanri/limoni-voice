package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/thebanri/limoni/widgets"
)

func findAudioTool(names ...string) string {
	home := os.Getenv("HOME")
	searchPaths := []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/opt/local/bin",
		"/usr/bin",
		"/bin",
	}
	if home != "" {
		searchPaths = append(searchPaths,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, "homebrew", "bin"),
			filepath.Join(home, ".homebrew", "bin"),
		)
	}
	if p, err := os.Executable(); err == nil {
		searchPaths = append([]string{filepath.Dir(p)}, searchPaths...)
	}

	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		for _, dir := range searchPaths {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

const (
	AudioSampleRate = 16000
	AudioChannels   = 1
	AudioChunkSize  = 640 // 320 samples @ 16-bit (20ms)
)

// AudioDevice represents a system microphone or speaker output device.
type AudioDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
	IsInput   bool   `json:"is_input"`
}

// EnumerateInputDevices discovers available microphone input devices on the system.
func EnumerateInputDevices() []AudioDevice {
	devices := []AudioDevice{
		{ID: "default", Name: "Default System Microphone", IsDefault: true, IsInput: true},
	}

	if runtime.GOOS == "windows" {
		winDevs := enumerateWindowsInputDevices()
		if len(winDevs) > 0 {
			return winDevs
		}
		return devices
	}

	// Linux PulseAudio / PipeWire device enumeration
	if p := findAudioTool("pactl"); p != "" {
		out, err := exec.Command(p, "list", "sources").Output()
		if err == nil {
			parsed := parsePactlSources(out)
			if len(parsed) > 0 {
				return append(devices, parsed...)
			}
		}
	}

	// Fallback to ALSA arecord -l
	if p := findAudioTool("arecord"); p != "" {
		out, err := exec.Command(p, "-l").Output()
		if err == nil {
			parsed := parseAlsaDevices(out, true)
			if len(parsed) > 0 {
				return append(devices, parsed...)
			}
		}
	}

	return devices
}

// EnumerateOutputDevices discovers available speaker/headphone playback devices on the system.
func EnumerateOutputDevices() []AudioDevice {
	devices := []AudioDevice{
		{ID: "default", Name: "Default System Output / Speakers", IsDefault: true, IsInput: false},
	}

	if runtime.GOOS == "windows" {
		winDevs := enumerateWindowsOutputDevices()
		if len(winDevs) > 0 {
			return winDevs
		}
		return devices
	}

	// Linux PulseAudio / PipeWire device enumeration
	if p := findAudioTool("pactl"); p != "" {
		out, err := exec.Command(p, "list", "sinks").Output()
		if err == nil {
			parsed := parsePactlSinks(out)
			if len(parsed) > 0 {
				return append(devices, parsed...)
			}
		}
	}

	// Fallback to ALSA aplay -l
	if p := findAudioTool("aplay"); p != "" {
		out, err := exec.Command(p, "-l").Output()
		if err == nil {
			parsed := parseAlsaDevices(out, false)
			if len(parsed) > 0 {
				return append(devices, parsed...)
			}
		}
	}

	return devices
}

func parsePactlSources(data []byte) []AudioDevice {
	var devices []AudioDevice
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var curName, curDesc string

	flush := func() {
		if curName != "" {
			// Skip monitor sources for microphone list
			if !strings.HasSuffix(curName, ".monitor") && !strings.HasPrefix(curDesc, "Monitor of") {
				displayName := curDesc
				if displayName == "" {
					displayName = curName
				}
				devices = append(devices, AudioDevice{
					ID:      curName,
					Name:    displayName,
					IsInput: true,
				})
			}
		}
		curName = ""
		curDesc = ""
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Source #") {
			flush()
		} else if strings.HasPrefix(line, "Name: ") {
			curName = strings.TrimSpace(strings.TrimPrefix(line, "Name: "))
		} else if strings.HasPrefix(line, "Description: ") {
			curDesc = strings.TrimSpace(strings.TrimPrefix(line, "Description: "))
		} else if strings.HasPrefix(line, "device.description = ") {
			val := strings.Trim(strings.TrimPrefix(line, "device.description = "), "\"")
			if curDesc == "" {
				curDesc = val
			}
		} else if strings.HasPrefix(line, "node.description = ") {
			val := strings.Trim(strings.TrimPrefix(line, "node.description = "), "\"")
			if curDesc == "" {
				curDesc = val
			}
		}
	}
	flush()
	return devices
}

func parsePactlSinks(data []byte) []AudioDevice {
	var devices []AudioDevice
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var curName, curDesc string

	flush := func() {
		if curName != "" {
			displayName := curDesc
			if displayName == "" {
				displayName = curName
			}
			devices = append(devices, AudioDevice{
				ID:      curName,
				Name:    displayName,
				IsInput: false,
			})
		}
		curName = ""
		curDesc = ""
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Sink #") {
			flush()
		} else if strings.HasPrefix(line, "Name: ") {
			curName = strings.TrimSpace(strings.TrimPrefix(line, "Name: "))
		} else if strings.HasPrefix(line, "Description: ") {
			curDesc = strings.TrimSpace(strings.TrimPrefix(line, "Description: "))
		} else if strings.HasPrefix(line, "device.description = ") {
			val := strings.Trim(strings.TrimPrefix(line, "device.description = "), "\"")
			if curDesc == "" {
				curDesc = val
			}
		} else if strings.HasPrefix(line, "node.description = ") {
			val := strings.Trim(strings.TrimPrefix(line, "node.description = "), "\"")
			if curDesc == "" {
				curDesc = val
			}
		}
	}
	flush()
	return devices
}

func parseAlsaDevices(data []byte, isInput bool) []AudioDevice {
	var devices []AudioDevice
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "card ") {
			// card 1: Device [USB Audio], device 0: USB Audio [USB Audio]
			parts := strings.Split(line, ":")
			if len(parts) >= 3 {
				cardNum := strings.TrimPrefix(strings.Fields(parts[0])[1], "card")
				cardNum = strings.TrimSpace(cardNum)
				devName := strings.TrimSpace(parts[1])
				if idx := strings.Index(devName, "["); idx != -1 {
					devName = strings.Trim(devName[idx:], "[]")
				}
				devID := fmt.Sprintf("hw:%s,0", cardNum)
				devices = append(devices, AudioDevice{
					ID:      devID,
					Name:    devName,
					IsInput: isInput,
				})
			}
		}
	}
	return devices
}

// PeerJitterBuffer maintains a smooth, jitter-free FIFO audio chunk queue for each peer.
// It absorbs network timing variance with a 2-chunk (40ms) pre-buffering cushion,
// eliminating audio underflow pops, micro-dropouts, and robotic stutter.
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
		isPlaying: false,
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

type AudioEngine struct {
	mu           sync.RWMutex
	Muted        bool
	Deafened     bool
	Loopback     bool // Mic test / echo loopback
	InTestMode   bool // True when test dialog is open
	PrevMuted    bool
	PrevDeafened bool

	// Push-to-Talk (PTT)
	InputMode      AudioInputMode
	IsPTTActive    bool
	PTTReleaseTime time.Time

	// Suppression mode: 0 = OFF (Bypass), 1 = ON (Standard Clean), 2 = HIGH
	SuppressionMode   int
	Gain              float64 // Mic Gain: 0.0 to 3.0 (1.0 = 100%, up to 300%)
	OutputVolume      float64 // Output Volume: 0.0 to 2.0 (1.0 = 100%, up to 200%)
	GainSliderState   *widgets.SliderState
	OutputSliderState *widgets.SliderState
	VADSliderState    *widgets.SliderState
	IsSpeaking        bool
	LocalRMS          float64
	LocalWave         []float64 // Last 40 samples for visualizer
	PeerWaves         map[string][]float64
	VADThreshold      float64

	// Devices
	InputDevices      []AudioDevice
	OutputDevices     []AudioDevice
	SelectedInputIdx  int
	SelectedOutputIdx int

	// DSP state
	hpPrevIn        float64
	hpPrevOut       float64
	noiseFloor      float64
	gateGain        float64
	speechHangover  int     // Hangover counter (chunks) to preserve word endings and pauses
	lastPlaybackRMS float64 // Tracks speaker playback energy for Acoustic Echo Suppression (AES)

	// Live audio capture & playback processes
	captureCmd   *exec.Cmd
	capturePipe  io.ReadCloser
	playbackCmd  *exec.Cmd
	playbackPipe io.WriteCloser
	onFrame      func(rms float64, speaking bool, pcm []byte)

	// Mixing buffer for incoming peer streams with jitter compensation
	peerJitterBuffers map[string]*PeerJitterBuffer
	mixChan           chan []byte
	stopChan          chan struct{}
	running           bool
}

func NewAudioEngine() *AudioEngine {
	inputDevs := EnumerateInputDevices()
	outputDevs := EnumerateOutputDevices()

	engine := &AudioEngine{
		Muted:             false,
		Deafened:          false,
		Loopback:          false,
		InTestMode:        false,
		InputMode:         InputModeVoiceActivity,
		SuppressionMode:   1, // Default: ON (Standard Clean)
		Gain:              1.0,  // Standard 100% initial mic gain
		OutputVolume:      1.0,  // 100% master playback volume
		GainSliderState:   widgets.NewSliderState(100),
		OutputSliderState: widgets.NewSliderState(100),
		VADSliderState:    widgets.NewSliderState(6),
		VADThreshold:      0.005, // Highly responsive, natural voice detection
		LocalWave:         make([]float64, 40),
		PeerWaves:         make(map[string][]float64),
		peerJitterBuffers: make(map[string]*PeerJitterBuffer),
		mixChan:           make(chan []byte, 64),
		stopChan:          make(chan struct{}),
		InputDevices:      inputDevs,
		OutputDevices:     outputDevs,
		SelectedInputIdx:  0,
		SelectedOutputIdx: 0,
		noiseFloor:        0.001,
		gateGain:          1.0,
	}

	return engine
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
	if idx < 0 {
		idx = 0
	}
	if idx >= len(a.InputDevices) {
		idx = len(a.InputDevices) - 1
	}
	if a.SelectedInputIdx == idx {
		a.mu.Unlock()
		return
	}
	a.SelectedInputIdx = idx
	isRunning := a.running
	onFrame := a.onFrame
	a.mu.Unlock()

	if isRunning {
		a.restartCapture(onFrame)
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
	if idx < 0 {
		idx = 0
	}
	if idx >= len(a.OutputDevices) {
		idx = len(a.OutputDevices) - 1
	}
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
	a.IsPTTActive = true
	a.PTTReleaseTime = time.Now().Add(duration)
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
	case 0:
		return "OFF"
	case 1:
		return "ON"
	case 2:
		return "HIGH"
	default:
		return "ON"
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
	a.onFrame = onFrame
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

func (a *AudioEngine) restartCapture(onFrame func(rms float64, speaking bool, pcm []byte)) {
	a.mu.Lock()
	if a.capturePipe != nil {
		_ = a.capturePipe.Close()
		a.capturePipe = nil
	}
	if a.captureCmd != nil && a.captureCmd.Process != nil {
		_ = a.captureCmd.Process.Kill()
		a.captureCmd = nil
	}
	a.mu.Unlock()

	a.startCapture(onFrame)
}

func (a *AudioEngine) restartPlayback() {
	a.mu.Lock()
	if a.playbackPipe != nil {
		_ = a.playbackPipe.Close()
		a.playbackPipe = nil
	}
	if a.playbackCmd != nil && a.playbackCmd.Process != nil {
		_ = a.playbackCmd.Process.Kill()
		a.playbackCmd = nil
	}
	a.mu.Unlock()

	a.startPlayback()
}

func (a *AudioEngine) startCapture(onFrame func(rms float64, speaking bool, pcm []byte)) {
	// 1. Try native Windows audio capture (winmm waveIn)
	if a.startWindowsCapture(onFrame) {
		return
	}

	var chosenDeviceID string
	a.mu.RLock()
	if a.SelectedInputIdx >= 0 && a.SelectedInputIdx < len(a.InputDevices) {
		chosenDeviceID = a.InputDevices[a.SelectedInputIdx].ID
	}
	a.mu.RUnlock()

	// 2. Try macOS specific audio capture (avfoundation ffmpeg or sox/rec)
	if runtime.GOOS == "darwin" {
		var darwinCmds []*exec.Cmd
		if p := findAudioTool("rec"); p != "" {
			darwinCmds = append(darwinCmds, exec.Command(p, "-q", "-r", "16000", "-c", "1", "-b", "16", "-e", "signed-integer", "-t", "raw", "-"))
		}
		if p := findAudioTool("sox"); p != "" {
			darwinCmds = append(darwinCmds, exec.Command(p, "-q", "-d", "-r", "16000", "-c", "1", "-b", "16", "-e", "signed-integer", "-t", "raw", "-"))
		}
		if p := findAudioTool("ffmpeg"); p != "" {
			inputArg := ":default"
			if chosenDeviceID != "" && chosenDeviceID != "default" {
				inputArg = ":" + chosenDeviceID
			}
			darwinCmds = append(darwinCmds,
				exec.Command(p, "-loglevel", "quiet", "-f", "avfoundation", "-i", inputArg, "-ar", "16000", "-ac", "1", "-f", "s16le", "pipe:1"),
				exec.Command(p, "-loglevel", "quiet", "-f", "avfoundation", "-i", ":0", "-ar", "16000", "-ac", "1", "-f", "s16le", "pipe:1"),
			)
		}

		for _, cmd := range darwinCmds {
			stdout, err := cmd.StdoutPipe()
			if err == nil && cmd.Start() == nil {
				a.mu.Lock()
				a.captureCmd = cmd
				a.capturePipe = stdout
				a.mu.Unlock()
				go a.readCaptureLoop(stdout, onFrame)
				return
			}
		}
	}

	// 3. Try Linux command-line capture tools with selected device ID
	var cmd *exec.Cmd
	if p := findAudioTool("parec"); p != "" {
		if chosenDeviceID != "" && chosenDeviceID != "default" {
			cmd = exec.Command(p, "-d", chosenDeviceID, "--rate=16000", "--channels=1", "--format=s16le", "--latency-msec=20")
		} else {
			cmd = exec.Command(p, "--rate=16000", "--channels=1", "--format=s16le", "--latency-msec=20")
		}
	} else if p := findAudioTool("pw-record"); p != "" {
		if chosenDeviceID != "" && chosenDeviceID != "default" {
			cmd = exec.Command(p, "--target", chosenDeviceID, "--rate", "16000", "--channels", "1", "--format", "s16", "-")
		} else {
			cmd = exec.Command(p, "--rate", "16000", "--channels", "1", "--format", "s16", "-")
		}
	} else if p := findAudioTool("rec"); p != "" {
		cmd = exec.Command(p, "-q", "-r", "16000", "-c", "1", "-b", "16", "-e", "signed-integer", "-t", "raw", "-")
		if chosenDeviceID != "" && chosenDeviceID != "default" {
			cmd.Env = append(os.Environ(), "AUDIODEV="+chosenDeviceID)
		}
	} else if p := findAudioTool("sox"); p != "" {
		cmd = exec.Command(p, "-q", "-d", "-r", "16000", "-c", "1", "-b", "16", "-e", "signed-integer", "-t", "raw", "-")
		if chosenDeviceID != "" && chosenDeviceID != "default" {
			cmd.Env = append(os.Environ(), "AUDIODEV="+chosenDeviceID)
		}
	} else if p := findAudioTool("arecord"); p != "" {
		if chosenDeviceID != "" && chosenDeviceID != "default" {
			cmd = exec.Command(p, "-D", chosenDeviceID, "-q", "-r", "16000", "-f", "S16_LE", "-c", "1", "-t", "raw")
		} else {
			cmd = exec.Command(p, "-q", "-r", "16000", "-f", "S16_LE", "-c", "1", "-t", "raw")
		}
	}

	if cmd != nil {
		stdout, err := cmd.StdoutPipe()
		if err == nil && cmd.Start() == nil {
			a.mu.Lock()
			a.captureCmd = cmd
			a.capturePipe = stdout
			a.mu.Unlock()
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
		inputMode := a.InputMode
		isPTT := a.IsPTTActive || time.Now().Before(a.PTTReleaseTime)

		var chunk []byte
		var finalRMS float64
		var speaking bool

		if muted || (inputMode == InputModePushToTalk && !isPTT) {
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
				// OFF (Bypass Mode): Direct clean audio with basic VAD
				rawRMS := calculateRMS(processed)
				speaking = rawRMS > a.VADThreshold
				finalRMS = rawRMS
				chunk = make([]byte, len(processed))
				copy(chunk, processed)
			} else {
				// ON / HIGH: Pristine linear-phase noise suppression & speech clarity enhancement
				var cleaned []byte
				speaking, finalRMS, cleaned = a.processNoiseCancellation(processed, suppressMode)
				chunk = cleaned
			}

			if inputMode == InputModePushToTalk && isPTT {
				speaking = true
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

// processNoiseCancellation performs full-spectrum, phase-linear voice enhancement:
// 1. High-Pass Filter (75 Hz) to eliminate desk thumps & DC offset without cutting vocal warmth
// 2. Adaptive noise floor tracking on silence
// 3. Transient isolation and intelligent VAD with generous hangover
// 4. Transparent dynamic spectral gate (OFF: 100%, ON: -18dB noise floor reduction, HIGH: -36dB)
// 5. Headroom vocal limiter with transparent saturation above 30,000 for crystal-clear clarity.
func (a *AudioEngine) processNoiseCancellation(pcm []byte, mode int) (bool, float64, []byte) {
	sampleCount := len(pcm) / 2
	if sampleCount != 320 {
		return false, 0, pcm
	}

	// 1. High-Pass Filter (75 Hz) - removes sub-audible DC offset & desk thumps without muffling voice
	filtered := make([]float64, 320)
	const hpAlpha = 0.970
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

	// 2. Transient Click Isolation (sharp mechanical clicks with no vocal energy)
	hRatio := highFreqEnergy / (frameEnergy + 1.0)
	peakToRMS := maxPeak / ((frameRMS * 32768.0) + 1.0)
	isTransientClick := (hRatio > 3.0 && peakToRMS > 7.0 && frameRMS < a.VADThreshold*2.0)

	// 3. Adaptive Noise Floor Tracking
	// Active human speech must NOT pollute the noise floor estimate.
	if !a.IsSpeaking {
		if a.noiseFloor <= 0 || math.IsNaN(a.noiseFloor) {
			a.noiseFloor = 0.001
		}
		if frameRMS < a.noiseFloor {
			a.noiseFloor = a.noiseFloor*0.85 + frameRMS*0.15
		} else {
			a.noiseFloor = a.noiseFloor*0.98 + frameRMS*0.02
		}
	} else {
		if frameRMS < a.noiseFloor {
			a.noiseFloor = frameRMS
		}
	}

	// 4. Voice Activity Detection (VAD) with Acoustic Echo Suppression (AES)
	threshold := a.VADThreshold

	// Dynamic Acoustic Echo Suppression: When speakers are playing sound,
	// dynamically duck the microphone threshold to prevent acoustic feedback loop!
	if a.lastPlaybackRMS > 0.001 {
		echoDucker := a.lastPlaybackRMS * 0.80
		if echoDucker > threshold {
			threshold = echoDucker
		}
		a.lastPlaybackRMS *= 0.88 // smooth decay
	}

	snrFloor := math.Max(a.noiseFloor, 0.0005)
	snr := frameRMS / snrFloor

	var isSpeech bool
	if mode == 2 { // HIGH (Aggressive mode for noisy rooms)
		isSpeech = (frameRMS > threshold*1.25 && snr > 1.25 && !isTransientClick)
	} else { // ON (Standard mode - natural, warm, crisp full-spectrum voice)
		isSpeech = (frameRMS > threshold && snr > 1.08 && !isTransientClick)
	}

	speaking := false
	if isSpeech {
		a.speechHangover = 25 // ~500ms hangover to retain word endings and natural pauses
		speaking = true
	} else {
		if a.speechHangover > 0 {
			a.speechHangover--
			speaking = true
		} else {
			speaking = false
		}
	}

	// 5. Target Gain Calculation
	var targetGain float64
	if speaking {
		// When speaking: pass 100% full-bandwidth audio with pristine linear-phase fidelity
		targetGain = 1.0
	} else {
		// When silent: smoothly attenuate background noise without phase distortion
		if mode == 1 { // Standard: natural -18dB noise floor attenuation
			targetGain = 0.12
		} else { // HIGH: -36dB aggressive gate
			targetGain = 0.02
		}
	}

	// Smooth gain envelope ramping (fast attack ~5ms, smooth release ~150ms)
	var alpha float64
	if targetGain > a.gateGain {
		alpha = 0.80
	} else {
		alpha = 0.15
	}
	a.gateGain = a.gateGain + (targetGain-a.gateGain)*alpha

	// 6. Apply Gain & Soft-Knee Peak Vocal Limiter with High Headroom
	outBytes := make([]byte, AudioChunkSize)
	var sumSquares float64
	g := a.gateGain

	for i := 0; i < 320; i++ {
		sample := filtered[i] * g

		// Soft compressor curve above 30000 to preserve full vocal punch without digital clipping
		if sample > 30000.0 {
			sample = 30000.0 + (sample-30000.0)*0.30
		} else if sample < -30000.0 {
			sample = -30000.0 + (sample+30000.0)*0.30
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
	if !speaking && a.gateGain < 0.15 {
		finalRMS = 0
	}

	return speaking, finalRMS, outBytes
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
	a.mu.Lock()
	if a.playbackPipe != nil {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	// 1. Try native Windows audio playback (winmm waveOut)
	if a.startWindowsPlayback() {
		return
	}

	var chosenOutputID string
	a.mu.RLock()
	if a.SelectedOutputIdx >= 0 && a.SelectedOutputIdx < len(a.OutputDevices) {
		chosenOutputID = a.OutputDevices[a.SelectedOutputIdx].ID
	}
	a.mu.RUnlock()

	// 2. Try macOS specific audio playback (mpv, sox/play, ffplay)
	if runtime.GOOS == "darwin" {
		var darwinCmds []*exec.Cmd
		if p := findAudioTool("mpv"); p != "" {
			darwinCmds = append(darwinCmds, exec.Command(p, "--really-quiet", "--no-video", "--idle=yes", "--keep-open=yes", "--profile=low-latency", "--untimed", "--cache=no", "--no-cache", "--demuxer=rawaudio", "--demuxer-rawaudio-rate=16000", "--demuxer-rawaudio-channels=1", "--demuxer-rawaudio-format=s16le", "-"))
		}
		if p := findAudioTool("play"); p != "" {
			darwinCmds = append(darwinCmds, exec.Command(p, "-q", "-t", "raw", "-r", "16000", "-c", "1", "-b", "16", "-e", "signed-integer", "-"))
		}
		if p := findAudioTool("sox"); p != "" {
			darwinCmds = append(darwinCmds, exec.Command(p, "-q", "-t", "raw", "-r", "16000", "-c", "1", "-b", "16", "-e", "signed-integer", "-", "-d"))
		}
		if p := findAudioTool("ffplay"); p != "" {
			darwinCmds = append(darwinCmds, exec.Command(p, "-loglevel", "quiet", "-nodisp", "-f", "s16le", "-ar", "16000", "-ac", "1", "-probesize", "32", "-analyzeduration", "0", "-fflags", "nobuffer", "-flags", "low_delay", "-i", "pipe:0"))
		}

		for _, cmd := range darwinCmds {
			stdin, err := cmd.StdinPipe()
			if err == nil && cmd.Start() == nil {
				a.mu.Lock()
				a.playbackCmd = cmd
				a.playbackPipe = stdin
				a.mu.Unlock()
				return
			}
		}
	}

	// 3. Try Linux command-line playback tools with selected device ID
	var cmd *exec.Cmd
	if p := findAudioTool("pacat"); p != "" {
		if chosenOutputID != "" && chosenOutputID != "default" {
			cmd = exec.Command(p, "-d", chosenOutputID, "--playback", "--rate=16000", "--channels=1", "--format=s16le", "--latency-msec=20")
		} else {
			cmd = exec.Command(p, "--playback", "--rate=16000", "--channels=1", "--format=s16le", "--latency-msec=20")
		}
	} else if p := findAudioTool("pw-play"); p != "" {
		if chosenOutputID != "" && chosenOutputID != "default" {
			cmd = exec.Command(p, "--target", chosenOutputID, "--rate", "16000", "--channels", "1", "--format", "s16", "-")
		} else {
			cmd = exec.Command(p, "--rate", "16000", "--channels", "1", "--format", "s16", "-")
		}
	} else if p := findAudioTool("ffplay"); p != "" {
		cmd = exec.Command(p, "-loglevel", "quiet", "-nodisp", "-f", "s16le", "-ar", "16000", "-ac", "1", "-probesize", "32", "-analyzeduration", "0", "-fflags", "nobuffer", "-flags", "low_delay", "-i", "pipe:0")
	} else if p := findAudioTool("mpv"); p != "" {
		cmd = exec.Command(p, "--really-quiet", "--no-video", "--idle=yes", "--keep-open=yes", "--profile=low-latency", "--untimed", "--cache=no", "--no-cache", "--demuxer=rawaudio", "--demuxer-rawaudio-rate=16000", "--demuxer-rawaudio-channels=1", "--demuxer-rawaudio-format=s16le", "-")
	} else if p := findAudioTool("play"); p != "" {
		cmd = exec.Command(p, "-q", "-r", "16000", "-c", "1", "-b", "16", "-e", "signed-integer", "-t", "raw", "-")
	} else if p := findAudioTool("sox"); p != "" {
		cmd = exec.Command(p, "-q", "-r", "16000", "-c", "1", "-b", "16", "-e", "signed-integer", "-t", "raw", "-", "-d")
	} else if p := findAudioTool("aplay"); p != "" {
		if chosenOutputID != "" && chosenOutputID != "default" {
			cmd = exec.Command(p, "-D", chosenOutputID, "-q", "-r", "16000", "-f", "S16_LE", "-c", "1", "-t", "raw")
		} else {
			cmd = exec.Command(p, "-q", "-r", "16000", "-f", "S16_LE", "-c", "1", "-t", "raw")
		}
	}

	if cmd != nil {
		stdin, err := cmd.StdinPipe()
		if err == nil && cmd.Start() == nil {
			a.mu.Lock()
			a.playbackCmd = cmd
			a.playbackPipe = stdin
			a.mu.Unlock()
		}
	}
}

func (a *AudioEngine) playbackMixerLoop() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	var silenceFramesLeft int

	for {
		select {
		case <-a.stopChan:
			return
		case <-ticker.C:
			a.mu.Lock()
			if a.playbackPipe == nil {
				a.mu.Unlock()
				a.startPlayback()
				continue
			}

			outputVol := a.OutputVolume

			// In test mode: only play local_loopback stream
			if a.InTestMode {
				jb := a.peerJitterBuffers["local_loopback"]
				if !a.Loopback || jb == nil {
					a.mu.Unlock()
					continue
				}
				chunk, ok := jb.Pop()
				pipe := a.playbackPipe
				a.mu.Unlock()

				if ok && pipe != nil {
					if _, err := pipe.Write(chunk); err != nil {
						a.mu.Lock()
						if a.playbackPipe == pipe {
							a.playbackPipe = nil
						}
						a.mu.Unlock()
						a.startPlayback()
					}
				}
				continue
			}

			// Normal room mode: check deafened
			if a.Deafened {
				a.mu.Unlock()
				continue
			}

			var streams [][]byte
			for id, jb := range a.peerJitterBuffers {
				if id == "local_loopback" {
					delete(a.peerJitterBuffers, id)
					continue
				}
				if chunk, ok := jb.Pop(); ok {
					streams = append(streams, chunk)
				}
			}
			pipe := a.playbackPipe
			a.mu.Unlock()

			if len(streams) > 0 && pipe != nil {
				silenceFramesLeft = 3 // keep 3 frames of comfort silence after active speech ends
				mixed := mixPCM(streams, AudioChunkSize/2, outputVol)
				var playEnergy float64
				for i := 0; i < len(mixed)/2; i++ {
					s := float64(int16(binary.LittleEndian.Uint16(mixed[i*2 : i*2+2])))
					playEnergy += s * s
				}
				playRMS := math.Sqrt(playEnergy/float64(len(mixed)/2)) / 32768.0
				a.mu.Lock()
				a.lastPlaybackRMS = playRMS
				a.mu.Unlock()

				if _, err := pipe.Write(mixed); err != nil {
					a.mu.Lock()
					if a.playbackPipe == pipe {
						a.playbackPipe = nil
					}
					a.mu.Unlock()
					a.startPlayback()
				}
			} else if silenceFramesLeft > 0 && pipe != nil {
				silenceFramesLeft--
				silenceFrame := make([]byte, AudioChunkSize)
				_, _ = pipe.Write(silenceFrame)
			}
		}
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

// PlayPeerPCM queues incoming audio data from a peer for real-time mixing and playback.
// Automatically applies dynamic voice leveling to boost quiet peers for optimal loudness and clarity.
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

	// Dynamic Peer Voice Leveling / AGC: If a peer's mic is quiet, gently boost it so they are loud and clear
	if rms > 0.002 && rms < 0.14 {
		boostFactor := math.Min(2.2, 0.16/math.Max(rms, 0.04))
		if boostFactor > 1.05 {
			pcm = applyGain(pcm, boostFactor)
		}
	}

	jb, exists := a.peerJitterBuffers[peerID]
	if !exists {
		jb = newPeerJitterBuffer(2) // 2 chunks (40ms) jitter cushion to eliminate pops and stutter
		a.peerJitterBuffers[peerID] = jb
	}
	jb.Push(pcm)
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
	a.Gain += delta
	if a.Gain < 0.0 {
		a.Gain = 0.0
	}
	if a.Gain > 3.0 {
		a.Gain = 3.0
	}
	if a.GainSliderState != nil {
		a.GainSliderState.Set(int(math.Round(a.Gain*100)), 0, 300)
	}
	return a.Gain
}

func (a *AudioEngine) AdjustOutputVolume(delta float64) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.OutputVolume += delta
	if a.OutputVolume < 0.0 {
		a.OutputVolume = 0.0
	}
	if a.OutputVolume > 2.0 {
		a.OutputVolume = 2.0
	}
	if a.OutputSliderState != nil {
		a.OutputSliderState.Set(int(math.Round(a.OutputVolume*100)), 0, 200)
	}
	return a.OutputVolume
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
		// Soft-knee saturation above 28000 to prevent harsh digital clipping
		if amplified > 28000.0 {
			amplified = 28000.0 + (amplified-28000.0)*0.35
		} else if amplified < -28000.0 {
			amplified = -28000.0 + (amplified+28000.0)*0.35
		}
		if amplified > 32767 {
			amplified = 32767
		} else if amplified < -32768 {
			amplified = -32768
		}
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
				s := int16(binary.LittleEndian.Uint16(stream[i*2 : i*2+2]))
				sum += float64(s)
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
		if sum > 32767 {
			sum = 32767
		} else if sum < -32768 {
			sum = -32768
		}
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(int16(sum)))
	}
	return out
}
