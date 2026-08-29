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
	modWinUser32 = syscall.NewLazyDLL("user32.dll")
	modWinGdi32  = syscall.NewLazyDLL("gdi32.dll")

	procWinGetDC                  = modWinUser32.NewProc("GetDC")
	procWinReleaseDC              = modWinUser32.NewProc("ReleaseDC")
	procWinGetClientRect          = modWinUser32.NewProc("GetClientRect")
	procWinClientToScreen         = modWinUser32.NewProc("ClientToScreen")
	procWinGetWindowRect          = modWinUser32.NewProc("GetWindowRect")
	procWinPrintWindow            = modWinUser32.NewProc("PrintWindow")
	procWinGetCursorInfo          = modWinUser32.NewProc("GetCursorInfo")
	procWinDrawIconEx             = modWinUser32.NewProc("DrawIconEx")
	procWinGetDpiForWindow        = modWinUser32.NewProc("GetDpiForWindow")
	procWinSetDpiAwarenessContext = modWinUser32.NewProc("SetProcessDpiAwarenessContext")
	procWinCreateCompatibleDC     = modWinGdi32.NewProc("CreateCompatibleDC")
	procWinCreateCompatibleBitmap = modWinGdi32.NewProc("CreateCompatibleBitmap")
	procWinSelectObject           = modWinGdi32.NewProc("SelectObject")
	procWinDeleteObject           = modWinGdi32.NewProc("DeleteObject")
	procWinDeleteDC               = modWinGdi32.NewProc("DeleteDC")
	procWinGetDIBits              = modWinGdi32.NewProc("GetDIBits")
)

const (
	PW_CLIENTONLY                              = 0x00000001
	PW_RENDERFULLCONTENT                       = 0x00000002
	DIB_RGB_COLORS                             = 0
	BI_RGB                                     = 0
	CURSOR_SHOWING                             = 0x00000001
	DI_NORMAL                                  = 0x0003
	DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = ^uintptr(3) // -4
)

func init() {
	if procWinSetDpiAwarenessContext.Find() == nil {
		procWinSetDpiAwarenessContext.Call(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2)
	}
}

type winRECT struct {
	Left, Top, Right, Bottom int32
}

type winPOINT struct {
	X, Y int32
}

type winCURSORINFO struct {
	CbSize      uint32
	Flags       uint32
	HCursor     syscall.Handle
	PtScreenPos winPOINT
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

// GetWindowDimensions returns the true uncropped physical dimensions of the window
func GetWindowDimensions(hwnd uintptr) (int, int) {
	var r winRECT
	ret, _, _ := procWinGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	w := int(r.Right)
	h := int(r.Bottom)
	if ret == 0 || w <= 0 || h <= 0 {
		procWinGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		w = int(r.Right - r.Left)
		h = int(r.Bottom - r.Top)
	}

	// If Windows is running with DPI scaling (125%, 150%), scale to physical pixels
	if procWinGetDpiForWindow.Find() == nil {
		dpi, _, _ := procWinGetDpiForWindow.Call(hwnd)
		if dpi > 0 && dpi != 96 {
			scale := float64(dpi) / 96.0
			w = int(float64(w) * scale)
			h = int(float64(h) * scale)
		}
	}

	if w <= 100 || h <= 100 {
		return 1280, 720
	}
	// Force even dimensions for H.264 encoder
	w = (w / 2) * 2
	h = (h / 2) * 2
	return w, h
}

// StreamWindowFrames captures isolated application windows via PrintWindow PW_RENDERFULLCONTENT,
// overlays the live mouse cursor, and writes raw BGRA frames directly to the stdin pipe of FFmpeg.
func StreamWindowFrames(ctx context.Context, hwnd uintptr, fps int, outPipe io.WriteCloser) error {
	defer outPipe.Close()

	if fps <= 0 || fps > 60 {
		fps = 60
	}

	w, h := GetWindowDimensions(hwnd)

	hdcWindow, _, _ := procWinGetDC.Call(hwnd)
	if hdcWindow == 0 {
		return fmt.Errorf("failed to get window DC for 0x%x", hwnd)
	}
	defer procWinReleaseDC.Call(hwnd, hdcWindow)

	hdcMem, _, _ := procWinCreateCompatibleDC.Call(hdcWindow)
	if hdcMem == 0 {
		return fmt.Errorf("failed to create compatible memory DC")
	}
	defer procWinDeleteDC.Call(hdcMem)

	hBitmap, _, _ := procWinCreateCompatibleBitmap.Call(hdcWindow, uintptr(w), uintptr(h))
	if hBitmap == 0 {
		return fmt.Errorf("failed to create compatible bitmap (%dx%d)", w, h)
	}
	defer procWinDeleteObject.Call(hBitmap)

	procWinSelectObject.Call(hdcMem, hBitmap)

	var bmi winBITMAPINFO
	bmi.BmiHeader.BiSize = uint32(unsafe.Sizeof(bmi.BmiHeader))
	bmi.BmiHeader.BiWidth = int32(w)
	bmi.BmiHeader.BiHeight = -int32(h) // Negative height for top-down DIB
	bmi.BmiHeader.BiPlanes = 1
	bmi.BmiHeader.BiBitCount = 32
	bmi.BmiHeader.BiCompression = BI_RGB

	frameSize := w * h * 4
	rawBytes := make([]byte, frameSize)
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// 1. Get window screen coordinates for mouse position
			var pt winPOINT
			procWinClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt)))

			// 2. Capture isolated window surface directly via PrintWindow (ignores overlapping foreground windows)
			procWinPrintWindow.Call(hwnd, hdcMem, uintptr(PW_RENDERFULLCONTENT))

			// 3. Draw live mouse cursor overlay
			var ci winCURSORINFO
			ci.CbSize = uint32(unsafe.Sizeof(ci))
			if ret, _, _ := procWinGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ret != 0 {
				if ci.Flags == CURSOR_SHOWING {
					mx := ci.PtScreenPos.X - pt.X
					my := ci.PtScreenPos.Y - pt.Y
					if mx >= 0 && mx < int32(w) && my >= 0 && my < int32(h) {
						procWinDrawIconEx.Call(hdcMem, uintptr(mx), uintptr(my), uintptr(ci.HCursor), 0, 0, 0, 0, DI_NORMAL)
					}
				}
			}

			// 4. Read bits into memory buffer
			procWinGetDIBits.Call(
				hdcMem,
				hBitmap,
				0,
				uintptr(h),
				uintptr(unsafe.Pointer(&rawBytes[0])),
				uintptr(unsafe.Pointer(&bmi)),
				DIB_RGB_COLORS,
			)

			// 5. Write to FFmpeg rawvideo pipe
			if _, err := outPipe.Write(rawBytes); err != nil {
				return err
			}
		}
	}
}
