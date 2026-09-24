// The hand pointer over what can be clicked, in the terminals that can change the mouse
// pointer (kitty, foot, Ghostty; LIMONI_POINTER overrides). Others ignore the request.

package main

import (
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
)

// pointerArea draws nothing; it asks for the hand pointer over its area. Limoni takes the
// request only from a widget, and a pointer-only action does not take clicks.
type pointerArea struct{}

func (pointerArea) Draw(ctx cell.Context, _ *buffer.Buffer) {
	if ctx.RegisterClickAction != nil {
		ctx.RegisterClickAction(ctx.Area, cell.ClickAction{Pointer: "pointer"})
	}
}

func (pointerArea) SizeHint(maxArea cell.Rect) (uint16, uint16) { return maxArea.Width, maxArea.Height }

// noPointers is set while a view is drawn at positions other than where it will show,
// since pointer regions cannot be moved afterwards the way click regions can.
var noPointers bool

// clickable makes area a click target and shows the hand pointer over it.
func clickable(frame *terminal.Frame, area cell.Rect, handler func(driver.MouseEvent)) {
	frame.RegisterClickHandler(area, handler)
	if !noPointers && area.Width > 0 && area.Height > 0 {
		frame.RenderWidget(pointerArea{}, area)
	}
}
