package p2p

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

// leavePacket is the authenticated Leave n sends when it leaves the room.
func leavePacket(n *P2PNode) P2PPacket {
	ts := time.Now().UnixMilli()
	n.mu.Lock()
	proof := n.leaveProofLocked(ts)
	n.mu.Unlock()
	return P2PPacket{Type: PacketLeave, RoomCode: rekeyRoom, SenderID: n.LocalID, Nickname: n.LocalID, Payload: proof, Timestamp: ts}
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

	leave := leavePacket(eve)
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

// knowEachOther gives members each other's identity keys, as a join rekey would.
func knowEachOther(nodes ...*P2PNode) {
	for _, a := range nodes {
		for _, b := range nodes {
			if a != b {
				a.memberKeys[b.LocalID] = b.identity.Public()
				if a.Peers[b.LocalID] == nil {
					a.Peers[b.LocalID] = &PeerInfo{ID: b.LocalID, Nickname: b.LocalID, LastSeen: time.Now()}
				}
			}
		}
	}
}

func hostOf(n *P2PNode) (string, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.HostID, n.IsHost
}

// Every member holds the group key, so before leave proofs any member could announce that
// the host left (forcing an election it might win) or that another member left (dropping it
// from the next rekey, i.e. kicking it).
func TestForgedLeaveIgnored(t *testing.T) {
	bob := rekeyMember("b_bob", "c_host")
	mallory := rekeyMember("a_mallory", "c_host") // lowest ID: would win the election
	host := rekeyHost("c_host", bob, mallory)
	knowEachOther(host, bob, mallory)
	defer stopRekey(host)

	// Forgeries reuse the genuine leave's timestamp: they must not poison the replay filter.
	genuine := leavePacket(host)
	ts := genuine.Timestamp
	for _, forged := range []struct {
		claim string
		to    *P2PNode
	}{{"c_host", bob}, {"b_bob", host}} {
		proof, err := mallory.identity.LeaveProof(rekeyRoom, forged.claim, ts,
			[]e2ee.MemberKey{{ID: forged.to.LocalID, Key: forged.to.identity.Public()}})
		if err != nil {
			t.Fatal(err)
		}
		for _, payload := range [][]byte{nil, proof} {
			pkt := P2PPacket{Type: PacketLeave, RoomCode: rekeyRoom, SenderID: forged.claim, Payload: payload, Timestamp: ts}
			forged.to.handlePacket(&pkt, nil)
		}
	}
	if id, _ := hostOf(bob); id != "c_host" || bob.GetPeer("c_host") == nil {
		t.Fatalf("forged host leave accepted: host=%s", id)
	}
	if host.GetPeer("b_bob") == nil {
		t.Fatal("forged member leave dropped bob")
	}

	// The genuine leave still triggers the election.
	bob.handlePacket(&genuine, nil)
	if id, _ := hostOf(bob); id != "a_mallory" {
		t.Fatalf("genuine host leave not processed: host=%s", id)
	}
}

// A member admitted just before the host leaves introduces its key with the host's vouch,
// so the member elected next can still rekey it.
func TestVouchSurvivesHostLeaving(t *testing.T) {
	newHost := rekeyMember("a_next", "c_host")
	newHost.resetMemberTrackingLocked()
	host := rekeyHost("c_host", newHost)
	knowEachOther(host, newHost)
	joiner := rekeyMember("d_join", "c_host")
	defer stopRekey(newHost)

	tags, err := host.identity.VouchTags(rekeyRoom, "c_host", "d_join", joiner.identity.Public(),
		[]e2ee.MemberKey{{ID: "a_next", Key: newHost.identity.Public()}})
	if err != nil {
		t.Fatal(err)
	}
	vouch, err := e2ee.MarshalVouch(e2ee.Vouch{HostID: "c_host", Key: joiner.identity.Public(), Tags: tags})
	if err != nil {
		t.Fatal(err)
	}

	// The host leaves before its join rekey went out.
	leave := leavePacket(host)
	newHost.handlePacket(&leave, nil)
	if _, isHost := hostOf(newHost); !isHost {
		t.Fatal("a_next did not become host")
	}

	// A tampered vouch is refused, the genuine one (from the previous host) accepted.
	bad := append([]byte(nil), vouch...)
	bad[len(bad)-1] ^= 1
	for _, v := range [][]byte{bad, vouch} {
		ping := P2PPacket{Type: PacketPing, RoomCode: rekeyRoom, SenderID: "d_join", Vouch: v, Timestamp: time.Now().UnixMilli()}
		newHost.handlePacket(&ping, nil)
		newHost.mu.RLock()
		got, ok := newHost.memberKeys["d_join"]
		newHost.mu.RUnlock()
		if string(v) == string(bad) && ok {
			t.Fatal("tampered vouch accepted")
		}
		if string(v) == string(vouch) && (!ok || got != joiner.identity.Public()) {
			t.Fatal("previous host's vouch not accepted")
		}
	}
	newHost.mu.Lock()
	sealed := newHost.sealGrantsLocked(5, e2ee.NewGroupKey())
	newHost.mu.Unlock()
	joiner.memberKeys["a_next"] = newHost.identity.Public()
	joiner.HostID = "a_next"
	deliverRekey(joiner, "a_next", 5, sealed["d_join"])
	if e, ok := joiner.keyring.StagedEpoch(); !ok || e != 5 {
		t.Fatal("vouched member did not receive the new host's rekey")
	}

	// A member that left cannot come back through a stale vouch.
	newHost.mu.Lock()
	newHost.forgetMemberLocked("d_join")
	newHost.mu.Unlock()
	ping := P2PPacket{Type: PacketPing, RoomCode: rekeyRoom, SenderID: "d_join", Vouch: vouch, Seq: 2, Timestamp: time.Now().UnixMilli()}
	newHost.handlePacket(&ping, nil)
	newHost.mu.RLock()
	_, back := newHost.memberKeys["d_join"]
	newHost.mu.RUnlock()
	if back {
		t.Fatal("departed member re-added through its old vouch")
	}
}

// The host leaves right after a join, before its join rekey fires: the remaining members
// must still end up on one group key the departed host does not know.
func TestHostLeavesRightAfterJoin(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "hj_host", "Alice", relayURL)
	a := newRelayNode(t, "hj_a", "Bob", relayURL)
	b := newRelayNode(t, "hj_b", "Carol", relayURL)
	var chatA, chatB chatSink
	a.OnChatMessage = chatA.add
	b.OnChatMessage = chatB.add

	code := "8383-amber-falcon-river"
	host.HostRoom(code)
	waitFor(t, "host registered", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	if res := joinAndWait(t, a, code, 5*time.Second); !res.ok {
		t.Fatalf("a join failed: %+v", res)
	}
	waitFor(t, "a's join settled", 8*time.Second, func() bool {
		a.mu.RLock()
		defer a.mu.RUnlock()
		_, staged := a.keyring.StagedEpoch()
		return !staged && a.keyring != nil
	})
	if res := joinAndWait(t, b, code, 5*time.Second); !res.ok {
		t.Fatalf("b join failed: %+v", res)
	}
	host.mu.RLock()
	oldRing := host.keyring
	host.mu.RUnlock()
	host.LeaveRoom() // within the 2 s join-rekey debounce

	waitFor(t, "a and b share a key the old host lacks", 12*time.Second, func() bool {
		a.mu.RLock()
		ae, ak := a.keyring.Current()
		a.mu.RUnlock()
		b.mu.RLock()
		be, bk := b.keyring.Current()
		b.mu.RUnlock()
		if ae != be || ak != bk {
			return false
		}
		pkt := P2PPacket{Type: PacketChatMessage, RoomCode: a.roomID, SenderID: a.LocalID, Payload: []byte("x"), Timestamp: time.Now().UnixMilli()}
		sealed, err := sealPacket(&pkt, a.keyring)
		if err != nil {
			return false
		}
		var out P2PPacket
		return openPacket(sealed, &out, oldRing) != nil
	})
	a.SendChatMessage("from a")
	b.SendChatMessage("from b")
	waitFor(t, "chat both ways", 3*time.Second, func() bool { return chatB.has("from a") && chatA.has("from b") })
}
