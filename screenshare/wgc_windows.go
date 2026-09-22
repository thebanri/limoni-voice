//go:build windows

package screenshare

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows Graphics Capture.
//
// PrintWindow reads a window's GDI surface, which is empty where the application draws with
// the GPU: a browser playing video, a game or a hardware-accelerated player comes out frozen
// or black as soon as the window is covered. Windows Graphics Capture reads the composited
// window instead, so it carries that content and keeps working when the window is behind
// others. A minimized window still has nothing to capture; nothing can change that.
//
// This needs no cgo: the WinRT factories and the D3D11 interfaces are reached through their
// COM vtables. Anything that fails here falls back to the GDI capture.

var (
	modCombase = windows.NewLazySystemDLL("combase.dll")
	modD3D11   = windows.NewLazySystemDLL("d3d11.dll")

	procRoInitialize           = modCombase.NewProc("RoInitialize")
	procRoUninitialize         = modCombase.NewProc("RoUninitialize")
	procRoGetActivationFactory = modCombase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = modCombase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = modCombase.NewProc("WindowsDeleteString")
	procD3D11CreateDevice      = modD3D11.NewProc("D3D11CreateDevice")
	procCreateDirect3D11Device = modD3D11.NewProc("CreateDirect3D11DeviceFromDXGIDevice")
	errWGCUnavailable          = errors.New("windows graphics capture is not available")
	iidGraphicsCaptureItem     = windows.GUID{Data1: 0x79C3F95B, Data2: 0x31F7, Data3: 0x4EC2, Data4: [8]byte{0xA4, 0x64, 0x63, 0x2E, 0xF5, 0xD3, 0x07, 0x60}}
	iidCaptureItemInterop      = windows.GUID{Data1: 0x3628E81B, Data2: 0x3CAC, Data3: 0x4C60, Data4: [8]byte{0xB7, 0xF4, 0x23, 0xCE, 0x0E, 0x0C, 0x33, 0x56}}
	iidFramePoolStatics2       = windows.GUID{Data1: 0x589B103F, Data2: 0x6BBC, Data3: 0x5DF5, Data4: [8]byte{0xA9, 0x91, 0x02, 0xE2, 0x8B, 0x3B, 0x66, 0xD5}}
	iidSession2                = windows.GUID{Data1: 0x2C39AE40, Data2: 0x7D2E, Data3: 0x5044, Data4: [8]byte{0x80, 0x4E, 0x8B, 0x67, 0x99, 0xD4, 0xCF, 0x9E}}
	iidSession3                = windows.GUID{Data1: 0xF2CDD966, Data2: 0x22AE, Data3: 0x5EA1, Data4: [8]byte{0x95, 0x96, 0x3A, 0x28, 0x93, 0x44, 0xC3, 0xBE}}
	iidClosable                = windows.GUID{Data1: 0x30D5A829, Data2: 0x7FA4, Data3: 0x4B82, Data4: [8]byte{0x9B, 0xA8, 0xB7, 0x31, 0x1B, 0x1C, 0x4E, 0x7B}}
	iidDxgiDevice              = windows.GUID{Data1: 0x54EC77FA, Data2: 0x1377, Data3: 0x44E6, Data4: [8]byte{0x8C, 0x32, 0x88, 0xFD, 0x5F, 0x44, 0xC8, 0x4C}}
	iidDxgiInterfaceAccess     = windows.GUID{Data1: 0xA9B3D012, Data2: 0x3DF2, Data3: 0x4EE3, Data4: [8]byte{0xB8, 0xD1, 0x86, 0x95, 0xF4, 0x57, 0xD3, 0xC1}}
	iidTexture2D               = windows.GUID{Data1: 0x6F15AAF2, Data2: 0xD208, Data3: 0x4E89, Data4: [8]byte{0x9A, 0xB4, 0x48, 0x95, 0x35, 0xD3, 0x4F, 0x9C}}
	classCaptureItem           = "Windows.Graphics.Capture.GraphicsCaptureItem"
	classFramePool             = "Windows.Graphics.Capture.Direct3D11CaptureFramePool"
)

// wgcDefault says whether the capture is used unless LIMONI_WGC says otherwise. It captures
// GPU-drawn content and covered windows, which the GDI capture cannot; LIMONI_WGC=0 goes back
// to that one.
const wgcDefault = true

