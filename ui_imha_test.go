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

// The bomb falls, the blast flashes, the screen empties and comes back whole, and a wide
// character is never left in halves.
func TestImhaPlaysThrough(t *testing.T) {
	_, orig := imhaScreen()
	halfBlock := func(c cell.Cell) bool { return c.Content == '▀' || c.Content == '▄' }
	for _, c := range []struct {
		at    float64
		check func(*buffer.Buffer) bool
		what  string
	}{
		{0, func(b *buffer.Buffer) bool { return countImha(b, halfBlock) == 0 }, "nothing before the bomb shows"},
		{0.9, func(b *buffer.Buffer) bool { return countImha(b, halfBlock) > 20 }, "the bomb falling"},
		{1.15, func(b *buffer.Buffer) bool { return countImha(b, halfBlock) == 60*20 }, "the flash over the whole screen"},
		{3.25, func(b *buffer.Buffer) bool {
			return countImha(b, func(c cell.Cell) bool { return c.Content != ' ' }) < 60
		}, "the screen fallen away"},
		{imhaTotal - 0.01, func(b *buffer.Buffer) bool { return sameScreen(b, orig) }, "the screen back as it was"},
	} {
		f, buf := imhaScreen()
		drawImhaAt(f, c.at)
		if !c.check(buf) {
			t.Errorf("at %.2fs: want %s", c.at, c.what)
		}
		for y := range uint16(20) {
			for x := uint16(1); x < 60; x++ {
				if buf.Get(x, y).Content == cell.RuneContinuation && cell.RuneWidth(buf.Get(x-1, y).Content) != 2 {
					t.Fatalf("at %.2fs: half a wide character at %d,%d", c.at, x, y)
				}
			}
		}
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

// Every cell falls off the screen before any climbs back, and each is back in place at the end.
func TestImhaOffsets(t *testing.T) {
	for y := range 20 {
		for x := range 60 {
			if o := imhaOffset(x, y, 60, 20, 0); o != 0 {
				t.Fatalf("%d,%d moved %d before the blast", x, y, o)
			}
			if o := imhaOffset(x, y, 60, 20, imhaRebuild-0.01); y+o < 20 {
				t.Fatalf("%d,%d still on screen (row %d) when the rebuild starts", x, y, y+o)
			}
			if o := imhaOffset(x, y, 60, 20, imhaTotal-0.01); o != 0 {
				t.Fatalf("%d,%d is %d rows off at the end", x, y, o)
			}
		}
	}
}
