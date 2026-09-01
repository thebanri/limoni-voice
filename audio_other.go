//go:build !windows

package main

func (a *AudioEngine) startWindowsCapture(onFrame func(rms float64, speaking bool, pcm []byte)) bool {
	return false
}

func (a *AudioEngine) startWindowsPlayback() bool {
	return false
}

func enumerateWindowsInputDevices() []AudioDevice {
	return nil
}

func enumerateWindowsOutputDevices() []AudioDevice {
	return nil
}
