package main

import (
	"crypto/sha256"
	"encoding/binary"
	"time"

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

// signedLeave returns a Leave from senderID that n accepts, setting up identity keys the
// way a real room would: n gets an identity and a host-vouched key for the sender.
func signedLeave(n *P2PNode, senderID, nick string) P2PPacket {
	return signedLeaveAt(n, senderID, nick, time.Now().UnixMilli())
}

func signedLeaveAt(n *P2PNode, senderID, nick string, ts int64) P2PPacket {
	sender := e2ee.NewIdentity()
	n.mu.Lock()
	if n.identity == nil {
		n.identity = e2ee.NewIdentity()
	}
	if n.memberKeys == nil {
		n.memberKeys = make(map[string]e2ee.PublicKey)
	}
	n.memberKeys[senderID] = sender.Public()
	roomID, self, selfKey := n.roomID, n.LocalID, n.identity.Public()
	n.mu.Unlock()
	proof, err := sender.LeaveProof(roomID, senderID, ts, []e2ee.MemberKey{{ID: self, Key: selfKey}})
	if err != nil {
		panic(err)
	}
	return P2PPacket{Type: PacketLeave, RoomCode: roomID, SenderID: senderID, Nickname: nick, Payload: proof, Timestamp: ts}
}
