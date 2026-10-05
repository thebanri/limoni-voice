package layout

import (
	"sync"

	"github.com/thebanri/limoni/core/cell"
)

type ConstraintKey struct {
	Type  ConstraintType
	Value uint16
}

type layoutKey struct {
	direction   Direction
	gap         uint16
	area        cell.Rect
	constraints [16]ConstraintKey
	fitSizes    [16]uint16
	numC        int
	numF        int
}

var splitCache = struct {
	sync.RWMutex
	m map[layoutKey][]cell.Rect
}{
	m: make(map[layoutKey][]cell.Rect),
}

// Direction is the direction layout elements are arranged in.
type Direction uint8

const (
	// Horizontal arranges elements left to right.
	Horizontal Direction = iota
	// Vertical arranges elements top to bottom.
	Vertical
)

// ConstraintType is the mathematical kind of a flexible layout constraint.
type ConstraintType uint8

const (
	// ConstraintFixed assigns a fixed width/height in character cells.
	ConstraintFixed ConstraintType = iota
	// ConstraintPercentage assigns a percentage of the parent area (0-100).
	ConstraintPercentage
	// ConstraintRatio shares the remaining space by weight (e.g. 1:2:3).
	ConstraintRatio
	// ConstraintMin assigns at least the specified size.
	ConstraintMin
	// ConstraintMax assigns at most the specified size.
	ConstraintMax
	// ConstraintFill takes all the remaining space.
	ConstraintFill
	// ConstraintFit sizes by the content.
	ConstraintFit
)

// Constraint is a single flexible layout constraint.
// It holds a Type and the Value that goes with it.
type Constraint struct {
	Type  ConstraintType
	Value uint16
}

// Fixed returns a constraint of a fixed size (in character cells).
// For example, Fixed(10) takes exactly 10 rows or columns.
func Fixed(val uint16) Constraint {
	return Constraint{Type: ConstraintFixed, Value: val}
}

// Percentage returns a constraint of a percentage of the total available space (0-100).
// For example, Percentage(30) takes 30% of the space.
func Percentage(val uint16) Constraint {
	if val > 100 {
		val = 100
	}
	return Constraint{Type: ConstraintPercentage, Value: val}
}

// Ratio shares out the remaining space by weight.
// For example, Ratio(2) and Ratio(1) split the remaining space 2/3 and 1/3.
func Ratio(val uint16) Constraint {
	if val == 0 {
		val = 1
	}
	return Constraint{Type: ConstraintRatio, Value: val}
}

// Min guarantees that at least the specified size is allocated.
func Min(val uint16) Constraint {
	return Constraint{Type: ConstraintMin, Value: val}
}

// Max caps the space allocated at the given size.
func Max(val uint16) Constraint {
	return Constraint{Type: ConstraintMax, Value: val}
}

// Fill returns a constraint that takes all the remaining space. (Equivalent to Ratio(1).)
func Fill() Constraint {
	return Constraint{Type: ConstraintFill}
}

// FitContent returns a constraint sized by the content (SizeHint).
func FitContent() Constraint {
	return Constraint{Type: ConstraintFit}
}

// FlexLayout is a flexible box that splits a terminal area along a direction by its constraints.
type FlexLayout struct {
	// Direction says whether elements are arranged horizontally or vertically.
	Direction Direction
	// Gap is the space left between the areas, in cells.
	Gap uint16
	// Constraints is the list of constraints that say how to split the area.
	Constraints []Constraint
}

// NewFlexLayout returns a new flexible layout (FlexLayout) engine.
func NewFlexLayout(dir Direction, gap uint16, constraints ...Constraint) FlexLayout {
	return FlexLayout{
		Direction:   dir,
		Gap:         gap,
		Constraints: constraints,
	}
}

