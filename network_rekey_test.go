package main

import (
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/e2ee"
)

const rekeyRoom = "4242"

// rekeyMember returns a connected non-host member of rekeyRoom with an identity key.
func rekeyMember(id, hostID string) *P2PNode {
	n := testRoomNode(id, rekeyRoom)
	n.HostID = hostID
	n.identity = e2ee.NewIdentity()
	n.memberKeys = make(map[string]e2ee.PublicKey)
	return n
}

// rekeyHost returns a host of rekeyRoom whose peers are the given members.
func rekeyHost(id string, members ...*P2PNode) *P2PNode {
	h := rekeyMember(id, id)
	h.IsHost = true
	h.joinSessions = make(map[string]*joinSession)
	for _, m := range members {
		h.Peers[m.LocalID] = &PeerInfo{ID: m.LocalID, Nickname: m.LocalID, LastSeen: time.Now()}
		h.memberKeys[m.LocalID] = m.identity.Public()
		m.memberKeys[id] = h.identity.Public()
	}
	return h
}

func stopRekey(n *P2PNode) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.rekeyTimer != nil {
		n.rekeyTimer.Stop()
		n.rekeyTimer = nil
	}
}

func deliverRekey(m *P2PNode, from string, epoch uint32, payload []byte) {
	pkt := P2PPacket{
		Type:      PacketRekey,
		RoomCode:  rekeyRoom,
		SenderID:  from,
		TargetID:  m.LocalID,
		Epoch:     epoch,
		Payload:   payload,
		Timestamp: time.Now().UnixMilli(),
	}
	m.mu.Lock()
	m.handleRekeyLocked(&pkt, nil)
	m.mu.Unlock()
}

// A member holds the group key, so it can build a packet claiming to come from the host.
// Before per-member sealing, a rekey with a huge epoch from such a member was installed and
// pinned the room to a key the member knew, even after it was removed.
func TestMemberCannotForgeRekey(t *testing.T) {
	bob := rekeyMember("bob", "host")
	mallory := rekeyMember("mallory", "host")
	host := rekeyHost("host", bob, mallory)
	bob.memberKeys["mallory"] = mallory.identity.Public()

	forged, err := mallory.identity.SealGrant(rekeyRoom, "host", "bob", bob.identity.Public(),
		e2ee.KeyGrant{Epoch: 1 << 30, Key: e2ee.NewGroupKey()})
	if err != nil {
		t.Fatal(err)
	}
	deliverRekey(bob, "host", 1<<30, forged)
	if e, ok := bob.keyring.StagedEpoch(); ok {
		t.Fatalf("forged rekey staged epoch %d", e)
	}

	// The genuine host rekey still goes through afterwards.
	newKey := e2ee.NewGroupKey()
	host.mu.Lock()
	sealed := host.sealGrantsLocked(2, newKey)
	host.mu.Unlock()
	deliverRekey(bob, "host", 2, sealed["bob"])
	if e, ok := bob.keyring.StagedEpoch(); !ok || e != 2 {
		t.Fatalf("genuine rekey not staged: epoch=%d ok=%v", e, ok)
	}
	if got := bob.memberKeys["mallory"]; got != mallory.identity.Public() {
		t.Fatal("rekey did not deliver the member directory")
	}
}

