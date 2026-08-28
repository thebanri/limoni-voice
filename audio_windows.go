//go:build windows

package main

import (
	"syscall"
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

	procWaveOutOpen            = modwinmm.NewProc("waveOutOpen")
	procWaveOutClose           = modwinmm.NewProc("waveOutClose")
	procWaveOutPrepareHeader   = modwinmm.NewProc("waveOutPrepareHeader")
	procWaveOutUnprepareHeader = modwinmm.NewProc("waveOutUnprepareHeader")
	procWaveOutWrite           = modwinmm.NewProc("waveOutWrite")
	procWaveOutReset           = modwinmm.NewProc("waveOutReset")
)

const (
	WAVE_FORMAT_PCM = 1
	WAVE_MAPPER     = ^uintptr(0) // (UINT)-1: default device
	CALLBACK_NULL   = 0
	CALLBACK_EVENT  = 0x00050000
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

const (
	WHDR_DONE = 0x00000001
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procCreateEvent = kernel32.NewProc("CreateEventW")
	procCloseHandle = kernel32.NewProc("CloseHandle")
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

// startWindowsCapture initializes native Windows audio capture using winmm waveIn API
func (a *AudioEngine) startWindowsCapture(onFrame func(rms float64, speaking bool, pcm []byte)) bool {
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
		return false
	}

	var hWaveIn uintptr
	ret, _, _ := procWaveInOpen.Call(
		uintptr(unsafe.Pointer(&hWaveIn)),
		WAVE_MAPPER,
		uintptr(unsafe.Pointer(&wfx)),
		eventHandle,
		0,
		CALLBACK_EVENT,
	)

	if ret != 0 {
		closeWindowsHandle(eventHandle)
		return false
	}

	// Create 4 alternating audio chunk buffers (each 20ms = 640 bytes)
	const numBufs = 4
	const bufSize = AudioChunkSize

	buffers := make([][]byte, numBufs)
	headers := make([]WAVEHDR, numBufs)

	for i := 0; i < numBufs; i++ {
		buffers[i] = make([]byte, bufSize)
		headers[i] = WAVEHDR{
			lpData:         uintptr(unsafe.Pointer(&buffers[i][0])),
			dwBufferLength: bufSize,
		}
		procWaveInPrepareHeader.Call(hWaveIn, uintptr(unsafe.Pointer(&headers[i])), unsafe.Sizeof(headers[i]))
		procWaveInAddBuffer.Call(hWaveIn, uintptr(unsafe.Pointer(&headers[i])), unsafe.Sizeof(headers[i]))
	}

	ret, _, _ = procWaveInStart.Call(hWaveIn)
	if ret != 0 {
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

			// Wait for audio buffer ready event (up to 100ms)
			res := waitForWindowsEvent(eventHandle, 100)
			if res != 0 {
				continue
			}

			for i := 0; i < numBufs; i++ {
				hdr := &headers[bufIdx]
				if hdr.dwFlags&WHDR_DONE != 0 {
					data := buffers[bufIdx]
					bytesRead := int(hdr.dwBytesRecorded)
					if bytesRead > 0 {
						chunk := make([]byte, bytesRead)
						copy(chunk, data[:bytesRead])

						a.mu.Lock()
						muted := a.Muted
						gain := a.Gain
						loopback := a.Loopback
						suppressMode := a.SuppressionMode

						var processedChunk []byte
						var finalRMS float64
						var speaking bool

						if muted {
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
								finalRMS = rawRMS
								processedChunk = make([]byte, len(processed))
								copy(processedChunk, processed)
							} else {
								speaking, finalRMS, processedChunk = a.processNoiseCancellation(processed, suppressMode)
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

					// Reset and re-add buffer to queue
					hdr.dwFlags = 0
					hdr.dwBytesRecorded = 0
					procWaveInAddBuffer.Call(hWaveIn, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
				}
				bufIdx = (bufIdx + 1) % numBufs
			}
		}
	}()

	return true
}

// windowsWaveWriter implements io.WriteCloser using winmm waveOut API
type windowsWaveWriter struct {
	hWaveOut    uintptr
	eventHandle uintptr
	headers     []WAVEHDR
	buffers     [][]byte
	bufIdx      int
	closed      bool
}

func newWindowsWaveWriter() *windowsWaveWriter {
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
		WAVE_MAPPER,
		uintptr(unsafe.Pointer(&wfx)),
		eventHandle,
		0,
		CALLBACK_EVENT,
	)

	if ret != 0 {
		closeWindowsHandle(eventHandle)
		return nil
	}

	const numBufs = 8
	const bufSize = AudioChunkSize

	buffers := make([][]byte, numBufs)
	headers := make([]WAVEHDR, numBufs)

	for i := 0; i < numBufs; i++ {
		buffers[i] = make([]byte, bufSize)
		headers[i] = WAVEHDR{
			lpData:         uintptr(unsafe.Pointer(&buffers[i][0])),
			dwBufferLength: bufSize,
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

	hdr := &w.headers[w.bufIdx]
	buf := w.buffers[w.bufIdx]

	copyLen := len(p)
	if copyLen > len(buf) {
		copyLen = len(buf)
	}
	copy(buf, p[:copyLen])

	hdr.dwBufferLength = uint32(copyLen)
	procWaveOutWrite.Call(w.hWaveOut, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))

	w.bufIdx = (w.bufIdx + 1) % len(w.headers)
	return copyLen, nil
}

func (w *windowsWaveWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	procWaveOutReset.Call(w.hWaveOut)
	for i := 0; i < len(w.headers); i++ {
		procWaveOutUnprepareHeader.Call(w.hWaveOut, uintptr(unsafe.Pointer(&w.headers[i])), unsafe.Sizeof(w.headers[i]))
	}
	procWaveOutClose.Call(w.hWaveOut)
	closeWindowsHandle(w.eventHandle)
	return nil
}

func (a *AudioEngine) startWindowsPlayback() bool {
	writer := newWindowsWaveWriter()
	if writer == nil {
		return false
	}
	a.playbackPipe = writer
	return true
}
