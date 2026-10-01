package screenshare

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestParseDshowVideoDevices(t *testing.T) {
	out := `[dshow @ 000001] "Integrated Camera" (video)
[dshow @ 000001]   Alternative name "@device_pnp_\\?\usb#vid_04f2"
[dshow @ 000001] "OBS Virtual Camera" (video)
[dshow @ 000001] "Microphone (Realtek)" (audio)
[dshow @ 000001] "Integrated Camera" (video)`
	if got := parseDshowVideoDevices(out); !slices.Equal(got, []string{"Integrated Camera", "OBS Virtual Camera"}) {
		t.Fatalf("got %q", got)
	}
}

func TestParseAVFoundationCameras(t *testing.T) {
	out := `[AVFoundation indev @ 0x7f8] AVFoundation video devices:
[AVFoundation indev @ 0x7f8] [0] FaceTime HD Camera
[AVFoundation indev @ 0x7f8] [1] iPhone Camera
[AVFoundation indev @ 0x7f8] [2] Capture screen 0
[AVFoundation indev @ 0x7f8] AVFoundation audio devices:
[AVFoundation indev @ 0x7f8] [0] MacBook Pro Microphone`
	got := parseAVFoundationCameras(out)
	want := []avfDevice{{"0", "FaceTime HD Camera"}, {"1", "iPhone Camera"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v", got)
	}
}

// A camera goes out at 720p30 and 2.5 Mbps at most, whatever preset is chosen; a lighter
// preset stays as it is.
func TestCameraCaps(t *testing.T) {
	heavy := CameraPreset(Presets[len(Presets)-1]) // Gaming 1080p120
	if heavy.Width != 1280 || heavy.Height != 720 || heavy.FPS != 30 || heavy.Kbps != 2500 || heavy.Name != "Camera 720p30" {
		t.Fatalf("gaming preset as camera: %+v", heavy)
	}
	light := CameraPreset(Presets[0]) // Eco 720p30, 1500 kbps
	if light.FPS != 30 || light.Kbps != 1500 {
		t.Fatalf("eco preset as camera: %+v", light)
	}
	if !IsCameraTarget("camera:/dev/video0") || IsCameraTarget("desktop") || cameraDevice("camera:Integrated Camera") != "Integrated Camera" {
		t.Fatal("camera target IDs")
	}
}

// A camera target gets the camera pipeline, not a screen capture.
func TestCameraTargetBuildsTheCameraPipeline(t *testing.T) {
	opt := PresetByIndex(4).Options(CameraPrefix + "/dev/video0")
	plan, err := buildBroadcastPlan(opt, "udp://127.0.0.1:5000?pkt_size=1128", nil, nil)
	if runtime.GOOS != "linux" {
		if err == nil && !strings.Contains(strings.Join(plan.args, " "), "1280:720") {
			t.Fatalf("camera args: %q", plan.args)
		}
		return
	}
	if err != nil {
		t.Skipf("no GStreamer here: %v", err)
	}
	args := strings.Join(plan.args, " ")
	for _, want := range []string{
		"v4l2src device=/dev/video0",
		"image/jpeg,width=[1,1280],height=[1,720],framerate=[15/1,30/1];video/x-raw,",
		"decodebin",
		"width=1280,height=720,framerate=30/1",
		"pixel-aspect-ratio=1/1",
		"host=127.0.0.1 port=5000",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if strings.Contains(args, "pipewiresrc") {
		t.Errorf("a camera went through the screen capture: %s", args)
	}
}

func TestCameraFFmpegTail(t *testing.T) {
	opt := cameraOptions(PresetByIndex(2).Options(CameraPrefix + "x"))
	args := strings.Join(cameraFFmpegTail(opt, []string{"-c:v", "libx264"}, "udp://127.0.0.1:5000"), " ")
	for _, want := range []string{"scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720", "-r 30", "-c:v libx264", "-f mpegts", "udp://127.0.0.1:5000"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
}
