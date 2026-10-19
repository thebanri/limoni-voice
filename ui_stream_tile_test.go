package main

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
)

func grey(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Rect, image.NewUniform(color.RGBA{128, 128, 128, 255}), image.Point{}, draw.Src)
	return img
}

// The play button is a white triangle on a dark disc in the middle of the picture, larger
// under the pointer; the rest of the picture is left as it was.
func TestPlayButtonOverThePreview(t *testing.T) {
	src := grey(400, 200)
	rest := withPlayButton(src, 0).(*image.RGBA)
	hover := withPlayButton(src, 1).(*image.RGBA)
	r0, r1 := 200*0.15, 200*0.20
	at := func(img *image.RGBA, x, y float64) uint8 { return img.RGBAAt(int(x), int(y)).R }
	if v := at(rest, 200, 100); v < 240 {
		t.Errorf("the middle is %d, want the white triangle", v)
	}
	if v := at(rest, 200-0.8*r0, 100); v >= 100 {
		t.Errorf("the disc beside the triangle is %d, want darker than 128", v)
	}
	if v := at(rest, 10, 10); v != 128 {
		t.Errorf("a corner is %d, want the picture's 128", v)
	}
	edge := 100 + (r0+r1)/2 // between the two radii, under the triangle
	if at(rest, 200, edge) != 128 || at(hover, 200, edge) >= 128 {
		t.Errorf("at %v below the middle: %d at rest, %d hovered; want the disc only hovered", edge-100, at(rest, 200, edge), at(hover, 200, edge))
	}
	if src.RGBAAt(200, 100).R != 128 {
		t.Error("the preview itself was drawn on")
	}
}

// Hovering moves the button in steps over a few frames and back; each step's picture is
// made once.
func TestHoverAnimates(t *testing.T) {
	var fx tileFx
	src := grey(80, 45)
	first, _ := fx.step(src, false)
	var seen []float64
	var last image.Image
	for range 6 {
		last, _ = fx.step(src, true)
		seen = append(seen, fx.hover)
	}
	if seen[0] <= 0 || seen[0] >= 1 || seen[len(seen)-1] != 1 {
		t.Fatalf("hover went %v, want a few steps to 1", seen)
	}
	if again, _ := fx.step(src, true); again != last {
		t.Fatal("a step's picture was made again")
	}
	for range 6 {
		fx.step(src, false)
	}
	if back, h := fx.step(src, false); h != 0 || back != first {
		t.Fatalf("after leaving: hover %v, same picture as at rest %v", h, back == first)
	}
}
