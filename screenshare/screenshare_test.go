package screenshare

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCheckDependencies(t *testing.T) {
	status := CheckDependencies()
	t.Logf("Dependency Status: MPV=%v, GSR=%v, FFmpeg=%v, Missing=%s",
		status.HasMPV, status.HasGPUScreenRecorder, status.HasFFmpeg, status.MissingRecommended)
}

func TestBroadcastOptionsDefaults(t *testing.T) {
	opt := DefaultBroadcastOptions()
	if opt.FPS != 30 && opt.FPS != 60 {
		t.Fatalf("expected 30 or 60 fps, got %d", opt.FPS)
	}
	if opt.Resolution != "1920x1080" {
		t.Fatalf("expected 1920x1080, got %s", opt.Resolution)
	}
}

func TestReceiverOptionsDefaults(t *testing.T) {
	opt := DefaultReceiverOptions()
	if opt.WindowTitle == "" {
		t.Fatal("expected non-empty window title")
	}
}

func TestSessionInvalidInputs(t *testing.T) {
	ctx := context.Background()

	// Empty IP
	_, err := StartBroadcasting(ctx, "", 5000)
	if err == nil {
		t.Fatal("expected error on empty target IP")
	}

	// Invalid Port
	_, err = StartBroadcasting(ctx, "127.0.0.1", -1)
	if err == nil {
		t.Fatal("expected error on invalid port")
	}

	_, err = StartReceiving(ctx, 0)
	if err == nil {
		t.Fatal("expected error on invalid receiver port")
	}
}

func TestSessionLifecycleWithDummyProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Session{
		ctx:    ctx,
		cancel: cancel,
		errCh:  make(chan error, 1),
		doneCh: make(chan struct{}),
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		close(s.doneCh)
	}()

	err := s.Stop()
	if err != nil {
		t.Fatalf("expected nil error on stop, got %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("session did not finish within timeout")
	}
}

func TestParseXrandrGeometry(t *testing.T) {
	// Standard case: 1920/531x1080/299+1920+0
	w, h, x, y := parseXrandrGeometry("1920/531x1080/299+1920+0")
	if w != "1920" || h != "1080" || x != "1920" || y != "0" {
		t.Fatalf("expected 1920, 1080, 1920, 0; got %s, %s, %s, %s", w, h, x, y)
	}

	// Simple case: 2560x1440+0+0
	w, h, x, y = parseXrandrGeometry("2560x1440+0+0")
	if w != "2560" || h != "1440" || x != "0" || y != "0" {
		t.Fatalf("expected 2560, 1440, 0, 0; got %s, %s, %s, %s", w, h, x, y)
	}
}

func TestListWindowsLinuxTargets(t *testing.T) {
	targets := ListWindows()
	if len(targets) == 0 {
		t.Fatal("expected at least 1 target from ListWindows()")
	}

	t.Logf("Found %d targets in ListWindows():", len(targets))
	for i, tg := range targets {
		t.Logf("  [%d] ID=%s | Title=%s", i, tg.ID, tg.Title)
	}
}