// COM vtable slots. WinRT interfaces start their own methods at 6, after IUnknown (3) and
// IInspectable (GetIids, GetRuntimeClassName, GetTrustLevel).
const (
	comQueryInterface = 0
	comRelease        = 2
	inspectableFirst  = 6

	interopCreateForWindow = 3 // IGraphicsCaptureItemInterop (a plain IUnknown)
	dxgiAccessGetInterface = 3 // IDirect3DDxgiInterfaceAccess

	itemGetSize = inspectableFirst + 1

	framePoolCreateFreeThreaded  = inspectableFirst // on IDirect3D11CaptureFramePoolStatics2
	framePoolRecreate            = inspectableFirst
	framePoolTryGetNextFrame     = inspectableFirst + 1
	framePoolCreateCaptureSesion = inspectableFirst + 4

	frameGetSurface     = inspectableFirst
	frameGetContentSize = inspectableFirst + 2

	sessionStartCapture        = inspectableFirst
	sessionPutCursorCapture    = inspectableFirst + 1 // IGraphicsCaptureSession2
	sessionPutIsBorderRequired = inspectableFirst + 1 // IGraphicsCaptureSession3
	closableClose              = inspectableFirst

	// ID3D11Device derives straight from IUnknown, so its own methods start at 3.
	deviceCreateTexture2D = 5
	// ID3D11Texture2D and ID3D11DeviceContext both reach their own methods past IUnknown (3),
	// ID3D11DeviceChild (4 more) and, for the texture, ID3D11Resource (3 more).
	textureGetDesc      = 10
	contextMap          = 14
	contextUnmap        = 15
	contextCopyResource = 47
)

const (
	roInitMultiThreaded  = 1
	driverTypeHardware   = 1
	driverTypeWARP       = 5
	d3d11SDKVersion      = 7
	createDeviceBGRA     = 0x20
	formatB8G8R8A8UNorm  = 87 // DXGI_FORMAT and DirectXPixelFormat agree on this value
	usageStaging         = 3
	cpuAccessRead        = 0x20000
	mapRead              = 1
	framePoolBufferCount = 2
)

// comObject is a COM interface pointer: a pointer to its vtable.
type comObject struct {
	vtbl *[64]uintptr
}

func call(obj *comObject, slot int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(obj.vtbl[slot], append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)...)
	return r
}

func hr(r uintptr, what string) error {
	if int32(r) < 0 {
		return fmt.Errorf("wgc: %s failed: HRESULT 0x%08X", what, uint32(r))
	}
	return nil
}

func releaseObj(obj *comObject) {
	if obj != nil {
		call(obj, comRelease)
	}
}

// closeObj closes a WinRT object that implements IClosable and releases it.
func closeObj(obj *comObject) {
	if obj == nil {
		return
	}
	var closable *comObject
	if call(obj, comQueryInterface, uintptr(unsafe.Pointer(&iidClosable)), uintptr(unsafe.Pointer(&closable))) == 0 {
		call(closable, closableClose)
		releaseObj(closable)
	}
	releaseObj(obj)
}

// sizeInt32 is Windows.Graphics.SizeInt32. Passed by value it fills one register on 64-bit
// Windows and two argument slots on 32-bit.
type sizeInt32 struct {
	Width  int32
	Height int32
}

func (s sizeInt32) args() []uintptr {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return []uintptr{uintptr(uint32(s.Width)) | uintptr(uint32(s.Height))<<32}
	}
	return []uintptr{uintptr(uint32(s.Width)), uintptr(uint32(s.Height))}
}

type texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type mappedSubresource struct {
	Data       *byte
	RowPitch   uint32
	DepthPitch uint32
}

// hstring is a WinRT string handle; the class names are created once per capture.
func newHString(s string) (uintptr, error) {
	u16, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h uintptr
	r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1), uintptr(unsafe.Pointer(&h)))
	if err := hr(r, "WindowsCreateString"); err != nil {
		return 0, err
	}
	return h, nil
}

func activationFactory(class string, iid *windows.GUID) (*comObject, error) {
	name, err := newHString(class)
	if err != nil {
		return nil, err
	}
	defer procWindowsDeleteString.Call(name)
	var factory *comObject
	r, _, _ := procRoGetActivationFactory.Call(name, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&factory)))
	if err := hr(r, "RoGetActivationFactory("+class+")"); err != nil {
		return nil, err
	}
	return factory, nil
}

// wgcEnabled reports whether the capture may be used. It is new and reached through raw COM
// vtables, so LIMONI_WGC=0 puts the GDI capture back without a new build.
func wgcEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LIMONI_WGC"))) {
	case "0", "off", "false", "no":
		return false
	case "1", "on", "true", "yes":
		return true
	}
	return wgcDefault
}

