package main

import (
	"context"
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

// A player that dies on startup is replaced by the alternative one. When watching ended while
// that replacement was starting, nobody owned it: the shutdown had already killed the player it
// knew about, so the replacement stayed on screen for good. That is the stuck viewer left
// behind by stopping a share and starting another one right away.
func TestAReplacementPlayerStartedWhileStoppingIsClosed(t *testing.T) {
	if _, err := screenshare.FindExecutable("ffplay"); err != nil {
		t.Skip("the fallback player is not installed, so the replacement path cannot run")
	}
	host, viewer := relayRoom(t, "5356-amber-falcon-river")

	var started atomic.Int64
	release := make(chan struct{})
	replacement := make(chan *screenshare.Session, 1)
	startPlayerSession = func(ctx context.Context, opts ...screenshare.ReceiverOptions) (*screenshare.Session, error) {
		if started.Add(1) == 1 {
			// The first player dies at once, which sends the viewer to the fallback.
			return screenshare.StartProcess(ctx, "sh", []string{"-c", "exit 1"}, nil, true)
		}
		<-release // the replacement starts only after watching has been stopped
		s, err := screenshare.StartProcess(ctx, "sh", []string{"-c", "sleep 30"}, nil, true)
		replacement <- s
		return s, err
	}
	t.Cleanup(func() { startPlayerSession = screenshare.StartReceiving })

	if err := viewer.StartWatchingScreen(host.LocalID, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the viewer reaches for the fallback player", 5*time.Second, func() bool { return started.Load() == 2 })
	if err := viewer.StopWatchingScreen(); err != nil {
		t.Fatal(err)
	}
	close(release)

	var next *screenshare.Session
	select {
	case next = <-replacement:
	case <-time.After(3 * time.Second):
		t.Fatal("the replacement player never started")
	}
	select {
	case <-next.Done():
	case <-next.Err():
	case <-time.After(3 * time.Second):
		t.Fatal("the replacement player was left running after watching stopped")
	}
}

// Stopping a viewer killed its player, and the watcher goroutine took that for a player that
// had crashed on startup and launched the alternative one. Nothing owned that replacement, so
// it stayed on screen for good — which is what happened when a share was stopped and another
// started right away.
func TestStoppingTheViewerLeavesNoPlayerBehind(t *testing.T) {
	if _, err := screenshare.FindExecutable("ffplay"); err != nil {
		t.Skip("the fallback player is not installed, so the replacement path cannot run")
	}
	host, viewer := relayRoom(t, "5356-amber-falcon-river")
	_ = host

	var started atomic.Int64
	startPlayerSession = func(ctx context.Context, opts ...screenshare.ReceiverOptions) (*screenshare.Session, error) {
		started.Add(1)
		// A player that ignores end of stream, so stopping has to kill it.
		return screenshare.StartProcess(ctx, "sh", []string{"-c", "sleep 30"}, nil, true)
	}
	t.Cleanup(func() { startPlayerSession = screenshare.StartReceiving })

	if err := viewer.StartWatchingScreen(host.LocalID, 0); err != nil {
		t.Fatal(err)
	}
	if err := viewer.StopWatchingScreen(); err != nil {
		t.Fatal(err)
	}

	// The replacement, if any, is launched right after the kill.
	time.Sleep(1500 * time.Millisecond)
	if n := started.Load(); n != 1 {
		t.Fatalf("%d players were started; the viewer left one behind", n)
	}
	viewer.mu.RLock()
	defer viewer.mu.RUnlock()
	if viewer.screenRx != nil || viewer.IsWatchingScreen {
		t.Fatal("the viewer is still watching after it was stopped")
	}
}

// A viewer must lose the picture as soon as the sharer stops, not when a guard notices the
// silence: the sharer says so before it tears its own capture down.
func TestTheViewerClosesAsSoonAsTheSharerStops(t *testing.T) {
	if testing.Short() {
		t.Skip("streams real video for a few seconds")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	host, viewer := relayRoom(t, "5357-amber-falcon-river")
	useSyntheticScreenPipeline(t, ffmpeg)
	shortStallGuards(t, time.Minute) // the guards must not be what closes the viewer here

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

	start := time.Now()
	go func() { _ = host.StopScreenShare() }()
	waitFor(t, "the viewer closes with the share", 3*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		return viewer.screenRx == nil && !viewer.IsWatchingScreen
	})
	t.Logf("the viewer closed %v after the sharer stopped", time.Since(start))
}
