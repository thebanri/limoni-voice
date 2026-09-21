package driver

import (
	"strconv"
	"strings"

	"github.com/thebanri/limoni/core/grapheme"
)

// inlineReserve makes room for an inline frame and parks the cursor on its
// first row.
//
// Printing the newlines first is what forces the terminal to scroll when the
// cursor is near the bottom, so the rows are guaranteed to exist before
// anything moves back up into them.
//
// Duplicated from core/buffer rather than imported: buffer already depends on
// this package, and a few bytes of escape sequence is cheaper than an import
// cycle. core/buffer has the same pair, with tests.
func inlineReserve(height uint16) string {
	if height == 0 {
		return ""
	}
	return strings.Repeat("\n", int(height)) + "\x1b[" + strconv.Itoa(int(height)) + "A\r"
}

// inlineRelease moves the cursor past the frame so a shell prompt lands below
// it rather than on top of it.
func inlineRelease(height uint16) string {
	if height == 0 {
		return "\r"
	}
	return "\x1b[" + strconv.Itoa(int(height)) + "B\r"
}

// inlineSetupCmds returns the terminal setup sequence for an inline frame of
// the given height.
//
// Inline mode keeps the normal screen buffer and leaves auto-wrap alone: the
// frame lives among the user's scrollback rather than replacing it.
func inlineSetupCmds(height uint16) string {
	set, _ := grapheme.ModeSequences()
	return "\x1b[?25l\x1b[?1003h\x1b[?1006h\x1b[?1004h\x1b[?2004h" + set + inlineReserve(height)
}

// inlineRestoreCmds returns the teardown sequence, which leaves what was drawn
// on screen instead of discarding it with the alternate screen.
func inlineRestoreCmds(height uint16) string {
	_, reset := grapheme.ModeSequences()
	return "\x1b[0m\x1b[?2004l\x1b[?1004l\x1b[?1006l\x1b[?1003l\x1b[?25h" + reset +
		inlineRelease(height) + "\n"
}

// fullScreenSetupCmds is the setup shared by every backend in full-screen
// mode: alternate screen, hidden cursor, SGR mouse, focus events, bracketed
// paste, no auto-wrap, and mode 2027 when grapheme clusters are on.
func fullScreenSetupCmds() string {
	set, _ := grapheme.ModeSequences()
	return "\x1b[?1049h\x1b[?25l\x1b[?1003h\x1b[?1006h\x1b[?1004h\x1b[?2004h\x1b[?7l" + set
}

// fullScreenRestoreCmds undoes fullScreenSetupCmds.
func fullScreenRestoreCmds() string {
	_, reset := grapheme.ModeSequences()
	return "\x1b[0m\x1b[?7h\x1b[?2004l\x1b[?1004l\x1b[?1006l\x1b[?1003l\x1b[?25h" + reset + "\x1b[?1049l"
}
