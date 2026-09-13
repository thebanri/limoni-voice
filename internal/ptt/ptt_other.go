//go:build !windows && !darwin && !linux

package ptt

func startPlatform(string, ChangeFunc) (Watcher, error) {
	return nil, ErrUnsupported
}
