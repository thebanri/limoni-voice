// Package voice contains the real-time voice pipeline building blocks: the Opus codec
// wrapper and the adaptive jitter buffer with packet loss concealment and in-band FEC.
package voice

import (
	"sync"

	"github.com/thesyncim/gopus"
)

// Codec defaults: fullband VoIP at constant bitrate. CBR keeps packet sizes independent of
// speech content, which avoids the known VBR length side channel on encrypted voice.
const (
	DefaultBitrate   = 32000
	DefaultLossHint  = 5
	maxOpusFrameSize = 1276
)

// Encoder encodes mono 16-bit PCM frames to Opus. Safe for concurrent use.
type Encoder struct {
	mu           sync.Mutex
	enc          *gopus.Encoder
	frameSamples int
	pcm          []int16
	buf          []byte
	lossHint     int
}

// NewEncoder creates an encoder for sampleRate (8/12/16/24/48 kHz) and frameSamples per frame.
func NewEncoder(sampleRate, frameSamples int) (*Encoder, error) {
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: sampleRate, Channels: 1, Application: gopus.ApplicationVoIP})
	if err != nil {
		return nil, err
	}
	_ = enc.SetBitrate(DefaultBitrate)
	enc.SetVBR(false)
	_ = enc.SetComplexity(8)
	_ = enc.SetInBandFEC(1)
	_ = enc.SetPacketLoss(DefaultLossHint)
	return &Encoder{enc: enc, frameSamples: frameSamples, pcm: make([]int16, frameSamples), buf: make([]byte, maxOpusFrameSize), lossHint: DefaultLossHint}, nil
}

// NewMusicEncoder creates an encoder tuned for general audio (screen share system audio):
// fullband music mode at bitrate bps.
func NewMusicEncoder(sampleRate, frameSamples, bitrate int) (*Encoder, error) {
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: sampleRate, Channels: 1, Application: gopus.ApplicationAudio})
	if err != nil {
		return nil, err
	}
	_ = enc.SetBitrate(bitrate)
	enc.SetVBR(false)
	_ = enc.SetComplexity(8)
	_ = enc.SetInBandFEC(1)
	_ = enc.SetPacketLoss(DefaultLossHint)
	return &Encoder{enc: enc, frameSamples: frameSamples, pcm: make([]int16, frameSamples), buf: make([]byte, maxOpusFrameSize), lossHint: DefaultLossHint}, nil
}

// EncodeInt16 encodes one frame of samples and returns a new packet slice.
func (e *Encoder) EncodeInt16(frame []int16) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	n, err := e.enc.EncodeInt16(frame, e.buf)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), e.buf[:n]...), nil
}

// EncodePCM16LE encodes one frame of little-endian 16-bit PCM and returns a new packet slice.
func (e *Encoder) EncodePCM16LE(frame []byte) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.pcm {
		if 2*i+1 < len(frame) {
			e.pcm[i] = int16(uint16(frame[2*i]) | uint16(frame[2*i+1])<<8)
		} else {
			e.pcm[i] = 0
		}
	}
	n, err := e.enc.EncodeInt16(e.pcm, e.buf)
	if err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, e.buf[:n])
	return out, nil
}

// SetPacketLoss adapts FEC redundancy to the loss reported by receivers.
func (e *Encoder) SetPacketLoss(percent int) {
	percent = max(0, min(percent, 30))
	e.mu.Lock()
	defer e.mu.Unlock()
	if percent == e.lossHint {
		return
	}
	e.lossHint = percent
	_ = e.enc.SetPacketLoss(percent)
}

// PacketLoss returns the current loss hint.
func (e *Encoder) PacketLoss() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lossHint
}

// Decoder decodes Opus packets to 16-bit PCM with concealment support. Not safe for concurrent use.
type Decoder struct {
	dec          *gopus.Decoder
	frameSamples int
	f32          []float32
}

// NewDecoder creates a mono decoder.
func NewDecoder(sampleRate, frameSamples int) (*Decoder, error) {
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(sampleRate, 1))
	if err != nil {
		return nil, err
	}
	return &Decoder{dec: dec, frameSamples: frameSamples, f32: make([]float32, frameSamples)}, nil
}

// Decode decodes packet into out (len frameSamples). A nil packet runs loss concealment.
func (d *Decoder) Decode(packet []byte, out []int16) error {
	n, err := d.dec.DecodeInt16(packet, out)
	if err != nil {
		clear(out)
		return err
	}
	if n < len(out) {
		clear(out[n:])
	}
	return nil
}

// DecodeFEC reconstructs the frame preceding next using its in-band FEC data (falls back to PLC).
func (d *Decoder) DecodeFEC(next []byte, out []int16) error {
	n, err := d.dec.DecodeWithFEC(next, d.f32, true)
	if err != nil {
		return d.Decode(nil, out)
	}
	for i := range out {
		if i >= n {
			out[i] = 0
			continue
		}
		v := d.f32[i] * 32768
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return nil
}
