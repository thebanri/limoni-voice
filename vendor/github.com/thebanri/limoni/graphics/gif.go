package graphics

import (
	"errors"
	"image"
	"image/draw"
	"image/gif"
	"io"
	"time"
)

// Animation is a decoded animated GIF: every frame whole, as a browser shows
// it, and how long each stays. Show Frame(time since the start) in an Image
// widget, with limoni.WithFPS to keep frames coming.
//
// shortcut: it loops forever; the GIF's loop count is not read.
type Animation struct {
	Frames []image.Image
	Delays []time.Duration
	total  time.Duration
}

// DecodeGIF reads every frame of a GIF. A GIF frame holds only what changed,
// laid over what the frame before left (its disposal method); each is made a
// whole picture here, and a value of its own, so the image caches and the
// terminal take every frame for a new picture without ForgetImage.
//
// It costs width×height×4 bytes a frame: 100 frames of 500×500 take 100 MB.
func DecodeGIF(r io.Reader) (*Animation, error) {
	g, err := gif.DecodeAll(r)
	if err != nil {
		return nil, err
	}
	if len(g.Image) == 0 {
		return nil, errors.New("graphics: a GIF without frames")
	}
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	if bounds.Empty() {
		for _, f := range g.Image {
			bounds = bounds.Union(f.Bounds())
		}
	}
	canvas := image.NewRGBA(bounds)
	a := &Animation{}
	for i, f := range g.Image {
		var disposal byte
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		var before *image.RGBA
		if disposal == gif.DisposalPrevious {
			before = cloneRGBA(canvas)
		}
		draw.Draw(canvas, f.Bounds(), f, f.Bounds().Min, draw.Over)
		a.Frames = append(a.Frames, cloneRGBA(canvas))

		// Browsers show delays under 20 ms (0 and 1, in GIF's hundredths)
		// as 100 ms, and GIFs are made to look right in browsers.
		d := 100 * time.Millisecond
		if i < len(g.Delay) && g.Delay[i] >= 2 {
			d = time.Duration(g.Delay[i]) * 10 * time.Millisecond
		}
		a.Delays = append(a.Delays, d)
		a.total += d

		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, f.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = before
		}
	}
	return a, nil
}

// Frame returns the frame shown elapsed after the start, looping. It does
// not allocate, so it can be called from a draw function every frame.
func (a *Animation) Frame(elapsed time.Duration) image.Image {
	if a.total <= 0 || len(a.Frames) == 1 {
		return a.Frames[0]
	}
	t := elapsed % a.total
	if t < 0 {
		t += a.total
	}
	for i, d := range a.Delays {
		if t < d {
			return a.Frames[i]
		}
		t -= d
	}
	return a.Frames[len(a.Frames)-1]
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Rect)
	copy(dst.Pix, src.Pix)
	return dst
}
