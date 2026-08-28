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
	procWaveInGetNumDevs       = modwinmm.NewProc("waveInGetNumDevs")

	procWaveOutOpen            = modwinmm.NewProc("waveOutOpen")
	procWaveOutClose           = modwinmm.NewProc("waveOutClose")
	procWaveOutPrepareHeader   = modwinmm.NewProc("waveOutPrepareHeader")
	procWaveOutUnprepareHeader = modwinmm.NewProc("waveOutUnprepareHeader")
	procWaveOutWrite           = modwinmm.NewProc("waveOutWrite")
	procWaveOutReset           = modwinmm.NewProc("waveOutReset")
	procWaveOutGetNumDevs      = modwinmm.NewProc("waveOutGetNumDevs")
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

// startWindowsCapture initializes native Windows audio capture using winmm waveIn API
func (a *AudioEngine) startWindowsCapture(onFrame func(rms float64, speaking bool, pcm []byte)) bool {
	numDevs, _, _ := procWaveInGetNumDevs.Call()
	if numDevs == 0 {
		return false
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

	// 8 alternating buffers (each 20ms = 640 bytes)
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
		procWaveInPrepareHeader.Call(hWaveIn, uintptr(unsafe.Pointer(&headers[i])), unsafe.Sizeof(headers[i]))
		procWaveInAddBuffer.Call(hWaveIn, uintptr(unsafe.Pointer(&headers[i])), unsafe.Sizeof(headers[i]))
	}

	ret, _, _ = procWaveInStart.Call(hWaveIn)
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

			// Wait up to 50ms for next recorded buffer
			waitForWindowsEvent(eventHandle, 50)

			// Process any completed buffers
			for count := 0; count < numBufs; count++ {
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

					// Re-queue the buffer
					hdr.dwFlags = 0
					hdr.dwBytesRecorded = 0
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

// windowsWaveWriter implements io.WriteCloser using winmm waveOut API with a robust multi-buffer pool
type windowsWaveWriter struct {
	hWaveOut    uintptr
	eventHandle uintptr
	headers     []WAVEHDR
	buffers     [][]byte
	bufIdx      int
	closed      bool
}

func newWindowsWaveWriter() *windowsWaveWriter {
	numDevs, _, _ := procWaveOutGetNumDevs.Call()
	if numDevs == 0 {
		return nil
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

	// 16 buffers to ensure smooth playback without glitches
	const numBufs = 16
	const bufSize = AudioChunkSize

	buffers := make([][]byte, numBufs)
	headers := make([]WAVEHDR, numBufs)

	for i := 0; i < numBufs; i++ {
		buffers[i] = make([]byte, bufSize)
		headers[i] = WAVEHDR{
			lpData:         uintptr(unsafe.Pointer(&buffers[i][0])),
			dwBufferLength: bufSize,
			dwFlags:        WHDR_DONE, // Start as available
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

	// Look for an available (done) buffer slot
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
		// Wait briefly for a playing buffer to complete
		waitForWindowsEvent(w.eventHandle, 10)
	}

	if foundIdx < 0 {
		// Fallback: force write to current index
		foundIdx = w.bufIdx
	}

	hdr := &w.headers[foundIdx]
	buf := w.buffers[foundIdx]

	copyLen := len(p)
	if copyLen > len(buf) {
		copyLen = len(buf)
	}
	copy(buf, p[:copyLen])

	// If already prepared, unprepare first
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
	writer := newWindowsWaveWriter()
	if writer == nil {
		return false
	}
	a.playbackPipe = writer
	return true
}
