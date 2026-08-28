//go:build !windows

package screenshare

import (
	"os"
	"os/exec"
	"syscall"
	"time"
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
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		go func(p int) {
			time.Sleep(200 * time.Millisecond)
			_ = syscall.Kill(-p, syscall.SIGKILL)
		}(pgid)
		return
	}

	// Fallback to safely killing only the subprocess
	_ = cmd.Process.Kill()
}
