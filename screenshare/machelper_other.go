//go:build !darwin

package screenshare

import "errors"

func prebuiltMacHelper() ([]byte, error) { return nil, errors.New("macOS only") }