// A member that left keeps its identity and the old group key and may see the rekey
// packets on the wire. It gets no grant of its own and cannot open anyone else's.
func TestDepartedMemberCannotReadRekey(t *testing.T) {
	bob := rekeyMember("bob", "host")
	eve := rekeyMember("eve", "host")
	host := rekeyHost("host", bob, eve)
	defer stopRekey(host)

	leave := P2PPacket{Type: PacketLeave, RoomCode: rekeyRoom, SenderID: "eve", Nickname: "eve", Timestamp: time.Now().UnixMilli()}
	host.handlePacket(&leave, nil)

	host.mu.Lock()
	if host.rekeyTimer == nil {
		t.Error("leave did not schedule a rekey")
	}
	sealed := host.sealGrantsLocked(2, e2ee.NewGroupKey())
	host.mu.Unlock()
	if _, ok := sealed["eve"]; ok {
		t.Fatal("departed member still receives rekeys")
	}
	if len(sealed) != 1 {
		t.Fatalf("expected a grant for bob only, got %d", len(sealed))
	}

	// eve sees bob's rekey on the wire and can strip the outer group-key layer.
	pkt := P2PPacket{Type: PacketRekey, RoomCode: rekeyRoom, SenderID: "host", TargetID: "bob", Epoch: 2, Payload: sealed["bob"], Timestamp: time.Now().UnixMilli()}
	wire, err := sealPacket(&pkt, host.keyring)
	if err != nil {
		t.Fatal(err)
	}
	var seen P2PPacket
	if err := openPacket(wire, &seen, eve.keyring); err != nil {
		t.Fatalf("test setup: eve should read the outer packet: %v", err)
	}
	for _, claim := range []string{"bob", "eve"} {
		if _, err := eve.identity.OpenGrant(rekeyRoom, "host", claim, host.identity.Public(), seen.Payload); err == nil {
			t.Fatalf("departed member opened the new group key (as %s)", claim)
		}
	}
	deliverRekey(eve, "host", 2, seen.Payload)
	if _, ok := eve.keyring.StagedEpoch(); ok {
		t.Fatal("departed member installed the new group key")
	}

	deliverRekey(bob, "host", 2, seen.Payload)
	if e, ok := bob.keyring.StagedEpoch(); !ok || e != 2 {
		t.Fatal("remaining member did not install the new group key")
	}
}

// A new host must be able to rekey the room with the keys the original host vouched for.
func TestGroupKeyRotatesAfterHostLeaves(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "hm_host", "Alice", relayURL)
	a := newRelayNode(t, "hm_a", "Bob", relayURL)
	b := newRelayNode(t, "hm_b", "Carol", relayURL)
	var chat chatSink
	a.OnChatMessage = chat.add
	b.OnChatMessage = chat.add

	code := "8282-amber-falcon-river"
	host.HostRoom(code)
	waitFor(t, "host registered", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	for _, n := range []*P2PNode{a, b} {
		if res := joinAndWait(t, n, code, 5*time.Second); !res.ok {
			t.Fatalf("%s join failed: %+v", n.LocalID, res)
		}
	}
	// The join rekeys spread every member's identity key to the others.
	waitFor(t, "members know each other's keys", 8*time.Second, func() bool {
		a.mu.RLock()
		_, aKnowsB := a.memberKeys[b.LocalID]
		a.mu.RUnlock()
		b.mu.RLock()
		_, bKnowsA := b.memberKeys[a.LocalID]
		b.mu.RUnlock()
		return aKnowsB && bKnowsA
	})

	// Let the join rekeys finish so the departing host's last key is the room's current one.
	waitFor(t, "join rekeys settled", 8*time.Second, func() bool {
		var epochs []uint32
		for _, n := range []*P2PNode{host, a, b} {
			n.mu.RLock()
			e, _ := n.keyring.Current()
			_, staged := n.keyring.StagedEpoch()
			n.mu.RUnlock()
			if staged {
				return false
			}
			epochs = append(epochs, e)
		}
		return epochs[0] == epochs[1] && epochs[1] == epochs[2]
	})
	host.mu.RLock()
	oldRing := host.keyring
	host.mu.RUnlock()
	startEpoch, _ := oldRing.Current()

	host.LeaveRoom()
	var newHost, member *P2PNode
	waitFor(t, "new host elected", 5*time.Second, func() bool {
		for _, n := range []*P2PNode{a, b} {
			n.mu.RLock()
			isHost := n.IsHost
			n.mu.RUnlock()
			if isHost {
				newHost = n
			} else {
				member = n
			}
		}
		return newHost != nil && member != nil
	})
	waitFor(t, "rekey by the new host", 10*time.Second, func() bool {
		newHost.mu.RLock()
		he, _ := newHost.keyring.Current()
		newHost.mu.RUnlock()
		member.mu.RLock()
		me, _ := member.keyring.Current()
		member.mu.RUnlock()
		return he > startEpoch && me == he
	})

	pkt := P2PPacket{Type: PacketChatMessage, RoomCode: newHost.roomID, SenderID: newHost.LocalID, Payload: []byte("secret"), Timestamp: time.Now().UnixMilli()}
	newHost.mu.RLock()
	sealed, err := sealPacket(&pkt, newHost.keyring)
	newHost.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	var out P2PPacket
	if err := openPacket(sealed, &out, oldRing); err == nil {
		t.Fatal("departed host can still decrypt new traffic")
	}

	newHost.SendChatMessage("after host change")
	waitFor(t, "chat after host change", 3*time.Second, func() bool { return chat.has("after host change") })
}
