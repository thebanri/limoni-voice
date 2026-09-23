//go:build darwin

package ptt

import (
	"errors"
	"sync"

	"github.com/ebitengine/purego"
)

const (
	coreGraphicsPath                  = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	ioKitPath                         = "/System/Library/Frameworks/IOKit.framework/IOKit"
	kCGEventSourceStateHIDSystemState = 1
	kIOHIDRequestTypeListenEvent      = 1
	kIOHIDAccessTypeGranted           = 0
	kIOHIDAccessTypeDenied            = 1
)

// ErrInputMonitoring means macOS keeps key and mouse state from the terminal. Without the
// permission the key reads as never pressed, so this is reported instead of a silent watcher.
var ErrInputMonitoring = errors.New("macOS Input Monitoring is off for this terminal: System Settings → Privacy & Security → Input Monitoring, turn on your terminal app, then restart it")

var (
	loadOnce              sync.Once
	loadErr               error
	cgEventSourceKeyState func(state int32, keycode uint16) bool
	cgEventSourceButton   func(state int32, button uint32) bool
	ioHIDCheckAccess      func(requestType uint32) uint32 // macOS 10.15+
	ioHIDRequestAccess    func(requestType uint32) bool
)

func load() error {
	loadOnce.Do(func() {
		lib, err := purego.Dlopen(coreGraphicsPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		purego.RegisterLibFunc(&cgEventSourceKeyState, lib, "CGEventSourceKeyState")
		purego.RegisterLibFunc(&cgEventSourceButton, lib, "CGEventSourceButtonState")
		if iokit, err := purego.Dlopen(ioKitPath, purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			if _, err := purego.Dlsym(iokit, "IOHIDCheckAccess"); err == nil {
				purego.RegisterLibFunc(&ioHIDCheckAccess, iokit, "IOHIDCheckAccess")
				purego.RegisterLibFunc(&ioHIDRequestAccess, iokit, "IOHIDRequestAccess")
			}
		}
	})
	return loadErr
}

// checkInputMonitoring asks macOS whether this process may read the keyboard. When it has
// not decided yet, the permission prompt is shown and the watcher starts anyway: the key works
// once the user allows it (macOS applies the choice after the terminal restarts).
func checkInputMonitoring() error {
	if ioHIDCheckAccess == nil {
		return nil // before macOS 10.15 no permission is needed
	}
	switch ioHIDCheckAccess(kIOHIDRequestTypeListenEvent) {
	case kIOHIDAccessTypeGranted:
		return nil
	case kIOHIDAccessTypeDenied:
		return ErrInputMonitoring
	default:
		ioHIDRequestAccess(kIOHIDRequestTypeListenEvent)
		return nil
	}
}

func startPlatform(key string, onChange ChangeFunc) (Watcher, error) {
	if err := load(); err != nil {
		return nil, ErrUnsupported
	}
	if err := checkInputMonitoring(); err != nil {
		return nil, err
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
	return startPoller("CGEventSourceKeyState", pressed, onChange, nil), nil
}
