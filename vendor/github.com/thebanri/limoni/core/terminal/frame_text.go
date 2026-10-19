package terminal

import "github.com/thebanri/limoni/core/cell"

// Text draws s at column x, row y, cut off at the frame's edges, and
// returns the column after it (whether or not all of it was drawn), so runs
// of differently styled text can follow one another:
//
//	x = f.Text(x, y, "error: ", red)
//	x = f.Text(x, y, msg, plain)
//
// Text is measured and cut by grapheme cluster, as the screen shows it; a
// wide character the left edge would halve is left out. Nothing is
// allocated.
func (f *Frame) Text(x, y int, s string, style cell.Style) int {
	end := x + cell.StringWidth(s) // where it ends, drawn whole or not
	area := f.Buffer.Area
	if y < 0 || y >= int(area.Height) || x >= int(area.Width) {
		return end
	}
	for x < 0 && s != "" {
		_, w, rest := cell.NextCluster(s)
		s, x = rest, x+w
	}
	if x >= 0 && s != "" {
		f.Buffer.SetStringWithin(uint16(x), uint16(y), s, style, area.Width-uint16(x))
	}
	return end
}

// TextFit draws s in exactly w columns at x, y: cut with "…" when it is
// longer, padded with blanks in style when it is shorter, so a column of
// labels lines up and a shorter label wipes out a longer one drawn there
// before. It returns x+w.
func (f *Frame) TextFit(x, y, w int, s string, style cell.Style) int {
	if w <= 0 {
		return x
	}
	end := x + w
	if cell.StringWidth(s) > w {
		cut, _ := cell.Truncate(s, w-1)
		x = f.Text(x, y, cut, style)
		s = "…"
	}
	for x = f.Text(x, y, s, style); x < end; x++ {
		f.Text(x, y, " ", style)
	}
	return end
}
