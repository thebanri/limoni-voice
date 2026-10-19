package terminal

import (
	"os"

	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/graphics"
	"github.com/thebanri/limoni/internal/overlay"
)

// SetOverlayPictures shows pictures in the overlay (graphics.ProtocolOverlay)
// where the terminal would get half blocks: one with no picture protocol of
// its own, such as Alacritty, in an X11 window (Xwayland is one). It is
// decided once the terminal has answered the capability probe, so a protocol
// it reports there wins, and LIMONI_GRAPHICS wins over both.
//
// It is off unless asked for: a terminal with tabs would show the pictures
// over every tab, not only the application's.
func (t *Terminal) SetOverlayPictures(on bool) {
	if t != nil {
		t.overlayFallback = on
	}
}

// syncOverlay opens the overlay the profile asks for (graphics.ProtocolOverlay),
// or SetOverlayPictures does, before the frame is drawn, so widgets already
// see half blocks if it cannot be had: no X server, no X11 window for the
// terminal, an inline application whose rows move with the scrollback, or a
// backend that is not this process's terminal (an SSH session's pictures
// would open on the server's screen).
func (t *Terminal) syncOverlay() {
	if t.caps.GraphicsProto != graphics.ProtocolOverlay && !t.overlayWanted() {
		if t.overlay != nil {
			_ = t.overlay.Close()
			t.overlay = nil
		}
		return
	}
	if t.overlay == nil && !t.overlayFailed {
		t.overlayFailed = true
		if !t.driver.Portable() && t.inline == 0 {
			if ov, err := overlay.Open(); err == nil {
				t.overlay, t.overlayFailed = ov, false
			}
		}
	}
	t.caps.GraphicsProto = graphics.ProtocolOverlay
	if t.overlay == nil {
		t.caps.GraphicsProto = graphics.ProtocolHalfBlock
	}
}

// overlayWanted reports whether SetOverlayPictures turns half blocks into the
// overlay now: the probe is answered and it found no protocol.
func (t *Terminal) overlayWanted() bool {
	if !t.overlayFallback || t.caps.GraphicsProto != graphics.ProtocolHalfBlock || os.Getenv("LIMONI_GRAPHICS") != "" || t.driver == nil {
		return false
	}
	select {
	case <-t.driver.ProbeAnswered():
		return true
	default:
		return false
	}
}

// placeOverlay shows the frame's pictures as overlay windows. A window covers
// whatever cells are under it, so pictures that cells must be drawn over are
// left out: the solid backgrounds opaque blocks register for the native
// protocols (z -2 and -4), and pictures behind an open modal or layer.
//
// The cell size comes from the overlay, which measures the terminal's window,
// not from the pty: a pty in between (tmux, a shell wrapper) often reports no
// pixels at all.
func (t *Terminal) placeOverlay(regions []ImageRegion) {
	cellW, cellH := t.overlay.CellSize(int(t.front.Area.Width), int(t.front.Area.Height))
	pics := t.overlayPics[:0]
	for _, reg := range regions {
		if reg.ZIndex == -2 || reg.ZIndex == -4 || reg.ZIndex == -3 && t.underLayer(reg.Area) {
			continue
		}
		img := reg.Img
		if c, ok := img.(*graphics.Clip); ok {
			img = c.Image
		}
		pics = append(pics, overlay.Picture{
			Img: img,
			X:   int(reg.Area.X) * cellW, Y: int(reg.Area.Y) * cellH,
			W: int(reg.Area.Width) * cellW, H: int(reg.Area.Height) * cellH,
		})
	}
	t.overlayPics = pics
	gridW, gridH := int(t.front.Area.Width)*cellW, int(t.front.Area.Height)*cellH
	if err := t.overlay.Show(pics, gridW, gridH); err != nil {
		// The X server went away: draw half blocks from the next frame on.
		_ = t.overlay.Close()
		t.overlay = nil
		t.caps.GraphicsProto = graphics.ProtocolHalfBlock
		t.ForceFullRedraw()
		return
	}
	// A picture given to ForgetImage will not be shown again: the copies
	// of those not on screen go, or a picture drawn anew every frame (a
	// turning record) would fill the cache.
	if gen := graphics.Generation(); gen != t.overlayGen {
		t.overlayGen = gen
		_ = t.overlay.Trim()
	}
}

// hideOverlay takes every overlay window down, for while the shell or another
// program has the terminal; the full redraw after brings them back.
func (t *Terminal) hideOverlay() {
	if t.overlay != nil {
		_ = t.overlay.Show(nil, 0, 0)
	}
}

func (t *Terminal) underLayer(area cell.Rect) bool {
	if m := t.frame.TopmostModal(); m != nil && Intersects(m.Area, area) {
		return true
	}
	for _, l := range t.frame.Layers {
		if Intersects(l.Area, area) {
			return true
		}
	}
	return false
}
