package screenshare

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// LogCallback is optional hook to receive internal screenshare logs
var LogCallback func(string)

func logMsg(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	if LogCallback != nil {
		LogCallback(msg)
	}
}

// DependencyStatus contains the availability of required external CLI tools
type DependencyStatus struct {
	HasMPV               bool   `json:"has_mpv"`
	HasGPUScreenRecorder bool   `json:"has_gpu_screen_recorder"`
	HasFFmpeg            bool   `json:"has_ffmpeg"`
	CanShare             bool   `json:"can_share"`
	CanWatch             bool   `json:"can_watch"`
	MissingRecommended   string `json:"missing_recommended,omitempty"`
	InstallHint          string `json:"install_hint,omitempty"` // shell command that installs what is missing
}

// BroadcastOptions defines configuration for the video stream
type BroadcastOptions struct {
	Resolution  string // e.g. "1280x720" or "1920x1080"
	FPS         int    // e.g. 60 or 30
	Bitrate     string // e.g. "4M" or "2500k" (BitrateKbps wins when set)
	BitrateKbps int
	WindowID    string // optional window id or "portal" for Wayland/X11 window picker, or "desktop"
	Quality     string // e.g. "medium", "ultra", "fast"

	// OnAudio receives system audio captured together with the screen (48 kHz mono, 20 ms
	// frames). Only the macOS ScreenCaptureKit path produces it; other platforms capture system
	// audio separately (see internal/sysaudio).
	OnAudio func(frame []int16)
}

// bitrateString returns the encoder bitrate as an ffmpeg style string ("2500k").
func (o BroadcastOptions) bitrateString(fallback string) string {
	if o.BitrateKbps > 0 {
		return fmt.Sprintf("%dk", o.BitrateKbps)
	}
	if o.Bitrate != "" {
		return o.Bitrate
	}
	return fallback
}

// WindowInfo represents a shareable window or screen target
type WindowInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Preset is a quality level offered in the share dialog. Kbps is the ceiling the adaptive
// bitrate controller starts from; it steps down automatically when viewers lose packets.
type Preset struct {
	Name          string
	Width, Height int
	FPS           int
	Kbps          int
}

// Presets are ordered from lightest to heaviest.
var Presets = []Preset{
	{Name: "Eco 720p30", Width: 1280, Height: 720, FPS: 30, Kbps: 1500},
	{Name: "Balanced 720p30", Width: 1280, Height: 720, FPS: 30, Kbps: 2500},
	{Name: "Sharp 1080p30", Width: 1920, Height: 1080, FPS: 30, Kbps: 4000},
	{Name: "Smooth 1080p60", Width: 1920, Height: 1080, FPS: 60, Kbps: 6000},
	{Name: "Gaming 1080p120", Width: 1920, Height: 1080, FPS: 120, Kbps: 9000},
}

// DefaultPreset is the index of the preset used when nothing was chosen (720p30, 2.5 Mbps),
// which fits typical home upload speeds.
const DefaultPreset = 1

// FloorKbps is the lowest bitrate the adaptive controller may use for the preset.
func (p Preset) FloorKbps() int { return max(600, p.Kbps/4) }

// Options returns broadcast options for the preset and target.
func (p Preset) Options(targetID string) BroadcastOptions {
	if targetID == "" {
		targetID = "portal"
	}
	return BroadcastOptions{
		Resolution:  fmt.Sprintf("%dx%d", p.Width, p.Height),
		FPS:         p.FPS,
		BitrateKbps: p.Kbps,
		WindowID:    targetID,
		Quality:     p.Name,
	}
}

// PresetByIndex clamps i into Presets.
func PresetByIndex(i int) Preset {
	if i < 0 || i >= len(Presets) {
		i = DefaultPreset
	}
	return Presets[i]
}

// GetPresetOptions returns tailored broadcasting options for 30, 60, or 120 FPS
func GetPresetOptions(fps int, targetID string) BroadcastOptions {
	if fps <= 0 {
		fps = 60
	}
	if targetID == "" {
		targetID = "portal"
	}

	bitrate := "4.5M"
	quality := "high"
	switch fps {
	case 120:
		bitrate = "6.5M"
		quality = "ultra"
	case 30:
		bitrate = "3.0M"
		quality = "fast"
	case 60:
		bitrate = "4.5M"
		quality = "high"
	default:
		fps = 60
		bitrate = "4.5M"
		quality = "high"
	}

	return BroadcastOptions{
		Resolution: "1920x1080",
		FPS:        fps,
		Bitrate:    bitrate,
		WindowID:   targetID,
		Quality:    quality,
	}
}

