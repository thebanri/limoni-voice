//go:build windows

package audioio

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"syscall"
	"unsafe"
)

// winmmBackend uses the classic waveIn/waveOut API (available on every Windows version,
// no dependencies). The wave mapper converts to the device format when needed.
type winmmBackend struct{}

func init() { register(winmmBackend{}, priorityNative) }

var (
	modwinmm                   = syscall.NewLazyDLL("winmm.dll")
	procWaveInOpen             = modwinmm.NewProc("waveInOpen")
	procWaveInClose            = modwinmm.NewProc("waveInClose")
	procWaveInPrepareHeader    = modwinmm.NewProc("waveInPrepareHeader")
	procWaveInUnprepareHeader  = modwinmm.NewProc("waveInUnprepareHeader")
	procWaveInAddBuffer        = modwinmm.NewProc("waveInAddBuffer")
	procWaveInStart            = modwinmm.NewProc("waveInStart")
	procWaveInStop             = modwinmm.NewProc("waveInStop")
	procWaveInReset            = modwinmm.NewProc("waveInReset")
	procWaveInGetNumDevs       = modwinmm.NewProc("waveInGetNumDevs")
	procWaveInGetDevCapsW      = modwinmm.NewProc("waveInGetDevCapsW")
	procWaveOutOpen            = modwinmm.NewProc("waveOutOpen")
	procWaveOutClose           = modwinmm.NewProc("waveOutClose")
	procWaveOutPrepareHeader   = modwinmm.NewProc("waveOutPrepareHeader")
	procWaveOutUnprepareHeader = modwinmm.NewProc("waveOutUnprepareHeader")
	procWaveOutWrite           = modwinmm.NewProc("waveOutWrite")
	procWaveOutReset           = modwinmm.NewProc("waveOutReset")
	procWaveOutGetNumDevs      = modwinmm.NewProc("waveOutGetNumDevs")
	procWaveOutGetDevCapsW     = modwinmm.NewProc("waveOutGetDevCapsW")

	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procCreateEvent         = kernel32.NewProc("CreateEventW")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
	procWaitForSingleObject = kernel32.NewProc("WaitForSingleObject")
)

const (
	waveFormatPCM = 1
	waveMapper    = ^uintptr(0)
	callbackEvent = 0x00050000
	whdrDone      = 0x00000001
	whdrPrepared  = 0x00000002
	whdrInQueue   = 0x00000010
	winmmBuffers  = 4
)

type waveFormatEx struct {
	wFormatTag      uint16
	nChannels       uint16
	nSamplesPerSec  uint32
	nAvgBytesPerSec uint32
	nBlockAlign     uint16
	wBitsPerSample  uint16
	cbSize          uint16
}

type waveHdr struct {
	lpData          uintptr
	dwBufferLength  uint32
	dwBytesRecorded uint32
	dwUser          uintptr
	dwFlags         uint32
	dwLoops         uint32
	lpNext          uintptr
	reserved        uintptr
}

type waveInCapsW struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [32]uint16
	dwFormats      uint32
	wChannels      uint16
	wReserved1     uint16
}

type waveOutCapsW struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [32]uint16
	dwFormats      uint32
	wChannels      uint16
	wReserved1     uint16
	dwSupport      uint32
}

func (winmmBackend) Name() string { return "winmm" }

func (winmmBackend) Available() bool {
	return os.Getenv("LIMONI_AUDIO_BACKEND") != "exec" && procWaveInOpen.Find() == nil
}

func (winmmBackend) InputDevices() ([]Device, error) {
	devs := []Device{{ID: "default", Name: "Default Microphone (Windows Preferred)", IsDefault: true}}
	num, _, _ := procWaveInGetNumDevs.Call()
	for i := uintptr(0); i < num; i++ {
		var caps waveInCapsW
		if ret, _, _ := procWaveInGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps)); ret == 0 {
			name := syscall.UTF16ToString(caps.szPname[:])
			if name == "" {
				name = fmt.Sprintf("Microphone Device %d", i)
			}
			devs = append(devs, Device{ID: strconv.Itoa(int(i)), Name: name})
		}
	}
	return devs, nil
}

func (winmmBackend) OutputDevices() ([]Device, error) {
	devs := []Device{{ID: "default", Name: "Default Output / Speakers (Windows Preferred)", IsDefault: true}}
	num, _, _ := procWaveOutGetNumDevs.Call()
	for i := uintptr(0); i < num; i++ {
		var caps waveOutCapsW
		if ret, _, _ := procWaveOutGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps)); ret == 0 {
			name := syscall.UTF16ToString(caps.szPname[:])
			if name == "" {
				name = fmt.Sprintf("Speaker Device %d", i)
			}
			devs = append(devs, Device{ID: strconv.Itoa(int(i)), Name: name})
		}
	}
	return devs, nil
}

func pcmWaveFormat() waveFormatEx {
	return waveFormatEx{wFormatTag: waveFormatPCM, nChannels: 1, nSamplesPerSec: SampleRate, nAvgBytesPerSec: SampleRate * 2, nBlockAlign: 2, wBitsPerSample: 16}
}

func deviceIndex(deviceID string, count uintptr) uintptr {
	if isDefault(deviceID) {
		return waveMapper
	}
	if idx, err := strconv.Atoi(deviceID); err == nil && idx >= 0 && uintptr(idx) < count {
		return uintptr(idx)
	}
	return waveMapper
}

