//go:build darwin

package driver

import (
	"golang.org/x/sys/unix"
)

// TermiosState holds the terminal's original termios settings.
type TermiosState struct {
	termios unix.Termios
}

// MakeRaw switches the terminal to raw mode and returns the old settings for restoring later.
// On macOS (Darwin) it uses the TIOCGETA / TIOCSETA ioctls, without cgo.
func MakeRaw(fd int) (*TermiosState, error) {
	// Read the current terminal settings
	termios, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}

	// Keep a copy of the old settings
	oldState := &TermiosState{termios: *termios}

	// Apply the raw mode settings
	raw := *termios

	// Clear the input flags
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON

	// Clear the output flags
	raw.Oflag &^= unix.OPOST

	// Set the control flags
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8

	// Clear the local flags
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN

	// Set the read parameters (control characters)
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0

	// Apply the new settings to the terminal (TIOCSETA - immediately)
	err = unix.IoctlSetTermios(fd, unix.TIOCSETA, &raw)
	if err != nil {
		return nil, err
	}

	return oldState, nil
}

// Restore returns the terminal to its original settings.
func Restore(fd int, state *TermiosState) error {
	if state == nil {
		return nil
	}
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, &state.termios)
}
