//go:build !windows

package screenshare

import (
	"os"
	"os/exec"
	"syscall"
)

func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	selfPgid, _ := syscall.Getpgid(os.Getpid())
	pgid, err := syscall.Getpgid(cmd.Process.Pid)

	// ONLY kill process group if it is a valid distinct group, and NOT the application's own process group!
	if err == nil && pgid > 1 && pgid != selfPgid {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}

	// Fallback to safely killing only the subprocess instantly
	_ = cmd.Process.Kill()
}