type winmmStream struct {
	handle  uintptr
	event   uintptr
	headers []waveHdr
	buffers [][]byte
	done    chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
	input   bool
}

func (s *winmmStream) Backend() string { return "winmm" }

func (s *winmmStream) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.wg.Wait()
		if s.input {
			procWaveInStop.Call(s.handle)
			procWaveInReset.Call(s.handle)
			for i := range s.headers {
				procWaveInUnprepareHeader.Call(s.handle, uintptr(unsafe.Pointer(&s.headers[i])), unsafe.Sizeof(s.headers[i]))
			}
			procWaveInClose.Call(s.handle)
		} else {
			procWaveOutReset.Call(s.handle)
			for i := range s.headers {
				procWaveOutUnprepareHeader.Call(s.handle, uintptr(unsafe.Pointer(&s.headers[i])), unsafe.Sizeof(s.headers[i]))
			}
			procWaveOutClose.Call(s.handle)
		}
		procCloseHandle.Call(s.event)
	})
	return nil
}

func newWinmmStream(input bool) *winmmStream {
	s := &winmmStream{input: input, done: make(chan struct{}), headers: make([]waveHdr, winmmBuffers), buffers: make([][]byte, winmmBuffers)}
	ev, _, _ := procCreateEvent.Call(0, 0, 0, 0)
	s.event = ev
	for i := range s.buffers {
		s.buffers[i] = make([]byte, FrameSamples*2)
		s.headers[i] = waveHdr{lpData: uintptr(unsafe.Pointer(&s.buffers[i][0])), dwBufferLength: uint32(FrameSamples * 2)}
	}
	return s
}

func (winmmBackend) OpenCapture(deviceID string, cb CaptureFunc) (Stream, error) {
	num, _, _ := procWaveInGetNumDevs.Call()
	if num == 0 {
		return nil, fmt.Errorf("winmm: no capture devices")
	}
	s := newWinmmStream(true)
	wfx := pcmWaveFormat()
	if ret, _, _ := procWaveInOpen.Call(uintptr(unsafe.Pointer(&s.handle)), deviceIndex(deviceID, num), uintptr(unsafe.Pointer(&wfx)), s.event, 0, callbackEvent); ret != 0 {
		procCloseHandle.Call(s.event)
		return nil, fmt.Errorf("winmm: waveInOpen failed (%d)", ret)
	}
	for i := range s.headers {
		procWaveInPrepareHeader.Call(s.handle, uintptr(unsafe.Pointer(&s.headers[i])), unsafe.Sizeof(s.headers[i]))
		procWaveInAddBuffer.Call(s.handle, uintptr(unsafe.Pointer(&s.headers[i])), unsafe.Sizeof(s.headers[i]))
	}
	if ret, _, _ := procWaveInStart.Call(s.handle); ret != 0 {
		s.Close()
		return nil, fmt.Errorf("winmm: waveInStart failed (%d)", ret)
	}
	acc := newFrameAccumulator(cb)
	var carry []byte
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-s.done:
				return
			default:
			}
			procWaitForSingleObject.Call(s.event, 50)
			for i := range s.headers {
				h := &s.headers[i]
				if h.dwFlags&whdrDone == 0 {
					continue
				}
				if n := int(h.dwBytesRecorded); n > 0 {
					acc.pushBytes(s.buffers[i][:n], &carry)
				}
				procWaveInUnprepareHeader.Call(s.handle, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
				h.dwBytesRecorded, h.dwFlags = 0, 0
				procWaveInPrepareHeader.Call(s.handle, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
				procWaveInAddBuffer.Call(s.handle, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
			}
		}
	}()
	return s, nil
}

func (winmmBackend) OpenPlayback(deviceID string, cb RenderFunc) (Stream, error) {
	num, _, _ := procWaveOutGetNumDevs.Call()
	if num == 0 {
		return nil, fmt.Errorf("winmm: no playback devices")
	}
	s := newWinmmStream(false)
	wfx := pcmWaveFormat()
	if ret, _, _ := procWaveOutOpen.Call(uintptr(unsafe.Pointer(&s.handle)), deviceIndex(deviceID, num), uintptr(unsafe.Pointer(&wfx)), s.event, 0, callbackEvent); ret != 0 {
		procCloseHandle.Call(s.event)
		return nil, fmt.Errorf("winmm: waveOutOpen failed (%d)", ret)
	}
	adapter := newRenderAdapter(cb)
	submit := func(i int) {
		h := &s.headers[i]
		if h.dwFlags&whdrPrepared != 0 {
			procWaveOutUnprepareHeader.Call(s.handle, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
		}
		adapter.fillBytes(s.buffers[i])
		h.dwBufferLength = uint32(len(s.buffers[i]))
		h.dwFlags = 0
		procWaveOutPrepareHeader.Call(s.handle, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
		procWaveOutWrite.Call(s.handle, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
	}
	// Keep three frames (60 ms) queued; each buffer is refilled when the device finishes it,
	// so rendering follows the device clock.
	for i := 0; i < 3; i++ {
		submit(i)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-s.done:
				return
			default:
			}
			procWaitForSingleObject.Call(s.event, 50)
			for i := 0; i < 3; i++ {
				h := &s.headers[i]
				if h.dwFlags&whdrDone != 0 && h.dwFlags&whdrInQueue == 0 {
					submit(i)
				}
			}
		}
	}()
	return s, nil
}
