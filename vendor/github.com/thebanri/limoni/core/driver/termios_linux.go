//go:build linux

package driver

import (
	"golang.org/x/sys/unix"
)

// TermiosState holds the terminal's original termios settings.
type TermiosState struct {
	termios unix.Termios
}

// MakeRaw switches the terminal to raw mode and returns the old settings for restoring later.
// It is pure Go with no cgo, using the Linux TCGETS/TCSETS ioctls directly.
func MakeRaw(fd int) (*TermiosState, error) {
	// Read the current terminal settings
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}

	// Keep a copy of the old settings
	oldState := &TermiosState{termios: *termios}

	// Apply the raw mode settings
	raw := *termios

	// Clear the input flags
	// BRKINT: do not send SIGINT on a break
	// ICRNL: do not turn carriage return (\r) into line feed (\n)
	// INPCK: turn off parity checking
	// ISTRIP: do not strip the 8th bit
	// IXON: disable software flow control (Ctrl-S / Ctrl-Q)
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON

	// Clear the output flags
	// OPOST: turn off output processing (no \n -> \r\n translation)
	raw.Oflag &^= unix.OPOST

	// Set the control flags
	// CSIZE: clear the character size mask
	// PARENB: disable parity
	// CS8: 8-bit characters
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8

	// Clear the local flags
	// ECHO: do not echo typed characters
	// ECHONL: do not echo newlines
	// ICANON: turn off canonical mode (input is read a character at a time, not a line at a time)
	// ISIG: disable signal characters (Ctrl-C: SIGINT, Ctrl-Z: SIGTSTP)
	// IEXTEN: turn off extended input processing (Ctrl-V)
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN

	// Set the read parameters (control characters)
	// VMIN = 1: block until at least 1 byte of input arrives (blocking read)
	// VTIME = 0: no read timeout
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0

	// Apply the new settings to the terminal (TCSETS - immediately)
	err = unix.IoctlSetTermios(fd, unix.TCSETS, &raw)
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
	return unix.IoctlSetTermios(fd, unix.TCSETS, &state.termios)
}
