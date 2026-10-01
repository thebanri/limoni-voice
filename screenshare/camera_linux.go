package screenshare

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// v4l2Camera is a V4L2 device that captures video.
type v4l2Camera struct{ device, name string }

// V4L2 capability flags and the VIDIOC_QUERYCAP request (_IOR('V', 0, struct v4l2_capability)).
const (
	v4l2CapVideoCapture = 0x00000001
	v4l2CapDeviceCaps   = 0x80000000
	vidiocQueryCap      = 0x80685600
)

// v4l2Capability is struct v4l2_capability.
type v4l2Capability struct {
	driver       [16]byte
	card         [32]byte
	busInfo      [32]byte
	version      uint32
	capabilities uint32
	deviceCaps   uint32
	reserved     [3]uint32
}

// listV4L2Cameras returns the /dev/video* devices that capture video. A camera usually has a
// second node for metadata, which captures nothing and is left out.
func listV4L2Cameras() []v4l2Camera {
	paths, _ := filepath.Glob("/dev/video*")
	sort.Slice(paths, func(i, j int) bool { // video2 before video10
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) < len(paths[j])
		}
		return paths[i] < paths[j]
	})
	var cams []v4l2Camera
	for _, p := range paths {
		name, ok := queryV4L2Capture(p)
		if !ok {
			continue
		}
		if name == "" {
			name = filepath.Base(p)
		}
		cams = append(cams, v4l2Camera{device: p, name: name})
	}
	return cams
}

// queryV4L2Capture asks a device whether it captures video, and its name.
func queryV4L2Capture(path string) (string, bool) {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	var c v4l2Capability
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), vidiocQueryCap, uintptr(unsafe.Pointer(&c))); errno != 0 {
		return "", false
	}
	caps := c.capabilities
	if caps&v4l2CapDeviceCaps != 0 {
		caps = c.deviceCaps // this node's own, not the whole device's
	}
	if caps&v4l2CapVideoCapture == 0 {
		return "", false
	}
	return strings.TrimRight(string(c.card[:]), "\x00 "), true
}

// gstCameraElements are the GStreamer elements a camera needs beyond the shared pipeline:
// v4l2src reads it, decodebin unpacks the MJPEG most webcams send at 720p.
var gstCameraElements = []struct {
	name string
	dep  linuxDep
}{
	{"v4l2src", depGstGood},
	{"decodebin", depGstBase},
	{"jpegdec", depGstGood},
}

// gstCameraDepsError checks the camera pipeline's elements, with the shared ones.
func gstCameraDepsError() error {
	if testing.Testing() {
		return nil
	}
	if err := gstPipewireDeps(true); err != nil {
		return err
	}
	inspect, err := FindExecutable("gst-inspect-1.0")
	if err != nil {
		return nil
	}
	var missing []string
	var deps []linuxDep
	for _, e := range gstCameraElements {
		if exec.Command(inspect, "--exists", e.name).Run() != nil {
			missing = append(missing, "GStreamer "+e.name)
			deps = append(deps, e.dep)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return newMissingDepsError(false, missing, deps)
}

// buildGstreamerCameraCommand streams a V4L2 camera. Its formats are narrowed to 720p or less
// at 15 to 30 frames a second, MJPEG first: raw video at that size often comes at a few frames
// a second over USB.
func buildGstreamerCameraCommand(opt BroadcastOptions, targetURL string) (string, []string, error) {
	if err := gstCameraDepsError(); err != nil {
		return "", nil, err
	}
	w, h := parseResolution(opt.Resolution, cameraMaxWidth, cameraMaxHeight)
	formats := ""
	for i, kind := range []string{"image/jpeg", "video/x-raw"} {
		if i > 0 {
			formats += ";"
		}
		formats += kind + ",width=[1," + strconv.Itoa(w) + "],height=[1," + strconv.Itoa(h) + "],framerate=[15/1," + strconv.Itoa(cameraMaxFPS) + "/1]"
	}
	source := []string{
		"v4l2src", "device=" + cameraDevice(opt.WindowID), "do-timestamp=true",
		"!", formats,
		"!", "decodebin",
	}
	return buildGstreamerCommand(source, targetURL, opt, true)
}
