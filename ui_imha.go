// The /imha easter egg: a pixel-art bomb falls onto the screen and explodes; the screen
// turns to sand that the blast throws about and gravity piles up at the bottom, then the
// sand flies back into place and the screen is whole again. It plays on this screen alone;
// nothing is sent to the room.

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
	imhaFall     = 1.1  // the bomb falls to the middle of the screen
	imhaFlashEnd = 1.22 // the blast's white flash
	imhaBlastEnd = 2.05 // the fireball has burnt out
	imhaWave     = 0.35 // how much later the grains farthest from the blast are thrown
	imhaRebuild  = 3.4  // the first grains start back
	imhaSweep    = 0.5  // how much later the top row starts back than the bottom one
	imhaFlight   = 0.8  // a grain's flight back
	imhaTotal    = imhaRebuild + imhaSweep + 0.25 + imhaFlight + 0.05
	imhaGravity  = 3.5 // screen heights a second squared
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
	a.imhaAt, a.imha = time.Now(), &imhaPlay{}
	audio := a.audio
	time.AfterFunc(time.Duration(imhaFall*float64(time.Second)), func() { audio.PlaySound(engine.SoundBoom) })
}

// drawImha draws the easter egg over the frame while it plays.
func (a *App) drawImha(f *terminal.Frame) {
	if a.imha == nil {
		return
	}
	t := time.Since(a.imhaAt).Seconds()
	if t >= imhaTotal {
		a.imha = nil
		return
	}
	a.imha.draw(f, t)
}

// imhaPlay is one playing of the easter egg: the sand, from the blast on.
type imhaPlay struct {
	sand *imhaSand
}

