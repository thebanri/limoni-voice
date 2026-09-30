//go:build !windows

package toolpath

// registryPathDirs is Windows only: elsewhere PATH has no saved copy to reread.
func registryPathDirs() []string { return nil }
