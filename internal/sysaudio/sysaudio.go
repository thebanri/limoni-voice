// Package sysaudio captures what the computer is playing (system / desktop audio) for screen
// sharing: the PulseAudio / PipeWire monitor of the default output on Linux and WASAPI loopback
// on Windows. On macOS system audio comes from the ScreenCaptureKit capture helper instead.
// On Linux OpenApp narrows the capture to the streams of one application.
//
// Captured audio still contains Limoni Voice's own playback (other participants' voices); the
// caller removes it with an echo canceller that uses the rendered mix as reference.
package sysaudio

import (
	"errors"
	"sync"
	"time"
)

// Audio format delivered to callbacks.
const (
	SampleRate   = 48000
	FrameSamples = 960 // 20 ms mono
)

// ErrUnsupported is returned where system audio is captured by other means (macOS) or not at all.
var ErrUnsupported = errors.New("sysaudio: system audio capture is not available on this platform")

// Stream is a running system audio capture.
type Stream interface {
	Close() error
	Backend() string
	// Frames reports how many audio frames were captured so far. Loopback capture stays at
	// zero while the computer plays nothing.
	Frames() uint64
}

// FrameFunc receives 20 ms mono frames at SampleRate. The slice is reused after the call.
type FrameFunc func(frame []int16)

// Open starts capturing system audio.
func Open(onFrame FrameFunc) (Stream, error) {
	return open(onFrame)
}

// framer turns arbitrary sized sample batches into fixed frames.
type framer struct {
	mu   sync.Mutex
	buf  []int16
	emit FrameFunc
}

func newFramer(emit FrameFunc) *framer {
	return &framer{buf: make([]int16, 0, FrameSamples), emit: emit}
}

func (f *framer) push(samples []int16) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(samples) > 0 {
		n := min(FrameSamples-len(f.buf), len(samples))
		f.buf = append(f.buf, samples[:n]...)
		samples = samples[n:]
		if len(f.buf) == FrameSamples {
			f.emit(f.buf)
			f.buf = f.buf[:0]
		}
	}
}

// resampler converts mono float samples from an arbitrary rate to SampleRate with linear
// interpolation (adequate for desktop audio monitoring; no rate change is the common case).
type resampler struct {
	ratio float64 // input samples per output sample
	pos   float64
	prev  float64
}

func newResampler(inRate int) *resampler {
	return &resampler{ratio: float64(inRate) / SampleRate}
}

func (r *resampler) process(in []float64, out []int16) []int16 {
	if r.ratio == 1 {
		for _, v := range in {
			out = append(out, clamp16(v))
		}
		return out
	}
	for _, cur := range in {
		for r.pos < 1 {
			v := r.prev + (cur-r.prev)*r.pos
			out = append(out, clamp16(v))
			r.pos += r.ratio
		}
		r.pos--
		r.prev = cur
	}
	return out
}

func clamp16(v float64) int16 {
	v *= 32767
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// App identifies the application whose audio OpenApp captures.
type App struct {
	PID  int    // a process of the application; its descendants count too
	Name string // executable name; also matches the application's other processes
}

// OpenApp captures only what one application plays, not the whole output. Browsers play from
// a helper process, so a stream matches when its process descends from app.PID or runs the
// same executable. It returns ErrUnsupported where per-application capture is not available.
func OpenApp(app App, onFrame FrameFunc) (Stream, error) {
	if app.PID <= 1 && app.Name == "" {
		return nil, errors.New("sysaudio: no application to capture")
	}
	return openApp(app, onFrame)
}

// maxMixBacklog bounds how much a stream that runs ahead of the clock stream may queue.
const maxMixBacklog = 5 * FrameSamples

// mixer sums several capture streams into 20 ms frames. One stream sets the pace (the
// clock); the others contribute whatever they have queued when it completes a frame, so the
// output never runs faster than real time however many streams there are.
type mixer struct {
	mu    sync.Mutex
	srcs  map[uint32]*mixSource
	clock uint32
	has   bool
	acc   []int32
	out   []int16
	emit  FrameFunc
}

type mixSource struct {
	buf  []int16
	last time.Time
}

func newMixer(emit FrameFunc) *mixer {
	return &mixer{
		srcs: map[uint32]*mixSource{},
		acc:  make([]int32, FrameSamples),
		out:  make([]int16, FrameSamples),
		emit: emit,
	}
}

func (m *mixer) add(id uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.srcs[id] == nil {
		m.srcs[id] = &mixSource{}
	}
}

func (m *mixer) remove(id uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.srcs, id)
	if m.has && m.clock == id {
		m.has = false
	}
}

// push queues samples of stream id and emits every frame the clock stream completes. A
// stream becomes the clock when there is none or the clock has gone quiet (paused).
func (m *mixer) push(id uint32, samples []int16, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.srcs[id]
	if src == nil {
		return
	}
	src.buf = append(src.buf, samples...)
	if over := len(src.buf) - maxMixBacklog; over > 0 {
		src.buf = append(src.buf[:0], src.buf[over:]...)
	}
	src.last = now
	if clock := m.srcs[m.clock]; !m.has || clock == nil || now.Sub(clock.last) > 60*time.Millisecond {
		m.clock, m.has = id, true
	}
	if m.clock != id {
		return
	}
	for len(src.buf) >= FrameSamples {
		clear(m.acc)
		for _, s := range m.srcs {
			n := min(FrameSamples, len(s.buf))
			for i, v := range s.buf[:n] {
				m.acc[i] += int32(v)
			}
			s.buf = append(s.buf[:0], s.buf[n:]...)
		}
		for i, v := range m.acc {
			m.out[i] = int16(max(-32768, min(32767, v)))
		}
		m.emit(m.out)
	}
}
