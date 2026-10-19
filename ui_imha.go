// The /imha easter egg: a pixel-art bomb falls onto the screen and explodes, the screen
// crumbles off the bottom, then builds itself back up column by column. It plays on this
// screen alone; nothing is sent to the room.

package main

import (
	"math"
	"time"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
)

// The timeline, in seconds from the command.
const (
	imhaFall      = 1.1  // the bomb falls to the middle of the screen
	imhaBlastEnd  = 2.05 // the explosion has burnt out
	imhaCrumble   = 1.35 // the first cells, those nearest the blast, start to fall
	imhaRebuild   = 3.3  // the first column starts to climb back
	imhaTotal     = 4.75
	imhaFlashEnd  = 1.22
	imhaShakeEnd  = 1.7
	imhaRiseTime  = 0.7 // one cell's climb back
	imhaGravity   = 3.0 // screen heights a second squared
	imhaFallDelay = 0.9 // how much later the cells farthest from the blast fall
)

// imhaBomb is the bomb, 12 pixels wide: k body, h highlight, g cap, f fuse, s the spark.
var imhaBomb = []string{
	"..........s.",
	".........f..",
	"........f...",
	"......ggf...",
	"....kkkkkk..",
	"...kkkkkkkk.",
	"..khhkkkkkkk",
	"..khkkkkkkkk",
	"..kkkkkkkkkk",
	"..kkkkkkkkkk",
	"..kkkkkkkkkk",
	"...kkkkkkkk.",
	"....kkkkkk..",
}

var imhaPalette = map[byte][3]uint8{
	'k': {28, 28, 38},
	'h': {120, 124, 150},
	'g': {96, 96, 110},
	'f': {170, 120, 64},
}

// startImha starts the easter egg; the boom comes as the bomb lands.
func (a *App) startImha() {
	a.imhaAt = time.Now()
	audio := a.audio
	time.AfterFunc(time.Duration(imhaFall*float64(time.Second)), func() { audio.PlaySound(engine.SoundBoom) })
}

// drawImha draws the easter egg over the frame while it plays.
func (a *App) drawImha(f *terminal.Frame) {
	if a.imhaAt.IsZero() {
		return
	}
	t := time.Since(a.imhaAt).Seconds()
	if t >= imhaTotal {
		a.imhaAt = time.Time{}
		return
	}
	drawImhaAt(f, t)
}

// drawImhaAt draws the easter egg as it is t seconds in.
func drawImhaAt(f *terminal.Frame, t float64) {
	area := f.Area()
	w, h := int(area.Width), int(area.Height)
	if w < 4 || h < 4 {
		return
	}
	// Pictures in windows over the terminal would float over the falling cells.
	f.ImageRegions = f.ImageRegions[:0]
	buf := f.Buffer
	if t >= imhaFall {
		snap := make([]cell.Cell, w*h)
		for y := range h {
			for x := range w {
				if c := buf.Get(area.X+uint16(x), area.Y+uint16(y)); c != nil {
					snap[y*w+x] = *c
				}
			}
		}
		shake := 0
		if t < imhaShakeEnd {
			shake = [4]int{2, -2, 1, -1}[int(t*30)%4]
		}
		blank := cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(8, 8, 12)}}
		for y := range h {
			for x := range w {
				*buf.Get(area.X+uint16(x), area.Y+uint16(y)) = blank
			}
		}
		for y := range h {
			for x := range w {
				c := snap[y*w+x]
				lead := x // a wide character falls in one piece
				if c.Content == cell.RuneContinuation && x > 0 {
					lead = x - 1
				}
				nx, ny := x+shake, y+imhaOffset(lead, y, w, h, t)
				if nx < 0 || nx >= w || ny < 0 || ny >= h {
					continue
				}
				*buf.Get(area.X+uint16(nx), area.Y+uint16(ny)) = c
			}
		}
	}

	px := newImhaPixels(w, h)
	switch {
	case t < imhaFall:
		drawImhaBomb(px, t)
	case t < imhaBlastEnd:
		if t < imhaFlashEnd {
			for i := range px.on {
				px.c[i], px.on[i] = [3]uint8{255, 248, 225}, true
			}
		}
		drawImhaBlast(px, (t-imhaFall)/(imhaBlastEnd-imhaFall), int(t*15))
	}
	px.flush(f, area)
}

// imhaOffset is how many rows the cell at x, y has moved t seconds in: down as it falls, then
// up from below the screen as it climbs back.
func imhaOffset(x, y, w, h int, t float64) int {
	dx := float64(x-w/2) / float64(w)
	dy := float64(y-h/2) / float64(h)
	fall := imhaCrumble + math.Hypot(dx, dy)*imhaFallDelay + imhaNoise(x, y, 1)*0.3
	rise := imhaRebuild + float64(x)/float64(w)*0.5 + imhaNoise(x, y, 2)*0.2
	switch {
	case t < fall:
		return 0
	case t < rise:
		dt := t - fall
		return int(0.5 * imhaGravity * float64(h) * dt * dt)
	default:
		p := min(1, (t-rise)/imhaRiseTime)
		e := 1 - math.Pow(1-p, 3)
		return int(math.Round(float64(h-y+2) * (1 - e)))
	}
}

// imhaNoise is a fixed pseudo-random number in [0, 1) for a point and a salt.
func imhaNoise(x, y, salt int) float64 {
	n := uint32(x)*73856093 ^ uint32(y)*19349663 ^ uint32(salt)*83492791
	n ^= n >> 13
	n *= 0x5bd1e995
	n ^= n >> 15
	return float64(n&0xffff) / 0x10000
}

// imhaPixels is a canvas of half-block pixels over the screen: two to a cell, one above the
// other.
type imhaPixels struct {
	w, h int
	c    [][3]uint8
	on   []bool
}

