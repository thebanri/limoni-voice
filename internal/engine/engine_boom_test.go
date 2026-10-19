package engine

import (
	"encoding/binary"
	"testing"
)

// The boom is loud at first and dies away to silence.
func TestBoomSound(t *testing.T) {
	pcm := generateBoomSoundPCM()
	peak := func(from, to int) int {
		m := 0
		for i := from; i < to; i += 2 {
			v := int(int16(binary.LittleEndian.Uint16(pcm[i:])))
			m = max(m, v, -v)
		}
		return m
	}
	n := len(pcm)
	if first, last := peak(0, n/5), peak(n*9/10, n); first < 8000 || last > first/8 {
		t.Fatalf("peak %d in the first fifth, %d at the end", first, last)
	}
	if len(pcm)%AudioChunkSize != 0 {
		t.Fatal("not whole chunks")
	}
}
