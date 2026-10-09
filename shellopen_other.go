//go:build !windows

package main

import "errors"

// shellOpen is Windows only; the other platforms open URLs and files with open or xdg-open.
func shellOpen(target string) error {
	return errors.New("shellOpen is only available on Windows")
}
