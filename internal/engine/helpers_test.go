package engine

import "encoding/binary"

// upsample16k builds one 48 kHz capture frame from a 16 kHz test signal generator (sample
// hold), so the 16 kHz analysis path sees exactly the generated samples.
func upsample16k(gen func(i int) int16) []byte {
	pcm := make([]byte, AudioChunkSize)
	for i := 0; i < analysisSamples; i++ {
		v := uint16(gen(i))
		for k := 0; k < 3; k++ {
			binary.LittleEndian.PutUint16(pcm[2*(3*i+k):], v)
		}
	}
	return pcm
}
