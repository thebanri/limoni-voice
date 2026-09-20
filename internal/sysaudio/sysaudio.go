// Package sysaudio captures what the computer is playing (system / desktop audio) for screen
// sharing: the PulseAudio / PipeWire monitor of the default output on Linux and WASAPI loopback
// on Windows. On macOS system audio comes from the ScreenCaptureKit capture helper instead.
//
// Captured audio still contains Limoni Voice's own playback (other participants' voices); the
// caller removes it with an echo canceller that uses the rendered mix as reference.
package sysaudio

import (
	"errors"
	"sync"
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
