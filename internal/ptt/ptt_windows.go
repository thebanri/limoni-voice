//go:build windows

package ptt

import "syscall"

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
)

var virtualKeys = map[string]uintptr{
	"Space": 0x20, "Tab": 0x09, "Enter": 0x0D, "CapsLock": 0x14,
	"RightCtrl": 0xA3, "RightAlt": 0xA5, "Mouse4": 0x05, "Mouse5": 0x06,
	"F1": 0x70, "F2": 0x71, "F3": 0x72, "F4": 0x73, "F5": 0x74, "F6": 0x75,
	"F7": 0x76, "F8": 0x77, "F9": 0x78, "F10": 0x79, "F11": 0x7A,
}

// modifierKeys: VK_CONTROL, VK_MENU and VK_SHIFT cover both sides; the Windows key has two.
var modifierKeys = map[string][]uintptr{"Ctrl": {0x11}, "Alt": {0x12}, "Shift": {0x10}, "Cmd": {0x5B, 0x5C}}

func startPlatform(name string, onChange ChangeFunc) (Watcher, error) {
	if procGetAsyncKeyState.Find() != nil {
		return nil, ErrUnsupported
	}
	mods, key := SplitCombo(name)
	vk, ok := virtualKeys[key]
	if !ok && len(key) == 1 {
		vk = uintptr(key[0]) // 'A'-'Z' and '0'-'9' match their ASCII codes
		ok = true
	}
	if !ok {
		return nil, ErrUnsupported
	}
	groups := [][]uintptr{{vk}}
	for _, m := range mods {
		groups = append(groups, modifierKeys[m])
	}
	down := func(vk uintptr) bool {
		state, _, _ := procGetAsyncKeyState.Call(vk)
		return state&0x8000 != 0
	}
	pressed := func() bool { return allHeld(groups, down) }
	return startPoller("GetAsyncKeyState", pressed, onChange, nil), nil
}
