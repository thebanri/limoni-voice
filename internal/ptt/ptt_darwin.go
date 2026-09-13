//go:build darwin

package ptt

import (
	"sync"

	"github.com/ebitengine/purego"
)

const (
	coreGraphicsPath                  = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	kCGEventSourceStateHIDSystemState = 1
)

var (
	loadOnce              sync.Once
	loadErr               error
	cgEventSourceKeyState func(state int32, keycode uint16) bool
	cgEventSourceButton   func(state int32, button uint32) bool
)

// macOS virtual key codes (Carbon kVK_*).
var macKeys = map[string]uint16{
	"A": 0x00, "S": 0x01, "D": 0x02, "F": 0x03, "H": 0x04, "G": 0x05, "Z": 0x06, "X": 0x07,
	"C": 0x08, "V": 0x09, "B": 0x0B, "Q": 0x0C, "W": 0x0D, "E": 0x0E, "R": 0x0F, "Y": 0x10,
	"T": 0x11, "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17, "9": 0x19,
	"7": 0x1A, "8": 0x1C, "0": 0x1D, "O": 0x1F, "U": 0x20, "I": 0x22, "P": 0x23, "L": 0x25,
	"J": 0x26, "K": 0x28, "N": 0x2D, "M": 0x2E,
	"Enter": 0x24, "Tab": 0x30, "Space": 0x31, "CapsLock": 0x39, "RightCtrl": 0x3E, "RightAlt": 0x3D,
	"F1": 0x7A, "F2": 0x78, "F3": 0x63, "F4": 0x76, "F5": 0x60, "F6": 0x61,
	"F7": 0x62, "F8": 0x64, "F9": 0x65, "F10": 0x6D, "F11": 0x67,
}

func load() error {
	loadOnce.Do(func() {
		lib, err := purego.Dlopen(coreGraphicsPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		purego.RegisterLibFunc(&cgEventSourceKeyState, lib, "CGEventSourceKeyState")
		purego.RegisterLibFunc(&cgEventSourceButton, lib, "CGEventSourceButtonState")
	})
	return loadErr
}

func startPlatform(key string, onChange ChangeFunc) (Watcher, error) {
	if err := load(); err != nil {
		return nil, ErrUnsupported
	}
	var pressed func() bool
	switch key {
	case "Mouse4", "Mouse5":
		button := uint32(3)
		if key == "Mouse5" {
			button = 4
		}
		pressed = func() bool { return cgEventSourceButton(kCGEventSourceStateHIDSystemState, button) }
	default:
		code, ok := macKeys[key]
		if !ok {
			return nil, ErrUnsupported
		}
		pressed = func() bool { return cgEventSourceKeyState(kCGEventSourceStateHIDSystemState, code) }
	}
	// Requires "Input Monitoring" permission for the terminal on macOS 10.15+.
	return startPoller("CGEventSourceKeyState", pressed, onChange, nil), nil
}
