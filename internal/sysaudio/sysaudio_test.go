package sysaudio

import (
	"encoding/binary"
	"math"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestFramerAndResampler(t *testing.T) {
	var frames int
	f := newFramer(func(fr []int16) {
		if len(fr) != FrameSamples {
			t.Fatalf("frame of %d samples", len(fr))
		}
		frames++
	})
	f.push(make([]int16, 500))
	f.push(make([]int16, 1500))
	if frames != 2 {
		t.Fatalf("got %d frames", frames)
	}

	rs := newResampler(44100)
	in := make([]float64, 44100)
	for i := range in {
		in[i] = 0.5 * math.Sin(2*math.Pi*1000*float64(i)/44100)
	}
	out := rs.process(in, nil)
	if d := len(out) - SampleRate; d < -2 || d > 2 {
		t.Fatalf("44.1 kHz second resampled to %d samples", len(out))
	}
	var peak int16
	for _, v := range out[100:] {
		peak = max(peak, v)
	}
	if peak < 15000 || peak > 16500 {
		t.Fatalf("resampled amplitude %d", peak)
	}
}

// Captures the real system monitor for a moment (opt-in: needs a running sound server).
func TestOpenSystemMonitor(t *testing.T) {
	if os.Getenv("LIMONI_SYSAUDIO_TEST") == "" {
		t.Skip("set LIMONI_SYSAUDIO_TEST=1 to capture the real system audio monitor")
	}
	var n atomic.Int32
	s, err := Open(func(fr []int16) { n.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(500 * time.Millisecond)
	t.Logf("%s delivered %d frames in 500 ms", s.Backend(), n.Load())
	if n.Load() < 10 {
		t.Fatal("no audio frames from the monitor")
	}
}

// wfx builds a packed WAVEFORMATEX(TENSIBLE) block exactly as Windows lays it out.
func wfx(tag, channels, bits int, rate int, sub *[16]byte) []byte {
	b := make([]byte, waveFormatExSize)
	binary.LittleEndian.PutUint16(b[0:], uint16(tag))
	binary.LittleEndian.PutUint16(b[2:], uint16(channels))
	binary.LittleEndian.PutUint32(b[4:], uint32(rate))
	binary.LittleEndian.PutUint32(b[8:], uint32(rate*channels*bits/8))
	binary.LittleEndian.PutUint16(b[12:], uint16(channels*bits/8))
	binary.LittleEndian.PutUint16(b[14:], uint16(bits))
	if sub == nil {
		return b
	}
	binary.LittleEndian.PutUint16(b[16:], 22)
	b = append(b, make([]byte, 22)...)
	binary.LittleEndian.PutUint16(b[18:], uint16(bits)) // wValidBitsPerSample
	binary.LittleEndian.PutUint32(b[20:], 3)            // dwChannelMask: front left + right
	copy(b[24:], sub[:])
	return b
}

func TestParseWaveFormat(t *testing.T) {
	cases := []struct {
		name  string
		block []byte
		want  waveFormat
	}{
		{"extensible float32 48k stereo", wfx(waveFormatExtensible, 2, 32, 48000, &subtypeFloatBytes), waveFormat{Channels: 2, Bits: 32, Rate: 48000, Float: true}},
		{"extensible pcm16 44.1k stereo", wfx(waveFormatExtensible, 2, 16, 44100, &subtypePCMBytes), waveFormat{Channels: 2, Bits: 16, Rate: 44100}},
		{"extensible 7.1 float 96k", wfx(waveFormatExtensible, 8, 32, 96000, &subtypeFloatBytes), waveFormat{Channels: 8, Bits: 32, Rate: 96000, Float: true}},
		{"plain float32", wfx(waveFormatIEEEFloat, 2, 32, 48000, nil), waveFormat{Channels: 2, Bits: 32, Rate: 48000, Float: true}},
		{"plain pcm24", wfx(waveFormatPCM, 2, 24, 48000, nil), waveFormat{Channels: 2, Bits: 24, Rate: 48000}},
	}
	for _, c := range cases {
		got, err := parseWaveFormat(c.block)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v (%v), want %+v", c.name, got, err, c.want)
		}
	}

	// An unknown subtype must not disable system audio: fall back to the sample size.
	unknown := wfx(waveFormatExtensible, 2, 32, 48000, &[16]byte{0x42})
	if got, err := parseWaveFormat(unknown); err != nil || !got.Float {
		t.Errorf("unknown subtype: got %+v (%v)", got, err)
	}
	if _, err := parseWaveFormat(wfx(waveFormatPCM, 2, 8, 48000, nil)); err == nil {
		t.Error("8 bit PCM accepted")
	}
	if _, err := parseWaveFormat([]byte{1, 2, 3}); err == nil {
		t.Error("truncated block accepted")
	}
}
