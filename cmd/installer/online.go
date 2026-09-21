//go:build !bundle

package main

// embeddedVoiceExe is empty in the default build: the installer downloads the application
// from the latest GitHub release instead.
var embeddedVoiceExe []byte
