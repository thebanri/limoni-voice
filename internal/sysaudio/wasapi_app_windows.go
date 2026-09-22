//go:build windows

package sysaudio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Per-application capture: WASAPI process loopback (Windows 10 2004 and later) records the
// audio of one process and its children, which is where browsers play from. The virtual
// loopback device is only reachable through ActivateAudioInterfaceAsync, whose completion
// handler is a small COM object implemented here with Go callbacks.

var (
	mmdevapi                        = windows.NewLazySystemDLL("mmdevapi.dll")
	procActivateAudioInterfaceAsync = mmdevapi.NewProc("ActivateAudioInterfaceAsync")

	iidIUnknown                                 = windows.GUID{Data1: 0x00000000, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIAgileObject                             = windows.GUID{Data1: 0x94EA2B94, Data2: 0xE9CC, Data3: 0x49E0, Data4: [8]byte{0xC0, 0xFF, 0xEE, 0x64, 0xCA, 0x8F, 0x5B, 0x90}}
	iidIActivateAudioInterfaceCompletionHandler = windows.GUID{Data1: 0x41D949AB, Data2: 0x9862, Data3: 0x444A, Data4: [8]byte{0x80, 0xF6, 0xC2, 0x61, 0x33, 0x4D, 0xA5, 0xEB}}
)

const (
	virtualProcessLoopback = `VAD\Process_Loopback`

	activationTypeProcessLoopback = 1
	loopbackIncludeProcessTree    = 0
	vtBlob                        = 65

	audclntStreamFlagEventCallback     = 0x00040000
	audclntStreamFlagSrcDefaultQuality = 0x08000000
	audclntStreamFlagAutoConvertPCM    = 0x80000000

	clientSetEventHandle       = 13
	operationGetActivateResult = 3
	unknownQueryInterface      = 0
	eNoInterface               = 0x80004002
)

// audioClientActivationParams is AUDIOCLIENT_ACTIVATION_PARAMS with its process loopback member.
type audioClientActivationParams struct {
	activationType uint32
	targetPID      uint32
	loopbackMode   uint32
}

// propVariantBlob is a PROPVARIANT holding a VT_BLOB; Go's field alignment matches the C
// layout on both 32 and 64-bit Windows.
type propVariantBlob struct {
	vt   uint16
	_    [3]uint16
	size uint32
	data *byte
}

// activationHandler is the IActivateAudioInterfaceCompletionHandler object handed to Windows.
// Its lifetime is held by the stream, so reference counting is a no-op.
type activationHandler struct {
	vtbl *[4]uintptr
	once sync.Once
	done chan struct{}
}

var handlerVtbl = sync.OnceValue(func() *[4]uintptr {
	return &[4]uintptr{
		syscall.NewCallback(handlerQueryInterface),
		syscall.NewCallback(handlerAddRef),
		syscall.NewCallback(handlerAddRef),
		syscall.NewCallback(handlerActivateCompleted),
	}
})

func handlerQueryInterface(this, riid, ppv uintptr) uintptr {
	iid := *(**windows.GUID)(unsafe.Pointer(&riid))
	out := *(**uintptr)(unsafe.Pointer(&ppv))
	switch *iid {
	case iidIUnknown, iidIAgileObject, iidIActivateAudioInterfaceCompletionHandler:
		*out = this
		return 0
	}
	*out = 0
	return eNoInterface
}

func handlerAddRef(uintptr) uintptr { return 1 }

func handlerActivateCompleted(this, _ uintptr) uintptr {
	h := *(**activationHandler)(unsafe.Pointer(&this))
	h.once.Do(func() { close(h.done) })
	return 0
}

func openApp(app App, onFrame FrameFunc) (Stream, error) {
	if app.PID <= 0 {
		return nil, errors.New("sysaudio: no process to capture")
	}
	if err := procActivateAudioInterfaceAsync.Find(); err != nil {
		return nil, ErrUnsupported
	}
	s := &wasapiStream{stop: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error, 1)
	go s.run(func(started func()) error { return s.captureProcess(app.PID, onFrame, started) }, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return s, nil
}

// activateProcessLoopback returns an IAudioClient that records process pid and its children.
func (s *wasapiStream) activateProcessLoopback(pid int) (*comObj, error) {
	params := audioClientActivationParams{
		activationType: activationTypeProcessLoopback,
		targetPID:      uint32(pid),
		loopbackMode:   loopbackIncludeProcessTree,
	}
	pv := propVariantBlob{vt: vtBlob, size: uint32(unsafe.Sizeof(params)), data: (*byte)(unsafe.Pointer(&params))}
	path, _ := windows.UTF16PtrFromString(virtualProcessLoopback)
	h := &activationHandler{vtbl: handlerVtbl(), done: make(chan struct{})}
	s.handlers = append(s.handlers, h) // Windows may call it after this function returns

	var op *comObj
	r, _, _ := procActivateAudioInterfaceAsync.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&iidIAudioClient)),
		uintptr(unsafe.Pointer(&pv)), uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&op)))
	if err := hresult(r, "ActivateAudioInterfaceAsync(process loopback)"); err != nil {
		return nil, err
	}
	defer release(op)
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		return nil, errors.New("sysaudio: process loopback activation timed out")
	}

	var activated int32
	var unk *comObj
	if err := hresult(comCall(op, operationGetActivateResult, uintptr(unsafe.Pointer(&activated)), uintptr(unsafe.Pointer(&unk))), "GetActivateResult"); err != nil {
		return nil, err
	}
	if err := hresult(uintptr(uint32(activated)), "process loopback activation"); err != nil {
		release(unk)
		return nil, err
	}
	defer release(unk)
	var client *comObj
	if err := hresult(comCall(unk, unknownQueryInterface, uintptr(unsafe.Pointer(&iidIAudioClient)), uintptr(unsafe.Pointer(&client))), "QueryInterface(IAudioClient)"); err != nil {
		return nil, err
	}
	return client, nil
}

