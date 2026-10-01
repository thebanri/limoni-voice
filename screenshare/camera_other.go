//go:build !linux

package screenshare

import "errors"

type v4l2Camera struct{ device, name string }

func listV4L2Cameras() []v4l2Camera { return nil }

func buildGstreamerCameraCommand(BroadcastOptions, string) (string, []string, error) {
	return "", nil, errors.New("V4L2 cameras are only supported on Linux")
}