func newImhaPixels(w, h int) *imhaPixels {
	return &imhaPixels{w: w, h: 2 * h, c: make([][3]uint8, w*2*h), on: make([]bool, w*2*h)}
}

func (p *imhaPixels) set(x, y int, c [3]uint8) {
	if x >= 0 && x < p.w && y >= 0 && y < p.h {
		p.c[y*p.w+x], p.on[y*p.w+x] = c, true
	}
}

// flush draws the pixels set into their cells; a cell with one of its pixels set keeps its
// background for the other.
func (p *imhaPixels) flush(f *terminal.Frame, area cell.Rect) {
	rgb := func(c [3]uint8) cell.Color { return cell.NewColorRGB(c[0], c[1], c[2]) }
	for cy := 0; cy < p.h/2; cy++ {
		for cx := 0; cx < p.w; cx++ {
			top, bot := (2*cy)*p.w+cx, (2*cy+1)*p.w+cx
			if !p.on[top] && !p.on[bot] {
				continue
			}
			x, y := area.X+uint16(cx), area.Y+uint16(cy)
			c := f.Buffer.Get(x, y)
			if c == nil {
				continue
			}
			// Half of a wide character left behind would draw over its neighbour.
			if c.Content == cell.RuneContinuation && cx > 0 {
				f.Buffer.Get(x-1, y).Content = ' '
			} else if cx+1 < p.w && f.Buffer.Get(x+1, y).Content == cell.RuneContinuation {
				f.Buffer.Get(x+1, y).Content = ' '
			}
			bg := c.Style.Bg
			switch {
			case p.on[top] && p.on[bot]:
				*c = cell.Cell{Content: '▀', Style: cell.Style{Fg: rgb(p.c[top]), Bg: rgb(p.c[bot])}}
			case p.on[top]:
				*c = cell.Cell{Content: '▀', Style: cell.Style{Fg: rgb(p.c[top]), Bg: bg}}
			default:
				*c = cell.Cell{Content: '▄', Style: cell.Style{Fg: rgb(p.c[bot]), Bg: bg}}
			}
		}
	}
}

// drawImhaBomb draws the bomb falling to the middle of the screen, its fuse burning.
func drawImhaBomb(p *imhaPixels, t float64) {
	s := 1 // pixels a sprite pixel takes, on a large screen two
	if p.w >= 90 && p.h >= 52 {
		s = 2
	}
	sw, sh := len(imhaBomb[0])*s, len(imhaBomb)*s
	fall := t / imhaFall
	x0 := p.w/2 - sw/2 + int(math.Sin(t*11)*float64(s))
	y0 := -sh - s + int(float64(p.h/2-sh/2+sh+s)*fall*fall) // from wholly above, outline too
	frame := int(t * 20)
	spark := [3][3]uint8{{255, 255, 255}, {255, 220, 60}, {255, 140, 30}}
	// A light outline first, one sprite pixel wide, so a black bomb shows on a dark theme.
	body := func(r, c int) bool {
		return r >= 0 && r < len(imhaBomb) && c >= 0 && c < len(imhaBomb[r]) && imhaBomb[r][c] != '.' && imhaBomb[r][c] != 's'
	}
	for r := -1; r <= len(imhaBomb); r++ {
		for c := -1; c <= len(imhaBomb[0]); c++ {
			if body(r, c) || !(body(r-1, c) || body(r+1, c) || body(r, c-1) || body(r, c+1)) {
				continue
			}
			for dy := range s {
				for dx := range s {
					p.set(x0+c*s+dx, y0+r*s+dy, [3]uint8{215, 215, 228})
				}
			}
		}
	}
	var tipX, tipY int
	for r, row := range imhaBomb {
		for c := range len(row) {
			col, ok := imhaPalette[row[c]]
			if row[c] == 's' {
				col, ok = spark[frame%3], true
				tipX, tipY = x0+c*s+s/2, y0+r*s+s/2
			}
			if !ok {
				continue
			}
			for dy := range s {
				for dx := range s {
					p.set(x0+c*s+dx, y0+r*s+dy, col)
				}
			}
		}
	}
	// Sparks thrown off the fuse.
	for i := range 4 {
		dx := int(imhaNoise(frame, i, 3)*float64(6*s)) - 3*s
		dy := int(imhaNoise(frame, i, 4)*float64(4*s)) - 3*s
		p.set(tipX+dx, tipY+dy, spark[(frame+i)%3])
	}
}

// drawImhaBlast draws the explosion u of the way through (0 to 1): a ragged ball of fire that
// grows from the middle, white hot inside, smoke at its edge, and breaks up as it dies.
func drawImhaBlast(p *imhaPixels, u float64, frame int) {
	cx, cy := p.w/2, p.h/2
	maxR := float64(min(p.w, p.h)) * 0.55
	r := maxR * (1 - math.Pow(1-u, 3))
	if r < 1 {
		return
	}
	fire := [][3]uint8{{255, 250, 210}, {255, 214, 70}, {255, 136, 32}, {206, 46, 24}, {92, 82, 82}}
	ri := int(r) + 2
	for y := cy - ri; y <= cy+ri; y++ {
		for x := cx - ri; x <= cx+ri; x++ {
			qx, qy := x/2, y/2 // chunky pixels: noise in blocks of two
			v := math.Hypot(float64(x-cx), float64(y-cy))/r + (imhaNoise(qx, qy, frame)-0.5)*0.35
			if v > 1 {
				continue
			}
			if u > 0.55 && imhaNoise(qx, qy, 7) < (u-0.55)/0.45 {
				continue // burnt out here
			}
			p.set(x, y, fire[min(len(fire)-1, int(v*float64(len(fire))))])
		}
	}
}
