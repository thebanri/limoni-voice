//go:build bundle

package main

import _ "embed"

// embeddedVoiceExe is the application binary; scripts/build_packages.sh copies it here and
// builds with -tags bundle.
//
//go:embed limoni-voice.exe
var embeddedVoiceExe []byte
