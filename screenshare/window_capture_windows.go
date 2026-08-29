//go:build windows

package screenshare

import (
	"context"
	"fmt"
	"io"
	"syscall"
	"time"
	"unsafe"
)

var (
	moduser32 = syscall.NewLazyDLL("user32.dll")
	modgdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetDC                  = moduser32.NewProc("GetDC")
	procReleaseDC              = moduser32.NewProc("ReleaseDC")
	procPrintWindow            = moduser32.NewProc("PrintWindow")
	procGetWindowRect          = moduser32.NewProc("GetWindowRect")
	procCreateCompatibleDC     = modgdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = modgdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = modgdi32.NewProc("SelectObject")
	procDeleteObject           = modgdi32.NewProc("DeleteObject")
	procDeleteDC               = modgdi32.NewProc("DeleteDC")
	procGetDIBits              = modgdi32.NewProc("GetDIBits")
)

const (
	PW_RENDERFULLCONTENT = 0x00000002
	DIB_RGB_COLORS       = 0
	BI_RGB               = 0
)

type winRECT struct {
	Left, Top, Right, Bottom int32
}

type winBITMAPINFOHEADER struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type winBITMAPINFO struct {
	BmiHeader winBITMAPINFOHEADER
	BmiColors [1]uint32
}

// GetWindowDimensions returns width and height for a given window handle
func GetWindowDimensions(hwnd uintptr) (int, int) {
	var r winRECT
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return 1280, 720
	}
	w := int(r.Right - r.Left)
	h := int(r.Bottom - r.Top)
	if w <= 100 || h <= 100 {
		return 1280, 720
	}
	// Force even dimensions
	w = (w / 2) * 2
	h = (h / 2) * 2
	return w, h
}

// StreamWindowFrames captures hardware-accelerated window frames via PrintWindow PW_RENDERFULLCONTENT
// and writes raw BGRA frames directly to the stdin pipe of FFmpeg.
func StreamWindowFrames(ctx context.Context, hwnd uintptr, fps int, outPipe io.WriteCloser) error {
	defer outPipe.Close()

	if fps <= 0 || fps > 60 {
		fps = 60
	}

	var r winRECT
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return fmt.Errorf("invalid window handle: 0x%x", hwnd)
	}

	w := int(r.Right - r.Left)
	h := int(r.Bottom - r.Top)
	if w <= 0 || h <= 0 {
		w, h = 1280, 720
	}
	// Force even dimensions for H.264
	w = (w / 2) * 2
	h = (h / 2) * 2

	hdcWindow, _, _ := procGetDC.Call(hwnd)
	if hdcWindow == 0 {
		return fmt.Errorf("failed to get window DC for 0x%x", hwnd)
	}
	defer procReleaseDC.Call(hwnd, hdcWindow)

	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcWindow)
	if hdcMem == 0 {
		return fmt.Errorf("failed to create compatible memory DC")
	}
	defer procDeleteDC.Call(hdcMem)

	hBitmap, _, _ := procCreateCompatibleBitmap.Call(hdcWindow, uintptr(w), uintptr(h))
	if hBitmap == 0 {
		return fmt.Errorf("failed to create compatible bitmap (%dx%d)", w, h)
	}
	defer procDeleteObject.Call(hBitmap)

	procSelectObject.Call(hdcMem, hBitmap)

	var bmi winBITMAPINFO
	bmi.BmiHeader.BiSize = uint32(unsafe.Sizeof(bmi.BmiHeader))
	bmi.BmiHeader.BiWidth = int32(w)
	bmi.BmiHeader.BiHeight = -int32(h) // Negative height for top-down DIB
	bmi.BmiHeader.BiPlanes = 1
	bmi.BmiHeader.BiBitCount = 32
	bmi.BmiHeader.BiCompression = BI_RGB

	rawBytes := make([]byte, w*h*4)
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Capture hardware-accelerated DWM frame
			procPrintWindow.Call(hwnd, hdcMem, uintptr(PW_RENDERFULLCONTENT))

			// Read bits into memory buffer
			procGetDIBits.Call(
				hdcMem,
				hBitmap,
				0,
				uintptr(h),
				uintptr(unsafe.Pointer(&rawBytes[0])),
				uintptr(unsafe.Pointer(&bmi)),
				DIB_RGB_COLORS,
			)

			// Write to FFmpeg rawvideo pipe
			if _, err := outPipe.Write(rawBytes); err != nil {
				return err
			}
		}
	}
}
