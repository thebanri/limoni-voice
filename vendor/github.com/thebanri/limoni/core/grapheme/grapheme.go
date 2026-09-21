// Package grapheme segments text into extended grapheme clusters (UAX #29)
// and measures them in terminal cells.
//
// A grapheme cluster is what a reader sees as one character: "é" written as e
// plus a combining accent, a flag made of two regional indicators, a family
// emoji joined from five code points with zero-width joiners. Walking runes
// splits these apart, which is how a terminal UI ends up drawing a flag as two
// letters or measuring 👨‍👩‍👧 as six columns and misaligning everything after it.
//
// Segmentation follows the Unicode rules exactly and is checked against the
// standard's own GraphemeBreakTest.txt. Width follows what modern terminals do
// when they render clusters as units (mode 2027): the widest code point in the
// cluster, with VS16 forcing emoji presentation to two columns and VS15 forcing
// text presentation to one.
//
// Nothing here allocates.
package grapheme

//go:generate go run gen.go -version 17.0.0

import "unicode/utf8"

type propRange struct {
	lo, hi rune
	props  uint16
}

// Property word layout, produced by gen.go.
const (
	maskGCB      = 0x000F
	bitExtPict   = 1 << 4
	shiftInCB    = 5
	maskInCB     = 3 << shiftInCB
	bitEmojiPres = 1 << 7
	bitEmoji     = 1 << 8
	shiftWidth   = 9
	maskWidth    = 3 << shiftWidth
)

// Grapheme_Cluster_Break values.
const (
	gcbOther = iota
	gcbCR
	gcbLF
	gcbControl
	gcbExtend
	gcbZWJ
	gcbRI
	gcbPrepend
	gcbSpacingMark
	gcbL
	gcbV
	gcbT
	gcbLV
	gcbLVT
)

// Indic_Conjunct_Break values.
const (
	incbNone = iota
	incbConsonant
	incbExtend
	incbLinker
)

// Width classes.
const (
	widthOne  = 0
	widthZero = 1
	widthWide = 2
)

const (
	variationText  = 0xFE0E // VS15
	variationEmoji = 0xFE0F // VS16
)

// bmpProps and pictProps answer lookups directly for the Basic Multilingual
// Plane and for U+1F000–U+1FFFF, where nearly all emoji live. Together they
// cover almost every code point a terminal UI draws — box drawing, symbols,
// CJK, emoji — in one indexed read instead of a binary search, for 136 KB of
// static tables. They are filled from propTable, so they cannot disagree
// with it.
var (
	bmpProps  [0x10000]uint16
	pictProps [0x1000]uint16
)

const pictBase = 0x1F000

func init() {
	for _, span := range propTable {
		for r := span.lo; r <= span.hi; r++ {
			switch {
			case r < 0x10000:
				bmpProps[r] = span.props
			case r >= pictBase && r < pictBase+rune(len(pictProps)):
				pictProps[r-pictBase] = span.props
			}
		}
	}
}

func lookup(r rune) uint16 {
	if uint32(r) < 0x10000 {
		return bmpProps[r]
	}
	if uint32(r-pictBase) < uint32(len(pictProps)) {
		return pictProps[r-pictBase]
	}
	return lookupSlow(r)
}

func lookupSlow(r rune) uint16 {
	// A hand-written binary search: sort.Search's closure is not inlined.
	lo, hi := 0, len(propTable)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if propTable[mid].hi < r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(propTable) && propTable[lo].lo <= r {
		return propTable[lo].props
	}
	return 0
}

func widthOf(props uint16) int {
	switch (props & maskWidth) >> shiftWidth {
	case widthZero:
		return 0
	case widthWide:
		return 2
	default:
		return 1
	}
}

// RuneWidth returns the width of a single code point in cells: 0, 1 or 2.
func RuneWidth(r rune) int {
	if r >= 0x20 && r < 0x7F {
		return 1
	}
	if r < 0 || r > utf8.MaxRune {
		return 0
	}
	return widthOf(lookup(r))
}