// DefaultBroadcastOptions returns sensible low-latency defaults (60 FPS balanced)
func DefaultBroadcastOptions() BroadcastOptions {
	return GetPresetOptions(60, "portal")
}

// ReceiverOptions defines configuration for the video player
type ReceiverOptions struct {
	WindowTitle     string   // e.g. "Limoni Voice - User Stream"
	KeepAspect      bool     // preserve aspect ratio
	FPS             int      // Stream framerate (30, 60, 120)
	PreferredPlayer string   // "ffplay", "mpv", or "" (auto)
	CustomMpvFlags  []string // additional mpv flags
}

// DefaultReceiverOptions returns ultra-low-latency receiver defaults
func DefaultReceiverOptions(fps ...int) ReceiverOptions {
	streamFPS := 60
	if len(fps) > 0 && fps[0] > 0 {
		streamFPS = fps[0]
	}
	return ReceiverOptions{
		WindowTitle: fmt.Sprintf("Limoni Voice - Live Screen Stream (%d FPS)", streamFPS),
		KeepAspect:  true,
		FPS:         streamFPS,
	}
}

// CheckDependencies checks for required tools based on current OS and roles
func CheckDependencies() DependencyStatus {
	_, errMpv := FindExecutable("mpv")
	_, errFFplay := FindExecutable("ffplay")
	_, errFFmpeg := FindExecutable("ffmpeg")
	_, errGSR := FindExecutable("gpu-screen-recorder")
	_, errGst := FindExecutable("gst-launch-1.0")
	_, errWf := FindExecutable("wf-recorder")

	hasReceiver := errMpv == nil || errFFplay == nil

	status := DependencyStatus{
		HasMPV:               hasReceiver,
		HasFFmpeg:            errFFmpeg == nil,
		HasGPUScreenRecorder: errGSR == nil,
		CanWatch:             hasReceiver,
	}

	var missing, packages []string
	if !hasReceiver {
		missing = append(missing, "mpv (to watch streams)")
		packages = append(packages, "mpv")
	}
	switch runtime.GOOS {
	case "linux":
		wayland := isWayland()
		status.CanShare = errGSR == nil || errGst == nil || (!wayland && errFFmpeg == nil) || (wayland && errWf == nil)
		if !status.CanShare {
			if wayland {
				missing = append(missing, "GStreamer with PipeWire (to share on Wayland)")
				packages = append(packages, "gstreamer")
			} else {
				missing = append(missing, "ffmpeg (to share)")
				packages = append(packages, "ffmpeg")
			}
		}
	case "windows", "darwin":
		status.CanShare = errFFmpeg == nil
		if !status.CanShare {
			missing = append(missing, "ffmpeg (to share)")
			packages = append(packages, "ffmpeg")
		}
	}
	if len(missing) > 0 {
		status.MissingRecommended = strings.Join(missing, ", ")
		status.InstallHint = installHint(packages)
	}
	return status
}

// installHint returns an install command for the platform's package manager.
func installHint(packages []string) string {
	has := func(bin string) bool { _, err := exec.LookPath(bin); return err == nil }
	names := func(m map[string]string) string {
		var out []string
		for _, p := range packages {
			out = append(out, m[p])
		}
		return strings.Join(out, " ")
	}
	switch runtime.GOOS {
	case "windows":
		ids := map[string]string{"mpv": "shinchiro.mpv", "ffmpeg": "Gyan.FFmpeg"}
		var cmds []string
		for _, p := range packages {
			cmds = append(cmds, "winget install -e --id "+ids[p])
		}
		return strings.Join(cmds, " && ")
	case "darwin":
		return "brew install " + names(map[string]string{"mpv": "mpv", "ffmpeg": "ffmpeg"})
	}
	switch {
	case has("pacman"):
		return "sudo pacman -S --needed " + names(map[string]string{"mpv": "mpv", "ffmpeg": "ffmpeg", "gstreamer": "gstreamer gst-plugin-pipewire gst-plugins-good gst-plugins-bad gst-plugins-ugly"})
	case has("apt-get"):
		return "sudo apt install " + names(map[string]string{"mpv": "mpv", "ffmpeg": "ffmpeg", "gstreamer": "gstreamer1.0-tools gstreamer1.0-pipewire gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly"})
	case has("dnf"):
		return "sudo dnf install " + names(map[string]string{"mpv": "mpv", "ffmpeg": "ffmpeg", "gstreamer": "gstreamer1-plugins-good gstreamer1-plugins-bad-free pipewire-gstreamer gstreamer1-plugins-ugly"})
	}
	return ""
}
