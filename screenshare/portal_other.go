//go:build !linux
// +build !linux

package screenshare

import (
	"context"
	"errors"
	"os"
)

func RequestPortalScreenCast(ctx context.Context, sourceType uint32) (uint32, *os.File, func(), error) {
	return 0, nil, nil, errors.New("portal is only supported on Linux")
}
