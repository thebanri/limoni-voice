//go:build !windows

package screenshare

// GetPhysicalDesktopSize returns 0, 0 on non-Windows platforms
func GetPhysicalDesktopSize() (int, int) {
	return 0, 0
}
