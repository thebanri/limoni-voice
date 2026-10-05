package animation

import (
	"time"
)

// Float animates a numeric value (float64) towards a target over time,
// converging smoothly along an easing curve.
type Float struct {
	startVal  float64
	endVal    float64
	current   float64
	startTime time.Time
	duration  time.Duration
	easing    EasingFunc
	animating bool
}

// NewFloat returns a Float animation starting at the given value.
func NewFloat(initial float64) *Float {
	return &Float{
		startVal:  initial,
		endVal:    initial,
		current:   initial,
		animating: false,
	}
}

// AnimateTo starts a new animation towards the target value.
// If duration is zero or negative, the value jumps to the target at once.
func (f *Float) AnimateTo(target float64, duration time.Duration, easing EasingFunc) {
	if easing == nil {
		easing = Linear
	}
	f.startVal = f.current
	f.endVal = target
	f.duration = duration
	f.easing = easing
	f.startTime = time.Now()

	if duration <= 0 {
		f.current = target
		f.animating = false
	} else {
		f.animating = true
	}
}

// Update advances the animation to the given time.
// It reports true while the animation is still running, false once it has finished or if it never started.
func (f *Float) Update(now time.Time) bool {
	if !f.animating {
		return false
	}

	elapsed := now.Sub(f.startTime)
	if elapsed >= f.duration {
		f.current = f.endVal
		f.animating = false
		return false
	}

	// Normalised time (0.0 - 1.0)
	t := float64(elapsed) / float64(f.duration)
	// Eased progress
	progress := f.easing(t)
	// Linear interpolation (lerp)
	f.current = f.startVal + (f.endVal-f.startVal)*progress

	return true
}

// Value returns the current value.
func (f *Float) Value() float64 {
	return f.current
}

// Target returns the value the animation ends at.
func (f *Float) Target() float64 {
	return f.endVal
}

// SetValue stops the animation and sets the value directly.
func (f *Float) SetValue(val float64) {
	f.startVal = val
	f.endVal = val
	f.current = val
	f.animating = false
}

// Stop halts the animation where it is. The value stays where it is.
func (f *Float) Stop() {
	f.animating = false
}

// IsAnimating reports whether the animation is running.
func (f *Float) IsAnimating() bool {
	return f.animating
}
