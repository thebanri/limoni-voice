//go:build darwin

package audioio

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// coreAudioBackend uses AudioToolbox AudioQueue services through purego (no cgo). It opens
// the system default devices; explicit device selection falls back to the tool backend.
type coreAudioBackend struct {
	once sync.Once
	err  error
}

func init() { register(&coreAudioBackend{}, priorityNative) }

type audioStreamBasicDescription struct {
	sampleRate       float64
	formatID         uint32
	formatFlags      uint32
	bytesPerPacket   uint32
	framesPerPacket  uint32
	bytesPerFrame    uint32
	channelsPerFrame uint32
	bitsPerChannel   uint32
	reserved         uint32
}

// audioQueueBuffer mirrors struct AudioQueueBuffer (LP64 layout).
type audioQueueBuffer struct {
	audioDataBytesCapacity    uint32
	_                         uint32
	audioData                 unsafe.Pointer
	audioDataByteSize         uint32
	_                         uint32
	userData                  unsafe.Pointer
	packetDescriptionCapacity uint32
	_                         uint32
	packetDescriptions        unsafe.Pointer
	packetDescriptionCount    uint32
}

const (
	kAudioFormatLinearPCM       = 0x6C70636D // 'lpcm'
	kAudioFormatFlagIsSignedInt = 1 << 2
	kAudioFormatFlagIsPacked    = 1 << 3
	coreAudioBufferCount        = 3
	coreAudioBufferBytes        = FrameSamples * 2
	audioToolboxPath            = "/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox"
)

var (
	aqNewInput     func(format *audioStreamBasicDescription, cb uintptr, userData uintptr, runLoop uintptr, runLoopMode uintptr, flags uint32, outAQ *uintptr) int32
	aqNewOutput    func(format *audioStreamBasicDescription, cb uintptr, userData uintptr, runLoop uintptr, runLoopMode uintptr, flags uint32, outAQ *uintptr) int32
	aqAllocate     func(aq uintptr, size uint32, out **audioQueueBuffer) int32
	aqEnqueue      func(aq uintptr, buf *audioQueueBuffer, numDescs uint32, descs uintptr) int32
	aqStart        func(aq uintptr, startTime uintptr) int32
	aqStop         func(aq uintptr, immediate bool) int32
	aqDispose      func(aq uintptr, immediate bool) int32
	inputCallback  uintptr
	outputCallback uintptr
	streamsMu      sync.Mutex
	streamsByID    = map[uintptr]*coreAudioStream{}
	nextStreamID   uintptr
)

func (b *coreAudioBackend) Name() string { return "coreaudio" }

func (b *coreAudioBackend) load() error {
	b.once.Do(func() {
		lib, err := purego.Dlopen(audioToolboxPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			b.err = err
			return
		}
		purego.RegisterLibFunc(&aqNewInput, lib, "AudioQueueNewInput")
		purego.RegisterLibFunc(&aqNewOutput, lib, "AudioQueueNewOutput")
		purego.RegisterLibFunc(&aqAllocate, lib, "AudioQueueAllocateBuffer")
		purego.RegisterLibFunc(&aqEnqueue, lib, "AudioQueueEnqueueBuffer")
		purego.RegisterLibFunc(&aqStart, lib, "AudioQueueStart")
		purego.RegisterLibFunc(&aqStop, lib, "AudioQueueStop")
		purego.RegisterLibFunc(&aqDispose, lib, "AudioQueueDispose")
		inputCallback = purego.NewCallback(func(userData uintptr, aq uintptr, buf *audioQueueBuffer, startTime uintptr, numPackets uint32, descs uintptr) {
			if s := lookupStream(userData); s != nil {
				s.onInput(aq, buf)
			}
		})
		outputCallback = purego.NewCallback(func(userData uintptr, aq uintptr, buf *audioQueueBuffer) {
			if s := lookupStream(userData); s != nil {
				s.onOutput(aq, buf)
			}
		})
	})
	return b.err
}

func (b *coreAudioBackend) Available() bool {
	if os.Getenv("LIMONI_AUDIO_BACKEND") == "exec" {
		return false
	}
	return b.load() == nil
}

func (b *coreAudioBackend) InputDevices() ([]Device, error) {
	return []Device{{ID: "default", Name: "Default System Microphone", IsDefault: true}}, nil
}

func (b *coreAudioBackend) OutputDevices() ([]Device, error) {
	return []Device{{ID: "default", Name: "Default System Output / Speakers", IsDefault: true}}, nil
}

func lookupStream(id uintptr) *coreAudioStream {
	streamsMu.Lock()
	defer streamsMu.Unlock()
	return streamsByID[id]
}

