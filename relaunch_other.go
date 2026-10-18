//go:build !linux

package main

// relaunchForDragAndDrop is only needed for Alacritty on Wayland.
func relaunchForDragAndDrop() bool { return false }

// openInUserTerminal is for the Linux invite link handler, which starts the app with no terminal.
func openInUserTerminal([]string) bool { return false }