// Split divides the given Rect into sub-areas according to the constraints.
// It handles rounding errors and gaps with zero heap allocations.
func (fl FlexLayout) Split(area cell.Rect, fitSizes ...uint16) []cell.Rect {
	if len(fl.Constraints) == 0 {
		return nil
	}

	canCache := len(fl.Constraints) <= 16 && len(fitSizes) <= 16
	var key layoutKey
	if canCache {
		key.direction = fl.Direction
		key.gap = fl.Gap
		key.area = area
		key.numC = len(fl.Constraints)
		for i := 0; i < key.numC; i++ {
			key.constraints[i] = ConstraintKey{
				Type:  fl.Constraints[i].Type,
				Value: fl.Constraints[i].Value,
			}
		}
		key.numF = len(fitSizes)
		for i := 0; i < key.numF; i++ {
			key.fitSizes[i] = fitSizes[i]
		}

		splitCache.RLock()
		if val, ok := splitCache.m[key]; ok {
			splitCache.RUnlock()
			res := make([]cell.Rect, len(val))
			copy(res, val)
			return res
		}
		splitCache.RUnlock()
	}

	// Total size along the split direction (width or height)
	var totalSize uint16
	if fl.Direction == Horizontal {
		totalSize = area.Width
	} else {
		totalSize = area.Height
	}

	// If the area has zero size, return zero-sized rectangles
	if totalSize == 0 {
		return make([]cell.Rect, len(fl.Constraints))
	}

	// Gap calculation
	numGaps := len(fl.Constraints) - 1
	totalGap := uint32(0)
	if numGaps > 0 {
		totalGap = uint32(numGaps) * uint32(fl.Gap)
	}

	// Net usable space
	var usableSize uint16
	if uint32(totalSize) > totalGap {
		usableSize = totalSize - uint16(totalGap)
	}

	sizes := make([]uint16, len(fl.Constraints))

	// Pass 1: work out the fixed and percentage (non-flexible) constraints
	var fixedTotal uint16
	fitIdx := 0

	for i, c := range fl.Constraints {
		switch c.Type {
		case ConstraintFixed:
			sizes[i] = c.Value
			fixedTotal += c.Value
		case ConstraintPercentage:
			sz := (uint32(usableSize) * uint32(c.Value)) / 100
			sizes[i] = uint16(sz)
			fixedTotal += uint16(sz)
		case ConstraintMin:
			sizes[i] = c.Value
			fixedTotal += c.Value
		case ConstraintFit:
			sz := uint16(0)
			if fitIdx < len(fitSizes) {
				sz = fitSizes[fitIdx]
				fitIdx++
			}
			sizes[i] = sz
			fixedTotal += sz
		}
	}

	// If the fixed constraints add up to more than the usable space, shrink them proportionally
	if fixedTotal > usableSize && fixedTotal > 0 {
		var scaledTotal uint16
		for i, c := range fl.Constraints {
			if c.Type != ConstraintRatio && c.Type != ConstraintFill && c.Type != ConstraintMin && c.Type != ConstraintMax {
				sz := (uint32(sizes[i]) * uint32(usableSize)) / uint32(fixedTotal)
				sizes[i] = uint16(sz)
				scaledTotal += uint16(sz)
			} else if c.Type == ConstraintMin {
				// Min constraints shrink like fixed ones too
				sz := (uint32(sizes[i]) * uint32(usableSize)) / uint32(fixedTotal)
				sizes[i] = uint16(sz)
				scaledTotal += uint16(sz)
			}
		}
		// Spread the remainder left by rounding
		diff := usableSize - scaledTotal
		for i := 0; i < len(sizes) && diff > 0; i++ {
			c := fl.Constraints[i]
			if c.Type != ConstraintRatio && c.Type != ConstraintFill && c.Type != ConstraintMax {
				sizes[i]++
				diff--
			}
		}
		fixedTotal = usableSize
	}

	// Remaining free space
	remaining := usableSize - fixedTotal

	// Pass 2: share the remaining space among the proportional constraints (Ratio, Fill, Min and Max)
	if remaining > 0 {
		n := len(fl.Constraints)
		var activeStack [32]bool
		var active []bool
		if n <= 32 {
			active = activeStack[:n]
		} else {
			active = make([]bool, n)
		}
		hasActive := false
		for i := 0; i < n; i++ {
			c := fl.Constraints[i]
			if c.Type == ConstraintRatio || c.Type == ConstraintFill || c.Type == ConstraintMin || c.Type == ConstraintMax {
				active[i] = true
				hasActive = true
			}
		}

		var addedStack [32]uint16
		var added []uint16
		if n <= 32 {
			added = addedStack[:n]
		} else {
			added = make([]uint16, n)
		}

		for hasActive && remaining > 0 {
			var totalWeight uint32
			for i := 0; i < n; i++ {
				if active[i] {
					c := fl.Constraints[i]
					switch c.Type {
					case ConstraintRatio:
						totalWeight += uint32(c.Value)
					case ConstraintFill, ConstraintMin, ConstraintMax:
						totalWeight += 1
					}
				}
			}

			if totalWeight == 0 {
				break
			}

			for i := range added {
				added[i] = 0
			}
			var distributed uint16
			var cappedThisIteration bool

			for i := 0; i < n; i++ {
				if active[i] {
					c := fl.Constraints[i]
					weight := uint32(1)
					if c.Type == ConstraintRatio {
						weight = uint32(c.Value)
					}
					sz := uint16((uint32(remaining) * weight) / totalWeight)

					// Check whether a Max constraint is exceeded
					if c.Type == ConstraintMax {
						currentTotal := sizes[i] + sz
						if currentTotal > c.Value {
							sz = c.Value - sizes[i] // Grow only up to the limit
							active[i] = false       // This element cannot grow any further
							cappedThisIteration = true
						}
					}

					added[i] = sz
					distributed += sz
				}
			}

			// Add the rounding remainder to the last active elements
			if !cappedThisIteration && remaining > distributed {
				diff := remaining - distributed
				for i := 0; i < n && diff > 0; i++ {
					if active[i] {
						added[i]++
						distributed++
						diff--
					}
				}
			}

			// Update the sizes
			for i := 0; i < n; i++ {
				sizes[i] += added[i]
			}
			remaining -= distributed

			// If no element hit its limit in this iteration, all the remaining space has been shared out
			if !cappedThisIteration {
				break
			}

			hasActive = false
			for i := 0; i < n; i++ {
				if active[i] {
					hasActive = true
					break
				}
			}
		}
	}

	// Work out the bounds and return them as a slice
	res := make([]cell.Rect, len(fl.Constraints))
	currX := area.X
	currY := area.Y

	for i, sz := range sizes {
		if fl.Direction == Horizontal {
			res[i] = cell.Rect{
				X:      currX,
				Y:      currY,
				Width:  sz,
				Height: area.Height,
			}
			currX += sz
			if i < len(sizes)-1 {
				currX += fl.Gap
			}
		} else {
			res[i] = cell.Rect{
				X:      currX,
				Y:      currY,
				Width:  area.Width,
				Height: sz,
			}
			currY += sz
			if i < len(sizes)-1 {
				currY += fl.Gap
			}
		}
	}

	if canCache {
		splitCache.Lock()
		if len(splitCache.m) > 1024 {
			clear(splitCache.m)
		}
		// Store a copy in cache so caller mutations cannot corrupt cache
		cached := make([]cell.Rect, len(res))
		copy(cached, res)
		splitCache.m[key] = cached
		splitCache.Unlock()
	}

	return res
}
