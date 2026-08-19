package main

import (
	"math"

	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/graphics"
	"github.com/thebanri/limoni/widgets"
)

type Line3D struct {
	A, B  graphics.Vertex3D
	Style cell.Style
}

type Polygon3D struct {
	Vertices []graphics.Vertex3D
	Color    cell.Color
	Normal   graphics.Vertex3D
}

type StudioMicrophone3D struct {
	Lines    []Line3D
	Polygons []Polygon3D
}

// NewMicrophone3D constructs a highly detailed, professional 3D studio podcast microphone.
func NewMicrophone3D() *StudioMicrophone3D {
	m := &StudioMicrophone3D{
		Lines:    make([]Line3D, 0, 500),
		Polygons: make([]Polygon3D, 0, 300),
	}

	// Styles
	grillAccent := cell.Style{Fg: cell.NewColorRGB(0x00, 0xF5, 0xD4)} // Neon Mint
	grillBody := cell.Style{Fg: cell.NewColorRGB(0x00, 0xBB, 0xF9)}   // Electric Blue
	metalRing := cell.Style{Fg: cell.NewColorRGB(0xF1, 0x5B, 0xB5)}   // Neon Magenta
	chassis := cell.Style{Fg: cell.NewColorRGB(0xFE, 0xE4, 0x40)}     // Warm Gold
	standStyle := cell.Style{Fg: cell.NewColorRGB(0x9B, 0x5D, 0xE5)}  // Studio Purple
	baseStyle := cell.Style{Fg: cell.NewColorRGB(0x83, 0x38, 0xEC)}   // Deep Violet

	// 1. CAPSULE GRILL (Hemisphere dome + cylindrical grill)
	capsuleRadius := 1.15
	capsuleBaseY := 1.2
	capsuleHeight := 1.6
	domeRadius := capsuleRadius

	// Latitude rings on grill & dome
	rings := 10
	segments := 20
	for r := 0; r <= rings; r++ {
		t := float64(r) / float64(rings)
		var y, rad float64

		if t <= 0.5 { // Lower cylinder portion of grill
			subT := t / 0.5
			y = capsuleBaseY + subT*(capsuleHeight*0.5)
			rad = capsuleRadius
		} else { // Upper dome hemisphere
			subT := (t - 0.5) / 0.5
			phi := subT * (math.Pi / 2.0)
			y = capsuleBaseY + (capsuleHeight * 0.5) + domeRadius*math.Sin(phi)
			rad = capsuleRadius * math.Cos(phi)
		}

		st := grillBody
		if r == 0 || r == rings || r == rings/2 {
			st = grillAccent
		}

		for s := 0; s < segments; s++ {
			th1 := float64(s) * 2 * math.Pi / float64(segments)
			th2 := float64(s+1) * 2 * math.Pi / float64(segments)

			p1 := graphics.Vertex3D{X: rad * math.Cos(th1), Y: y, Z: rad * math.Sin(th1)}
			p2 := graphics.Vertex3D{X: rad * math.Cos(th2), Y: y, Z: rad * math.Sin(th2)}
			m.Lines = append(m.Lines, Line3D{A: p1, B: p2, Style: st})
		}
	}

	// Longitude ribs on grill
	ribs := 12
	for rib := 0; rib < ribs; rib++ {
		th := float64(rib) * 2 * math.Pi / float64(ribs)
		cosT := math.Cos(th)
		sinT := math.Sin(th)

		for r := 0; r < rings; r++ {
			t1 := float64(r) / float64(rings)
			t2 := float64(r+1) / float64(rings)

			var y1, rad1, y2, rad2 float64
			if t1 <= 0.5 {
				y1 = capsuleBaseY + (t1/0.5)*(capsuleHeight*0.5)
				rad1 = capsuleRadius
			} else {
				phi := ((t1 - 0.5) / 0.5) * (math.Pi / 2.0)
				y1 = capsuleBaseY + (capsuleHeight * 0.5) + domeRadius*math.Sin(phi)
				rad1 = capsuleRadius * math.Cos(phi)
			}

			if t2 <= 0.5 {
				y2 = capsuleBaseY + (t2/0.5)*(capsuleHeight*0.5)
				rad2 = capsuleRadius
			} else {
				phi := ((t2 - 0.5) / 0.5) * (math.Pi / 2.0)
				y2 = capsuleBaseY + (capsuleHeight * 0.5) + domeRadius*math.Sin(phi)
				rad2 = capsuleRadius * math.Cos(phi)
			}

			p1 := graphics.Vertex3D{X: rad1 * cosT, Y: y1, Z: rad1 * sinT}
			p2 := graphics.Vertex3D{X: rad2 * cosT, Y: y2, Z: rad2 * sinT}
			m.Lines = append(m.Lines, Line3D{A: p1, B: p2, Style: grillAccent})
		}
	}

	// 2. METALLIC CENTER BAND
	bandY := capsuleBaseY
	bandHeight := 0.25
	bandRadius := capsuleRadius * 1.08
	for s := 0; s < segments; s++ {
		th1 := float64(s) * 2 * math.Pi / float64(segments)
		th2 := float64(s+1) * 2 * math.Pi / float64(segments)

		top1 := graphics.Vertex3D{X: bandRadius * math.Cos(th1), Y: bandY, Z: bandRadius * math.Sin(th1)}
		top2 := graphics.Vertex3D{X: bandRadius * math.Cos(th2), Y: bandY, Z: bandRadius * math.Sin(th2)}
		bot1 := graphics.Vertex3D{X: bandRadius * math.Cos(th1), Y: bandY - bandHeight, Z: bandRadius * math.Sin(th1)}
		bot2 := graphics.Vertex3D{X: bandRadius * math.Cos(th2), Y: bandY - bandHeight, Z: bandRadius * math.Sin(th2)}

		m.Lines = append(m.Lines, Line3D{A: top1, B: top2, Style: metalRing})
		m.Lines = append(m.Lines, Line3D{A: bot1, B: bot2, Style: metalRing})
		if s%2 == 0 {
			m.Lines = append(m.Lines, Line3D{A: top1, B: bot1, Style: metalRing})
		}
	}

	// 3. LOWER MIC BODY (Tapered Cylinder)
	bodyTopY := bandY - bandHeight
	bodyBottomY := 0.1
	bodyTopRadius := capsuleRadius
	bodyBottomRadius := capsuleRadius * 0.75
	bodyRings := 4

	for r := 0; r <= bodyRings; r++ {
		t := float64(r) / float64(bodyRings)
		y := bodyTopY - t*(bodyTopY-bodyBottomY)
		rad := bodyTopRadius - t*(bodyTopRadius-bodyBottomRadius)

		for s := 0; s < segments; s++ {
			th1 := float64(s) * 2 * math.Pi / float64(segments)
			th2 := float64(s+1) * 2 * math.Pi / float64(segments)

			p1 := graphics.Vertex3D{X: rad * math.Cos(th1), Y: y, Z: rad * math.Sin(th1)}
			p2 := graphics.Vertex3D{X: rad * math.Cos(th2), Y: y, Z: rad * math.Sin(th2)}
			m.Lines = append(m.Lines, Line3D{A: p1, B: p2, Style: chassis})
		}
	}

	// Body Longitude Lines
	for rib := 0; rib < 8; rib++ {
		th := float64(rib) * 2 * math.Pi / 8.0
		cosT := math.Cos(th)
		sinT := math.Sin(th)

		p1 := graphics.Vertex3D{X: bodyTopRadius * cosT, Y: bodyTopY, Z: bodyTopRadius * sinT}
		p2 := graphics.Vertex3D{X: bodyBottomRadius * cosT, Y: bodyBottomY, Z: bodyBottomRadius * sinT}
		m.Lines = append(m.Lines, Line3D{A: p1, B: p2, Style: chassis})
	}

	// 4. SHOCKMOUNT U-BRACKET & PIVOT SCREWS
	mountY := (capsuleBaseY + bodyBottomY) / 2.0
	mountRadius := capsuleRadius * 1.55
	mountSegs := 24

	// Horseshoe curved bracket
	for s := 0; s < mountSegs; s++ {
		th1 := float64(s)*math.Pi/float64(mountSegs) - math.Pi/2
		th2 := float64(s+1)*math.Pi/float64(mountSegs) - math.Pi/2

		y1 := mountY + mountRadius*math.Sin(th1)*0.75
		y2 := mountY + mountRadius*math.Sin(th2)*0.75

		p1 := graphics.Vertex3D{X: mountRadius * math.Cos(th1), Y: y1, Z: 0}
		p2 := graphics.Vertex3D{X: mountRadius * math.Cos(th2), Y: y2, Z: 0}
		m.Lines = append(m.Lines, Line3D{A: p1, B: p2, Style: standStyle})

		// Double thickness line
		p1Inner := graphics.Vertex3D{X: (mountRadius - 0.1) * math.Cos(th1), Y: y1, Z: 0}
		p2Inner := graphics.Vertex3D{X: (mountRadius - 0.1) * math.Cos(th2), Y: y2, Z: 0}
		m.Lines = append(m.Lines, Line3D{A: p1Inner, B: p2Inner, Style: standStyle})
	}

	// Pivot knobs on Left/Right
	m.Lines = append(m.Lines, Line3D{
		A: graphics.Vertex3D{X: -mountRadius, Y: mountY, Z: 0},
		B: graphics.Vertex3D{X: -capsuleRadius, Y: mountY, Z: 0}, Style: metalRing,
	})
	m.Lines = append(m.Lines, Line3D{
		A: graphics.Vertex3D{X: mountRadius, Y: mountY, Z: 0},
		B: graphics.Vertex3D{X: capsuleRadius, Y: mountY, Z: 0}, Style: metalRing,
	})

	// 5. VERTICAL TELESCOPING STEM & COLLAR
	stemTopY := mountY - mountRadius*0.75
	stemBottomY := -1.7
	m.Lines = append(m.Lines, Line3D{
		A: graphics.Vertex3D{X: 0, Y: stemTopY, Z: 0},
		B: graphics.Vertex3D{X: 0, Y: stemBottomY, Z: 0}, Style: standStyle,
	})
	m.Lines = append(m.Lines, Line3D{
		A: graphics.Vertex3D{X: -0.08, Y: stemTopY, Z: 0},
		B: graphics.Vertex3D{X: -0.08, Y: stemBottomY, Z: 0}, Style: standStyle,
	})
	m.Lines = append(m.Lines, Line3D{
		A: graphics.Vertex3D{X: 0.08, Y: stemTopY, Z: 0},
		B: graphics.Vertex3D{X: 0.08, Y: stemBottomY, Z: 0}, Style: standStyle,
	})

	// Adjustment Collar Ring
	collarY := (stemTopY + stemBottomY) / 2.0
	collarRad := 0.35
	for s := 0; s < 12; s++ {
		th1 := float64(s) * 2 * math.Pi / 12.0
		th2 := float64(s+1) * 2 * math.Pi / 12.0
		p1 := graphics.Vertex3D{X: collarRad * math.Cos(th1), Y: collarY, Z: collarRad * math.Sin(th1)}
		p2 := graphics.Vertex3D{X: collarRad * math.Cos(th2), Y: collarY, Z: collarRad * math.Sin(th2)}
		m.Lines = append(m.Lines, Line3D{A: p1, B: p2, Style: metalRing})
	}

	// 6. HEAVY ROUND DESK STAND BASE
	baseRadius := 2.2
	baseY := stemBottomY
	baseHeight := 0.25
	baseSegs := 20

	for s := 0; s < baseSegs; s++ {
		th1 := float64(s) * 2 * math.Pi / float64(baseSegs)
		th2 := float64(s+1) * 2 * math.Pi / float64(baseSegs)

		top1 := graphics.Vertex3D{X: (baseRadius - 0.3) * math.Cos(th1), Y: baseY, Z: (baseRadius - 0.3) * math.Sin(th1)}
		top2 := graphics.Vertex3D{X: (baseRadius - 0.3) * math.Cos(th2), Y: baseY, Z: (baseRadius - 0.3) * math.Sin(th2)}
		bot1 := graphics.Vertex3D{X: baseRadius * math.Cos(th1), Y: baseY - baseHeight, Z: baseRadius * math.Sin(th1)}
		bot2 := graphics.Vertex3D{X: baseRadius * math.Cos(th2), Y: baseY - baseHeight, Z: baseRadius * math.Sin(th2)}

		m.Lines = append(m.Lines, Line3D{A: top1, B: top2, Style: baseStyle})
		m.Lines = append(m.Lines, Line3D{A: bot1, B: bot2, Style: baseStyle})
		m.Lines = append(m.Lines, Line3D{A: top1, B: bot1, Style: baseStyle})

		// Center hub spokes
		if s%4 == 0 {
			m.Lines = append(m.Lines, Line3D{
				A: graphics.Vertex3D{X: 0, Y: baseY, Z: 0},
				B: top1, Style: baseStyle,
			})
		}
	}

	return m
}

// Render draws the rotating 3D studio microphone onto a Limoni Canvas.
func (m *StudioMicrophone3D) Render(canvas *widgets.Canvas, width, height uint16, angleY, angleX float64, scale, distance float64) {
	if canvas == nil || len(m.Lines) == 0 {
		return
	}

	screenW := float64(width) * 2.0
	screenH := float64(height) * 4.0

	for _, line := range m.Lines {
		vA := line.A.RotateX(angleX).RotateY(angleY)
		vB := line.B.RotateX(angleX).RotateY(angleY)

		xA, yA, visA := graphics.Project(vA, screenW, screenH, distance, scale)
		xB, yB, visB := graphics.Project(vB, screenW, screenH, distance, scale)

		if visA && visB {
			canvas.DrawLine(int(xA), int(yA), int(xB), int(yB), line.Style)
		}
	}
}
