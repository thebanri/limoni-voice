//go:build windows

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCoCreateInstance = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")

	clsidShellLink  = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// Method indexes in the IShellLinkW and IPersistFile vtables.
const (
	vtQueryInterface  = 0
	vtRelease         = 2
	vtSetDescription  = 7
	vtSetWorkingDir   = 9
	vtSetIconLocation = 17
	vtSetPath         = 20
	vtPersistSave     = 6
)

// shortcutFolders returns the current user's desktop and Start Menu programs folders, as
// Windows knows them (a desktop moved to OneDrive included).
func shortcutFolders() map[string]string {
	folders := map[string]string{}
	for name, id := range map[string]*windows.KNOWNFOLDERID{"desktop": windows.FOLDERID_Desktop, "Start Menu": windows.FOLDERID_Programs} {
		if dir, err := windows.KnownFolderPath(id, windows.KF_FLAG_CREATE); err == nil && dir != "" {
			folders[name] = dir
		}
	}
	return folders
}

// createShortcut writes a .lnk file through the shell's own IShellLink object. It needs no
// PowerShell, so a blocked or restricted PowerShell cannot stop it.
func createShortcut(lnkPath, target, workDir, icon, description string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil || errors.Is(err, syscall.Errno(1)) { // S_FALSE: already initialized
		defer windows.CoUninitialize()
	}

	var link unsafe.Pointer
	if hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, uintptr(windows.CLSCTX_INPROC_SERVER),
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&link)),
	); int32(hr) < 0 || link == nil {
		return fmt.Errorf("CoCreateInstance(ShellLink): HRESULT 0x%08X", uint32(hr))
	}
	defer comCall(link, vtRelease)

	for _, set := range []struct {
		method int
		value  string
		what   string
	}{
		{vtSetPath, target, "target"},
		{vtSetWorkingDir, workDir, "working directory"},
		{vtSetDescription, description, "description"},
	} {
		if err := comCallString(link, set.method, set.value); err != nil {
			return fmt.Errorf("set %s: %w", set.what, err)
		}
	}
	if icon != "" {
		iconp, err := windows.UTF16PtrFromString(icon)
		if err != nil {
			return err
		}
		if err := comCall(link, vtSetIconLocation, uintptr(unsafe.Pointer(iconp)), 0); err != nil {
			return fmt.Errorf("set icon: %w", err)
		}
		runtime.KeepAlive(iconp)
	}

	var persist unsafe.Pointer
	if err := comCall(link, vtQueryInterface, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&persist))); err != nil {
		return fmt.Errorf("IPersistFile: %w", err)
	}
	defer comCall(persist, vtRelease)
	if err := comCallString(persist, vtPersistSave, filepath.Clean(lnkPath), 1); err != nil {
		return fmt.Errorf("save %s: %w", lnkPath, err)
	}
	return nil
}

// comCallString calls a COM method whose first argument is a string.
func comCallString(obj unsafe.Pointer, method int, s string, rest ...uintptr) error {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	err = comCall(obj, method, append([]uintptr{uintptr(unsafe.Pointer(p))}, rest...)...)
	runtime.KeepAlive(p)
	return err
}

// comCall calls method number method of the COM object obj and turns a failed HRESULT into an error.
func comCall(obj unsafe.Pointer, method int, args ...uintptr) error {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(method)*unsafe.Sizeof(uintptr(0))))
	hr, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	if int32(hr) < 0 {
		return fmt.Errorf("HRESULT 0x%08X", uint32(hr))
	}
	return nil
}
