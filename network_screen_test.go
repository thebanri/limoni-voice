package main

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/screenshare"
)

// useSyntheticScreenPipeline replaces screen capture with an ffmpeg test pattern encoder (30 FPS,
// 1 s GOP, bitrate from the options like the real pipelines) and the player with a headless
// decoder writing framecrc lines and a decoder error report.
func useSyntheticScreenPipeline(t *testing.T, ffmpeg string) (frames, report string) {
	t.Helper()
	startCaptureSession = func(ctx context.Context, ip string, port int, opts ...screenshare.BroadcastOptions) (*screenshare.Session, error) {
		return screenshare.StartCustomBroadcast(ctx, ip, port, opts[0], func(opt screenshare.BroadcastOptions, url string) (string, []string) {
			kbps := fmt.Sprintf("%dk", opt.BitrateKbps)
			return ffmpeg, []string{
				"-hide_banner", "-loglevel", "error", "-re",
				"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
				"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
				"-g", "30", "-bf", "0", "-b:v", kbps, "-maxrate", kbps, "-bufsize", kbps,
				"-f", "mpegts", url,
			}
		})
	}
	dir := t.TempDir()
	frames = filepath.Join(dir, "frames.crc")
	report = filepath.Join(dir, "decode.log")
	startPlayerSession = func(ctx context.Context, opts ...screenshare.ReceiverOptions) (*screenshare.Session, error) {
		return screenshare.StartProcess(ctx, ffmpeg, []string{
			"-hide_banner", "-f", "mpegts", "-i", "pipe:0", "-fps_mode", "passthrough", "-f", "framecrc", "-flush_packets", "1", "-y", frames,
		}, []string{"FFREPORT=file=" + report + ":level=16"}, true)
	}
	t.Cleanup(func() {
		startCaptureSession = screenshare.StartBroadcasting
		startPlayerSession = screenshare.StartReceiving
		screenSendFilter = nil
	})
	return frames, report
}

func countFrames(path string) int {
	count := 0
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if line := sc.Text(); line != "" && !strings.HasPrefix(line, "#") {
				count++
			}
		}
		f.Close()
	}
	return count
}

func decodeErrorLines(path string) []string {
	data, _ := os.ReadFile(path)
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		l := strings.ToLower(line)
		if strings.Contains(l, "error while decoding") || strings.Contains(l, "concealing") || strings.Contains(l, "corrupt") {
			out = append(out, line)
		}
	}
	return out
}

// relayRoom starts a relay with a host and an admitted joiner.
func relayRoom(t *testing.T, code string) (*P2PNode, *P2PNode) {
	t.Helper()
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "screen_host", "Sharer", relayURL)
	viewer := newRelayNode(t, "screen_viewer", "Viewer", relayURL)
	host.HostRoom(code)
	waitFor(t, "host registered on relay", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	if r := joinAndWait(t, viewer, code, 5*time.Second); !r.ok {
		t.Fatalf("join failed: %s", r.reason)
	}
	waitFor(t, "both members see each other", 5*time.Second, func() bool {
		host.mu.RLock()
		_, a := host.Peers[viewer.LocalID]
		host.mu.RUnlock()
		viewer.mu.RLock()
		_, b := viewer.Peers[host.LocalID]
		viewer.mu.RUnlock()
		return a && b
	})
	return host, viewer
}

// TestScreenShareEndToEnd streams a real H.264 test pattern from one node to another through
// the relay with 4 % injected loss, and checks the viewer decodes it cleanly thanks to NACK
// retransmission, and that a late viewer starts from the GOP cache.
func TestScreenShareEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("streams real video for several seconds")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	host, viewer := relayRoom(t, "5353-amber-falcon-river")

	frames, report := useSyntheticScreenPipeline(t, ffmpeg)
	var sent, dropped atomic.Int64
	screenSendFilter = func(data []byte, to string, class byte) bool {
		if len(data) < 1000 { // control / audio packets pass
			return true
		}
		if sent.Add(1)%25 == 0 { // 4 % loss on video chunks
			dropped.Add(1)
			return false
		}
		return true
	}
	if err := host.StartScreenShareWith(ScreenShareConfig{TargetID: "desktop", Preset: screenshare.DefaultPreset}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "viewer sees the share", 5*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		p := viewer.Peers[host.LocalID]
		return p != nil && p.IsSharingScreen
	})
	if s := host.ScreenStats(); s.Watchers != 0 {
		t.Fatalf("watchers before anyone watches: %d", s.Watchers)
	}
	if sent.Load() != 0 {
		t.Fatalf("video sent with no viewers: %d chunks", sent.Load())
	}

	time.Sleep(1500 * time.Millisecond) // join mid-GOP
	if err := viewer.StartWatchingScreen(host.LocalID, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "sharer registers the viewer", 3*time.Second, func() bool { return host.ScreenStats().Watchers == 1 })

	const watchFor = 6 * time.Second
	time.Sleep(watchFor)
	stats := host.ScreenStats()
	vstats := viewer.ScreenStats()
	_ = viewer.StopWatchingScreen()
	waitFor(t, "sharer drops the viewer", 3*time.Second, func() bool { return host.ScreenStats().Watchers == 0 })
	_ = host.StopScreenShare()
	time.Sleep(300 * time.Millisecond)

	count := countFrames(frames)
	decodeErrors := decodeErrorLines(report)
	t.Logf("sent %d video chunks, dropped %d, retransmitted %d, viewer residual loss %.2f%%, decoded %d frames in %v, decode errors %d",
		sent.Load(), dropped.Load(), stats.Retransmits, vstats.LossPct, count, watchFor, len(decodeErrors))
	if dropped.Load() == 0 || stats.Retransmits == 0 {
		t.Fatal("loss injection / NACK retransmission did not happen")
	}
	// 6 s at 30 FPS = 180 frames; starting from the GOP cache loses at most a few frames.
	if count < 160 {
		t.Fatalf("viewer decoded only %d frames", count)
	}
	if len(decodeErrors) > 2 {
		t.Fatalf("decoder errors despite retransmission:\n%s", strings.Join(decodeErrors, "\n"))
	}
}