// draw draws the easter egg as it is t seconds in, over the frame just drawn. Frames come in
// order: the sand moves on from the last.
func (p *imhaPlay) draw(f *terminal.Frame, t float64) {
	area := f.Area()
	w, h := int(area.Width), int(area.Height)
	if w < 4 || h < 4 {
		return
	}
	// Pictures in windows over the terminal would float over the sand.
	f.ImageRegions = f.ImageRegions[:0]
	px := newImhaPixels(w, h)
	if t >= imhaFall {
		if p.sand == nil || p.sand.w != w || p.sand.h != 2*h {
			p.sand = newImhaSand(f, area, t)
		}
		p.sand.step(t)
		p.sand.draw(px, t)
		p.sand.cover(f, area, px, t)
	}
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

// imhaSand is the screen as grains of sand: one for each half-block pixel that is not the
// screen's background, which burns away in the blast.
type imhaSand struct {
	w, h    int // in pixels: the screen's columns, and twice its rows
	grains  []imhaGrain
	heights []int // settled grains in each column
	dark    [3]uint8
	last    float64 // when it last moved
	back    bool    // flying back
}

type imhaGrain struct {
	ox, oy       int // where it belongs
	x, y, vx, vy float64
	c            [3]uint8
	launch       float64 // when the blast throws it
	settled      bool
	fromX, fromY float64 // where it was when it started back
}

// newImhaSand makes sand of the screen as the frame has it, at the moment of the blast.
func newImhaSand(f *terminal.Frame, area cell.Rect, t float64) *imhaSand {
	w, h := int(area.Width), 2*int(area.Height)
	cols := make([][3]uint8, w*h)
	count := map[[3]uint8]int{}
	for cy := 0; cy < h/2; cy++ {
		for cx := 0; cx < w; cx++ {
			top, bot := imhaCellPixels(f.Buffer.Get(area.X+uint16(cx), area.Y+uint16(cy)))
			cols[2*cy*w+cx], cols[(2*cy+1)*w+cx] = top, bot
			count[top]++
			count[bot]++
		}
	}
	var bg [3]uint8 // the most common colour is the background
	for c, n := range count {
		if n > count[bg] {
			bg = c
		}
	}
	s := &imhaSand{w: w, h: h, heights: make([]int, w), last: t,
		dark: [3]uint8{bg[0] / 3, bg[1] / 3, bg[2] / 3}}
	cx, cy := float64(w)/2, float64(h)/2
	diag := math.Hypot(cx, cy)
	for y := range h {
		for x := range w {
			c := cols[y*w+x]
			if imhaNear(c, bg) {
				continue
			}
			dx, dy := float64(x)-cx, float64(y)-cy
			d := math.Hypot(dx, dy)
			if d < 1 {
				dx, dy, d = 0, -1, 1
			}
			near := 1 - min(1, d/diag) // 1 at the blast, 0 in the corners
			speed := float64(h) * (0.6 + 2.2*near) * (0.6 + 0.8*imhaNoise(x, y, 5))
			s.grains = append(s.grains, imhaGrain{
				ox: x, oy: y, x: float64(x), y: float64(y), c: c,
				vx:     dx / d * speed * 1.3, // cells are half as wide as two pixels are tall
				vy:     dy/d*speed - float64(h)*(0.4+0.6*imhaNoise(x, y, 6)),
				launch: imhaFall + (1-near)*imhaWave + imhaNoise(x, y, 7)*0.08,
			})
		}
	}
	return s
}

// imhaCellPixels is the colour of a cell's two pixels: a half block's own two, a blank's
// background, and for a letter its colours mixed, as a glyph covers part of its cell.
func imhaCellPixels(c *cell.Cell) (top, bot [3]uint8) {
	if c == nil {
		return
	}
	rgb := func(col cell.Color, def [3]uint8) [3]uint8 {
		if col.Type() == cell.ColorDefault {
			return def
		}
		r, g, b := col.RGB()
		return [3]uint8{r, g, b}
	}
	bg := rgb(c.Style.Bg, [3]uint8{12, 12, 16})
	fg := rgb(c.Style.Fg, [3]uint8{200, 200, 210})
	mix := func(k float64) [3]uint8 {
		return [3]uint8{uint8(float64(bg[0]) + (float64(fg[0])-float64(bg[0]))*k),
			uint8(float64(bg[1]) + (float64(fg[1])-float64(bg[1]))*k),
			uint8(float64(bg[2]) + (float64(fg[2])-float64(bg[2]))*k)}
	}
	switch c.Content {
	case 0, ' ', cell.RuneImage:
		return bg, bg
	case '▀':
		return fg, bg
	case '▄':
		return bg, fg
	case '█':
		return fg, fg
	}
	return mix(0.55), mix(0.4)
}

func imhaNear(a, b [3]uint8) bool {
	d := 0
	for i := range 3 {
		d += max(int(a[i])-int(b[i]), int(b[i])-int(a[i]))
	}
	return d < 24
}

// step moves the grains on to t: thrown by the blast, falling, and settling on the pile,
// where a grain slides off a slope the way sand does.
func (s *imhaSand) step(t float64) {
	if t >= imhaRebuild {
		if !s.back {
			s.back = true
			for i := range s.grains {
				s.grains[i].fromX, s.grains[i].fromY = s.grains[i].x, s.grains[i].y
			}
		}
		return
	}
	g := imhaGravity * float64(s.h)
	for s.last < t {
		dt := min(t-s.last, 1.0/60)
		s.last += dt
		for i := range s.grains {
			gr := &s.grains[i]
			if gr.settled || s.last < gr.launch {
				continue
			}
			gr.vy += g * dt
			gr.x += gr.vx * dt
			gr.y += gr.vy * dt
			if gr.x < 0 {
				gr.x, gr.vx = 0, -gr.vx*0.3
			} else if gr.x > float64(s.w-1) {
				gr.x, gr.vx = float64(s.w-1), -gr.vx*0.3
			}
			col := int(gr.x + 0.5)
			if floor := float64(s.h - 1 - s.heights[col]); gr.vy > 0 && gr.y >= floor {
				s.settle(gr, col)
			}
		}
	}
}

func (s *imhaSand) settle(gr *imhaGrain, col int) {
	for range 8 {
		cur := s.heights[col]
		left, right := col > 0 && s.heights[col-1] < cur-1, col < s.w-1 && s.heights[col+1] < cur-1
		switch {
		case left && right:
			col += 2*int(imhaNoise(gr.ox, gr.oy, 8)*2) - 1
		case left:
			col--
		case right:
			col++
		default:
			gr.x, gr.y, gr.settled = float64(col), float64(s.h-1-cur), true
			s.heights[col]++
			return
		}
	}
	gr.x, gr.y, gr.settled = float64(col), float64(s.h-1-s.heights[col]), true
	s.heights[col]++
}

// imhaHome is when the grain belonging at x, y starts back: the bottom row first, the screen
// building itself up from the pile.
func (s *imhaSand) imhaHome(x, y int) float64 {
	return imhaRebuild + (1-float64(y)/float64(s.h))*imhaSweep + imhaNoise(x, y, 9)*0.25
}

// draw puts the grains on the canvas, in the dark the blast left.
func (s *imhaSand) draw(px *imhaPixels, t float64) {
	for i := range px.on {
		px.c[i], px.on[i] = s.dark, true
	}
	for i := range s.grains {
		gr := &s.grains[i]
		x, y := gr.x, gr.y
		if s.back {
			// An arc from the pile back to its place, rising over the way.
			u := min(1, max(0, (t-s.imhaHome(gr.ox, gr.oy))/imhaFlight))
			e := u * u * (3 - 2*u)
			midX, midY := (gr.fromX+float64(gr.ox))/2, min(gr.fromY, float64(gr.oy))-float64(s.h)*0.25
			x = (1-e)*(1-e)*gr.fromX + 2*(1-e)*e*midX + e*e*float64(gr.ox)
			y = (1-e)*(1-e)*gr.fromY + 2*(1-e)*e*midY + e*e*float64(gr.oy)
		}
		px.set(int(x+0.5), int(y+0.5), gr.c)
	}
}

// cover takes the cells whose grains are all home off the canvas, so they show as the frame
// drew them: text comes back where its sand lands.
func (s *imhaSand) cover(f *terminal.Frame, area cell.Rect, px *imhaPixels, t float64) {
	if !s.back {
		return
	}
	for cy := 0; cy < s.h/2; cy++ {
		for cx := 0; cx < s.w; cx++ {
			if t >= max(s.imhaHome(cx, 2*cy), s.imhaHome(cx, 2*cy+1))+imhaFlight {
				px.on[2*cy*s.w+cx], px.on[(2*cy+1)*s.w+cx] = false, false
			}
		}
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
