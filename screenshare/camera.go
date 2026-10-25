package screenshare

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// CameraPrefix starts the target ID of a camera: "camera:" + the device (/dev/video0 on
// Linux, the DirectShow name on Windows, the AVFoundation index on macOS).
const CameraPrefix = "camera:"

// Cameras stream at most this: webcams rarely do better, and more only costs upload.
const (
	cameraMaxWidth  = 1280
	cameraMaxHeight = 720
	cameraMaxFPS    = 30
	cameraMaxKbps   = 2500
)

// IsCameraTarget reports whether a share target is a camera.
func IsCameraTarget(targetID string) bool { return strings.HasPrefix(targetID, CameraPrefix) }

// cameraDevice returns the device a camera target names.
func cameraDevice(targetID string) string { return strings.TrimPrefix(targetID, CameraPrefix) }

// cameraTitle is how a camera is listed in the share dialog.
func cameraTitle(name string) string { return "[Camera] " + name }

// cameraOptions caps a preset to what a camera streams: 720p, 30 frames a second, 2.5 Mbps.
func cameraOptions(opt BroadcastOptions) BroadcastOptions {
	w, h := parseResolution(opt.Resolution, cameraMaxWidth, cameraMaxHeight)
	if w > cameraMaxWidth || h > cameraMaxHeight {
		w, h = cameraMaxWidth, cameraMaxHeight
	}
	opt.Resolution = fmt.Sprintf("%dx%d", w, h)
	if opt.FPS <= 0 || opt.FPS > cameraMaxFPS {
		opt.FPS = cameraMaxFPS
	}
	if opt.BitrateKbps <= 0 || opt.BitrateKbps > cameraMaxKbps {
		opt.BitrateKbps = cameraMaxKbps
		opt.Bitrate = ""
	}
	return opt
}

// CameraPreset caps a preset the way a camera share is sent, so what the room is told (the
// frame rate viewers play at) and the bitrate controller match the stream.
func CameraPreset(p Preset) Preset {
	o := cameraOptions(p.Options(CameraPrefix))
	p.Width, p.Height = parseResolution(o.Resolution, cameraMaxWidth, cameraMaxHeight)
	p.FPS, p.Kbps = o.FPS, o.BitrateKbps
	p.Name = fmt.Sprintf("Camera %dp%d", p.Height, p.FPS)
	return p
}

// ListCameras returns the cameras that can be shared, as share targets.
func ListCameras() []WindowInfo {
	var names, ids []string
	switch runtime.GOOS {
	case "linux":
		for _, c := range listV4L2Cameras() {
			ids, names = append(ids, c.device), append(names, c.name)
		}
	case "windows", "darwin":
		ffmpeg, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil
		}
		if runtime.GOOS == "windows" {
			out, _ := exec.Command(ffmpeg, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy").CombinedOutput()
			names = parseDshowVideoDevices(string(out))
			ids = names
		} else {
			out, _ := exec.Command(ffmpeg, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()
			for _, d := range parseAVFoundationCameras(string(out)) {
				ids, names = append(ids, d.index), append(names, d.name)
			}
		}
	}
	targets := make([]WindowInfo, 0, len(ids))
	for i := range ids {
		targets = append(targets, WindowInfo{ID: CameraPrefix + ids[i], Title: cameraTitle(names[i])})
	}
	return targets
}

// reDshowVideo matches a video device in ffmpeg's DirectShow list: `"HD Webcam" (video)`.
var reDshowVideo = regexp.MustCompile(`"([^"]+)"\s+\(video\)`)

// parseDshowVideoDevices returns the video devices ffmpeg -list_devices lists on Windows.
func parseDshowVideoDevices(out string) []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range reDshowVideo.FindAllStringSubmatch(out, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// avfDevice is a video device in ffmpeg's AVFoundation list.
type avfDevice struct{ index, name string }

// reAVFDevice matches a device line: "[AVFoundation indev @ 0x…] [0] FaceTime HD Camera".
var reAVFDevice = regexp.MustCompile(`\]\s*\[(\d+)\]\s*(.+)$`)

// parseAVFoundationCameras returns the cameras ffmpeg lists on macOS: the video devices but
// the "Capture screen" ones, which are displays.
func parseAVFoundationCameras(out string) []avfDevice {
	var devs []avfDevice
	inVideo := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "AVFoundation video devices"):
			inVideo = true
			continue
		case strings.Contains(line, "AVFoundation audio devices"):
			inVideo = false
			continue
		}
		if !inVideo {
			continue
		}
		m := reAVFDevice.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || strings.HasPrefix(m[2], "Capture screen") {
			continue
		}
		devs = append(devs, avfDevice{index: m[1], name: strings.TrimSpace(m[2])})
	}
	return devs
}