// wgcSupported reports whether this Windows build has the capture API at all.
func wgcSupported() bool {
	for _, p := range []*windows.LazyProc{procRoInitialize, procRoGetActivationFactory, procWindowsCreateString, procD3D11CreateDevice, procCreateDirect3D11Device} {
		if p.Find() != nil {
			return false
		}
	}
	return true
}

// wgcSession holds everything one capture needs, so a failure part-way can release it all.
type wgcSession struct {
	device   *comObject // ID3D11Device
	context  *comObject // ID3D11DeviceContext
	rtDevice *comObject // IDirect3DDevice (the WinRT wrapper)
	item     *comObject // IGraphicsCaptureItem
	pool     *comObject // IDirect3D11CaptureFramePool
	session  *comObject // IGraphicsCaptureSession
	staging  *comObject // ID3D11Texture2D the frames are copied into for reading
	stagingW int32
	stagingH int32
}

func (w *wgcSession) close() {
	releaseObj(w.staging)
	closeObj(w.session)
	closeObj(w.pool)
	closeObj(w.item)
	releaseObj(w.rtDevice)
	releaseObj(w.context)
	releaseObj(w.device)
	*w = wgcSession{}
}

// startWGC opens a capture of one window.
func startWGC(hwnd uintptr) (*wgcSession, error) {
	if !wgcEnabled() {
		return nil, fmt.Errorf("%w: turned off", errWGCUnavailable)
	}
	if !wgcSupported() {
		return nil, errWGCUnavailable
	}
	w := &wgcSession{}
	ok := false
	defer func() {
		if !ok {
			w.close()
		}
	}()

	// A machine without a usable GPU driver (a VM, a remote session) still captures through
	// the software renderer.
	var r uintptr
	for _, driver := range []uintptr{driverTypeHardware, driverTypeWARP} {
		r, _, _ = procD3D11CreateDevice.Call(0, driver, 0, createDeviceBGRA, 0, 0, d3d11SDKVersion,
			uintptr(unsafe.Pointer(&w.device)), 0, uintptr(unsafe.Pointer(&w.context)))
		if int32(r) >= 0 {
			break
		}
	}
	if err := hr(r, "D3D11CreateDevice"); err != nil {
		return nil, err
	}
	if w.device == nil || w.context == nil {
		return nil, errors.New("wgc: D3D11CreateDevice returned no device")
	}

	var dxgi *comObject
	if err := hr(call(w.device, comQueryInterface, uintptr(unsafe.Pointer(&iidDxgiDevice)), uintptr(unsafe.Pointer(&dxgi))), "QueryInterface(IDXGIDevice)"); err != nil {
		return nil, err
	}
	defer releaseObj(dxgi)
	r, _, _ = procCreateDirect3D11Device.Call(uintptr(unsafe.Pointer(dxgi)), uintptr(unsafe.Pointer(&w.rtDevice)))
	if err := hr(r, "CreateDirect3D11DeviceFromDXGIDevice"); err != nil {
		return nil, err
	}
	if w.rtDevice == nil {
		return nil, errors.New("wgc: no Direct3D device for the capture")
	}

	interop, err := activationFactory(classCaptureItem, &iidCaptureItemInterop)
	if err != nil {
		return nil, err
	}
	defer releaseObj(interop)
	if err := hr(call(interop, interopCreateForWindow, hwnd, uintptr(unsafe.Pointer(&iidGraphicsCaptureItem)), uintptr(unsafe.Pointer(&w.item))), "CreateForWindow"); err != nil {
		return nil, err
	}
	if w.item == nil {
		return nil, errors.New("wgc: the window cannot be captured")
	}

	var size sizeInt32
	if err := hr(call(w.item, itemGetSize, uintptr(unsafe.Pointer(&size))), "GraphicsCaptureItem.Size"); err != nil {
		return nil, err
	}
	if size.Width <= 0 || size.Height <= 0 {
		return nil, errors.New("wgc: the window has no size to capture")
	}

	statics, err := activationFactory(classFramePool, &iidFramePoolStatics2)
	if err != nil {
		return nil, err
	}
	defer releaseObj(statics)
	args := append([]uintptr{uintptr(unsafe.Pointer(w.rtDevice)), formatB8G8R8A8UNorm, framePoolBufferCount}, size.args()...)
	args = append(args, uintptr(unsafe.Pointer(&w.pool)))
	if err := hr(call(statics, framePoolCreateFreeThreaded, args...), "Direct3D11CaptureFramePool.CreateFreeThreaded"); err != nil {
		return nil, err
	}
	if w.pool == nil {
		return nil, errors.New("wgc: no frame pool")
	}
	if err := hr(call(w.pool, framePoolCreateCaptureSesion, uintptr(unsafe.Pointer(w.item)), uintptr(unsafe.Pointer(&w.session))), "CreateCaptureSession"); err != nil {
		return nil, err
	}
	if w.session == nil {
		return nil, errors.New("wgc: no capture session")
	}

	// Keep the mouse pointer in the picture, and drop the yellow capture border where Windows
	// allows it. Both are newer than the capture API itself, so failures are not fatal.
	var session2 *comObject
	if call(w.session, comQueryInterface, uintptr(unsafe.Pointer(&iidSession2)), uintptr(unsafe.Pointer(&session2))) == 0 {
		call(session2, sessionPutCursorCapture, 1)
		releaseObj(session2)
	}
	var session3 *comObject
	if call(w.session, comQueryInterface, uintptr(unsafe.Pointer(&iidSession3)), uintptr(unsafe.Pointer(&session3))) == 0 {
		call(session3, sessionPutIsBorderRequired, 0)
		releaseObj(session3)
	}

	if err := hr(call(w.session, sessionStartCapture), "StartCapture"); err != nil {
		return nil, err
	}
	ok = true
	return w, nil
}

