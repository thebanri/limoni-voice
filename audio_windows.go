//go:build windows

package main

import (
	"fmt"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

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
)

const (
	WAVE_FORMAT_PCM = 1
	WAVE_MAPPER     = ^uintptr(0) // (UINT)-1: default device
	CALLBACK_EVENT  = 0x00050000

	WHDR_DONE     = 0x00000001
	WHDR_PREPARED = 0x00000002
	WHDR_INQUEUE  = 0x00000010
)

type WAVEFORMATEX struct {
	wFormatTag      uint16
	nChannels       uint16
	nSamplesPerSec  uint32
	nAvgBytesPerSec uint32
	nBlockAlign     uint16
	wBitsPerSample  uint16
	cbSize          uint16
}

type WAVEHDR struct {
	lpData          uintptr
	dwBufferLength  uint32
	dwBytesRecorded uint32
	dwUser          uintptr
	dwFlags         uint32
	dwLoops         uint32
	lpNext          uintptr
	reserved        uintptr
}

type WAVEINCAPSW struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [32]uint16
	dwFormats      uint32
	wChannels      uint16
	wReserved1     uint16
}

type WAVEOUTCAPSW struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [32]uint16
	dwFormats      uint32
	wChannels      uint16
	wReserved1     uint16
	dwSupport      uint32
}

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procCreateEvent         = kernel32.NewProc("CreateEventW")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
	procWaitForSingleObject = kernel32.NewProc("WaitForSingleObject")
)

func createWindowsEvent() uintptr {
	ret, _, _ := procCreateEvent.Call(0, 0, 0, 0)
	return ret
}

func closeWindowsHandle(h uintptr) {
	if h != 0 {
		procCloseHandle.Call(h)
	}
}

func waitForWindowsEvent(h uintptr, ms uint32) uint32 {
	ret, _, _ := procWaitForSingleObject.Call(h, uintptr(ms))
	return uint32(ret)
}

func enumerateWindowsInputDevices() []AudioDevice {
	devs := []AudioDevice{
		{ID: "default", Name: "Default Microphone (Windows Preferred)", IsDefault: true, IsInput: true},
	}

	numDevs, _, _ := procWaveInGetNumDevs.Call()
	for i := uintptr(0); i < numDevs; i++ {
		var caps WAVEINCAPSW
		ret, _, _ := procWaveInGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if ret == 0 {
			name := syscall.UTF16ToString(caps.szPname[:])
			if name == "" {
				name = fmt.Sprintf("Microphone Device %d", i)
			}
			devs = append(devs, AudioDevice{
				ID:      fmt.Sprintf("%d", i),
				Name:    name,
				IsInput: true,
			})
		}
	}
	return devs
}

func enumerateWindowsOutputDevices() []AudioDevice {
	devs := []AudioDevice{
		{ID: "default", Name: "Default Output / Speakers (Windows Preferred)", IsDefault: true, IsInput: false},
	}

	numDevs, _, _ := procWaveOutGetNumDevs.Call()
	for i := uintptr(0); i < numDevs; i++ {
		var caps WAVEOUTCAPSW
		ret, _, _ := procWaveOutGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if ret == 0 {
			name := syscall.UTF16ToString(caps.szPname[:])
			if name == "" {
				name = fmt.Sprintf("Speaker Device %d", i)
			}
			devs = append(devs, AudioDevice{
				ID:      fmt.Sprintf("%d", i),
				Name:    name,
				IsInput: false,
			})
		}
	}
	return devs
}

