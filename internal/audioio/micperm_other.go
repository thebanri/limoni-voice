//go:build !darwin

package audioio

func microphonePermission() MicPermission { return MicPermissionUnknown }
