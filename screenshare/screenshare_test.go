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
	if opt.Resolution != "1280x720" {
		t.Fatalf("expected 1280x720, got %s", opt.Resolution)
	}
}

func TestReceiverOptionsDefaults(t *testing.T) {
	opt := DefaultReceiverOptions()
	if opt.VO != "kitty" {
		t.Fatalf("expected kitty VO, got %s", opt.VO)
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
