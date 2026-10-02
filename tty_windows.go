package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether f is a console the TUI can put in raw mode. NUL and pipes are
// not: GetConsoleMode fails on them.
func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
