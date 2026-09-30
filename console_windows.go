package main

import (
	"os"

	"golang.org/x/sys/windows"
)

const cpUTF8 = 65001

// Console code pages to put back on exit (0 = unchanged).
var savedInCP, savedOutCP uint32

// setupConsole prepares the Windows console for the TUI; restoreConsole undoes it.
//
// The console code page is switched to UTF-8 so box drawing, Braille and emoji render, and
// COLORTERM is set because Windows 10 1709+ consoles (conhost, Windows Terminal, ConEmu) draw
// 24-bit color once virtual terminal processing is on, but never advertise it.
func setupConsole() {
	savedInCP, _ = windows.GetConsoleCP()
	savedOutCP, _ = windows.GetConsoleOutputCP()
	_ = windows.SetConsoleCP(cpUTF8)
	_ = windows.SetConsoleOutputCP(cpUTF8)

	if os.Getenv("COLORTERM") == "" && os.Getenv("TERM") != "dumb" {
		_ = os.Setenv("COLORTERM", "truecolor")
	}
}

func restoreConsole() {
	if savedInCP != 0 {
		_ = windows.SetConsoleCP(savedInCP)
	}
	if savedOutCP != 0 {
		_ = windows.SetConsoleOutputCP(savedOutCP)
	}
}

// disableQuickEdit turns off the console's QuickEdit mode once Limoni has put it in raw mode.
// With it on, conhost takes a click, a drag or Ctrl+A for a text selection and holds every
// write to the console until the selection ends: the app freezes, and mouse input never
// reaches it. Limoni puts the original mode back on exit.
func disableQuickEdit() {
	in := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(in, &mode) != nil {
		return
	}
	_ = windows.SetConsoleMode(in, (mode&^windows.ENABLE_QUICK_EDIT_MODE)|windows.ENABLE_EXTENDED_FLAGS)
}