// startWindowsCapture initializes native Windows audio capture using winmm waveIn API.
// Supports 16kHz native, or fallback to 48kHz with 3:1 decimation if hardware requires it.
func (a *AudioEngine) startWindowsCapture(onFrame func(rms float64, speaking bool, pcm []byte)) bool {
	numDevs, _, _ := procWaveInGetNumDevs.Call()
	if numDevs == 0 {
		return false
	}

	targetDevID := WAVE_MAPPER
	a.mu.RLock()
	if a.SelectedInputIdx > 0 && a.SelectedInputIdx < len(a.InputDevices) {
		devStr := a.InputDevices[a.SelectedInputIdx].ID
		if idx, err := strconv.Atoi(devStr); err == nil && uintptr(idx) < numDevs {
			targetDevID = uintptr(idx)
		}
	}
	a.mu.RUnlock()

	sampleRates := []uint32{16000, 48000, 44100}
	var hWaveIn uintptr
	var eventHandle uintptr
	var chosenRate uint32
	var chosenChannels uint16

	for _, rate := range sampleRates {
		wfx := WAVEFORMATEX{
			wFormatTag:      WAVE_FORMAT_PCM,
			nChannels:       1,
			nSamplesPerSec:  rate,
			nAvgBytesPerSec: rate * 2,
			nBlockAlign:     2,
			wBitsPerSample:  16,
			cbSize:          0,
		}

		ev := createWindowsEvent()
		var h uintptr
		ret, _, _ := procWaveInOpen.Call(
			uintptr(unsafe.Pointer(&h)),
			targetDevID,
			uintptr(unsafe.Pointer(&wfx)),
			ev,
			0,
			CALLBACK_EVENT,
		)

		if ret == 0 {
			hWaveIn = h
			eventHandle = ev
			chosenRate = rate
			chosenChannels = 1
			break
		}
		closeWindowsHandle(ev)
	}

	if hWaveIn == 0 {
		return false
	}

	// Calculate buffer size for ~20ms chunks
	samplesPerChunk := (chosenRate * 20) / 1000
	rawBufSize := samplesPerChunk * uint32(chosenChannels) * 2

	const numBufs = 8
	buffers := make([][]byte, numBufs)
	headers := make([]WAVEHDR, numBufs)

	for i := 0; i < numBufs; i++ {
		buffers[i] = make([]byte, rawBufSize)
		headers[i] = WAVEHDR{
			lpData:         uintptr(unsafe.Pointer(&buffers[i][0])),
			dwBufferLength: rawBufSize,
		}
		procWaveInPrepareHeader.Call(hWaveIn, uintptr(unsafe.Pointer(&headers[i])), unsafe.Sizeof(headers[i]))
		procWaveInAddBuffer.Call(hWaveIn, uintptr(unsafe.Pointer(&headers[i])), unsafe.Sizeof(headers[i]))
	}

	ret, _, _ := procWaveInStart.Call(hWaveIn)
	if ret != 0 {
		procWaveInReset.Call(hWaveIn)
		for i := 0; i < numBufs; i++ {
			procWaveInUnprepareHeader.Call(hWaveIn, uintptr(unsafe.Pointer(&headers[i])), unsafe.Sizeof(headers[i]))
		}
		procWaveInClose.Call(hWaveIn)
		closeWindowsHandle(eventHandle)
		return false
	}

	go func() {
		defer func() {
			procWaveInStop.Call(hWaveIn)
			procWaveInReset.Call(hWaveIn)
			for i := 0; i < numBufs; i++ {
				procWaveInUnprepareHeader.Call(hWaveIn, uintptr(unsafe.Pointer(&headers[i])), unsafe.Sizeof(headers[i]))
			}
			procWaveInClose.Call(hWaveIn)
			closeWindowsHandle(eventHandle)
		}()

		bufIdx := 0
		for {
			select {
			case <-a.stopChan:
				return
			default:
			}

			waitForWindowsEvent(eventHandle, 50)

			for count := 0; count < numBufs; count++ {
				hdr := &headers[bufIdx]
				if hdr.dwFlags&WHDR_DONE != 0 {
					data := buffers[bufIdx]
					bytesRead := int(hdr.dwBytesRecorded)
					if bytesRead > 0 {
						var chunk []byte

						if chosenRate == 16000 {
							chunk = make([]byte, bytesRead)
							copy(chunk, data[:bytesRead])
						} else if chosenRate == 48000 {
							// 3:1 downsample to 16000 Hz
							numSamples := bytesRead / 2
							outSamples := numSamples / 3
							chunk = make([]byte, outSamples*2)
							for s := 0; s < outSamples; s++ {
								copy(chunk[s*2:s*2+2], data[s*6:s*6+2])
							}
						} else {
							// 44100 fallback (approximate 2.75 downsample)
							outSamples := 320
							chunk = make([]byte, AudioChunkSize)
							step := float64(bytesRead/2) / float64(outSamples)
							for s := 0; s < outSamples; s++ {
								srcIdx := int(float64(s) * step)
								if srcIdx*2+2 <= bytesRead {
									copy(chunk[s*2:s*2+2], data[srcIdx*2:srcIdx*2+2])
								}
							}
						}

						if len(chunk) >= AudioChunkSize {
							chunk = chunk[:AudioChunkSize]
						} else {
							padded := make([]byte, AudioChunkSize)
							copy(padded, chunk)
							chunk = padded
						}

						a.mu.Lock()
						muted := a.Muted
						gain := a.Gain
						loopback := a.Loopback
						suppressMode := a.SuppressionMode
						inputMode := a.InputMode
						isPTT := a.IsPTTActive || time.Now().Before(a.PTTReleaseTime)

						var processedChunk []byte
						var finalRMS float64
						var speaking bool

						if muted || (inputMode == InputModePushToTalk && !isPTT) {
							processedChunk = make([]byte, AudioChunkSize)
							finalRMS = 0
							speaking = false
							a.LocalRMS = 0
							a.IsSpeaking = false
							a.shiftWave(0)
						} else {
							processed := applyGain(chunk, gain)
							if suppressMode == 0 {
								rawRMS := calculateRMS(processed)
								speaking = rawRMS > a.VADThreshold
								if inputMode == InputModePushToTalk && isPTT {
									speaking = true
								}
								finalRMS = rawRMS
								processedChunk = make([]byte, len(processed))
								copy(processedChunk, processed)

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
									processedChunk = applyGain(processedChunk, a.gateGain)
								}
							} else {
								speaking, finalRMS, processedChunk = a.processNoiseCancellation(processed, suppressMode)
								if inputMode == InputModePushToTalk && isPTT {
									speaking = true
								}
							}
							a.LocalRMS = finalRMS
							a.IsSpeaking = speaking
							a.shiftWave(finalRMS)
						}
						a.mu.Unlock()

						if loopback && len(processedChunk) > 0 && !muted {
							a.queueLoopbackPCM(processedChunk, finalRMS, speaking)
						}

						if onFrame != nil {
							onFrame(finalRMS, speaking, processedChunk)
						}
					}

					procWaveInUnprepareHeader.Call(hWaveIn, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
					hdr.dwBufferLength = rawBufSize
					hdr.dwBytesRecorded = 0
					hdr.dwFlags = 0
					procWaveInPrepareHeader.Call(hWaveIn, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
					procWaveInAddBuffer.Call(hWaveIn, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))

					bufIdx = (bufIdx + 1) % numBufs
				} else {
					break
				}
			}
		}
	}()

	return true
}

