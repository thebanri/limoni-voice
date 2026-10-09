//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The installer changes the system through the Win32 API alone, not through reg, cmd or
// PowerShell: a blocked or restricted PowerShell cannot stop it, and an installer that runs
// shell commands is what antivirus heuristics flag.

var procSendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// registerInviteScheme registers the limoni:// URL scheme under HKCU, so no administrator
// rights are needed; removing HKCU\Software\Classes\limoni undoes it.
func registerInviteScheme(exe, icon string) error {
	const key = `Software\Classes\limoni`
	for _, v := range []struct{ path, name, value string }{
		{key, "", "URL:Limoni Voice invite"},
		{key, "URL Protocol", ""},
		{key + `\DefaultIcon`, "", icon},
		{key + `\shell\open\command`, "", `"` + exe + `" "%1"`},
	} {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, v.path, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetStringValue(v.name, v.value)
		k.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// addToUserPath puts dir at the front of the user's PATH unless it is there already, and tells
// running programs (Explorer, so new terminals) that the environment changed.
func addToUserPath(dir string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	path, typ, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return err
	}
	for entry := range strings.SplitSeq(path, ";") {
		if strings.EqualFold(filepath.Clean(strings.Trim(strings.TrimSpace(entry), `"`)), filepath.Clean(dir)) {
			return nil
		}
	}
	if path = strings.TrimLeft(path, ";"); path != "" {
		path = dir + ";" + path
	} else {
		path = dir
	}
	// Keep a REG_SZ path REG_SZ; a new or REG_EXPAND_SZ one keeps its %VARIABLES% working.
	if typ == registry.SZ {
		err = k.SetStringValue("Path", path)
	} else {
		err = k.SetExpandStringValue("Path", path)
	}
	if err != nil {
		return err
	}

	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
	env, _ := windows.UTF16PtrFromString("Environment")
	var result uintptr
	procSendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
	return nil
}

// launchInNewConsole starts exe in a console window of its own, which stays open after the
// installer's window closes.
func launchInNewConsole(exe, dir string) error {
	cmd := exec.Command(exe)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
	return cmd.Start()
}
