package main

import (
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/screenshare"
)

// shortStallGuards makes the stall guards fire in test time instead of after several seconds.
func shortStallGuards(t *testing.T, d time.Duration) {
	t.Helper()
	capture, stream := captureStall.Load(), streamStall.Load()
	captureStall.Store(int64(d))
	streamStall.Store(int64(d))
	t.Cleanup(func() {
		captureStall.Store(capture)
		streamStall.Store(stream)
	})
}

// A stream that stops dead used to leave the player showing its last frame for good: the
// sharer's machine sleeping or its capture dying looks exactly like a frozen picture. The
// viewer now closes the player when nothing arrives for a while.
func TestViewerClosesThePlayerWhenTheStreamStops(t *testing.T) {
	if testing.Short() {
		t.Skip("streams real video for a few seconds")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	host, viewer := relayRoom(t, "5354-amber-falcon-river")
	useSyntheticScreenPipeline(t, ffmpeg)
	shortStallGuards(t, 700*time.Millisecond)

	// Once streamGone is set nothing leaves the sharer any more, the way it goes when its
	// machine sleeps mid-share.
	var streamGone atomic.Bool
	setScreenSendFilter(func(data []byte, to string, class byte) bool { return !streamGone.Load() })

	if err := host.StartScreenShareWith(ScreenShareConfig{TargetID: "desktop", Preset: screenshare.DefaultPreset}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "viewer sees the share", 5*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		p := viewer.Peers[host.LocalID]
		return p != nil && p.IsSharingScreen
	})
	if err := viewer.StartWatchingScreen(host.LocalID, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "sharer registers the viewer", 5*time.Second, func() bool { return host.ScreenStats().Watchers == 1 })

	streamGone.Store(true)

	waitFor(t, "the viewer stops watching a dead stream", 3*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		return viewer.screenRx == nil && !viewer.IsWatchingScreen
	})
	_ = host.StopScreenShare()
}

// A capture that stops producing frames (the machine slept, the encoder wedged) used to leave
// the share announced and the viewers staring at the last frame. The sharer now ends it.
func TestSharerEndsAShareWhoseCaptureStopped(t *testing.T) {
	if testing.Short() {
		t.Skip("streams real video for a few seconds")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	host, viewer := relayRoom(t, "5355-amber-falcon-river")
	useSyntheticScreenPipeline(t, ffmpeg)
	shortStallGuards(t, 700*time.Millisecond)

	if err := host.StartScreenShareWith(ScreenShareConfig{TargetID: "desktop", Preset: screenshare.DefaultPreset}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "viewer sees the share", 5*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		p := viewer.Peers[host.LocalID]
		return p != nil && p.IsSharingScreen
	})

	host.mu.RLock()
	tx := host.screenTx
	host.mu.RUnlock()
	if tx == nil {
		t.Fatal("the host is not sharing")
	}
	waitFor(t, "the encoder delivers frames", 10*time.Second, func() bool {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return !tx.srcSeen.IsZero()
	})
	// The capture stops delivering while its process stays alive, the way a wedged encoder or
	// a machine coming back from sleep does.
	_ = tx.conn.Close()

	waitFor(t, "the sharer ends a dead share", 5*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.screenTx == nil && !host.IsSharingScreen
	})
	waitFor(t, "the viewer is told the share ended", 5*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		p := viewer.Peers[host.LocalID]
		return p != nil && !p.IsSharingScreen
	})
}
