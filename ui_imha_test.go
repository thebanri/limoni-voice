package main

import (
	"testing"

	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
)

// imhaScreen is a 60×20 screen of text with wide characters in it, as the room would draw.
func imhaScreen() (*terminal.Frame, *buffer.Buffer) {
	area := cell.NewRect(0, 0, 60, 20)
	buf := buffer.NewBuffer(area)
	for y := range uint16(20) {
		buf.SetString(0, y, "limoni 界 voice 界 room limoni voice room limoni voice room x", cell.Style{Bg: cell.NewColorRGB(30, 30, 40)})
	}
	return terminal.NewFrame(buf, terminal.NewFocusManager()), buf
}

func countImha(buf *buffer.Buffer, keep func(cell.Cell) bool) int {
	n := 0
	for y := range uint16(20) {
		for x := range uint16(60) {
			if keep(*buf.Get(x, y)) {
				n++
			}
		}
	}
	return n
}

// The bomb falls, the blast flashes, the screen turns to sand that piles up at the bottom,
// and it all comes back as it was; a wide character is never left in halves. Frames are
// drawn as the room draws them, 30 a second, each over a freshly drawn screen.
func TestImhaPlaysThrough(t *testing.T) {
	_, orig := imhaScreen()
	halfBlock := func(c cell.Cell) bool { return c.Content == '▀' || c.Content == '▄' }
	var play imhaPlay
	checks := map[int]func(*buffer.Buffer) string{
		0: func(b *buffer.Buffer) string {
			if countImha(b, halfBlock) != 0 {
				return "nothing before the bomb shows"
			}
			return ""
		},
		27: func(b *buffer.Buffer) string { // 0.9 s
			if countImha(b, halfBlock) < 20 {
				return "the bomb falling"
			}
			return ""
		},
		96: func(b *buffer.Buffer) string { // 3.2 s: sand piled at the bottom, the top dark
			top, bottom := 0, 0
			for x := range uint16(60) {
				for y := range uint16(5) {
					if c := b.Get(x, y); c.Style.Fg != c.Style.Bg {
						top++
					}
					if c := b.Get(x, 19-y); c.Style.Fg != c.Style.Bg {
						bottom++
					}
				}
			}
			if bottom < 60 || top > bottom/4 {
				return "sand piled at the bottom"
			}
			return ""
		},
	}
	grains := 0
	for frame := 0; float64(frame)/30 < imhaTotal; frame++ {
		at := float64(frame) / 30
		f, buf := imhaScreen()
		play.draw(f, at)
		if check := checks[frame]; check != nil {
			if want := check(buf); want != "" {
				t.Errorf("at %.2fs: want %s", at, want)
			}
		}
		if at >= imhaFall+0.01 && at < imhaFlashEnd && countImha(buf, halfBlock) != 60*20 {
			t.Errorf("at %.2fs: want the flash over the whole screen", at)
		}
		if play.sand != nil {
			if grains == 0 {
				grains = len(play.sand.grains)
			} else if len(play.sand.grains) != grains {
				t.Fatalf("at %.2fs: %d grains, there were %d", at, len(play.sand.grains), grains)
			}
		}
		for y := range uint16(20) {
			for x := uint16(1); x < 60; x++ {
				if buf.Get(x, y).Content == cell.RuneContinuation && cell.RuneWidth(buf.Get(x-1, y).Content) != 2 {
					t.Fatalf("at %.2fs: half a wide character at %d,%d", at, x, y)
				}
			}
		}
	}
	if grains < 200 {
		t.Fatalf("only %d grains of sand from a screen of text", grains)
	}
	f, buf := imhaScreen()
	play.draw(f, imhaTotal-0.01)
	if !sameScreen(buf, orig) {
		t.Error("the screen did not come back as it was")
	}
}

func sameScreen(a, b *buffer.Buffer) bool {
	for y := range uint16(20) {
		for x := range uint16(60) {
			if *a.Get(x, y) != *b.Get(x, y) {
				return false
			}
		}
	}
	return true
}

// Grains settle on the pile as sand does: no column ends more than a grain above both its
// neighbours.
func TestImhaSandPiles(t *testing.T) {
	f, _ := imhaScreen()
	s := newImhaSand(f, f.Area(), imhaFall)
	s.step(imhaRebuild - 0.01)
	settled := 0
	for _, g := range s.grains {
		if g.settled {
			settled++
		}
	}
	if settled < len(s.grains)*9/10 {
		t.Fatalf("%d of %d grains settled before the rebuild", settled, len(s.grains))
	}
	for x := 1; x < s.w-1; x++ {
		if h := s.heights[x]; h > s.heights[x-1]+2 && h > s.heights[x+1]+2 {
			t.Fatalf("column %d stands %d high between %d and %d", x, h, s.heights[x-1], s.heights[x+1])
		}
	}
}