// windowsWaveWriter implements io.WriteCloser using winmm waveOut API with robust multi-buffer pool
type windowsWaveWriter struct {
	hWaveOut    uintptr
	eventHandle uintptr
	headers     []WAVEHDR
	buffers     [][]byte
	bufIdx      int
	closed      bool
}

func newWindowsWaveWriter(devIndex int) *windowsWaveWriter {
	numDevs, _, _ := procWaveOutGetNumDevs.Call()
	if numDevs == 0 {
		return nil
	}

	targetDevID := WAVE_MAPPER
	if devIndex > 0 && uintptr(devIndex) <= numDevs {
		targetDevID = uintptr(devIndex - 1)
	}

	wfx := WAVEFORMATEX{
		wFormatTag:      WAVE_FORMAT_PCM,
		nChannels:       1,
		nSamplesPerSec:  16000,
		nAvgBytesPerSec: 32000,
		nBlockAlign:     2,
		wBitsPerSample:  16,
		cbSize:          0,
	}

	eventHandle := createWindowsEvent()
	if eventHandle == 0 {
		return nil
	}

	var hWaveOut uintptr
	ret, _, _ := procWaveOutOpen.Call(
		uintptr(unsafe.Pointer(&hWaveOut)),
		targetDevID,
		uintptr(unsafe.Pointer(&wfx)),
		eventHandle,
		0,
		CALLBACK_EVENT,
	)

	if ret != 0 {
		closeWindowsHandle(eventHandle)
		return nil
	}

	const numBufs = 16
	const bufSize = AudioChunkSize

	buffers := make([][]byte, numBufs)
	headers := make([]WAVEHDR, numBufs)

	for i := 0; i < numBufs; i++ {
		buffers[i] = make([]byte, bufSize)
		headers[i] = WAVEHDR{
			lpData:         uintptr(unsafe.Pointer(&buffers[i][0])),
			dwBufferLength: bufSize,
			dwFlags:        WHDR_DONE,
		}
		procWaveOutPrepareHeader.Call(hWaveOut, uintptr(unsafe.Pointer(&headers[i])), unsafe.Sizeof(headers[i]))
	}

	return &windowsWaveWriter{
		hWaveOut:    hWaveOut,
		eventHandle: eventHandle,
		headers:     headers,
		buffers:     buffers,
	}
}