// recreatePool points the capture at a new size after the window was resized.
func (w *wgcSession) recreatePool(size sizeInt32) error {
	args := append([]uintptr{uintptr(unsafe.Pointer(w.rtDevice)), formatB8G8R8A8UNorm, framePoolBufferCount}, size.args()...)
	return hr(call(w.pool, framePoolRecreate, args...), "Direct3D11CaptureFramePool.Recreate")
}

// nextFrame copies the newest captured frame into the staging texture and hands its pixels to
// fn as top-down BGRA rows. It reports whether a frame was available.
func (w *wgcSession) nextFrame(fn func(pixels []byte, width, height int32, rowPitch uint32)) (bool, error) {
	var frame *comObject
	if err := hr(call(w.pool, framePoolTryGetNextFrame, uintptr(unsafe.Pointer(&frame))), "TryGetNextFrame"); err != nil {
		return false, err
	}
	if frame == nil {
		return false, nil
	}
	defer closeObj(frame)

	var surface *comObject
	if err := hr(call(frame, frameGetSurface, uintptr(unsafe.Pointer(&surface))), "Frame.Surface"); err != nil {
		return false, err
	}
	if surface == nil {
		return false, nil
	}
	defer releaseObj(surface)

	var access *comObject
	if err := hr(call(surface, comQueryInterface, uintptr(unsafe.Pointer(&iidDxgiInterfaceAccess)), uintptr(unsafe.Pointer(&access))), "QueryInterface(IDirect3DDxgiInterfaceAccess)"); err != nil {
		return false, err
	}
	defer releaseObj(access)

	var texture *comObject
	if err := hr(call(access, dxgiAccessGetInterface, uintptr(unsafe.Pointer(&iidTexture2D)), uintptr(unsafe.Pointer(&texture))), "GetInterface(ID3D11Texture2D)"); err != nil {
		return false, err
	}
	if texture == nil {
		return false, nil
	}
	defer releaseObj(texture)

	var desc texture2DDesc
	call(texture, textureGetDesc, uintptr(unsafe.Pointer(&desc)))
	if desc.Width == 0 || desc.Height == 0 {
		return false, nil
	}
	// No display is this large: a size like that means the description was read wrongly, and
	// acting on it would allocate nonsense or worse.
	if desc.Width > 16384 || desc.Height > 16384 {
		return false, fmt.Errorf("wgc: the captured texture reports %dx%d", desc.Width, desc.Height)
	}
	if err := w.ensureStaging(int32(desc.Width), int32(desc.Height)); err != nil {
		return false, err
	}
	call(w.context, contextCopyResource, uintptr(unsafe.Pointer(w.staging)), uintptr(unsafe.Pointer(texture)))

	var mapped mappedSubresource
	if err := hr(call(w.context, contextMap, uintptr(unsafe.Pointer(w.staging)), 0, mapRead, 0, uintptr(unsafe.Pointer(&mapped))), "Map(staging)"); err != nil {
		return false, err
	}
	defer call(w.context, contextUnmap, uintptr(unsafe.Pointer(w.staging)), 0)

	// The frame may be smaller than the pool's textures after a resize; take what is valid.
	content := sizeInt32{Width: int32(desc.Width), Height: int32(desc.Height)}
	if err := hr(call(frame, frameGetContentSize, uintptr(unsafe.Pointer(&content))), "Frame.ContentSize"); err != nil {
		content = sizeInt32{Width: int32(desc.Width), Height: int32(desc.Height)}
	}
	width := min(content.Width, int32(desc.Width))
	height := min(content.Height, int32(desc.Height))
	if width <= 0 || height <= 0 {
		return false, nil
	}
	if mapped.Data == nil || mapped.RowPitch < 4*desc.Width {
		return false, nil
	}
	pixels := unsafe.Slice(mapped.Data, int(mapped.RowPitch)*int(desc.Height))
	fn(pixels, width, height, mapped.RowPitch)
	return true, nil
}

