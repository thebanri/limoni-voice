package audioio

// MicPermission is the operating system's decision about microphone access.
type MicPermission int

const (
	MicPermissionUnknown    MicPermission = iota // no such permission, or it cannot be read
	MicPermissionUndecided                       // the system will ask when capture starts
	MicPermissionDenied                          // capture delivers silence
	MicPermissionRestricted                      // blocked by a profile or parental controls
	MicPermissionGranted
)

// MicPermissionHint tells the user how to give the microphone back.
const MicPermissionHint = "macOS is blocking the microphone: System Settings → Privacy & Security → Microphone, turn on your terminal app, then restart it"

// MicrophonePermission reports whether the system lets this process record. Only macOS has
// such a permission; elsewhere it is MicPermissionUnknown.
func MicrophonePermission() MicPermission { return microphonePermission() }
