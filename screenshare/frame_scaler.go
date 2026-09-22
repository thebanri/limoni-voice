package screenshare

// frameScaler turns captured rows of any size into the fixed output size with nearest
// neighbour sampling, which costs one lookup per pixel and no allocation per frame.
type frameScaler struct {
	dstW, dstH int
	srcW, srcH int
	cols       []int
	rows       []int
}

func newFrameScaler(dstW, dstH int) *frameScaler {
	return &frameScaler{dstW: dstW, dstH: dstH, cols: make([]int, dstW), rows: make([]int, dstH)}
}

func (f *frameScaler) source(w, h int) {
	f.srcW, f.srcH = w, h
	for x := range f.cols {
		f.cols[x] = x * w / f.dstW * 4
	}
	for y := range f.rows {
		f.rows[y] = y * h / f.dstH
	}
}

func (f *frameScaler) scale(src []byte, rowPitch int, dst []byte) {
	if f.srcW <= 0 || f.srcH <= 0 {
		return
	}
	rowBytes := f.dstW * 4
	for y := 0; y < f.dstH; y++ {
		srcRow := f.rows[y] * rowPitch
		if srcRow+f.srcW*4 > len(src) {
			break
		}
		out := dst[y*rowBytes : (y+1)*rowBytes]
		if f.srcW == f.dstW {
			copy(out, src[srcRow:srcRow+rowBytes])
			continue
		}
		row := src[srcRow:]
		for x := 0; x < f.dstW; x++ {
			copy(out[x*4:x*4+4], row[f.cols[x]:f.cols[x]+4])
		}
	}
}
