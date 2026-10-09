//go:build !windows

package main

import (
	"errors"
	"os/exec"
)

// The installer only runs on Windows; these keep the package building elsewhere.

func shortcutFolders() map[string]string { return nil }

func createShortcut(lnkPath, target, workDir, icon, description string) error {
	return errors.New("shortcuts are only created on Windows")
}

func registerInviteScheme(exe, icon string) error {
	return errors.New("invite links are only registered on Windows")
}

func addToUserPath(dir string) error {
	return errors.New("PATH is only changed on Windows")
}

func launchInNewConsole(exe, dir string) error {
	cmd := exec.Command(exe)
	cmd.Dir = dir
	return cmd.Start()
}
