package driver

import (
	"strings"
	"sync/atomic"
)

// Mouse reporting: any-motion tracking and SGR coordinates, as the setup
// sequences ask for it. Off clears every tracking mode, whichever was set.
const (
	mouseOnSeq  = "\x1b[?1003h\x1b[?1006h"
	mouseOffSeq = "\x1b[?1006l\x1b[?1003l\x1b[?1002l\x1b[?1000l"
)

// MouseMode is how much of the mouse the terminal reports while the
// application takes it.
type MouseMode uint8

const (
	// MouseMotion reports clicks, the wheel, drags and every pointer move
	// (DECSET 1003). It is the default; hover and pointer shapes need it.
	MouseMotion MouseMode = iota
	// MouseDrag reports clicks, the wheel and moves with a button held (1002).
	MouseDrag
	// MouseClicks reports clicks and the wheel (1000).
	MouseClicks
)

func (m MouseMode) onSeq() string {
	switch m {
	case MouseDrag:
		return "\x1b[?1002h\x1b[?1006h"
	case MouseClicks:
		return "\x1b[?1000h\x1b[?1006h"
	}
	return mouseOnSeq
}

// mouseCapture is whether the application takes the mouse — on unless it
// said otherwise — in which mode, and whether the setup sequence has gone
// out, after which a change is written at once.
type mouseCapture struct {
	off    atomic.Bool
	mode   atomic.Uint32
	active atomic.Bool
}

func (m *mouseCapture) enabled() bool { return !m.off.Load() }

func (m *mouseCapture) onSeq() string { return MouseMode(m.mode.Load()).onSeq() }

// setupSequence is the setup for a full-screen or inline frame, with mouse
// reporting in the application's mode, or none if it does not take the mouse.
func setupSequence(inline uint16, mouse *mouseCapture) string {
	seq := fullScreenSetupCmds()
	if inline > 0 {
		seq = inlineSetupCmds(inline)
	}
	on := ""
	if mouse.enabled() {
		on = mouse.onSeq()
	}
	return strings.Replace(seq, mouseOnSeq, on, 1)
}

// SetMouse says whether the application takes the mouse: clicks, the wheel
// and pointer movement reported to it. It is on by default. Off, the
// terminal keeps the mouse for itself, so text can be selected without
// holding Shift, and most terminals turn the wheel into arrow keys on the
// alternate screen.
//
// Called before Setup it decides what Setup sends; afterwards the change is
// written to the terminal at once. Either way it holds through a Suspend or
// a Release.
func (b *Backend) SetMouse(enabled bool) error {
	if b.mouse.off.Swap(!enabled) == !enabled {
		return nil // no change
	}
	if !b.mouse.active.Load() {
		return nil
	}
	seq := mouseOffSeq
	if enabled {
		seq = b.mouse.onSeq()
	}
	_, err := b.Write([]byte(seq))
	return err
}

// MouseEnabled reports whether the application takes the mouse.
func (b *Backend) MouseEnabled() bool { return b.mouse.enabled() }

// SetMouseMode says how much of the mouse is reported: every move
// (MouseMotion, the default), moves with a button held (MouseDrag), or
// clicks and the wheel alone (MouseClicks). Fewer reports are less input to
// read, and less noise in the shell if a crash leaves reporting on.
//
// Like SetMouse it decides what Setup sends, or is written at once after it,
// and holds through a Suspend or a Release. With the mouse off it only
// records the mode for when it is turned on.
func (b *Backend) SetMouseMode(mode MouseMode) error {
	if MouseMode(b.mouse.mode.Swap(uint32(mode))) == mode {
		return nil // no change
	}
	if !b.mouse.active.Load() || !b.mouse.enabled() {
		return nil
	}
	_, err := b.Write([]byte(mouseOffSeq + mode.onSeq()))
	return err
}

// MouseMode reports how much of the mouse is reported.
func (b *Backend) MouseMode() MouseMode { return MouseMode(b.mouse.mode.Load()) }
