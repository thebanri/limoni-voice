package cell

// Box-drawing line segments, one bit per direction leaving the cell centre.
const (
	LineUp uint8 = 1 << iota
	LineDown
	LineLeft
	LineRight
)

// boxDrawingSegments maps a light box-drawing rune to the segments it draws.
//
// Only the light set is merged. Heavy and double lines have no well-defined
// junction with light ones — ┃ meeting ─ has no single glyph that is honest
// about both — so a mixed pair is left alone rather than silently downgraded.
var boxDrawingSegments = map[rune]uint8{
	'─': LineLeft | LineRight,
	'│': LineUp | LineDown,

	'┌': LineDown | LineRight,
	'┐': LineDown | LineLeft,
	'└': LineUp | LineRight,
	'┘': LineUp | LineLeft,

	// Rounded corners draw the same segments as their square counterparts.
	'╭': LineDown | LineRight,
	'╮': LineDown | LineLeft,
	'╰': LineUp | LineRight,
	'╯': LineUp | LineLeft,

	'├': LineUp | LineDown | LineRight,
	'┤': LineUp | LineDown | LineLeft,
	'┬': LineLeft | LineRight | LineDown,
	'┴': LineLeft | LineRight | LineUp,
	'┼': LineUp | LineDown | LineLeft | LineRight,
}

// segmentsToBoxDrawing is the reverse mapping, built from the square-cornered
// set so a merge produces a canonical glyph.
var segmentsToBoxDrawing = map[uint8]rune{
	LineLeft | LineRight: '─',
	LineUp | LineDown:    '│',

	LineDown | LineRight: '┌',
	LineDown | LineLeft:  '┐',
	LineUp | LineRight:   '└',
	LineUp | LineLeft:    '┘',

	LineUp | LineDown | LineRight:            '├',
	LineUp | LineDown | LineLeft:             '┤',
	LineLeft | LineRight | LineDown:          '┬',
	LineLeft | LineRight | LineUp:            '┴',
	LineUp | LineDown | LineLeft | LineRight: '┼',
}

// BoxDrawingSegments reports which line segments a rune draws, and whether it
// is a light box-drawing rune at all.
func BoxDrawingSegments(r rune) (uint8, bool) {
	segments, ok := boxDrawingSegments[r]
	return segments, ok
}

// MergeBoxDrawing returns the glyph that draws both runes' line segments.
//
// This is what lets adjacent blocks share an edge: where one block's right
// border meets another's left, the cell holds │ and receives │ again, but
// where a horizontal rule arrives the union is ├, ┤ or ┼ rather than one line
// overwriting the other.
//
// A cell grid can do this because the character already in the cell is still
// there to consult. A renderer that concatenates strings has overwritten it.
//
// Returns incoming unchanged when either rune is not a light box-drawing glyph,
// so ordinary text, heavy lines and double lines are never altered.
func MergeBoxDrawing(existing, incoming rune) rune {
	if existing == incoming {
		return incoming
	}
	existingSegments, ok := boxDrawingSegments[existing]
	if !ok {
		return incoming
	}
	incomingSegments, ok := boxDrawingSegments[incoming]
	if !ok {
		return incoming
	}
	merged, ok := segmentsToBoxDrawing[existingSegments|incomingSegments]
	if !ok {
		return incoming
	}
	return merged
}
