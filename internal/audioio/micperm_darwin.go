//go:build darwin

package audioio

import (
	"unsafe"

	"github.com/ebitengine/purego"
)

// microphonePermission asks AVFoundation for the microphone authorization of this process.
// A command line program inherits the decision made for the terminal it runs in.
func microphonePermission() MicPermission {
	objc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return MicPermissionUnknown
	}
	av, err := purego.Dlopen("/System/Library/Frameworks/AVFoundation.framework/AVFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return MicPermissionUnknown
	}
	mediaTypeAudio, err := purego.Dlsym(av, "AVMediaTypeAudio") // NSString *const
	if err != nil || mediaTypeAudio == 0 {
		return MicPermissionUnknown
	}
	var (
		getClass func(name string) uintptr
		selector func(name string) uintptr
		msgSend  func(receiver, sel, arg uintptr) int
	)
	purego.RegisterLibFunc(&getClass, objc, "objc_getClass")
	purego.RegisterLibFunc(&selector, objc, "sel_registerName")
	purego.RegisterLibFunc(&msgSend, objc, "objc_msgSend")

	class := getClass("AVCaptureDevice")
	if class == 0 {
		return MicPermissionUnknown
	}
	audio := **(**uintptr)(unsafe.Pointer(&mediaTypeAudio))
	// AVAuthorizationStatus: 0 not determined, 1 restricted, 2 denied, 3 authorized.
	switch msgSend(class, selector("authorizationStatusForMediaType:"), audio) {
	case 0:
		return MicPermissionUndecided
	case 1:
		return MicPermissionRestricted
	case 2:
		return MicPermissionDenied
	case 3:
		return MicPermissionGranted
	}
	return MicPermissionUnknown
}