// Next returns the first grapheme cluster of s, its width in cells, and the
// remainder of s. It returns empty strings and zero width when s is empty.
func Next(s string) (cluster string, width int, rest string) {
	if s == "" {
		return "", 0, ""
	}

	// ASCII followed by ASCII always breaks (GB999), apart from CR LF (GB3).
	// This covers the overwhelming majority of terminal text without a lookup.
	if c := s[0]; c < utf8.RuneSelf && (len(s) == 1 || s[1] < utf8.RuneSelf) {
		if c == '\r' && len(s) > 1 && s[1] == '\n' {
			return s[:2], 0, s[2:]
		}
		if c < 0x20 || c == 0x7F {
			return s[:1], 0, s[1:]
		}
		return s[:1], 1, s[1:]
	}

	r, size := utf8.DecodeRuneInString(s)
	p := lookup(r)
	firstEmoji := p&bitEmoji != 0
	width = widthOf(p)

	var (
		prev = p & maskGCB
		// pictRun is true while the cluster so far ends in
		// Extended_Pictographic Extend*, the prefix GB11 needs before a ZWJ.
		pictRun = p&bitExtPict != 0
		// zwjAfterPict is true when the previous code point is a ZWJ that
		// followed such a prefix.
		zwjAfterPict bool
		// riCount counts consecutive regional indicators ending at prev.
		riCount int
		// conjunct tracks GB9c: 1 after a consonant followed only by
		// extenders, 2 once a linker has been seen in that run.
		conjunct int
		// variation overrides the width when VS15 or VS16 appears.
		variation int
	)
	if prev == gcbRI {
		riCount = 1
	}
	if (p&maskInCB)>>shiftInCB == incbConsonant {
		conjunct = 1
	}

	i := size

	// An ASCII byte next always starts a new cluster: ASCII holds no
	// Extend, ZWJ, SpacingMark or Extended_Pictographic code point, so the
	// only rules that could join it are GB3 (CR LF, but CR is ASCII and
	// handled above) and GB9b (after Prepend). This is the common shape of
	// non-ASCII text in a UI — a symbol or an accented letter between ASCII
	// words — and it skips the state machine.
	if i < len(s) && s[i] < utf8.RuneSelf && prev != gcbPrepend {
		return s[:i], width, s[i:]
	}

	for i < len(s) {
		r, size = utf8.DecodeRuneInString(s[i:])
		q := lookup(r)
		next := q & maskGCB
		nextInCB := (q & maskInCB) >> shiftInCB

		if breaks(prev, next, q, zwjAfterPict, riCount, conjunct, nextInCB) {
			break
		}

		// Extend the cluster and advance the state.
		if w := widthOf(q); w > width {
			width = w
		}
		switch r {
		case variationEmoji:
			variation = 2
		case variationText:
			variation = 1
		}

		zwjAfterPict = next == gcbZWJ && pictRun
		switch {
		case q&bitExtPict != 0:
			pictRun = true
		case next == gcbExtend:
			// pictRun carries across Extend.
		default:
			pictRun = false
		}

		if next == gcbRI {
			riCount++
		} else {
			riCount = 0
		}

		switch nextInCB {
		case incbConsonant:
			conjunct = 1
		case incbLinker:
			if conjunct > 0 {
				conjunct = 2
			}
		case incbExtend:
			// Extenders keep the conjunct state.
		default:
			conjunct = 0
		}

		prev = next
		i += size
	}

	if firstEmoji && variation != 0 {
		width = variation
	}
	return s[:i], width, s[i:]
}

// breaks reports whether there is a cluster boundary between a code point
// with Grapheme_Cluster_Break value prev and the next code point.
func breaks(prev, next uint16, nextProps uint16, zwjAfterPict bool, riCount, conjunct int, nextInCB uint16) bool {
	switch {
	case prev == gcbCR && next == gcbLF: // GB3
		return false
	case prev == gcbControl || prev == gcbCR || prev == gcbLF: // GB4
		return true
	case next == gcbControl || next == gcbCR || next == gcbLF: // GB5
		return true
	case prev == gcbL && (next == gcbL || next == gcbV || next == gcbLV || next == gcbLVT): // GB6
		return false
	case (prev == gcbLV || prev == gcbV) && (next == gcbV || next == gcbT): // GB7
		return false
	case (prev == gcbLVT || prev == gcbT) && next == gcbT: // GB8
		return false
	case next == gcbExtend || next == gcbZWJ: // GB9
		return false
	case next == gcbSpacingMark: // GB9a
		return false
	case prev == gcbPrepend: // GB9b
		return false
	case nextInCB == incbConsonant && conjunct == 2: // GB9c
		return false
	case prev == gcbZWJ && zwjAfterPict && nextProps&bitExtPict != 0: // GB11
		return false
	case prev == gcbRI && next == gcbRI && riCount%2 == 1: // GB12, GB13
		return false
	}
	return true // GB999
}

// StringWidth returns the width of s in cells, measuring each grapheme cluster
// as a unit.
func StringWidth(s string) int {
	total := 0
	for s != "" {
		var w int
		_, w, s = Next(s)
		total += w
	}
	return total
}

// Count returns the number of grapheme clusters in s.
func Count(s string) int {
	n := 0
	for s != "" {
		_, _, s = Next(s)
		n++
	}
	return n
}
