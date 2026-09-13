// Package video contains the screen share transport building blocks: MPEG-TS chunking with
// keyframe detection, the receiver's time-based reorder buffer with NACK scheduling, the
// sender's retransmission history, a token bucket pacer and the adaptive bitrate controller.
package video

// MPEG-TS framing.
const (
	TSPacketSize = 188
	ChunkPackets = 6 // 6 × 188 = 1128 bytes per network chunk, safely below every relay / MTU limit
	ChunkSize    = TSPacketSize * ChunkPackets
	tsSync       = 0x47
)

// Chunker splits the encoder's MPEG-TS output (datagrams or a byte stream) into whole-packet
// chunks of at most ChunkPackets and flags chunks that start a keyframe.
type Chunker struct {
	buf []byte
}

// Push appends data and emits every complete chunk. Packets left at the end of data are
// emitted too (a short chunk) so no frame waits for the next encoder write.
func (c *Chunker) Push(data []byte, emit func(chunk []byte, keyframe bool)) {
	c.buf = append(c.buf, data...)
	// Resynchronise on the sync byte.
	start := 0
	for start < len(c.buf) && c.buf[start] != tsSync {
		start++
	}
	b := c.buf[start:]
	whole := len(b) / TSPacketSize
	for off := 0; off < whole; {
		n := min(ChunkPackets, whole-off)
		// A lost sync byte inside the chunk: cut the chunk there and resync.
		for k := 1; k < n; k++ {
			if b[(off+k)*TSPacketSize] != tsSync {
				n = k
				break
			}
		}
		chunk := make([]byte, n*TSPacketSize)
		copy(chunk, b[off*TSPacketSize:(off+n)*TSPacketSize])
		emit(chunk, IsKeyframeChunk(chunk))
		off += n
		if off < whole && b[off*TSPacketSize] != tsSync {
			rest := b[off*TSPacketSize:]
			c.buf = append(c.buf[:0], rest...)
			c.Push(nil, emit)
			return
		}
	}
	rest := b[whole*TSPacketSize:]
	c.buf = append(c.buf[:0], rest...)
}

// IsKeyframeChunk reports whether chunk contains the start of a random access point: a TS
// packet with the adaptation field random_access_indicator set, or a payload unit start whose
// PES payload begins with an H.264 SPS / IDR NAL unit.
func IsKeyframeChunk(chunk []byte) bool {
	for off := 0; off+TSPacketSize <= len(chunk); off += TSPacketSize {
		if isRandomAccess(chunk[off : off+TSPacketSize]) {
			return true
		}
	}
	return false
}

func isRandomAccess(p []byte) bool {
	if p[0] != tsSync {
		return false
	}
	pusi := p[1]&0x40 != 0
	afc := (p[3] >> 4) & 0x3
	payload := 4
	if afc&0x2 != 0 {
		afLen := int(p[4])
		if afLen > 0 && 5 < len(p) && p[5]&0x40 != 0 {
			return true
		}
		payload = 5 + afLen
	}
	if !pusi || afc&0x1 == 0 || payload+9 > len(p) {
		return false
	}
	pes := p[payload:]
	// PES start code 00 00 01, stream id 0xE0-0xEF (video).
	if pes[0] != 0 || pes[1] != 0 || pes[2] != 1 || pes[3]&0xF0 != 0xE0 {
		return false
	}
	es := pes[9+int(pes[8]):]
	return hasKeyNAL(es)
}

// hasKeyNAL looks for an SPS (7) or IDR slice (5) among the first NAL units of an access unit.
func hasKeyNAL(es []byte) bool {
	for i := 0; i+3 < len(es); i++ {
		if es[i] == 0 && es[i+1] == 0 && es[i+2] == 1 {
			switch es[i+3] & 0x1F {
			case 5, 7:
				return true
			case 1: // non-IDR slice: this access unit is not a keyframe
				return false
			}
			i += 2
		}
	}
	return false
}

// ValidTS reports whether data is a non-empty sequence of whole TS packets.
func ValidTS(data []byte) bool {
	if len(data) == 0 || len(data)%TSPacketSize != 0 {
		return false
	}
	for off := 0; off < len(data); off += TSPacketSize {
		if data[off] != tsSync {
			return false
		}
	}
	return true
}
