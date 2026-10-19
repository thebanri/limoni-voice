package p2p

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os/exec"
	"strconv"
	"time"

	"github.com/thebanri/limoni-voice/internal/protocol"
	"github.com/thebanri/limoni-voice/screenshare"
)

// Stream previews, as Discord shows them: the sharer takes a picture of its share from the
// encoder's own output when it starts and once a minute, and sends it to the room, whose
// members show it on their stage. The picture is a keyframe of the stream decoded by ffmpeg,
// so it is what viewers would see, from any capture (screen, window, camera) on any system,
// without a second capture of the screen.
const (
	previewEvery    = time.Minute
	previewRetry    = 5 * time.Second
	previewMaxBytes = 15 * 1024 // one packet, as a chat part is
	previewWidth    = 480
	maxPreviewSide  = 1920
	// From a keyframe on, this much of the stream, or this long of it, holds a whole picture.
	previewGrabBytes = 768 * 1024
	previewGrabTime  = 1500 * time.Millisecond
)

// errNoDecoder means no picture can be made here at all: there is no ffmpeg to decode with.
var errNoDecoder = errors.New("no ffmpeg to decode the preview")

// decodePreviewFrame decodes the first picture of MPEG-TS that starts at a keyframe. It is a
// variable so tests can stand in for ffmpeg.
var decodePreviewFrame = func(ts []byte) (image.Image, error) {
	ffmpeg, err := screenshare.FindExecutable("ffmpeg")
	if err != nil {
		return nil, errNoDecoder
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "mpegts", "-i", "pipe:0",
		"-frames:v", "1", "-vf", "scale='min("+strconv.Itoa(previewWidth)+",iw)':-2",
		"-f", "image2pipe", "-c:v", "png", "pipe:1")
	cmd.Stdin = bytes.NewReader(ts)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(out))
}

// collectPreviewLocked gathers the stream for the next preview when one is due, from the
// next keyframe on, and hands it to makePreview once it holds a picture.
func (tx *screenTx) collectPreviewLocked(chunk []byte, keyframe bool) {
	now := time.Now()
	switch {
	case tx.previewBuf != nil:
		tx.previewBuf = append(tx.previewBuf, chunk...)
		if len(tx.previewBuf) < previewGrabBytes && now.Sub(tx.previewStart) < previewGrabTime {
			return
		}
		ts := tx.previewBuf
		tx.previewBuf, tx.previewBusy = nil, true
		go tx.makePreview(ts)
	case keyframe && !tx.previewBusy && !now.Before(tx.previewDue):
		tx.previewBuf, tx.previewStart = append([]byte(nil), chunk...), now
	}
}

// makePreview decodes a picture from the stream, keeps it as our own share's preview and
// sends it to the room.
func (tx *screenTx) makePreview(ts []byte) {
	img, err := decodePreviewFrame(ts)
	var jpg []byte
	if err == nil {
		jpg, err = encodePreview(img)
	}
	n := tx.n
	tx.mu.Lock()
	tx.previewBusy = false
	switch {
	case errors.Is(err, errNoDecoder):
		tx.previewDue = time.Now().Add(100 * 365 * 24 * time.Hour) // never: no ffmpeg will appear
	case err != nil:
		tx.previewDue = time.Now().Add(previewRetry)
	default:
		tx.previewDue = time.Now().Add(previewEvery)
	}
	tx.mu.Unlock()
	if err != nil {
		n.debugLog("[SCREEN] [SHARE] No preview: " + err.Error())
		return
	}
	select {
	case <-tx.stop:
		return // the share ended meanwhile
	default:
	}
	n.mu.Lock()
	n.ownScreenPreview = img
	room := n.roomID
	n.mu.Unlock()
	n.sendToRoom(&P2PPacket{
		Type:      PacketScreenPreview,
		RoomCode:  room,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		Payload:   jpg,
		Timestamp: time.Now().UnixMilli(),
	}, protocol.FrameReliable)
}

// encodePreview is img as a JPEG small enough for one packet: its quality lowered until it
// is, and for a picture too busy even then (film grain, noise), its size halved.
func encodePreview(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	for img.Bounds().Dx() >= 60 {
		for _, q := range []int{75, 60, 45, 30} {
			buf.Reset()
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
				return nil, err
			}
			if buf.Len() <= previewMaxBytes {
				return buf.Bytes(), nil
			}
		}
		img = halve(img)
	}
	return nil, errors.New("preview does not fit a packet")
}

// halve is img at half its size, each pixel the mean of four.
func halve(img image.Image) image.Image {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()/2, b.Dy()/2))
	for y := range out.Rect.Dy() {
		for x := range out.Rect.Dx() {
			var sum [4]uint32
			for _, d := range [4][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				r, g, bl, a := img.At(b.Min.X+2*x+d[0], b.Min.Y+2*y+d[1]).RGBA()
				sum[0], sum[1], sum[2], sum[3] = sum[0]+r, sum[1]+g, sum[2]+bl, sum[3]+a
			}
			i := out.PixOffset(x, y)
			for c := range 4 {
				out.Pix[i+c] = uint8(sum[c] / 4 >> 8)
			}
		}
	}
	return out
}

// onScreenPreview keeps a sharing member's preview. It comes from the room, so it is checked
// before it is decoded: a JPEG of a sensible size, from a member that shares.
func (n *P2PNode) onScreenPreview(from string, jpg []byte) {
	if len(jpg) > 2*previewMaxBytes {
		return
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpg))
	if err != nil || cfg.Width > maxPreviewSide || cfg.Height > maxPreviewSide {
		return
	}
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return
	}
	n.mu.Lock()
	if p := n.Peers[from]; p != nil && p.IsSharingScreen {
		p.ScreenPreview = img
	}
	n.mu.Unlock()
}

// OwnScreenPreview is our share's latest preview, nil while there is none.
func (n *P2PNode) OwnScreenPreview() image.Image {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.ownScreenPreview
}
