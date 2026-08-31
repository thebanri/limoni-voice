package screenshare

import (
	"context"
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
	if opt.FPS != 60 {
		t.Fatalf("expected 60 fps, got %d", opt.FPS)
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
	opts := BroadcastOptions{
		Resolution: "1920x1080",
		FPS:        60,
		Bitrate:    "6M",
	}

	opts.WindowID = "desktop"
	bin, args, pwFile, cleanup, err := buildLinuxBroadcastCommand(opts, "udp://127.0.0.1:50100")
	if err != nil {
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
}

