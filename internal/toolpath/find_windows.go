//go:build windows

package toolpath

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// registryPathDirs returns the directories of the user and system PATH as they are saved in the
// registry right now. A running process keeps the PATH it started with, so a program that
// winget, Scoop or an installer adds to PATH later is only found through the registry.
func registryPathDirs() []string {
	var dirs []string
	for _, src := range []struct {
		root windows.Handle
		key  string
	}{
		{windows.HKEY_CURRENT_USER, `Environment`},
		{windows.HKEY_LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
	} {
		for dir := range strings.SplitSeq(readRegistryString(src.root, src.key, "Path"), ";") {
			if dir = strings.Trim(strings.TrimSpace(dir), `"`); dir != "" {
				dirs = append(dirs, filepath.Clean(dir))
			}
		}
	}
	return dirs
}

// readRegistryString reads a REG_SZ or REG_EXPAND_SZ value, expanding %VARIABLES% ("" on error).
func readRegistryString(root windows.Handle, path, name string) string {
	pathp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	namep, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return ""
	}
	var key windows.Handle
	if windows.RegOpenKeyEx(root, pathp, 0, windows.KEY_QUERY_VALUE, &key) != nil {
		return ""
	}
	defer windows.RegCloseKey(key)

	var typ, size uint32
	if windows.RegQueryValueEx(key, namep, nil, &typ, nil, &size) != nil || size < 2 {
		return ""
	}
	if typ != windows.REG_SZ && typ != windows.REG_EXPAND_SZ {
		return ""
	}
	buf := make([]uint16, size/2+1)
	if windows.RegQueryValueEx(key, namep, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &size) != nil {
		return ""
	}
	value := windows.UTF16ToString(buf)
	if typ == windows.REG_EXPAND_SZ {
		value = expandEnv(value)
	}
	return value
}

func expandEnv(s string) string {
	src, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return s
	}
	n, err := windows.ExpandEnvironmentStrings(src, nil, 0)
	if err != nil || n == 0 {
		return s
	}
	buf := make([]uint16, n)
	if _, err := windows.ExpandEnvironmentStrings(src, &buf[0], n); err != nil {
		return s
	}
	return windows.UTF16ToString(buf)
}