// cameraFFmpegTail is the scaling, encoding and output that follow a camera input in ffmpeg:
// the picture is fitted into the frame with bars rather than stretched.
func cameraFFmpegTail(opt BroadcastOptions, encoder []string, targetURL string) []string {
	w, h := parseResolution(opt.Resolution, cameraMaxWidth, cameraMaxHeight)
	args := []string{
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,format=yuv420p", w, h, w, h),
		"-r", fmt.Sprint(opt.FPS),
	}
	args = append(args, encoder...)
	return append(args,
		"-pix_fmt", "yuv420p",
		"-bsf:v", "dump_extra",
		"-f", "mpegts",
		"-mpegts_flags", "+pat_pmt_at_frames",
		"-pcr_period", "20",
		"-flush_packets", "1",
		targetURL,
	)
}

// cameraFFmpegInput is the ffmpeg input that captures a camera device. On macOS the frame
// rate must be one the camera offers; 30 is offered by all of them, ffmpeg's default 29.97 by
// few.
func cameraFFmpegInput(device string) []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"-f", "dshow", "-rtbufsize", "64M", "-i", "video=" + device}
	case "darwin":
		return []string{"-f", "avfoundation", "-framerate", "30", "-i", device + ":none"}
	}
	return []string{"-f", "v4l2", "-framerate", "30", "-video_size", "640x480", "-i", device}
}

// buildWindowsCameraArgs captures a DirectShow camera with ffmpeg.
func buildWindowsCameraArgs(opt BroadcastOptions, targetURL, ffmpegBin string) []string {
	kbps := fmt.Sprintf("%dk", opt.BitrateKbps)
	encoder := buildWindowsEncoderArgs(getBestWindowsEncoder(ffmpegBin), kbps, kbps, kbps, max(opt.FPS, 30))
	args := append([]string{"-fflags", "nobuffer+flush_packets"}, cameraFFmpegInput(cameraDevice(opt.WindowID))...)
	return append(args, cameraFFmpegTail(opt, encoder, targetURL)...)
}

// buildMacCameraArgs captures an AVFoundation camera with ffmpeg.
func buildMacCameraArgs(opt BroadcastOptions, targetURL, ffmpegBin string) []string {
	encoder := buildMacEncoderArgs(bestMacEncoder(ffmpegBin), fmt.Sprintf("%dk", opt.BitrateKbps), opt.FPS)
	return append(cameraFFmpegInput(cameraDevice(opt.WindowID)), cameraFFmpegTail(opt, encoder, targetURL)...)
}

// The camera shown over a screen share sits in the bottom right corner, a quarter of the
// frame high.
const cameraOverlayMargin = 16

func cameraOverlayHeight(frameH int) int { return frameH / 4 / 2 * 2 }

// withCameraOverlay adds opt.Camera to an ffmpeg screen capture whose picture goes through
// one -vf. Both inputs are stamped with the wall clock: a screen that only sends frames when
// it changes must not leave the camera behind. The macOS pipe is the exception: stamped so,
// ffmpeg made 2 FPS of it; its helper writes at the frame rate, so its frame count keeps time.
func withCameraOverlay(args []string, opt BroadcastOptions) []string {
	vf := slices.Index(args, "-vf")
	if opt.Camera == "" || vf < 0 || vf+1 >= len(args) {
		return args
	}
	_, h := parseResolution(opt.Resolution, 1920, 1080)
	graph := fmt.Sprintf("[0:v]setpts=PTS-STARTPTS,%s[screen];[1:v]setpts=PTS-STARTPTS,scale=-2:%d[cam];[screen][cam]overlay=W-w-%d:H-h-%d:eof_action=pass,format=yuv420p[v]",
		args[vf+1], cameraOverlayHeight(h), cameraOverlayMargin, cameraOverlayMargin)
	out := append([]string(nil), args[:vf]...)
	if i := slices.Index(out, "-i"); i < 0 || i+1 >= len(out) || out[i+1] != "-" {
		out = append([]string{"-use_wallclock_as_timestamps", "1"}, out...)
	}
	out = append(out, "-use_wallclock_as_timestamps", "1")
	out = append(out, cameraFFmpegInput(cameraDevice(opt.Camera))...)
	out = append(out, "-filter_complex", graph, "-map", "[v]")
	return append(out, args[vf+2:]...)
}