func (w *wgcSession) ensureStaging(width, height int32) error {
	if w.staging != nil && w.stagingW == width && w.stagingH == height {
		return nil
	}
	releaseObj(w.staging)
	w.staging = nil
	desc := texture2DDesc{
		Width:          uint32(width),
		Height:         uint32(height),
		MipLevels:      1,
		ArraySize:      1,
		Format:         formatB8G8R8A8UNorm,
		SampleCount:    1,
		Usage:          usageStaging,
		CPUAccessFlags: cpuAccessRead,
	}
	if err := hr(call(w.device, deviceCreateTexture2D, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&w.staging))), "CreateTexture2D(staging)"); err != nil {
		return err
	}
	if w.staging == nil {
		return errors.New("wgc: no staging texture")
	}
	w.stagingW, w.stagingH = width, height
	return nil
}

// streamWindowFramesWGC captures hwnd with Windows Graphics Capture and writes scaled BGRA
// frames, the same shape the GDI capture produces.
func streamWindowFramesWGC(ctx context.Context, hwnd uintptr, fps, outWidth, outHeight int, outPipe io.WriteCloser) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := procRoInitialize.Call(roInitMultiThreaded); int32(r) < 0 && uint32(r) != 0x80010106 { // RPC_E_CHANGED_MODE
		return fmt.Errorf("wgc: RoInitialize failed: HRESULT 0x%08X", uint32(r))
	}
	defer procRoUninitialize.Call()

	capture, err := startWGC(hwnd)
	if err != nil {
		return fmt.Errorf("%w: %v", errWGCUnavailable, err)
	}
	defer capture.close()

	frame := make([]byte, outWidth*outHeight*4)
	scaler := newFrameScaler(outWidth, outHeight)
	writer := bufio.NewWriterSize(outPipe, 256*1024)
	defer writer.Flush()

	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()
	var delivered int
	wasMinimized := false
	lastSize := sizeInt32{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if ret, _, _ := procWinIsWindow.Call(hwnd); ret == 0 {
			logMsg("[WGC] The captured window was closed. Ending the screen stream.")
			return errors.New("captured window was closed by user")
		}

		got, err := capture.nextFrame(func(pixels []byte, width, height int32, rowPitch uint32) {
			if width != lastSize.Width || height != lastSize.Height {
				lastSize = sizeInt32{Width: width, Height: height}
				scaler.source(int(width), int(height))
			}
			scaler.scale(pixels, int(rowPitch), frame)
		})
		if err != nil {
			if delivered == 0 {
				// Nothing was written yet, so the caller can still fall back to GDI capture.
				return fmt.Errorf("%w: %v", errWGCUnavailable, err)
			}
			logMsg("[WGC] Capture error: %v. Ending the screen stream.", err)
			return err
		}
		if got && lastSize.Width != capture.stagingW {
			// The window was resized: the pool keeps handing out the old texture size until
			// it is told about the new one.
			_ = capture.recreatePool(lastSize)
		}
		if minimized := isIconicWindow(hwnd); minimized != wasMinimized {
			wasMinimized = minimized
			if minimized {
				logMsg("[SHARE] The shared window is minimized: viewers keep seeing the last picture (sound still plays) until you restore it.")
			} else {
				logMsg("[SHARE] The shared window is back: the picture is live again.")
			}
		}
		// A frame that did not arrive in time repeats the last one, so the encoder keeps a
		// steady rate instead of stalling.
		if _, err := writer.Write(frame); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if got {
			delivered++
			if delivered == 1 {
				logMsg("[WGC] Capturing the window with Windows Graphics Capture (%dx%d).", lastSize.Width, lastSize.Height)
			}
		}
	}
}
