package animation

import (
	"math"
)

// EasingFunc is an easing function: it takes normalised time/progress (0.0 - 1.0)
// and returns a normalised output value (0.0 - 1.0).
type EasingFunc func(t float64) float64

// Linear is a straight, constant-speed transition.
func Linear(t float64) float64 {
	return t
}

// EaseInQuad starts slowly and accelerates (quadratic).
func EaseInQuad(t float64) float64 {
	return t * t
}

// EaseOutQuad starts fast and slows towards the end (quadratic).
func EaseOutQuad(t float64) float64 {
	return t * (2 - t)
}

// EaseInOutQuad starts slowly, speeds up in the middle and slows again at the end (quadratic).
func EaseInOutQuad(t float64) float64 {
	if t < 0.5 {
		return 2 * t * t
	}
	return -1 + (4-2*t)*t
}

// EaseInCubic starts slowly and accelerates (cubic).
func EaseInCubic(t float64) float64 {
	return t * t * t
}

// EaseOutCubic starts fast and slows towards the end (cubic).
func EaseOutCubic(t float64) float64 {
	t2 := t - 1
	return t2*t2*t2 + 1
}

// EaseInOutCubic starts slowly, speeds up in the middle and slows at the end (cubic).
func EaseInOutCubic(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	t2 := 2*t - 2
	return 0.5*t2*t2*t2 + 1
}

// EaseInSine starts slowly and accelerates along a sine curve.
func EaseInSine(t float64) float64 {
	return 1 - math.Cos((t*math.Pi)/2)
}

// EaseOutSine starts fast and slows along a sine curve.
func EaseOutSine(t float64) float64 {
	return math.Sin((t * math.Pi) / 2)
}

// EaseInOutSine is a smooth transition that starts and ends along a sine curve.
func EaseInOutSine(t float64) float64 {
	return -(math.Cos(math.Pi*t) - 1) / 2
}

// EaseInExpo starts slowly and ends very fast (exponential).
func EaseInExpo(t float64) float64 {
	if t == 0 {
		return 0
	}
	return math.Pow(2, 10*(t-1))
}

// EaseOutExpo starts very fast and slows towards the end (exponential).
func EaseOutExpo(t float64) float64 {
	if t == 1 {
		return 1
	}
	return 1 - math.Pow(2, -10*t)
}

// EaseInOutExpo is an exponential curve that starts slowly, is very fast in the middle and slows at the end.
func EaseInOutExpo(t float64) float64 {
	if t == 0 {
		return 0
	}
	if t == 1 {
		return 1
	}
	if t < 0.5 {
		return math.Pow(2, 20*t-10) / 2
	}
	return (2 - math.Pow(2, -20*t+10)) / 2
}

// EaseOutBounce hits the end and bounces back.
func EaseOutBounce(t float64) float64 {
	const n1 = 7.5625
	const d1 = 2.75

	if t < 1/d1 {
		return n1 * t * t
	} else if t < 2/d1 {
		t -= 1.5 / d1
		return n1*t*t + 0.75
	} else if t < 2.5/d1 {
		t -= 2.25 / d1
		return n1*t*t + 0.9375
	} else {
		t -= 2.625 / d1
		return n1*t*t + 0.984375
	}
}

// EaseInBounce is the bounce in reverse.
func EaseInBounce(t float64) float64 {
	return 1 - EaseOutBounce(1-t)
}

// EaseInOutBounce bounces at both the start and the end.
func EaseInOutBounce(t float64) float64 {
	if t < 0.5 {
		return (1 - EaseOutBounce(1-2*t)) / 2
	}
	return (1 + EaseOutBounce(2*t-1)) / 2
}
