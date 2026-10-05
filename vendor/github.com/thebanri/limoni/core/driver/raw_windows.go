//go:build windows

package driver

import (
	"golang.org/x/sys/windows"
)

// WindowsConsoleState holds the terminal's previous console modes.
type WindowsConsoleState struct {
	inHandle  windows.Handle
	outHandle windows.Handle
	inMode    uint32
	outMode   uint32
}

// MakeRaw switches the Windows console to VT100 / virtual terminal (raw) mode.
func MakeRaw(inFd, outFd uintptr) (*WindowsConsoleState, error) {
	inHandle := windows.Handle(inFd)
	outHandle := windows.Handle(outFd)

	var inMode, outMode uint32
	if err := windows.GetConsoleMode(inHandle, &inMode); err != nil {
		return nil, err
	}
	if err := windows.GetConsoleMode(outHandle, &outMode); err != nil {
		return nil, err
	}

	state := &WindowsConsoleState{
		inHandle:  inHandle,
		outHandle: outHandle,
		inMode:    inMode,
		outMode:   outMode,
	}

	// Raw input: turn off Line/Echo/Processed, turn on Virtual Terminal Input and Window/Mouse Input
	rawInMode := inMode &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT)
	rawInMode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_EXTENDED_FLAGS

	// Raw output: turn on VT100 virtual terminal processing
	rawOutMode := outMode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.ENABLE_PROCESSED_OUTPUT

	if err := windows.SetConsoleMode(inHandle, rawInMode); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(outHandle, rawOutMode); err != nil {
		_ = windows.SetConsoleMode(inHandle, inMode)
		return nil, err
	}

	return state, nil
}

// Restore returns the terminal to its previous console modes.
func RestoreConsole(state *WindowsConsoleState) error {
	if state == nil {
		return nil
	}
	var lastErr error
	if err := windows.SetConsoleMode(state.inHandle, state.inMode); err != nil {
		lastErr = err
	}
	if err := windows.SetConsoleMode(state.outHandle, state.outMode); err != nil {
		lastErr = err
	}
	return lastErr
}
