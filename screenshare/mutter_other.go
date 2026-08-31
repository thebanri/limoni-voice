//go:build !linux
// +build !linux

package screenshare

import (
	"context"
	"errors"
)

func RequestMutterScreenCast(ctx context.Context, connector string) (uint32, func(), error) {
	return 0, nil, errors.New("mutter screencast is only supported on Linux")
}

func isMutterAvailable() bool {
	return false
}

func buildGstreamerPipewireCommand(nodeID uint32, targetURL string, fps int) (string, []string, error) {
	return "", nil, errors.New("gstreamer pipewire is only supported on Linux")
}
