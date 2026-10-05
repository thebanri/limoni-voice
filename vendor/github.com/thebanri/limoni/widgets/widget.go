package widgets

import (
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
)

// Widget is the basic interface of stateless TUI components that can draw themselves.
// Every visual component in Limoni (Block, Paragraph, Table and so on) implements it.
type Widget interface {
	// Draw draws the component into the terminal buffer (buffer.Buffer), within the area it was
	// given and with the style it inherited.
	//
	// Parameters:
	//   - ctx: the stack-allocated drawing context passed down from the parent (area and inherited style).
	//   - buf: the terminal cell buffer to draw into.
	Draw(ctx cell.Context, buf *buffer.Buffer)

	// SizeHint returns the widget's preferred (width, height) within the given maximum
	// bounds (maxArea). The layout negotiation engine uses it to distribute
	// space among flexible boxes.
	//
	// Parameters:
	//   - maxArea: the largest area the parent can give this widget.
	SizeHint(maxArea cell.Rect) (width, height uint16)
}

// DrawFocusRing draws a thick, dashed, 1-cell focus ring (a bright frame) around the area.
func DrawFocusRing(buf *buffer.Buffer, area cell.Rect, style cell.Style) {
	if area.Width < 2 || area.Height < 2 {
		return
	}
	// Horizontal dashed lines
	for col := area.X + 1; col < area.X+area.Width-1; col++ {
		if c := buf.Get(col, area.Y); c != nil {
			c.Content = '╍'
			c.Style = style
		}
		if c := buf.Get(col, area.Y+area.Height-1); c != nil {
			c.Content = '╍'
			c.Style = style
		}
	}
	// Vertical dashed lines
	for row := area.Y + 1; row < area.Y+area.Height-1; row++ {
		if c := buf.Get(area.X, row); c != nil {
			c.Content = '╏'
			c.Style = style
		}
		if c := buf.Get(area.X+area.Width-1, row); c != nil {
			c.Content = '╏'
			c.Style = style
		}
	}
	// Corner joins (thick corners)
	if c := buf.Get(area.X, area.Y); c != nil {
		c.Content = '┏'
		c.Style = style
	}
	if c := buf.Get(area.X+area.Width-1, area.Y); c != nil {
		c.Content = '┓'
		c.Style = style
	}
	if c := buf.Get(area.X, area.Y+area.Height-1); c != nil {
		c.Content = '┗'
		c.Style = style
	}
	if c := buf.Get(area.X+area.Width-1, area.Y+area.Height-1); c != nil {
		c.Content = '┛'
		c.Style = style
	}
}