type coreAudioStream struct {
	id      uintptr
	aq      uintptr
	capture *frameAccumulator
	render  *renderAdapter
	mu      sync.Mutex
	closed  bool
	samples []int16
}

func (s *coreAudioStream) Backend() string { return "coreaudio" }

func pcmFormat() *audioStreamBasicDescription {
	return &audioStreamBasicDescription{
		sampleRate:       SampleRate,
		formatID:         kAudioFormatLinearPCM,
		formatFlags:      kAudioFormatFlagIsSignedInt | kAudioFormatFlagIsPacked,
		bytesPerPacket:   2,
		framesPerPacket:  1,
		bytesPerFrame:    2,
		channelsPerFrame: 1,
		bitsPerChannel:   16,
	}
}

func (s *coreAudioStream) onInput(aq uintptr, buf *audioQueueBuffer) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	if n := int(buf.audioDataByteSize) / 2; n > 0 && buf.audioData != nil {
		s.capture.push(unsafe.Slice((*int16)(buf.audioData), n))
	}
	aqEnqueue(aq, buf, 0, 0)
}

func (s *coreAudioStream) onOutput(aq uintptr, buf *audioQueueBuffer) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed || buf.audioData == nil {
		return
	}
	n := int(buf.audioDataBytesCapacity) / 2
	s.render.fill(unsafe.Slice((*int16)(buf.audioData), n))
	buf.audioDataByteSize = uint32(n * 2)
	aqEnqueue(aq, buf, 0, 0)
}

func newStreamID(s *coreAudioStream) {
	streamsMu.Lock()
	defer streamsMu.Unlock()
	nextStreamID++
	s.id = nextStreamID
	streamsByID[s.id] = s
}

func (b *coreAudioBackend) OpenCapture(deviceID string, cb CaptureFunc) (Stream, error) {
	if !isDefault(deviceID) {
		return nil, fmt.Errorf("coreaudio: only the default input device is supported")
	}
	if err := b.load(); err != nil {
		return nil, err
	}
	s := &coreAudioStream{capture: newFrameAccumulator(cb)}
	newStreamID(s)
	if st := aqNewInput(pcmFormat(), inputCallback, s.id, 0, 0, 0, &s.aq); st != 0 {
		s.forget()
		return nil, fmt.Errorf("coreaudio: AudioQueueNewInput failed (%d); grant microphone access to the terminal", st)
	}
	for i := 0; i < coreAudioBufferCount; i++ {
		var buf *audioQueueBuffer
		if st := aqAllocate(s.aq, coreAudioBufferBytes, &buf); st != 0 {
			s.Close()
			return nil, fmt.Errorf("coreaudio: AudioQueueAllocateBuffer failed (%d)", st)
		}
		aqEnqueue(s.aq, buf, 0, 0)
	}
	if st := aqStart(s.aq, 0); st != 0 {
		s.Close()
		return nil, fmt.Errorf("coreaudio: AudioQueueStart failed (%d)", st)
	}
	return s, nil
}

func (b *coreAudioBackend) OpenPlayback(deviceID string, cb RenderFunc) (Stream, error) {
	if !isDefault(deviceID) {
		return nil, fmt.Errorf("coreaudio: only the default output device is supported")
	}
	if err := b.load(); err != nil {
		return nil, err
	}
	s := &coreAudioStream{render: newRenderAdapter(cb)}
	newStreamID(s)
	if st := aqNewOutput(pcmFormat(), outputCallback, s.id, 0, 0, 0, &s.aq); st != 0 {
		s.forget()
		return nil, fmt.Errorf("coreaudio: AudioQueueNewOutput failed (%d)", st)
	}
	for i := 0; i < coreAudioBufferCount; i++ {
		var buf *audioQueueBuffer
		if st := aqAllocate(s.aq, coreAudioBufferBytes, &buf); st != 0 {
			s.Close()
			return nil, fmt.Errorf("coreaudio: AudioQueueAllocateBuffer failed (%d)", st)
		}
		s.onOutput(s.aq, buf) // prime the queue
	}
	if st := aqStart(s.aq, 0); st != 0 {
		s.Close()
		return nil, fmt.Errorf("coreaudio: AudioQueueStart failed (%d)", st)
	}
	return s, nil
}

func (s *coreAudioStream) forget() {
	streamsMu.Lock()
	delete(streamsByID, s.id)
	streamsMu.Unlock()
}

func (s *coreAudioStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.aq != 0 {
		aqStop(s.aq, true)
		aqDispose(s.aq, true)
	}
	s.forget()
	return nil
}
