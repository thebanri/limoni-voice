package terminal

import (
	"os"
	"time"

	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
)

// Backdrop is an animated scene drawn behind everything the application
// draws. It works in every terminal that has 256 colours or more: the scene is
// made of ordinary cells, so there is no image protocol to support.
//
// The application's cells are laid over the scene by one rule: a cell whose
// background is the terminal default is transparent. A blank cell takes the
// scene's cell whole, glyph and all; a cell with text keeps its glyph and
// foreground and takes the scene's background colour. A cell with a
// background colour of its own, a reversed cell and an image cell hide the
// scene. So a widget that wants a solid panel paints a background colour, and
// everything else floats over the scene without being changed.
//
// The terminal never learns about the scene either: what reaches it is the
// same cell diff as always, so a scene costs only the cells it changes from
// one frame to the next. A scene that changes every cell every frame costs a
// full repaint every frame. The scenes in package backdrop are built so it
// does not come to that.
type Backdrop interface {
	// Render paints the scene at time t, measured from when the backdrop
	// was set, into dst, which covers the whole screen. Every cell must be
	// painted: dst still holds the previous frame. Glyphs must be one column
	// wide. Render runs once per frame and must not allocate once dst has
	// kept its size.
	Render(dst *buffer.Buffer, t time.Duration)

	// Interval is how often the scene changes. The run loops draw it at this
	// rate on their own, without calling the application, when the
	// application does not already draw faster. Zero means the scene is
	// still and is only drawn along with the application.
	Interval() time.Duration
}

// SetBackdrop draws bg behind everything the application draws, from the
// next frame on; nil removes it. The scene's clock starts now.
//
// A backdrop is not drawn in a 16-colour terminal, where a gradient turns
// into blocks of eight colours, nor when the user set LIMONI_BACKDROP=off
// — for motion sensitivity, a slow link, or taste. The application does not
// need to handle either case.
func (t *Terminal) SetBackdrop(bg Backdrop) {
	t.backdrop = bg
	t.bgStart = t.now()
	if bg == nil {
		t.appLayer, t.bgLayer = nil, nil
	}
}

// Backdrop returns the backdrop set with SetBackdrop, or nil.
func (t *Terminal) Backdrop() Backdrop { return t.backdrop }

// BackdropInterval is how often the backdrop needs a frame: its Interval
// when one is set and drawn, zero otherwise.
func (t *Terminal) BackdropInterval() time.Duration {
	if !t.backdropActive() {
		return 0
	}
	return t.backdrop.Interval()
}

// DrawBackdrop draws the next frame of the backdrop under the frame the
// application drew last, without calling the application. The run loops call
// it between application frames; a scene can move at 30 frames a second
// while the application is only drawn when something happens to it.
//
// Nothing is drawn before the application's first frame, or after the window
// changed size: the application has to lay itself out again first, and the
// resize event that is on its way will have it do so.
func (t *Terminal) DrawBackdrop() error {
	if !t.backdropActive() || t.appLayer == nil || t.appLayer.Area != t.front.Area {
		return nil
	}
	t0 := time.Now()
	t.refreshCapabilities()
	if w, h, err := t.driver.Size(); err == nil && w != 0 && h != 0 {
		if t.inline > 0 {
			h = t.inline
		}
		if w != t.front.Area.Width || h != t.front.Area.Height {
			return nil
		}
	}
	copy(t.front.Content, t.appLayer.Content)
	t.front.Invalidate()
	t.composeBackdrop()
	return t.present(t0)
}

// backdropActive reports whether a backdrop is set and can be drawn here.
func (t *Terminal) backdropActive() bool {
	return t.backdrop != nil && !t.bgOff && (t.caps.TrueColor || t.caps.Colors256)
}

// keepAppLayer copies the cells the application drew, so DrawBackdrop can
// lay them over a later frame of the scene without asking for them again.
func (t *Terminal) keepAppLayer() {
	if t.appLayer == nil {
		t.appLayer = buffer.NewEmptyBuffer()
	}
	if t.appLayer.Area != t.front.Area {
		t.appLayer.Resize(t.front.Area)
	}
	copy(t.appLayer.Content, t.front.Content)
}

// composeBackdrop renders the scene for now and lays the front buffer, which
// holds the application's cells, over it.
func (t *Terminal) composeBackdrop() {
	if t.bgLayer == nil {
		t.bgLayer = buffer.NewEmptyBuffer()
	}
	if t.bgLayer.Area != t.front.Area {
		t.bgLayer.Resize(t.front.Area)
	}
	t.backdrop.Render(t.bgLayer, t.now().Sub(t.bgStart))
	ComposeBackdrop(t.front.Content, t.bgLayer.Content)
	t.front.Invalidate()
}

// ComposeBackdrop lays the application's cells in dst over the scene in bg,
// cell for cell, by the rule described on Backdrop. The two slices must be
// the same length.
func ComposeBackdrop(dst, bg []cell.Cell) {
	bg = bg[:len(dst)]
	for i := range dst {
		c := &dst[i]
		if c.Style.Bg.Type() != cell.ColorDefault || c.Style.Modifier&cell.ModifierReverse != 0 || c.Content == cell.RuneImage {
			continue
		}
		if c.Content == ' ' && c.Style == (cell.Style{}) {
			*c = bg[i]
			continue
		}
		c.Style.Bg = bg[i].Style.Bg
	}
}

// now is the backdrop's clock; tests replace it through bgClock.
func (t *Terminal) now() time.Time {
	if t.bgClock != nil {
		return t.bgClock()
	}
	return time.Now()
}

// backdropOff reports whether the user turned backdrops off.
func backdropOff() bool {
	switch os.Getenv("LIMONI_BACKDROP") {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// BackdropTicker paces a run loop's backdrop frames. C returns the
// channel to wait on for the next one, or nil when there is nothing to pace:
// no backdrop, a still one, or an application that already draws at least
// as often as the scene changes.
//
// C is called on every turn of the loop, so SetBackdrop can be called from
// the application at any time and the pace follows it.
type BackdropTicker struct {
	ticker   *time.Ticker
	interval time.Duration
}

// C returns the channel for term's backdrop, given how often the
// application draws on its own (zero if it only draws on events).
func (b *BackdropTicker) C(term *Terminal, appInterval time.Duration) <-chan time.Time {
	iv := term.BackdropInterval()
	if iv <= 0 || (appInterval > 0 && appInterval <= iv) {
		b.Stop()
		return nil
	}
	if b.ticker == nil {
		b.ticker = time.NewTicker(iv)
		b.interval = iv
	} else if b.interval != iv {
		b.ticker.Reset(iv)
		b.interval = iv
	}
	return b.ticker.C
}

// Stop releases the ticker.
func (b *BackdropTicker) Stop() {
	if b.ticker != nil {
		b.ticker.Stop()
		b.ticker = nil
		b.interval = 0
	}
}