// TestScreenShareAdaptsBitrateAndCarriesSystemAudio drives heavy loss so the sharer restarts the
// encoder at a lower bitrate, checks the viewer keeps decoding across the restart, and sends
// system audio frames that must come out of the viewer's mixer.
func TestScreenShareAdaptsBitrateAndCarriesSystemAudio(t *testing.T) {
	if testing.Short() {
		t.Skip("streams real video for several seconds")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	host, viewer := relayRoom(t, "6464-amber-falcon-river")
	frames, report := useSyntheticScreenPipeline(t, ffmpeg)
	var lossy atomic.Bool
	lossy.Store(true)
	var n atomic.Int64
	screenSendFilter = func(data []byte, to string, class byte) bool {
		// 25 % loss on video (retransmissions too) until the encoder has adapted.
		return len(data) < 1000 || !lossy.Load() || n.Add(1)%4 != 0
	}

	if err := host.StartScreenShareWith(ScreenShareConfig{TargetID: "desktop", Preset: 2, SystemAudio: false}); err != nil {
		t.Fatal(err)
	}
	start := host.ScreenStats().Kbps
	waitFor(t, "viewer sees the share", 5*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		p := viewer.Peers[host.LocalID]
		return p != nil && p.IsSharingScreen
	})
	if err := viewer.StartWatchingScreen(host.LocalID, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bitrate step down", 20*time.Second, func() bool { return host.ScreenStats().Kbps < start })
	lowered := host.ScreenStats().Kbps
	lossy.Store(false)
	waitFor(t, "viewer learns the new bitrate", 5*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		return viewer.Peers[host.LocalID].VideoKbps == lowered
	})

	// The restarted encoder's picture must reach the viewer promptly.
	restartFrames := countFrames(frames)
	waitFor(t, "frames from the restarted encoder", 1500*time.Millisecond, func() bool { return countFrames(frames) >= restartFrames+15 })

	// System audio: a 440 Hz tone injected into the sharer's audio path.
	host.mu.RLock()
	tx := host.screenTx
	host.mu.RUnlock()
	enc, err := newScreenAudioEncoder()
	if err != nil {
		t.Fatal(err)
	}
	tx.mu.Lock()
	tx.audioEnc, tx.audioOn = enc, true
	tx.mu.Unlock()
	tone := make([]int16, AudioFrameSamples)
	out := make([]int16, AudioFrameSamples)
	var peak int16
	for f := 0; f < 60; f++ {
		for i := range tone {
			tone[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(f*AudioFrameSamples+i)/AudioSampleRate))
		}
		tx.onSystemAudio(tone)
		time.Sleep(20 * time.Millisecond)
		viewer.audio.renderFrame(out)
		for _, v := range out {
			peak = max(peak, v)
		}
	}

	before := countFrames(frames)
	time.Sleep(2 * time.Second) // the viewer keeps decoding the restarted encoder at full rate
	after := countFrames(frames)
	_ = viewer.StopWatchingScreen()
	_ = host.StopScreenShare()
	time.Sleep(300 * time.Millisecond)
	errs := decodeErrorLines(report)
	t.Logf("bitrate %d → %d kbps, frames before/after restart window %d/%d, system audio peak %d, decode error lines %d",
		start, lowered, before, countFrames(frames), peak, len(errs))
	if after-before < 45 {
		t.Fatalf("viewer stopped decoding after the encoder restart (%d → %d frames)", before, after)
	}
	if peak < 2000 {
		t.Fatalf("system audio did not reach the viewer's mixer (peak %d)", peak)
	}
}