func TestBuildLinuxBroadcastCommand(t *testing.T) {
	t.Run("DesktopTarget", func(t *testing.T) {
		opts := BroadcastOptions{
			Resolution: "1920x1080",
			FPS:        60,
			Bitrate:    "6M",
			WindowID:   "desktop",
		}
		bin, args, pwFile, cleanup, err := buildLinuxBroadcastCommand(opts, "udp://127.0.0.1:50100")
		if err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "required") {
				t.Skipf("Skipping on CI without screen capture tools: %v", err)
			}
			t.Fatalf("buildLinuxBroadcastCommand desktop failed: %v", err)
		}
		if pwFile != nil {
			defer pwFile.Close()
		}
		if cleanup != nil {
			defer cleanup()
		}
		if bin == "" || len(args) == 0 {
			t.Fatal("expected non-empty bin and args")
		}
		t.Logf("Built desktop broadcast command: %s %v", bin, args)
	})

	t.Run("MonitorTarget", func(t *testing.T) {
		opts := BroadcastOptions{
			Resolution: "1920x1080",
			FPS:        60,
			Bitrate:    "6M",
			WindowID:   "monitor:DP-1:1920:1080:1920:0",
		}
		bin, args, pwFile, cleanup, err := buildLinuxBroadcastCommand(opts, "udp://127.0.0.1:50100")
		if err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "required") {
				t.Skipf("Skipping on CI without screen capture tools: %v", err)
			}
			t.Fatalf("buildLinuxBroadcastCommand monitor failed: %v", err)
		}
		if pwFile != nil {
			defer pwFile.Close()
		}
		if cleanup != nil {
			defer cleanup()
		}
		if strings.Contains(bin, "gpu-screen-recorder") {
			foundDP1 := false
			for i, a := range args {
				if a == "-w" && i+1 < len(args) && args[i+1] == "DP-1" {
					foundDP1 = true
					break
				}
			}
			if !foundDP1 {
				t.Fatalf("expected gpu-screen-recorder -w DP-1, got args: %v", args)
			}
		}
		t.Logf("Built monitor broadcast command: %s %v", bin, args)
	})

	t.Run("WaylandAppTarget", func(t *testing.T) {
		if !isWayland() {
			t.Skip("Skipping WaylandAppTarget on non-Wayland environment")
		}
		opts := BroadcastOptions{
			Resolution: "1920x1080",
			FPS:        60,
			Bitrate:    "6M",
			WindowID:   "app:1416:zen",
		}
		bin, args, pwFile, cleanup, err := buildLinuxBroadcastCommand(opts, "udp://127.0.0.1:50100")
		if err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "required") {
				t.Skipf("Skipping on CI without screen capture tools: %v", err)
			}
			t.Fatalf("buildLinuxBroadcastCommand app failed: %v", err)
		}
		if pwFile != nil {
			defer pwFile.Close()
		}
		if cleanup != nil {
			defer cleanup()
		}
		if strings.Contains(bin, "gpu-screen-recorder") {
			t.Fatalf("FATAL: gpu-screen-recorder was selected for Wayland window target: %s %v", bin, args)
		}
		if !strings.Contains(bin, "gst-launch-1.0") {
			t.Fatalf("expected gst-launch-1.0 for Wayland window portal capture, got: %s", bin)
		}
		foundPipewire := false
		for _, a := range args {
			if strings.Contains(a, "pipewiresrc") {
				foundPipewire = true
				break
			}
		}
		if !foundPipewire {
			t.Fatalf("expected pipewiresrc in gst args, got: %v", args)
		}
		t.Logf("Built Wayland app broadcast command: %s %v", bin, args)
	})
}

func TestWatchPIDLiveness(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("watchPIDLiveness is Linux-specific")
	}

	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelledCh := make(chan struct{})
	customCancel := func() {
		close(cancelledCh)
		cancel()
	}

	go watchPIDLiveness(ctx, cmd.Process.Pid, customCancel)

	// Wait for process to naturally finish
	_ = cmd.Wait()

	select {
	case <-cancelledCh:
		t.Log("watchPIDLiveness successfully detected process termination and called cancel")
	case <-time.After(3 * time.Second):
		t.Fatal("watchPIDLiveness failed to detect process termination within 3 seconds")
	}
}

