package p2p

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/thebanri/limoni-voice/internal/engine"
)

// A sound longer than SoundboardMaxSeconds is cut to it and still fits one packet.
func TestSoundboardRoundTrip(t *testing.T) {
	pcm := make([]byte, 0, 8*engine.AudioSampleRate*2)
	for i := range 8 * engine.AudioSampleRate {
		pcm = binary.LittleEndian.AppendUint16(pcm, uint16(int16(8000*math.Sin(float64(i)*0.05))))
	}
	payload, err := encodeSoundboard("tıpa", pcm)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > chatPartSize {
		t.Fatalf("payload is %d bytes, more than one packet's %d", len(payload), chatPartSize)
	}
	name, got, ok := decodeSoundboard(payload)
	if !ok || name != "tıpa" || len(got) != SoundboardMaxSeconds*engine.AudioSampleRate*2 {
		t.Fatalf("decoded %q, %d bytes, ok=%v", name, len(got), ok)
	}
	if _, _, ok := decodeSoundboard(payload[:len(payload)-10]); ok {
		t.Fatal("a cut payload decoded")
	}
}
