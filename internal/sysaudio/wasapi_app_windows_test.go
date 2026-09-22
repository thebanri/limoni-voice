//go:build windows

package sysaudio

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The PROPVARIANT blob must match the C layout: the byte count after the 8-byte header and
// the pointer at the next pointer-aligned offset.
func TestPropVariantBlobLayout(t *testing.T) {
	var pv propVariantBlob
	wantData := uintptr(12)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		wantData = 16
	}
	if unsafe.Offsetof(pv.size) != 8 || unsafe.Offsetof(pv.data) != wantData {
		t.Fatalf("size at %d, data at %d; want 8 and %d", unsafe.Offsetof(pv.size), unsafe.Offsetof(pv.data), wantData)
	}
	if unsafe.Sizeof(audioClientActivationParams{}) != 12 {
		t.Fatal("AUDIOCLIENT_ACTIVATION_PARAMS must be 12 bytes")
	}
}

// Windows requires the completion handler to be agile and rejects other interfaces.
func TestActivationHandlerInterfaces(t *testing.T) {
	h := &activationHandler{vtbl: handlerVtbl(), done: make(chan struct{})}
	this := uintptr(unsafe.Pointer(h))
	for _, iid := range []*windows.GUID{&iidIUnknown, &iidIAgileObject, &iidIActivateAudioInterfaceCompletionHandler} {
		var out uintptr
		if hr := handlerQueryInterface(this, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))); hr != 0 || out != this {
			t.Fatalf("QueryInterface(%v) = %#x, %#x", *iid, hr, out)
		}
	}
	var out uintptr = 1
	if hr := handlerQueryInterface(this, uintptr(unsafe.Pointer(&iidIAudioClient)), uintptr(unsafe.Pointer(&out))); hr != eNoInterface || out != 0 {
		t.Fatalf("QueryInterface(IAudioClient) = %#x, %#x", hr, out)
	}
	handlerActivateCompleted(this, 0)
	select {
	case <-h.done:
	default:
		t.Fatal("ActivateCompleted did not signal")
	}
}
