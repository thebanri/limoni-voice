//go:build !windows

package screenshare

import (
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

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		go func(p int) {
			time.Sleep(300 * time.Millisecond)
			_ = syscall.Kill(-p, syscall.SIGKILL)
		}(pgid)
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
}
