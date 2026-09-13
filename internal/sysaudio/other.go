//go:build darwin

package sysaudio

// On macOS system audio is delivered by the ScreenCaptureKit helper together with the video.
func open(FrameFunc) (Stream, error) { return nil, ErrUnsupported }
