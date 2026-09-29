//go:build !linux

package screenshare

func gstPipewireDepsError() error { return nil }

func gstPipewireDeps(bool) error { return nil }
