// Wrapping rows of clickable labels, used where a fixed row of buttons would
// fall off the edge of a narrow terminal.

package main

import (
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
)

// flowItem is one clickable label in a flow of controls.
type flowItem struct {
	label    string
	short    string // used instead of label when the full labels need too many rows
	style    cell.Style
	onClick  func(driver.MouseEvent)
	zones    []flowZone // split click areas; used instead of onClick when set
	pinRight bool       // push to the right edge of its row
}

// flowZone is a click area covering part of a flowItem's label.
type flowZone struct {
	off, width uint16
	onClick    func(driver.MouseEvent)
}

// flowLayout is the result of packing flow items into rows.
type flowLayout struct {
	rows  [][]int // item indices per row
	short bool    // the short labels are in use
}

func (it flowItem) text(short bool) string {
	if short && it.short != "" {
		return it.short
	}
	return it.label
}

// layoutFlow packs items into rows no wider than width, gap columns apart. When the full
// labels need more than maxRows rows it switches to the short ones.
func layoutFlow(items []flowItem, width, gap, maxRows int) flowLayout {
	pack := func(short bool) [][]int {
		var rows [][]int
		var row []int
		used := 0
		for i, it := range items {
			w := cell.StringWidth(it.text(short))
			if len(row) > 0 && used+gap+w > width {
				rows = append(rows, row)
				row, used = nil, 0
			}
			if len(row) > 0 {
				used += gap
			}
			row = append(row, i)
			used += w
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
		return rows
	}
	rows := pack(false)
	if len(rows) <= maxRows {
		return flowLayout{rows: rows}
	}
	return flowLayout{rows: pack(true), short: true}
}

// flowSpacing is the number of blank lines between flow rows that fit in height.
func flowSpacing(rows, height int) int {
	if rows > 1 && 2*rows-1 <= height {
		return 1
	}
	return 0
}

// drawFlow draws a packed flow into area and registers the click handlers. Rows that do not
// fit in area are dropped; labels are clipped at its right edge.
func drawFlow(frame *terminal.Frame, area cell.Rect, items []flowItem, fl flowLayout, gap int) {
	spacing := flowSpacing(len(fl.rows), int(area.Height))
	right := int(area.X) + int(area.Width)
	for i, row := range fl.rows {
		y := int(area.Y) + i*(1+spacing)
		if y >= int(area.Y)+int(area.Height) {
			return
		}
		x := int(area.X)
		for n, idx := range row {
			it := items[idx]
			label := it.text(fl.short)
			w := cell.StringWidth(label)
			if it.pinRight && n == len(row)-1 && right-w > x {
				x = right - w
			}
			if x >= right {
				break
			}
			if x+w > right {
				label = clipToWidth(label, right-x)
				w = cell.StringWidth(label)
			}
			frame.Buffer.SetString(uint16(x), uint16(y), label, it.style)
			if len(it.zones) > 0 {
				for _, z := range it.zones {
					if zw := min(int(z.width), w-int(z.off)); zw > 0 {
						frame.RegisterClickHandler(cell.NewRect(uint16(x)+z.off, uint16(y), uint16(zw), 1), z.onClick)
					}
				}
			} else if it.onClick != nil {
				frame.RegisterClickHandler(cell.NewRect(uint16(x), uint16(y), uint16(w), 1), it.onClick)
			}
			x += w + gap
		}
	}
}

// clipToWidth cuts s to at most width columns, ending in an ellipsis when it had to cut.
func clipToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if cell.StringWidth(s) <= width {
		return s
	}
	out := make([]rune, 0, width)
	used := 0
	for _, r := range s {
		w := cell.RuneWidth(r)
		if used+w > width-1 {
			break
		}
		out = append(out, r)
		used += w
	}
	return string(out) + "…"
}

// clipScratch holds drawClipped's scratch buffers, one per nesting level, reused between
// frames. Drawing happens on the UI goroutine only.
var (
	clipScratch []*buffer.Buffer
	clipDepth   int
)

// drawClipped runs draw and keeps only what it puts inside area: cells drawn outside are
// discarded and click regions are cut to area. It is for views whose rows are laid out for
// more height than they may be given.
func drawClipped(frame *terminal.Frame, area cell.Rect, draw func()) {
	screen := frame.Buffer
	if len(clipScratch) <= clipDepth {
		clipScratch = append(clipScratch, buffer.NewEmptyBuffer())
	}
	scratch := clipScratch[clipDepth]
	if scratch.Area != screen.Area || len(scratch.Content) != len(screen.Content) {
		scratch.Resize(screen.Area)
	}
	copy(scratch.Content, screen.Content)
	firstRegion := len(frame.ClickRegions)

	clipDepth++
	frame.Buffer = scratch
	draw()
	frame.Buffer = screen
	clipDepth--

	for y := area.Y; y < area.Y+area.Height; y++ {
		for x := area.X; x < area.X+area.Width; x++ {
			screen.SetCellDirect(x, y, scratch.CellAt(x, y))
		}
	}
	kept := frame.ClickRegions[:firstRegion]
	for _, reg := range frame.ClickRegions[firstRegion:] {
		if a := reg.Area.Intersection(area); a.Width > 0 && a.Height > 0 {
			reg.Area = a
			kept = append(kept, reg)
		}
	}
	frame.ClickRegions = kept
}
