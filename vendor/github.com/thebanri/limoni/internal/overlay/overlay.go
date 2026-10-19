// Package overlay shows pictures in windows of their own inside the
// terminal's window, for terminals that have no image protocol (Alacritty,
// older VTE): the picture keeps every pixel instead of becoming half blocks.
// It backs graphics.ProtocolOverlay; where it cannot work (no X11 window for
// the terminal, another platform), Open fails and the terminal falls back to
// half blocks.
package overlay

import "image"

// Picture is one picture to show: Img fitted into W×H pixels at X, Y
// pixels from the top left of the terminal's cell grid.
type Picture struct {
	Img        image.Image
	X, Y, W, H int
}
