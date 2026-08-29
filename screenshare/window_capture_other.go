//go:build !windows

package screenshare

import (
	"context"
	"errors"
	"io"
)

// GetWindowDimensions is a stub on non-Windows platforms
func GetWindowDimensions(hwnd uintptr) (int, int) {
	return 1280, 720
}

// StreamWindowFrames is a stub on non-Windows platforms (Linux uses gpu-screen-recorder portal)
func StreamWindowFrames(ctx context.Context, hwnd uintptr, fps int, outPipe io.WriteCloser) error {
	defer outPipe.Close()
	return errors.New("StreamWindowFrames is only supported on Windows")
}