// captureProcess runs one process loopback session and returns when it ends or fails.
func (s *wasapiStream) captureProcess(pid int, onFrame FrameFunc, started func()) error {
	client, err := s.activateProcessLoopback(pid)
	if err != nil {
		return err
	}
	defer release(client)

	// The virtual device has no mix format; ask for 48 kHz 16-bit stereo PCM.
	wf := waveFormat{Channels: 2, Bits: 16, Rate: SampleRate}
	var wfx [waveFormatExSize]byte
	binary.LittleEndian.PutUint16(wfx[0:], 1) // WAVE_FORMAT_PCM
	binary.LittleEndian.PutUint16(wfx[2:], uint16(wf.Channels))
	binary.LittleEndian.PutUint32(wfx[4:], uint32(wf.Rate))
	binary.LittleEndian.PutUint32(wfx[8:], uint32(wf.Rate*wf.Channels*wf.Bits/8))
	binary.LittleEndian.PutUint16(wfx[12:], uint16(wf.Channels*wf.Bits/8))
	binary.LittleEndian.PutUint16(wfx[14:], uint16(wf.Bits))
	flags := uintptr(audclntStreamFlagLoopback | audclntStreamFlagEventCallback | audclntStreamFlagAutoConvertPCM | audclntStreamFlagSrcDefaultQuality)
	if err := initialize(client, flags, unsafe.Pointer(&wfx[0])); err != nil {
		return err
	}

	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return fmt.Errorf("sysaudio: CreateEvent: %w", err)
	}
	defer windows.CloseHandle(ev)
	if err := hresult(comCall(client, clientSetEventHandle, uintptr(ev)), "SetEventHandle"); err != nil {
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
	s.backend = fmt.Sprintf("wasapi process loopback (pid %d, %s)", pid, wf)
	s.mu.Unlock()
	started()

	return s.pump(capture, wf, onFrame, func() bool {
		_, _ = windows.WaitForSingleObject(ev, 20)
		select {
		case <-s.stop:
			return false
		default:
			return true
		}
	})
}
