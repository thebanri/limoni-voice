package p2p

import (
	"bytes"
	"image"
	"image/jpeg"
	"math/rand"
	"os/exec"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/screenshare"
)

// A share's preview reaches every member, watching or not, as the share starts; it is made
// from the stream itself, and goes when the share ends.
func TestScreenPreviewReachesTheRoom(t *testing.T) {
	if testing.Short() {
		t.Skip("streams real video")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	host, viewer := relayRoom(t, "6464-amber-falcon-river")
	useSyntheticScreenPipeline(t, ffmpeg)
	if err := host.StartScreenShareWith(ScreenShareConfig{TargetID: "desktop", Preset: screenshare.DefaultPreset}); err != nil {
		t.Fatal(err)
	}
	var got image.Image
	waitFor(t, "the viewer gets the preview", 15*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		if p := viewer.Peers[host.LocalID]; p != nil {
			got = p.ScreenPreview
		}
		return got != nil
	})
	own := host.OwnScreenPreview()
	if own == nil || own.Bounds().Dx() != previewWidth || got.Bounds().Dx() != previewWidth {
		t.Fatalf("previews %v (own) and %v (received), want %d wide", own, got.Bounds(), previewWidth)
	}
	if s := host.ScreenStats(); s.Watchers != 0 {
		t.Fatalf("a preview made %d watchers", s.Watchers)
	}
	_ = host.StopScreenShare()
	waitFor(t, "the preview goes with the share", 5*time.Second, func() bool {
		viewer.mu.RLock()
		defer viewer.mu.RUnlock()
		p := viewer.Peers[host.LocalID]
		return p != nil && p.ScreenPreview == nil && host.OwnScreenPreview() == nil
	})
}

// A preview from the room is taken only from a member that shares, and only as a JPEG of a
// sensible size.
func TestScreenPreviewIsChecked(t *testing.T) {
	n := testRoomNode("me", "room")
	n.Peers["a"] = &PeerInfo{ID: "a", IsSharingScreen: true}
	n.Peers["b"] = &PeerInfo{ID: "b"}
	jpg, err := encodePreview(noise(480, 270))
	if err != nil {
		t.Fatal(err)
	}
	if len(jpg) > previewMaxBytes {
		t.Fatalf("preview of %d bytes, want at most %d", len(jpg), previewMaxBytes)
	}
	n.onScreenPreview("b", jpg)
	n.onScreenPreview("a", []byte("not a picture"))
	if n.Peers["a"].ScreenPreview != nil || n.Peers["b"].ScreenPreview != nil {
		t.Fatal("a preview was taken from a member not sharing, or from garbage")
	}
	var huge bytes.Buffer
	jpeg.Encode(&huge, image.NewGray(image.Rect(0, 0, maxPreviewSide+1, 8)), nil)
	n.onScreenPreview("a", huge.Bytes())
	if n.Peers["a"].ScreenPreview != nil {
		t.Fatal("a picture larger than a preview was taken")
	}
	n.onScreenPreview("a", jpg)
	if p := n.Peers["a"].ScreenPreview; p == nil || p.Bounds().Dx() < 120 {
		t.Fatal("a sharing member's preview was not kept")
	}
}

// The stream is gathered from a keyframe on, only when a preview is due, and one at a time.
func TestScreenPreviewWaitsForAKeyframe(t *testing.T) {
	decoded := make(chan []byte, 4)
	old := decodePreviewFrame
	decodePreviewFrame = func(ts []byte) (image.Image, error) {
		decoded <- ts
		return noise(64, 36), nil
	}
	t.Cleanup(func() { decodePreviewFrame = old })
	tx := &screenTx{n: testRoomNode("me", "room"), stop: make(chan struct{})}
	big := make([]byte, previewGrabBytes)

	tx.collectPreviewLocked([]byte("delta"), false)
	tx.collectPreviewLocked([]byte("key"), true)
	tx.collectPreviewLocked(big, false)
	select {
	case ts := <-decoded:
		if !bytes.HasPrefix(ts, []byte("key")) {
			t.Fatalf("decoded from %q, want from the keyframe", ts[:5])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was decoded")
	}
	waitFor(t, "the preview is kept", 3*time.Second, func() bool { return tx.n.OwnScreenPreview() != nil })
	tx.mu.Lock()
	due := time.Until(tx.previewDue)
	tx.mu.Unlock()
	if due < previewEvery-time.Second {
		t.Fatalf("next preview in %v, want a minute", due)
	}
	tx.collectPreviewLocked([]byte("key"), true)
	if tx.previewBuf != nil {
		t.Fatal("a preview was gathered before it was due")
	}
}

func noise(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = uint8(r.Intn(256))
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}
