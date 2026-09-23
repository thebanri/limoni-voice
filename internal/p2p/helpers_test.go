package p2p

import (
	"crypto/sha256"
	"time"

	"github.com/thebanri/limoni-voice/internal/e2ee"
	"github.com/thebanri/limoni-voice/internal/engine"
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
	n := NewP2PNode(localID, localID, engine.NewAudioEngine())
	n.RoomCode = roomID
	n.roomID = roomID
	n.keyring = testKeyring(roomID)
	n.IsConnected = true
	return n
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
