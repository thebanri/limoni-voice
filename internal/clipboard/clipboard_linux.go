//go:build linux

package clipboard

import (
	"errors"
	"os"
	"time"
)

// ErrUnavailable means neither a Wayland data-control compositor nor an X server was
// reachable.
var ErrUnavailable = errors.New("clipboard: no Wayland data-control or X11 display")

const readTimeout = time.Second

// Read returns the clipboard's text ("" when it holds none).
func Read() (string, error) {
	var errs []error
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		text, err := readWayland(readTimeout)
		if err == nil {
			return text, nil
		}
		errs = append(errs, err)
	}
	if os.Getenv("DISPLAY") != "" {
		text, err := readX11(readTimeout)
		if err == nil {
			return text, nil
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return "", ErrUnavailable
	}
	return "", errors.Join(errs...)
}

// Write puts text on the clipboard. This process keeps serving it in the background
// until something else is copied, so it is gone once the process exits.
func Write(text string) error {
	var errs []error
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		err := writeWayland(text)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	if os.Getenv("DISPLAY") != "" {
		err := writeX11(text)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return ErrUnavailable
	}
	return errors.Join(errs...)
}
