package p2p

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/protocol"
	"github.com/thebanri/limoni-voice/internal/voice"
)

// A /music sound goes to the room whole, in one packet: [name length][name], then each 20 ms
// Opus frame as [length][frame]. Members with sound effects off (/sfx) do not play it.
const (
	// SoundboardMaxSeconds is the most of a sound that is sent; at soundboardBitrate a frame
	// is 50 bytes, so this much fits one packet, as a chat part does.
	SoundboardMaxSeconds = 5
	soundboardBitrate    = 20000
	soundboardMaxFrames  = SoundboardMaxSeconds * engine.AudioSampleRate / engine.AudioFrameSamples
	soundboardMaxName    = 32
)

// SendSoundboard sends pcm (the engine's mono 16-bit PCM) to the room as the sound name.
func (n *P2PNode) SendSoundboard(name string, pcm []byte) error {
	payload, err := encodeSoundboard(name, pcm)
	if err != nil {
		return err
	}
	n.mu.Lock()
	if !n.IsConnected {
		n.mu.Unlock()
		return nil
	}
	room := n.roomID
	n.seqCounter++
	seq := n.seqCounter
	n.mu.Unlock()
	n.sendToRoom(&P2PPacket{
		Type:      PacketSoundboard,
		RoomCode:  room,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		Seq:       seq,
		Payload:   payload,
		Timestamp: time.Now().UnixMilli(),
	}, protocol.FrameReliable)
	return nil
}

func encodeSoundboard(name string, pcm []byte) ([]byte, error) {
	if len(name) > soundboardMaxName {
		return nil, errors.New("sound name too long")
	}
	enc, err := voice.NewMusicEncoder(engine.AudioSampleRate, engine.AudioFrameSamples, soundboardBitrate)
	if err != nil {
		return nil, err
	}
	payload := append([]byte{byte(len(name))}, name...)
	frame := make([]byte, engine.AudioChunkSize)
	for i := 0; i < len(pcm) && i < soundboardMaxFrames*engine.AudioChunkSize; i += engine.AudioChunkSize {
		clear(frame)
		copy(frame, pcm[i:])
		opus, err := enc.EncodePCM16LE(frame)
		if err != nil {
			return nil, err
		}
		if len(opus) > 255 {
			return nil, errors.New("sound frame too large")
		}
		payload = append(append(payload, byte(len(opus))), opus...)
	}
	return payload, nil
}

// decodeSoundboard is the name and PCM of a sound encodeSoundboard made; ok is false for a
// malformed one.
func decodeSoundboard(payload []byte) (name string, pcm []byte, ok bool) {
	if len(payload) == 0 || int(payload[0]) > soundboardMaxName || 1+int(payload[0]) > len(payload) {
		return "", nil, false
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(payload[1:1+payload[0]]), ""))
	rest := payload[1+payload[0]:]
	dec, err := voice.NewDecoder(engine.AudioSampleRate, engine.AudioFrameSamples)
	if err != nil {
		return "", nil, false
	}
	out := make([]int16, engine.AudioFrameSamples)
	for frames := 0; len(rest) > 0 && frames < soundboardMaxFrames; frames++ {
		l := int(rest[0])
		if 1+l > len(rest) || dec.Decode(rest[1:1+l], out) != nil {
			return "", nil, false
		}
		for _, s := range out {
			pcm = binary.LittleEndian.AppendUint16(pcm, uint16(s))
		}
		rest = rest[1+l:]
	}
	return name, pcm, len(pcm) > 0
}

func (n *P2PNode) playSoundboard(nickname string, payload []byte) {
	name, pcm, ok := decodeSoundboard(payload)
	if !ok {
		return
	}
	n.audio.PlaySoundboard(pcm)
	n.log(fmt.Sprintf("[SFX] %s played /%s", nickname, name))
}
