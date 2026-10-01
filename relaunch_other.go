//go:build !linux

package main

// relaunchForDragAndDrop is only needed for Alacritty on Wayland.
func relaunchForDragAndDrop() bool { return false }
