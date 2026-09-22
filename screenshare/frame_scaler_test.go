package screenshare

import (
	"bytes"
	"testing"
)

// bgra builds a source frame whose blue channel counts the column and whose green channel
// counts the row, so a scaled pixel says where it came from. rowPitch may exceed the row's
// own bytes: a mapped GPU texture is padded.
func bgra(w, h, rowPitch int) []byte {
	buf := make([]byte, rowPitch*h)
	for y := range h {
		for x := range w {
			p := y*rowPitch + x*4
			buf[p] = byte(x)
			buf[p+1] = byte(y)
			buf[p+2] = 0x20
			buf[p+3] = 0xFF
		}
	}
	return buf
}

func TestFrameScalerCopiesMatchingSizes(t *testing.T) {
	const w, h = 8, 4
	src := bgra(w, h, w*4+64) // padded rows, as a mapped texture has
	s := newFrameScaler(w, h)
	s.source(w, h)
	dst := make([]byte, w*h*4)
	s.scale(src, w*4+64, dst)

	for y := range h {
		want := src[y*(w*4+64) : y*(w*4+64)+w*4]
		got := dst[y*w*4 : (y+1)*w*4]
		if !bytes.Equal(got, want) {
			t.Fatalf("row %d: %v, want %v", y, got, want)
		}
	}
}

func TestFrameScalerSamplesAcrossSizes(t *testing.T) {
	// Half the width and height: every second pixel and row survives.
	src := bgra(8, 4, 8*4)
	s := newFrameScaler(4, 2)
	s.source(8, 4)
	dst := make([]byte, 4*2*4)
	s.scale(src, 8*4, dst)
	for y := range 2 {
		for x := range 4 {
			p := (y*4 + x) * 4
			if dst[p] != byte(x*2) || dst[p+1] != byte(y*2) {
				t.Fatalf("pixel %d,%d came from column %d row %d, want %d and %d", x, y, dst[p], dst[p+1], x*2, y*2)
			}
		}
	}

	// Doubling repeats each source pixel, and the output is filled to the last row.
	src = bgra(2, 2, 2*4)
	s = newFrameScaler(4, 4)
	s.source(2, 2)
	dst = make([]byte, 4*4*4)
	s.scale(src, 2*4, dst)
	for y := range 4 {
		for x := range 4 {
			p := (y*4 + x) * 4
			if dst[p] != byte(x/2) || dst[p+1] != byte(y/2) || dst[p+3] != 0xFF {
				t.Fatalf("pixel %d,%d = %v", x, y, dst[p:p+4])
			}
		}
	}
}

// A frame shorter than the rows it claims must not take the capture down.
func TestFrameScalerStopsAtTheEndOfTheData(t *testing.T) {
	s := newFrameScaler(4, 4)
	s.source(4, 4)
	dst := make([]byte, 4*4*4)
	s.scale(bgra(4, 2, 4*4), 4*4, dst) // only two rows of data
	if dst[0] == 0 && dst[1] == 0 && dst[3] == 0 {
		t.Fatal("the first row was not scaled")
	}
	for _, b := range dst[2*4*4:] {
		if b != 0 {
			t.Fatal("rows past the end of the data were written")
		}
	}

	// A scaler that was never told its source size writes nothing instead of panicking.
	fresh := newFrameScaler(4, 4)
	out := make([]byte, 4*4*4)
	fresh.scale(bgra(4, 4, 4*4), 4*4, out)
	for _, b := range out {
		if b != 0 {
			t.Fatal("a scaler with no source size wrote pixels")
		}
	}
}
