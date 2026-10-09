package main

import (
	"runtime"

	"golang.org/x/sys/windows"
)

// shellOpen hands a URL or file to its default program through ShellExecuteW, as Explorer
// does. It starts no rundll32, cmd or PowerShell in between, so nothing in the target is ever
// parsed as a command line.
func shellOpen(target string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// The shell's handlers may use COM; Microsoft asks callers to initialize it first.
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
		defer windows.CoUninitialize()
	}

	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
