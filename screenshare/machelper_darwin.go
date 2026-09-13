//go:build darwin

package screenshare

import "embed"

// machelper holds an optional prebuilt universal helper (machelper/limoni-sck) that release
// builds place there, so users do not need the Xcode command line tools.
//
//go:embed machelper
var machelper embed.FS

func prebuiltMacHelper() ([]byte, error) { return machelper.ReadFile("machelper/limoni-sck") }
