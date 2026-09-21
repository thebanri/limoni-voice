// Package assets holds the files embedded into the Limoni Voice binaries.
package assets

import _ "embed"

// MicrophoneOBJ is the 3D studio microphone shown in the lobby (Wavefront OBJ).
//
//go:embed microphone.obj
var MicrophoneOBJ []byte

// IconICO is the Windows application icon the installer places next to the app.
//
//go:embed icon.ico
var IconICO []byte
