//go:build !linux
// +build !linux

package screenshare

import (
	"context"
	"errors"
	"os"
)

func RequestPortalScreenCast(ctx context.Context) (uint32, *os.File, func(), error) {
	return 0, nil, nil, errors.New("desktop portal screencast is only supported on Linux")
}

func buildGstreamerPipewireCommand(nodeID uint32, targetURL string, fps int) (string, []string, error) {
	return "", nil, errors.New("gstreamer pipewire is only supported on Linux")
}
