//go:build freebsd || openbsd || netbsd || dragonfly

package driver

import (
	"golang.org/x/sys/unix"
)

// TermiosState holds the terminal's original termios settings.
type TermiosState struct {
	termios unix.Termios
}

// MakeRaw switches the terminal to raw mode and returns the old settings for restoring later.
// On BSD it uses the TIOCGETA / TIOCSETA ioctls, without cgo.
func MakeRaw(fd int) (*TermiosState, error) {
	termios, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}

	oldState := &TermiosState{termios: *termios}
	raw := *termios

	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN

	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0

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
