//go:build !linux && !darwin && !windows

package notify

import "errors"

func send(string, string) error { return errors.New("notify: not supported on this platform") }
