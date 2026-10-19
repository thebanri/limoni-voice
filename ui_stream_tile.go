// The stage's stream tiles: a play button laid over each preview, which grows when the
// pointer is over the tile.

package main

import (
	"image"
	"image/draw"
	"math"

	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/widgets"
)

// playSteps are the pictures of a tile's hover animation, from the button at rest to the
// button under the pointer. Each is made once per preview, so the overlay (or the terminal)
// is sent each once, not every frame.
const playSteps = 6

// hoverStep is how far the animation moves in a frame, at the room's 30 frames a second:
// about 170 ms from rest to hover.
const hoverStep = 0.2

// stageTile is what a tile keeps between frames.
type stageTile struct {
	fx  tileFx
	img widgets.Image
}

// tileFx is a tile's hover state and the pictures of its animation.
type tileFx struct {
	src    image.Image
	frames [playSteps]image.Image
	hover  float64 // 0 at rest, 1 under the pointer
}

// step moves the hover toward the pointer and returns the picture for it.
func (fx *tileFx) step(src image.Image, hovered bool) (image.Image, float64) {
	if src != fx.src {
		fx.src, fx.frames = src, [playSteps]image.Image{}
	}
	if hovered {
		fx.hover = min(1, fx.hover+hoverStep)
	} else {
		fx.hover = max(0, fx.hover-hoverStep)
	}
	if src == nil {
		return nil, fx.hover
	}
	i := int(math.Round(fx.hover * (playSteps - 1)))
	if fx.frames[i] == nil {
		fx.frames[i] = withPlayButton(src, float64(i)/(playSteps-1))
	}
	return fx.frames[i], fx.hover
}

// withPlayButton is src with a play button in the middle: a dark disc and a white triangle,
// larger and darker at hover t (0 to 1), edges smoothed over a pixel.
func withPlayButton(src image.Image, t float64) image.Image {
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Rect, src, b.Min, draw.Src)
	w, h := float64(b.Dx()), float64(b.Dy())
	r := min(w, h) * (0.15 + 0.05*t)
	cx, cy := w/2, h/2
	discA := 0.45 + 0.3*t
	// The triangle points right, its centre a little right of the disc's, as play buttons are.
	ax, ay := cx-0.35*r, cy-0.5*r
	bx, by := cx-0.35*r, cy+0.5*r
	px, py := cx+0.55*r, cy
	x0, x1 := int(cx-r-1), int(cx+r+2)
	y0, y1 := int(cy-r-1), int(cy+r+2)
	for y := max(0, y0); y < min(b.Dy(), y1); y++ {
		for x := max(0, x0); x < min(b.Dx(), x1); x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			disc := clamp01(r - math.Hypot(fx-cx, fy-cy) + 0.5)
			if disc == 0 {
				continue
			}
			tri := clamp01(min(edge(ax, ay, bx, by, fx, fy), edge(bx, by, px, py, fx, fy), edge(px, py, ax, ay, fx, fy)) + 0.5)
			i := out.PixOffset(x, y)
			for c := range 3 {
				v := float64(out.Pix[i+c]) * (1 - discA*disc) // darken under the disc
				v += (255 - v) * tri                          // white triangle
				out.Pix[i+c] = uint8(v)
			}
		}
	}
	return out
}

// edge is the signed distance of (x, y) from the line a→b, positive to its right in screen
// coordinates: inside a triangle given counter-clockwise on screen.
func edge(ax, ay, bx, by, x, y float64) float64 {
	dx, dy := bx-ax, by-ay
	return ((x-ax)*dy - (y-ay)*dx) / math.Hypot(dx, dy)
}

func clamp01(v float64) float64 { return min(1, max(0, v)) }

// mixColor is a blended toward b by t.
func mixColor(a, b cell.Color, t float64) cell.Color {
	ar, ag, ab := a.RGB()
	br, bg, bb := b.RGB()
	m := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return cell.NewColorRGB(m(ar, br), m(ag, bg), m(ab, bb))
}
