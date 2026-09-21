package grapheme

import (
	"os"
	"sync/atomic"
)

// codePoints switches segmentation off, back to one code point per cell. It
// lives in this leaf package, rather than beside the cell table, so the terminal
// driver can read it without an import cycle: it decides whether to ask the
// terminal for mode 2027.
var codePoints atomic.Bool

func init() {
	if os.Getenv("LIMONI_GRAPHEME") == "0" {
		codePoints.Store(true)
	}
}

// SetClusters turns grapheme-cluster segmentation on (the default) or off.
//
// Terminals that render clusters as units — and those supporting mode 2027 —
// measure a flag or a family emoji as one wide glyph, matching this package.
// Older terminals measure each code point with wcwidth and may draw such a
// sequence wider; the diff re-anchors the cursor after every cluster, so only
// the cluster itself looks wrong there. Turning clusters off, or setting
// LIMONI_GRAPHEME=0, makes the layout match those terminals instead.
func SetClusters(enabled bool) { codePoints.Store(!enabled) }

// Clusters reports whether grapheme-cluster segmentation is on.
func Clusters() bool { return !codePoints.Load() }

// ModeSequences returns the DECSET and DECRST sequences for mode 2027, which
// tells a terminal that supports it to measure grapheme clusters as units —
// the way this package does. A terminal that does not recognise the mode
// ignores it. In code-point mode both are empty, so the terminal is left
// measuring code points, which is what the layout then assumes.
func ModeSequences() (set, reset string) {
	if codePoints.Load() {
		return "", ""
	}
	return "\x1b[?2027h", "\x1b[?2027l"
}