func (w *windowsWaveWriter) Write(p []byte) (n int, err error) {
	if w.closed || len(p) == 0 {
		return 0, nil
	}

	numBufs := len(w.headers)
	foundIdx := -1

	for attempt := 0; attempt < 3; attempt++ {
		for i := 0; i < numBufs; i++ {
			idx := (w.bufIdx + i) % numBufs
			hdr := &w.headers[idx]
			if hdr.dwFlags&WHDR_INQUEUE == 0 || hdr.dwFlags&WHDR_DONE != 0 {
				foundIdx = idx
				break
			}
		}
		if foundIdx >= 0 {
			break
		}
		waitForWindowsEvent(w.eventHandle, 10)
	}

	if foundIdx < 0 {
		foundIdx = w.bufIdx
	}

	hdr := &w.headers[foundIdx]
	buf := w.buffers[foundIdx]

	copyLen := len(p)
	if copyLen > len(buf) {
		copyLen = len(buf)
	}
	copy(buf, p[:copyLen])

	if hdr.dwFlags&WHDR_PREPARED != 0 {
		procWaveOutUnprepareHeader.Call(w.hWaveOut, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
	}

	hdr.dwBufferLength = uint32(copyLen)
	hdr.dwFlags = 0
	procWaveOutPrepareHeader.Call(w.hWaveOut, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
	procWaveOutWrite.Call(w.hWaveOut, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))

	w.bufIdx = (foundIdx + 1) % numBufs
	return copyLen, nil
}

func (w *windowsWaveWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	procWaveOutReset.Call(w.hWaveOut)
	for i := 0; i < len(w.headers); i++ {
		if w.headers[i].dwFlags&WHDR_PREPARED != 0 {
			procWaveOutUnprepareHeader.Call(w.hWaveOut, uintptr(unsafe.Pointer(&w.headers[i])), unsafe.Sizeof(w.headers[i]))
		}
	}
	procWaveOutClose.Call(w.hWaveOut)
	closeWindowsHandle(w.eventHandle)
	return nil
}

func (a *AudioEngine) startWindowsPlayback() bool {
	devIndex := 0
	a.mu.RLock()
	devIndex = a.SelectedOutputIdx
	a.mu.RUnlock()

	writer := newWindowsWaveWriter(devIndex)
	if writer == nil {
		return false
	}
	a.playbackPipe = writer
	return true
}
