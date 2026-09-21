//go:build !windows

package main

// setupConsole and restoreConsole prepare the console for the TUI; only Windows needs it.
func setupConsole()   {}
func restoreConsole() {}
