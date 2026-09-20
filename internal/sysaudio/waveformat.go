package sysaudio

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Windows WAVEFORMATEX / WAVEFORMATEXTENSIBLE parsing.
//
// Both structures are byte packed (mmreg.h wraps them in pshpack1.h), so they must be read
// field by field from the raw block: a Go struct with the same fields would be padded and read
// the extensible part from the wrong offsets.
//
//	WAVEFORMATEX          wFormatTag 0, nChannels 2, nSamplesPerSec 4, nAvgBytesPerSec 8,
//	                      nBlockAlign 12, wBitsPerSample 14, cbSize 16 (size 18)
//	WAVEFORMATEXTENSIBLE  wValidBitsPerSample 18, dwChannelMask 20, SubFormat 24 (size 40)
const (
	waveFormatPCM        = 1
	waveFormatIEEEFloat  = 3
	waveFormatExtensible = 0xFFFE
	waveFormatExSize     = 18
)

// KSDATAFORMAT_SUBTYPE_{PCM,IEEE_FLOAT} as little endian GUID bytes.
var (
	subtypePCMBytes   = [16]byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}
	subtypeFloatBytes = [16]byte{0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}
)

// waveFormat is the part of a WAVEFORMATEX block this package needs.
type waveFormat struct {
	Channels int
	Bits     int
	Rate     int
	Float    bool
}

func (f waveFormat) String() string {
	kind := "int"
	if f.Float {
		kind = "float"
	}
	return fmt.Sprintf("%d Hz, %d ch, %d bit %s", f.Rate, f.Channels, f.Bits, kind)
}

// parseWaveFormat reads a WAVEFORMATEX block as Windows lays it out in memory.
func parseWaveFormat(b []byte) (waveFormat, error) {
	if len(b) < waveFormatExSize {
		return waveFormat{}, errors.New("sysaudio: wave format block too short")
	}
	f := waveFormat{
		Channels: int(binary.LittleEndian.Uint16(b[2:])),
		Rate:     int(binary.LittleEndian.Uint32(b[4:])),
		Bits:     int(binary.LittleEndian.Uint16(b[14:])),
	}
	tag := binary.LittleEndian.Uint16(b)
	cbSize := int(binary.LittleEndian.Uint16(b[16:]))
	switch {
	case tag == waveFormatIEEEFloat:
		f.Float = true
	case tag == waveFormatPCM:
	case tag == waveFormatExtensible && cbSize >= 22 && len(b) >= 40:
		var sub [16]byte
		copy(sub[:], b[24:40])
		switch sub {
		case subtypeFloatBytes:
			f.Float = true
		case subtypePCMBytes:
		default:
			// Unknown subtype (for example a compressed endpoint format): fall back to the
			// usual meaning of the sample size instead of giving up on system audio.
			f.Float = f.Bits == 32
		}
	default:
		return f, fmt.Errorf("sysaudio: unsupported wave format tag %d", tag)
	}
	if f.Channels < 1 || f.Rate < 8000 || f.Rate > 384000 {
		return f, fmt.Errorf("sysaudio: unsupported wave format (%s)", f)
	}
	if (f.Float && f.Bits != 32) || (!f.Float && f.Bits != 16 && f.Bits != 24 && f.Bits != 32) {
		return f, fmt.Errorf("sysaudio: unsupported sample format (%s)", f)
	}
	return f, nil
}
