//go:build !windows

package main

import "errors"

// The installer only runs on Windows; these keep the package building elsewhere.

func shortcutFolders() map[string]string { return nil }

func createShortcut(lnkPath, target, workDir, icon, description string) error {
	return errors.New("shortcuts are only created on Windows")
}
