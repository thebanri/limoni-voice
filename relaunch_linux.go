package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Alacritty on Wayland passes no dropped files to the program inside it (winit has no
// Wayland drag and drop yet); under XWayland it does. When the app menu opened Limoni Voice
// in an Alacritty on Wayland, the same Alacritty is opened again on X11 and this one closes.
// Limoni Voice itself keeps Wayland (the clipboard, mpv): WAYLAND_DISPLAY is handed over in
// LIMONI_WAYLAND_DISPLAY and put back. LIMONI_NO_X11=1 turns it off.

// relaunchedWaylandEnv names the variable that carries WAYLAND_DISPLAY over the relaunch.
const relaunchedWaylandEnv = "LIMONI_WAYLAND_DISPLAY"

// relaunchForDragAndDrop reopens the app's Alacritty window on X11 when needed, and reports
// whether this process should exit because the new window took over.
func relaunchForDragAndDrop() bool {
	if v := os.Getenv(relaunchedWaylandEnv); v != "" {
		// The relaunched app: Alacritty is on X11, the app goes on using Wayland.
		if os.Getenv("WAYLAND_DISPLAY") == "" {
			os.Setenv("WAYLAND_DISPLAY", v)
		}
		os.Unsetenv(relaunchedWaylandEnv)
		return false
	}
	ppid := os.Getppid()
	parentExe, _ := os.Readlink("/proc/" + strconv.Itoa(ppid) + "/exe")
	cmdline, _ := os.ReadFile("/proc/" + strconv.Itoa(ppid) + "/cmdline")
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	if !needsX11Relaunch(os.Getenv, parentExe, args) {
		return false
	}
	cmd := exec.Command(parentExe, args[1:]...)
	cmd.Env = append(withoutEnv(os.Environ(), "WAYLAND_DISPLAY"), relaunchedWaylandEnv+"="+os.Getenv("WAYLAND_DISPLAY"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlives this window
	if err := cmd.Start(); err != nil {
		return false // stay in this window: everything works but dropping files
	}
	go func() { _ = cmd.Wait() }()
	return true
}

// needsX11Relaunch reports whether the app runs straight inside an Alacritty on Wayland that
// started it (the app menu does: alacritty -e limoni-voice), with XWayland there to move to.
// Started from a shell, the parent is the shell, and nothing changes.
func needsX11Relaunch(getenv func(string) string, parentExe string, parentArgs []string) bool {
	if getenv("LIMONI_NO_X11") != "" || getenv("WAYLAND_DISPLAY") == "" || getenv("DISPLAY") == "" {
		return false
	}
	if filepath.Base(parentExe) != "alacritty" || len(parentArgs) < 2 {
		return false
	}
	// Only the window that runs this program: "-e" / "--command" followed by it.
	self, _ := os.Executable()
	for i, a := range parentArgs {
		if (a == "-e" || a == "--command") && i+1 < len(parentArgs) {
			return sameProgram(parentArgs[i+1], self)
		}
	}
	return false
}

// sameProgram reports whether a command names the running executable.
func sameProgram(command, self string) bool {
	if command == self || filepath.Base(command) == filepath.Base(self) {
		return true
	}
	a, errA := os.Stat(command)
	b, errB := os.Stat(self)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// withoutEnv drops a variable from an environment list.
func withoutEnv(env []string, name string) []string {
	prefix := []byte(name + "=")
	out := env[:0:0]
	for _, kv := range env {
		if !bytes.HasPrefix([]byte(kv), prefix) {
			out = append(out, kv)
		}
	}
	return out
}
