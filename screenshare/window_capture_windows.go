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
	modWinUser32  = syscall.NewLazyDLL("user32.dll")
	modWinGdi32   = syscall.NewLazyDLL("gdi32.dll")
	modWinDwmapi  = syscall.NewLazyDLL("dwmapi.dll")

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
	procWinIsIconic               = modWinUser32.NewProc("IsIconic")
	procWinShowWindow             = modWinUser32.NewProc("ShowWindow")
	procWinIsWindow               = modWinUser32.NewProc("IsWindow")

	procWinCreateCompatibleDC     = modWinGdi32.NewProc("CreateCompatibleDC")
	procWinCreateCompatibleBitmap = modWinGdi32.NewProc("CreateCompatibleBitmap")
	procWinSelectObject           = modWinGdi32.NewProc("SelectObject")
	procWinDeleteObject           = modWinGdi32.NewProc("DeleteObject")
	procWinDeleteDC               = modWinGdi32.NewProc("DeleteDC")
	procWinGetDIBits              = modWinGdi32.NewProc("GetDIBits")
	procWinBitBlt                 = modWinGdi32.NewProc("BitBlt")
	procWinStretchBlt             = modWinGdi32.NewProc("StretchBlt")
	procWinSetStretchBltMode      = modWinGdi32.NewProc("SetStretchBltMode")
	procWinPatBlt                 = modWinGdi32.NewProc("PatBlt")
	procWinSetBrushOrgEx          = modWinGdi32.NewProc("SetBrushOrgEx")

	procWinDwmGetWindowAttribute  = modWinDwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	PW_CLIENTONLY                              = 0x00000001
	PW_RENDERFULLCONTENT                       = 0x00000002
	DIB_RGB_COLORS                             = 0
	BI_RGB                                     = 0
	CURSOR_SHOWING                             = 0x00000001
	DI_NORMAL                                  = 0x0003
	DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = ^uintptr(3) // -4
	DWMWA_EXTENDED_FRAME_BOUNDS                = 9
	SW_RESTORE                                 = 9
	SRCCOPY                                    = 0x00CC0020
	BLACKNESS                                  = 0x00000042
	HALFTONE                                   = 4
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
	if procWinIsWindow.Find() == nil {
		if ret, _, _ := procWinIsWindow.Call(hwnd); ret == 0 {
			return 1280, 720
		}
	}

	// If minimized, restore it so that DWM renders it and dimensions are valid
	if procWinIsIconic.Find() == nil {
		if ret, _, _ := procWinIsIconic.Call(hwnd); ret != 0 {
			if procWinShowWindow.Find() == nil {
				procWinShowWindow.Call(hwnd, uintptr(SW_RESTORE))
				time.Sleep(60 * time.Millisecond)
			}
		}
	}

	var r winRECT
	gotBounds := false

	// Try DwmGetWindowAttribute first for exact visible window frame (excluding invisible drop shadow margins)
	if procWinDwmGetWindowAttribute.Find() == nil {
		hr, _, _ := procWinDwmGetWindowAttribute.Call(
			hwnd,
			uintptr(DWMWA_EXTENDED_FRAME_BOUNDS),
			uintptr(unsafe.Pointer(&r)),
			uintptr(unsafe.Sizeof(r)),
		)
		if hr == 0 && (r.Right-r.Left) > 50 && (r.Bottom-r.Top) > 50 {
			gotBounds = true
		}
	}

	if !gotBounds {
		ret, _, _ := procWinGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		if ret != 0 && (r.Right-r.Left) > 50 && (r.Bottom-r.Top) > 50 {
			gotBounds = true
		}
	}

	var w, h int
	if gotBounds {
		w = int(r.Right - r.Left)
		h = int(r.Bottom - r.Top)
	} else {
		// Fallback to client rect
		procWinGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		w = int(r.Right)
		h = int(r.Bottom)
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
// dynamically adapts to window resizing with high-quality aspect-fit scaling, overlays the live mouse cursor,
// and writes raw BGRA frames directly to the stdin pipe of FFmpeg.
func StreamWindowFrames(ctx context.Context, hwnd uintptr, fps int, outWidth int, outHeight int, outPipe io.WriteCloser) error {
	defer outPipe.Close()

	if fps <= 0 || fps > 60 {
		fps = 60
	}
	if outWidth <= 0 {
		outWidth = 1920
	}
	if outHeight <= 0 {
		outHeight = 1080
	}
	// Force even dimensions
	outWidth = (outWidth / 2) * 2
	outHeight = (outHeight / 2) * 2

	// If minimized at start, restore so window has valid bounds and DWM renders it
	if procWinIsIconic.Find() == nil {
		if ret, _, _ := procWinIsIconic.Call(hwnd); ret != 0 {
			if procWinShowWindow.Find() == nil {
				procWinShowWindow.Call(hwnd, uintptr(SW_RESTORE))
				time.Sleep(60 * time.Millisecond)
			}
		}
	}

	hdcWindow, _, _ := procWinGetDC.Call(hwnd)
	if hdcWindow == 0 {
		hdcWindow, _, _ = procWinGetDC.Call(0)
		if hdcWindow == 0 {
			return fmt.Errorf("failed to get window DC for 0x%x", hwnd)
		}
	}
	defer procWinReleaseDC.Call(hwnd, hdcWindow)

	// Fixed output DC & Bitmap for continuous FFmpeg stream
	hdcOutMem, _, _ := procWinCreateCompatibleDC.Call(hdcWindow)
	if hdcOutMem == 0 {
		return fmt.Errorf("failed to create compatible output memory DC")
	}
	defer procWinDeleteDC.Call(hdcOutMem)

	hOutBitmap, _, _ := procWinCreateCompatibleBitmap.Call(hdcWindow, uintptr(outWidth), uintptr(outHeight))
	if hOutBitmap == 0 {
		return fmt.Errorf("failed to create compatible output bitmap (%dx%d)", outWidth, outHeight)
	}
	defer procWinDeleteObject.Call(hOutBitmap)

	procWinSelectObject.Call(hdcOutMem, hOutBitmap)
	if procWinSetStretchBltMode.Find() == nil {
		procWinSetStretchBltMode.Call(hdcOutMem, uintptr(HALFTONE))
	}

	// Dynamic window capture DC & Bitmap (adapted dynamically when window is resized)
	hdcWinMem, _, _ := procWinCreateCompatibleDC.Call(hdcWindow)
	if hdcWinMem == 0 {
		return fmt.Errorf("failed to create compatible window memory DC")
	}
	defer procWinDeleteDC.Call(hdcWinMem)

	var curWinBitmap syscall.Handle
	var curCapW, curCapH int

	reallocWinBitmap := func(w, h int) bool {
		if curWinBitmap != 0 {
			procWinDeleteObject.Call(uintptr(curWinBitmap))
			curWinBitmap = 0
		}
		bm, _, _ := procWinCreateCompatibleBitmap.Call(hdcWindow, uintptr(w), uintptr(h))
		if bm == 0 {
			return false
		}
		curWinBitmap = syscall.Handle(bm)
		procWinSelectObject.Call(hdcWinMem, uintptr(curWinBitmap))
		curCapW = w
		curCapH = h
		return true
	}

	var bmi winBITMAPINFO
	bmi.BmiHeader.BiSize = uint32(unsafe.Sizeof(bmi.BmiHeader))
	bmi.BmiHeader.BiWidth = int32(outWidth)
	bmi.BmiHeader.BiHeight = -int32(outHeight) // Negative height for top-down DIB
	bmi.BmiHeader.BiPlanes = 1
	bmi.BmiHeader.BiBitCount = 32
	bmi.BmiHeader.BiCompression = BI_RGB

	frameSize := outWidth * outHeight * 4
	rawBytes := make([]byte, frameSize)
	lastGoodFrame := make([]byte, frameSize)
	hasValidFrame := false

	defer func() {
		if curWinBitmap != 0 {
			procWinDeleteObject.Call(uintptr(curWinBitmap))
		}
	}()

	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Check if window is still alive
			if procWinIsWindow.Find() == nil {
				if ret, _, _ := procWinIsWindow.Call(hwnd); ret == 0 {
					if hasValidFrame {
						_, _ = outPipe.Write(lastGoodFrame)
					} else {
						_, _ = outPipe.Write(rawBytes)
					}
					continue
				}
			}

			// Check if window is minimized (Iconic)
			isIconic := false
			if procWinIsIconic.Find() == nil {
				ret, _, _ := procWinIsIconic.Call(hwnd)
				isIconic = (ret != 0)
			}

			if isIconic {
				// Window is minimized - preserve and send the last valid frame
				// so viewers never get a black void!
				if hasValidFrame {
					if _, err := outPipe.Write(lastGoodFrame); err != nil {
						return err
					}
				} else {
					if _, err := outPipe.Write(rawBytes); err != nil {
						return err
					}
				}
				continue
			}

			// 1. Get current window dimensions
			curW, curH := GetWindowDimensions(hwnd)
			if curW <= 50 || curH <= 50 {
				if hasValidFrame {
					_, _ = outPipe.Write(lastGoodFrame)
				}
				continue
			}

			// Ensure window capture bitmap matches current window size
			if curWinBitmap == 0 || curCapW != curW || curCapH != curH {
				if !reallocWinBitmap(curW, curH) {
					if hasValidFrame {
						_, _ = outPipe.Write(lastGoodFrame)
					}
					continue
				}
			}

			// 2. Capture isolated window surface directly via PrintWindow (PW_RENDERFULLCONTENT)
			pwRet, _, _ := procWinPrintWindow.Call(hwnd, hdcWinMem, uintptr(PW_RENDERFULLCONTENT))
			if pwRet == 0 {
				// Fallback: try PrintWindow with 0 or BitBlt
				pwRet2, _, _ := procWinPrintWindow.Call(hwnd, hdcWinMem, 0)
				if pwRet2 == 0 && procWinBitBlt.Find() == nil {
					procWinBitBlt.Call(hdcWinMem, 0, 0, uintptr(curW), uintptr(curH), hdcWindow, 0, 0, uintptr(SRCCOPY))
				}
			}

			// 3. Draw live mouse cursor overlay relative to window top-left
			var winRect winRECT
			procWinGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&winRect)))

			var ci winCURSORINFO
			ci.CbSize = uint32(unsafe.Sizeof(ci))
			if ret, _, _ := procWinGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ret != 0 {
				if ci.Flags == CURSOR_SHOWING {
					mx := ci.PtScreenPos.X - winRect.Left
					my := ci.PtScreenPos.Y - winRect.Top
					if mx >= 0 && mx < int32(curW) && my >= 0 && my < int32(curH) {
						procWinDrawIconEx.Call(hdcWinMem, uintptr(mx), uintptr(my), uintptr(ci.HCursor), 0, 0, 0, 0, DI_NORMAL)
					}
				}
			}

			// 4. Calculate aspect-fit scaling into fixed output canvas
			scaleX := float64(outWidth) / float64(curW)
			scaleY := float64(outHeight) / float64(curH)
			scale := scaleX
			if scaleY < scale {
				scale = scaleY
			}
			destW := int(float64(curW) * scale)
			destH := int(float64(curH) * scale)
			// Force even dimensions
			destW = (destW / 2) * 2
			destH = (destH / 2) * 2
			destX := (outWidth - destW) / 2
			destY := (outHeight - destH) / 2

			// Clear output canvas to black before painting (erases old borders / prevents ghosting)
			if procWinPatBlt.Find() == nil {
				procWinPatBlt.Call(hdcOutMem, 0, 0, uintptr(outWidth), uintptr(outHeight), uintptr(BLACKNESS))
			} else if procWinBitBlt.Find() == nil {
				procWinBitBlt.Call(hdcOutMem, 0, 0, uintptr(outWidth), uintptr(outHeight), 0, 0, 0, uintptr(BLACKNESS))
			}

			// Blit or Stretch window into output canvas
			if destW == curW && destH == curH {
				procWinBitBlt.Call(hdcOutMem, uintptr(destX), uintptr(destY), uintptr(destW), uintptr(destH), hdcWinMem, 0, 0, uintptr(SRCCOPY))
			} else if procWinStretchBlt.Find() == nil {
				if procWinSetBrushOrgEx.Find() == nil {
					procWinSetBrushOrgEx.Call(hdcOutMem, 0, 0, 0)
				}
				procWinStretchBlt.Call(
					hdcOutMem,
					uintptr(destX), uintptr(destY), uintptr(destW), uintptr(destH),
					hdcWinMem,
					0, 0, uintptr(curW), uintptr(curH),
					uintptr(SRCCOPY),
				)
			} else {
				procWinBitBlt.Call(hdcOutMem, uintptr(destX), uintptr(destY), uintptr(destW), uintptr(destH), hdcWinMem, 0, 0, uintptr(SRCCOPY))
			}

			// 5. Read bits from output canvas into memory buffer
			procWinGetDIBits.Call(
				hdcOutMem,
				hOutBitmap,
				0,
				uintptr(outHeight),
				uintptr(unsafe.Pointer(&rawBytes[0])),
				uintptr(unsafe.Pointer(&bmi)),
				DIB_RGB_COLORS,
			)

			// Update last good frame
			copy(lastGoodFrame, rawBytes)
			hasValidFrame = true

			// 6. Write to FFmpeg rawvideo pipe
			if _, err := outPipe.Write(rawBytes); err != nil {
				return err
			}
		}
	}
}