func TestBuildWindowsEncoderArgs(t *testing.T) {
	encoders := []string{"h264_nvenc", "h264_amf", "h264_qsv", "libx264"}
	for _, enc := range encoders {
		args := buildWindowsEncoderArgs(enc, "4.5M", "6.5M", "2000k", 60)
		if len(args) == 0 {
			t.Fatalf("expected non-empty encoder args for %s", enc)
		}
		foundEnc := false
		for i, a := range args {
			if a == "-c:v" && i+1 < len(args) && args[i+1] == enc {
				foundEnc = true
				break
			}
		}
		if !foundEnc {
			t.Fatalf("expected -c:v %s in args, got: %v", enc, args)
		}

		// Ensure unsupported options are not present
		for _, a := range args {
			if a == "-repeat-headers" && enc == "h264_nvenc" {
				t.Fatalf("h264_nvenc should not contain unsupported -repeat-headers")
			}
			if a == "-header_insertion_mode" && enc == "h264_amf" {
				t.Fatalf("h264_amf should not contain unsupported -header_insertion_mode")
			}
		}

		// Verify tuning parameters
		switch enc {
		case "h264_nvenc":
			hasSpatialAQ := false
			for i, a := range args {
				if a == "-spatial-aq" && i+1 < len(args) && args[i+1] == "1" {
					hasSpatialAQ = true
					break
				}
			}
			if !hasSpatialAQ {
				t.Fatalf("h264_nvenc should have -spatial-aq 1")
			}
		case "h264_amf":
			hasVBAQ := false
			for i, a := range args {
				if a == "-vbaq" && i+1 < len(args) && args[i+1] == "1" {
					hasVBAQ = true
					break
				}
			}
			if !hasVBAQ {
				t.Fatalf("h264_amf should have -vbaq 1")
			}
		case "h264_qsv":
			hasScenario := false
			for i, a := range args {
				if a == "-scenario" && i+1 < len(args) && args[i+1] == "displayremoting" {
					hasScenario = true
					break
				}
			}
			if !hasScenario {
				t.Fatalf("h264_qsv should have -scenario displayremoting")
			}
		case "libx264":
			hasCRF19 := false
			for i, a := range args {
				if a == "-crf" && i+1 < len(args) && args[i+1] == "19" {
					hasCRF19 = true
					break
				}
			}
			if !hasCRF19 {
				t.Fatalf("libx264 should have -crf 19")
			}
		}
		t.Logf("Encoder %s args: %v", enc, args)
	}
}

func TestBuildWindowsBroadcastArgs(t *testing.T) {
	t.Run("DesktopDDAOrGDI", func(t *testing.T) {
		opt := BroadcastOptions{
			Resolution: "1920x1080",
			FPS:        120,
			Bitrate:    "6.5M",
			WindowID:   "desktop",
		}
		args := buildWindowsBroadcastArgs(opt, "udp://127.0.0.1:50100", "ffmpeg", 0, 0, 0)
		if len(args) == 0 {
			t.Fatal("expected non-empty args for Windows desktop broadcast")
		}
		foundMpegts := false
		foundColorRange := false
		for i, a := range args {
			if a == "-f" && i+1 < len(args) && args[i+1] == "mpegts" {
				foundMpegts = true
			}
			if a == "-color_range" && i+1 < len(args) && args[i+1] == "2" {
				foundColorRange = true
			}
		}
		if !foundMpegts {
			t.Fatalf("expected mpegts format in args: %v", args)
		}
		if !foundColorRange {
			t.Fatalf("expected -color_range 2 in args: %v", args)
		}
		t.Logf("Windows desktop broadcast args: %v", args)
	})

	t.Run("MonitorIndexSupport", func(t *testing.T) {
		opt := BroadcastOptions{
			Resolution: "1920x1080",
			FPS:        60,
			WindowID:   "monitor:1:1920:0:1920:1080",
		}
		args := buildWindowsBroadcastArgs(opt, "udp://127.0.0.1:50100", "ffmpeg", 0, 0, 0)
		if len(args) == 0 {
			t.Fatal("expected non-empty args for Windows monitor broadcast")
		}
		t.Logf("Windows monitor 1 broadcast args: %v", args)
	})

	t.Run("WindowCapturePipe", func(t *testing.T) {
		opt := BroadcastOptions{
			Resolution: "1280x720",
			FPS:        60,
			WindowID:   "hwnd:12345:Notepad",
		}
		args := buildWindowsBroadcastArgs(opt, "udp://127.0.0.1:50100", "ffmpeg", 12345, 1280, 720)
		if len(args) == 0 {
			t.Fatal("expected non-empty args for Windows window broadcast")
		}
		foundPipe := false
		foundColorRange := false
		for i, a := range args {
			if a == "-i" && i+1 < len(args) && args[i+1] == "pipe:0" {
				foundPipe = true
			}
			if a == "-color_range" && i+1 < len(args) && args[i+1] == "2" {
				foundColorRange = true
			}
		}
		if !foundPipe {
			t.Fatalf("expected -i pipe:0 in window capture args, got: %v", args)
		}
		if !foundColorRange {
			t.Fatalf("expected -color_range 2 in window capture args, got: %v", args)
		}
		t.Logf("Windows window capture args: %v", args)
	})
}

