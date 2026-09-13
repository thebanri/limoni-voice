package main

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/thebanri/limoni-voice/internal/e2ee"
)

// testKeyring returns a deterministic room keyring for unit tests.
func testKeyring(seed string) *e2ee.Keyring {
	ring, err := e2ee.NewKeyring(1, e2ee.GroupKey(sha256.Sum256([]byte(seed))))
	if err != nil {
		panic(err)
	}
	return ring
}

// testRoomNode returns a connected node that accepts packets for roomID without a network.
func testRoomNode(localID, roomID string) *P2PNode {
	n := NewP2PNode(localID, localID, NewAudioEngine())
	n.RoomCode = roomID
	n.roomID = roomID
	n.keyring = testKeyring(roomID)
	n.IsConnected = true
	return n
}

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
