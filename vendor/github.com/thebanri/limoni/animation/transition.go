package animation

import (
	"github.com/thebanri/limoni/core/buffer"
)

var bayer4x4 = [4][4]float64{
	{0.0 / 16.0, 8.0 / 16.0, 2.0 / 16.0, 10.0 / 16.0},
	{12.0 / 16.0, 4.0 / 16.0, 14.0 / 16.0, 6.0 / 16.0},
	{3.0 / 16.0, 11.0 / 16.0, 1.0 / 16.0, 9.0 / 16.0},
	{15.0 / 16.0, 7.0 / 16.0, 13.0 / 16.0, 5.0 / 16.0},
}

// ApplyDitherFade applies a dither (retro speckle) transition from oldBuf to newBuf, writing into newBuf.
// progress is a value between 0.0 and 1.0.
func ApplyDitherFade(newBuf *buffer.Buffer, oldBuf *buffer.Buffer, progress float64) {
	if oldBuf == nil || newBuf == nil || progress >= 1.0 {
		return
	}
	if progress <= 0.0 {
		// Copy oldBuf over newBuf entirely
		if len(newBuf.Content) == len(oldBuf.Content) {
			newBuf.Invalidate()
			copy(newBuf.Content, oldBuf.Content)
		}
		return
	}

	w := newBuf.Area.Width
	h := newBuf.Area.Height

	for y := uint16(0); y < h; y++ {
		// Rows containing text or border characters do not transition cell by
		// cell. That way a word never becomes unreadable from mixing characters
		// of the old and the new frame.
		textRow := transitionRowHasGlyph(oldBuf, newBuf, y, w)
		rowThreshold := (float64(y) + 0.5) / float64(h)

		for x := uint16(0); x < w; x++ {
			threshold := bayer4x4[y%4][x%4]
			if textRow {
				threshold = rowThreshold
			}
			if progress < threshold {
				// Copy the oldBuf cell into newBuf
				oldCell := oldBuf.Get(x+oldBuf.Area.X, y+oldBuf.Area.Y)
				newCell := newBuf.Get(x+newBuf.Area.X, y+newBuf.Area.Y)
				if oldCell != nil && newCell != nil {
					*newCell = *oldCell
				}
			}
		}
	}
}

// transitionRowHasGlyph reports whether a row contains a character or border.
// Blank rows and rows of coloured graphics keep the Bayer dither; text
// rows transition as a whole.
func transitionRowHasGlyph(oldBuf, newBuf *buffer.Buffer, y, width uint16) bool {
	for x := uint16(0); x < width; x++ {
		oldCell := oldBuf.Get(x+oldBuf.Area.X, y+oldBuf.Area.Y)
		newCell := newBuf.Get(x+newBuf.Area.X, y+newBuf.Area.Y)
		if (oldCell != nil && oldCell.Content != ' ' && oldCell.Content != 0) ||
			(newCell != nil && newCell.Content != ' ' && newCell.Content != 0) {
			return true
		}
	}
	return false
}
