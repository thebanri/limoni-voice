// Package audioio provides cross-platform, cgo-free audio capture and playback at the voice
// pipeline format (48 kHz, mono, 16-bit, 20 ms frames). Backends are tried in order of
// quality: native APIs (PulseAudio protocol, CoreAudio, winmm) first, then external tools.
package audioio

import (
	"errors"
	"sort"
	"sync"
)

const (
	SampleRate   = 48000
	FrameSamples = 960 // 20 ms
)

// Device is an input or output device.
type Device struct {
	ID        string
	Name      string
	IsDefault bool
}

// CaptureFunc receives exactly FrameSamples samples per call. The slice is reused.
type CaptureFunc func(frame []int16)

// RenderFunc must fill exactly FrameSamples samples per call. Calls are paced by the device clock
// for native backends.
type RenderFunc func(frame []int16)

// Stream is an open capture or playback stream.
type Stream interface {
	Close() error
	// Backend names the implementation ("pulse", "coreaudio", "winmm", "parec", ...).
	Backend() string
}

// Backend opens streams on one audio system.
type Backend interface {
	Name() string
	Available() bool
	InputDevices() ([]Device, error)
	OutputDevices() ([]Device, error)
	OpenCapture(deviceID string, cb CaptureFunc) (Stream, error)
	OpenPlayback(deviceID string, cb RenderFunc) (Stream, error)
}

var ErrUnavailable = errors.New("audioio: backend unavailable")

type registered struct {
	b        Backend
	priority int
}

var (
	registryMu sync.Mutex
	registry   []registered
)

// Backend priorities: native APIs before external tools.
const (
	priorityNative = 10
	priorityTools  = 100
)

// register adds a backend; lower priority values are tried first.
func register(b Backend, priority int) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, registered{b, priority})
	sort.SliceStable(registry, func(i, j int) bool { return registry[i].priority < registry[j].priority })
}

// Backends returns the registered backends in preference order.
func Backends() []Backend {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]Backend, len(registry))
	for i, r := range registry {
		out[i] = r.b
	}
	return out
}

// OpenCapture opens the first backend that can capture from deviceID ("" or "default" = default).
func OpenCapture(deviceID string, cb CaptureFunc) (Stream, error) {
	var lastErr error = ErrUnavailable
	for _, b := range Backends() {
		if !b.Available() {
			continue
		}
		s, err := b.OpenCapture(deviceID, cb)
		if err == nil {
			return s, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// OpenPlayback opens the first backend that can play to deviceID.
func OpenPlayback(deviceID string, cb RenderFunc) (Stream, error) {
	var lastErr error = ErrUnavailable
	for _, b := range Backends() {
		if !b.Available() {
			continue
		}
		s, err := b.OpenPlayback(deviceID, cb)
		if err == nil {
			return s, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// Devices lists devices from the first available backend that can enumerate them.
func Devices(input bool) []Device {
	for _, b := range Backends() {
		if !b.Available() {
			continue
		}
		var devs []Device
		var err error
		if input {
			devs, err = b.InputDevices()
		} else {
			devs, err = b.OutputDevices()
		}
		if err == nil && len(devs) > 0 {
			return devs
		}
	}
	return nil
}

func isDefault(id string) bool { return id == "" || id == "default" }

// frameAccumulator turns arbitrarily sized capture chunks into fixed frames.
type frameAccumulator struct {
	buf  []int16
	n    int
	emit CaptureFunc
}

func newFrameAccumulator(cb CaptureFunc) *frameAccumulator {
	return &frameAccumulator{buf: make([]int16, FrameSamples), emit: cb}
}

//lint:ignore U1000 used by the PulseAudio and CoreAudio backends, not by winmm
func (a *frameAccumulator) push(samples []int16) {
	for len(samples) > 0 {
		k := copy(a.buf[a.n:], samples)
		a.n += k
		samples = samples[k:]
		if a.n == FrameSamples {
			a.emit(a.buf)
			a.n = 0
		}
	}
}

func (a *frameAccumulator) pushBytes(data []byte, carry *[]byte) {
	if len(*carry) > 0 {
		data = append(*carry, data...)
		*carry = (*carry)[:0]
	}
	n := len(data) / 2
	for i := 0; i < n; {
		k := min(FrameSamples-a.n, n-i)
		for j := 0; j < k; j++ {
			a.buf[a.n+j] = int16(uint16(data[2*(i+j)]) | uint16(data[2*(i+j)+1])<<8)
		}
		a.n += k
		i += k
		if a.n == FrameSamples {
			a.emit(a.buf)
			a.n = 0
		}
	}
	if len(data)%2 == 1 {
		*carry = append(*carry, data[len(data)-1])
	}
}

// renderAdapter fills arbitrarily sized buffers from fixed rendered frames.
type renderAdapter struct {
	frame []int16
	pos   int
	cb    RenderFunc
}

func newRenderAdapter(cb RenderFunc) *renderAdapter {
	return &renderAdapter{frame: make([]int16, FrameSamples), pos: FrameSamples, cb: cb}
}

//lint:ignore U1000 used by the PulseAudio and CoreAudio backends, not by winmm
func (r *renderAdapter) fill(out []int16) {
	for len(out) > 0 {
		if r.pos == FrameSamples {
			r.cb(r.frame)
			r.pos = 0
		}
		k := copy(out, r.frame[r.pos:])
		r.pos += k
		out = out[k:]
	}
}

func (r *renderAdapter) fillBytes(out []byte) {
	n := len(out) / 2
	for i := 0; i < n; {
		if r.pos == FrameSamples {
			r.cb(r.frame)
			r.pos = 0
		}
		k := min(FrameSamples-r.pos, n-i)
		for j := 0; j < k; j++ {
			v := uint16(r.frame[r.pos+j])
			out[2*(i+j)] = byte(v)
			out[2*(i+j)+1] = byte(v >> 8)
		}
		r.pos += k
		i += k
	}
}
