//go:build windows

package sysaudio

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WASAPI loopback capture of the default render endpoint through raw COM calls (no cgo).

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")

	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = windows.GUID{Data1: 0x1CB9AD4C, Data2: 0xDBFA, Data3: 0x4C32, Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = windows.GUID{Data1: 0xC8ADBD64, Data2: 0xE71E, Data3: 0x48A0, Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

const (
	clsctxAll                 = 0x17
	eRender                   = 0
	eConsole                  = 0
	audclntShareModeShared    = 0
	audclntStreamFlagLoopback = 0x00020000
	audclntBufferFlagsSilent  = 0x2
	bufferDuration100ns       = 200 * 10000 // 200 ms shared buffer
)

// COM vtable slot indices.
const (
	enumGetDefaultAudioEndpoint = 4
	deviceActivate              = 3
	clientInitialize            = 3
	clientGetMixFormat          = 8
	clientStart                 = 10
	clientStop                  = 11
	clientGetService            = 14
	captureGetBuffer            = 3
	captureReleaseBuffer        = 4
	captureGetNextPacketSize    = 5
	unknownRelease              = 2
)

// comObj is the memory layout of a COM interface pointer target: a pointer to its vtable.
type comObj struct {
	vtbl *[16]uintptr
}

// comCall invokes vtable method idx on a COM object.
func comCall(obj *comObj, idx int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(obj.vtbl[idx], append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)...)
	return r
}

func hresult(r uintptr, what string) error {
	if int32(r) < 0 {
		return fmt.Errorf("sysaudio: %s failed: HRESULT 0x%08X", what, uint32(r))
	}
	return nil
}

func release(obj *comObj) {
	if obj != nil {
		comCall(obj, unknownRelease)
	}
}

type wasapiStream struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	backend string
	frames  uint64
}

func (s *wasapiStream) Backend() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backend
}

// Frames reports how many audio frames the loopback delivered. It stays at zero while the
// default output plays nothing: Windows delivers no loopback data from an idle endpoint.
func (s *wasapiStream) Frames() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames
}

func (s *wasapiStream) Close() error {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
	})
	return nil
}

func open(onFrame FrameFunc) (Stream, error) {
	s := &wasapiStream{stop: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error, 1)
	go s.run(onFrame, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return s, nil
}

// run keeps a loopback capture running. Switching the default output device invalidates the
// stream on Windows, so a failed session is simply reopened until Close.
func (s *wasapiStream) run(onFrame FrameFunc, ready chan<- error) {
	defer close(s.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if r, _, _ := procCoInitializeEx.Call(0, 0 /* COINIT_MULTITHREADED */); int32(r) < 0 && uint32(r) != 0x80010106 {
		ready <- hresult(r, "CoInitializeEx")
		return
	}
	defer procCoUninitialize.Call()

	first := true
	for {
		err := s.capture(onFrame, func() {
			if first {
				first = false
				ready <- nil
			}
		})
		if first { // never started: report why
			ready <- err
			return
		}
		select {
		case <-s.stop:
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// capture runs one loopback session and returns when it ends or fails.
func (s *wasapiStream) capture(onFrame FrameFunc, started func()) error {
	var enumerator *comObj
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enumerator)))
	if err := hresult(r, "CoCreateInstance(MMDeviceEnumerator)"); err != nil {
		return err
	}
	defer release(enumerator)

	var device *comObj
	if err := hresult(comCall(enumerator, enumGetDefaultAudioEndpoint, eRender, eConsole, uintptr(unsafe.Pointer(&device))), "GetDefaultAudioEndpoint"); err != nil {
		return err
	}
	defer release(device)

	var client *comObj
	if err := hresult(comCall(device, deviceActivate, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&client))), "IMMDevice.Activate"); err != nil {
		return err
	}
	defer release(client)

	var format *byte
	if err := hresult(comCall(client, clientGetMixFormat, uintptr(unsafe.Pointer(&format))), "GetMixFormat"); err != nil {
		return err
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(format)))
	// WAVEFORMATEX is byte packed on Windows, so read it from the raw block.
	head := unsafe.Slice(format, waveFormatExSize)
	blockLen := waveFormatExSize + int(binary.LittleEndian.Uint16(head[16:]))
	wf, err := parseWaveFormat(unsafe.Slice(format, blockLen))
	if err != nil {
		return err
	}
	channels, bits, rate, isFloat := wf.Channels, wf.Bits, wf.Rate, wf.Float

	if err := hresult(comCall(client, clientInitialize, audclntShareModeShared, audclntStreamFlagLoopback, bufferDuration100ns, 0, uintptr(unsafe.Pointer(format)), 0), "IAudioClient.Initialize(loopback)"); err != nil {
		return err
	}
	var capture *comObj
	if err := hresult(comCall(client, clientGetService, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture))), "GetService(IAudioCaptureClient)"); err != nil {
		return err
	}
	defer release(capture)
	if err := hresult(comCall(client, clientStart), "IAudioClient.Start"); err != nil {
		return err
	}
	defer comCall(client, clientStop)

	s.mu.Lock()
	s.backend = "wasapi loopback (" + wf.String() + ")"
	s.mu.Unlock()
	started()

	fr := newFramer(onFrame)
	rs := newResampler(rate)
	bytesPerSample := bits / 8
	var mono []float64
	var out []int16
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return nil
		case <-ticker.C:
		}
		for {
			var packet uint32
			if hr := comCall(capture, captureGetNextPacketSize, uintptr(unsafe.Pointer(&packet))); int32(hr) < 0 {
				return hresult(hr, "GetNextPacketSize") // device changed / stream invalidated
			} else if packet == 0 {
				break
			}
			var data *byte
			var frames uint32
			var flags uint32
			if hr := comCall(capture, captureGetBuffer, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), 0, 0); int32(hr) < 0 {
				return hresult(hr, "GetBuffer")
			}
			n := int(frames)
			mono = mono[:0]
			if flags&audclntBufferFlagsSilent != 0 || data == nil {
				for range n {
					mono = append(mono, 0)
				}
			} else {
				raw := unsafe.Slice(data, n*channels*bytesPerSample)
				for i := range n {
					var sum float64
					for c := range channels {
						off := (i*channels + c) * bytesPerSample
						sum += sampleAt(raw[off:off+bytesPerSample], isFloat, bits)
					}
					mono = append(mono, sum/float64(channels))
				}
			}
			comCall(capture, captureReleaseBuffer, uintptr(frames))
			s.mu.Lock()
			s.frames += uint64(n)
			s.mu.Unlock()
			out = rs.process(mono, out[:0])
			fr.push(out)
		}
	}
}

func sampleAt(b []byte, isFloat bool, bits int) float64 {
	switch {
	case isFloat:
		return float64(math.Float32frombits(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24))
	case bits == 16:
		return float64(int16(uint16(b[0])|uint16(b[1])<<8)) / 32768
	case bits == 24:
		v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
		return float64(v) / 8388608
	default:
		return float64(int32(uint32(b[0])|uint32(b[1])<<8|uint32(b[2])<<16|uint32(b[3])<<24)) / 2147483648
	}
}
