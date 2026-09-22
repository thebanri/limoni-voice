//go:build windows

package screenshare

import (
	"fmt"
	"os/exec"

	"golang.org/x/sys/windows"
)

func setupProcessGroup(cmd *exec.Cmd) {
	// On Windows, child processes are managed via process trees
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Terminate process tree forcefully on Windows
	_ = exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", cmd.Process.Pid)).Run()
}

// windowProcessID returns the process that owns a top-level window.
func windowProcessID(hwnd uintptr) int {
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(windows.HWND(hwnd), &pid); err != nil {
		return 0
	}
	return int(pid)
}
