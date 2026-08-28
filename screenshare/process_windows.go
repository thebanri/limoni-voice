//go:build windows

package screenshare

import (
	"fmt"
	"os/exec"
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
