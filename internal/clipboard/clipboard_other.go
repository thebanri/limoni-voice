//go:build !linux

// Package clipboard reads and owns the desktop clipboard natively on Linux; elsewhere the
// platform's own tools are used by the caller.
package clipboard

import "errors"

// ErrUnavailable is returned on platforms without a native implementation.
var ErrUnavailable = errors.New("clipboard: no native implementation on this platform")

// Read is not implemented here.
func Read() (string, error) { return "", ErrUnavailable }

// Write is not implemented here.
func Write(string) error { return ErrUnavailable }
