package animation

import (
	"time"

	"github.com/thebanri/limoni/core/cell"
)

// Color animates a cell.Color (TrueColor/RGB) towards a target colour over time,
// fading smoothly along an easing curve.
type Color struct {
	startCol  cell.Color
	endCol    cell.Color
	current   cell.Color
	startTime time.Time
	duration  time.Duration
	easing    EasingFunc
	animating bool
}

// NewColor returns a Color animation starting at the given colour.
func NewColor(initial cell.Color) *Color {
	return &Color{
		startCol:  initial,
		endCol:    initial,
		current:   initial,
		animating: false,
	}
}

// AnimateTo starts a new transition towards the target colour.
// If duration is zero or negative, the colour jumps to the target at once.
func (c *Color) AnimateTo(target cell.Color, duration time.Duration, easing EasingFunc) {
	if easing == nil {
		easing = Linear
	}
	c.startCol = c.current
	c.endCol = target
	c.duration = duration
	c.easing = easing
	c.startTime = time.Now()

	if duration <= 0 {
		c.current = target
		c.animating = false
	} else {
		c.animating = true
	}
}

// Update advances the animation to the given time.
// It reports true while the animation is running, false once it has finished or if it never started.
func (c *Color) Update(now time.Time) bool {
	if !c.animating {
		return false
	}

	elapsed := now.Sub(c.startTime)
	if elapsed >= c.duration {
		c.current = c.endCol
		c.animating = false
		return false
	}

	// Normalised time (0.0 - 1.0)
	t := float64(elapsed) / float64(c.duration)
	// Eased progress
	progress := c.easing(t)

	// If both colours are RGB, interpolate each TrueColor channel
	if c.startCol.Type() == cell.ColorRGB && c.endCol.Type() == cell.ColorRGB {
		sr, sg, sb := c.startCol.RGB()
		er, eg, eb := c.endCol.RGB()

		r := uint8(float64(sr) + (float64(er)-float64(sr))*progress)
		g := uint8(float64(sg) + (float64(eg)-float64(sg))*progress)
		b := uint8(float64(sb) + (float64(eb)-float64(sb))*progress)

		c.current = cell.NewColorRGB(r, g, b)
	} else {
		// Other colour kinds (Default or ANSI) switch over at the midpoint (step function)
		if progress >= 0.5 {
			c.current = c.endCol
		} else {
			c.current = c.startCol
		}
	}

	return true
}

// Value returns the current colour.
func (c *Color) Value() cell.Color {
	return c.current
}

// SetColor stops the animation and sets the colour directly.
func (c *Color) SetColor(col cell.Color) {
	c.startCol = col
	c.endCol = col
	c.current = col
	c.animating = false
}

// Stop halts the animation where it is. The colour stays at its current blend.
func (c *Color) Stop() {
	c.animating = false
}

// IsAnimating reports whether the animation is running.
func (c *Color) IsAnimating() bool {
	return c.animating
}
