//go:build windows

package screenshare

import (
	"syscall"
)

var (
	winModGDI32        = syscall.NewLazyDLL("gdi32.dll")
	winModUser32       = syscall.NewLazyDLL("user32.dll")
	winProcGetDC       = winModUser32.NewProc("GetDC")
	winProcReleaseDC   = winModUser32.NewProc("ReleaseDC")
	winProcGetDevCaps  = winModGDI32.NewProc("GetDeviceCaps")
	winProcSetDPIAware = winModUser32.NewProc("SetProcessDPIAware")
)

const (
	winDESKTOPHORZRES = 118
	winDESKTOPVERTRES = 117
)

func init() {
	// Enable DPI awareness so Windows never virtualizes coordinates or screen dimensions
	winProcSetDPIAware.Call()
}

// GetPhysicalDesktopSize returns the true physical display resolution (e.g. 1920x1080, 2560x1440)
func GetPhysicalDesktopSize() (int, int) {
	hdc, _, _ := winProcGetDC.Call(0)
	if hdc == 0 {
		return 0, 0
	}
	defer winProcReleaseDC.Call(0, hdc)

	w, _, _ := winProcGetDevCaps.Call(hdc, uintptr(winDESKTOPHORZRES))
	h, _, _ := winProcGetDevCaps.Call(hdc, uintptr(winDESKTOPVERTRES))
	if w == 0 || h == 0 {
		return 0, 0
	}
	// Force even dimensions for H.264
	w = (w / 2) * 2
	h = (h / 2) * 2
	return int(w), int(h)
}
