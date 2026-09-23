package p2p

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/thebanri/limoni-voice/internal/e2ee"
	"github.com/thebanri/limoni-voice/internal/protocol"
)

// Removing members.
//
// The host removes a member by telling the room, with a proof only the host can make, and
// then rotating the group key at once, so the removed member cannot follow the room even if
// it ignores the notice. The relay, when it supports it, drops the member as well. A ban
// also refuses the member's ID and addresses for as long as the room lives; a member that
// was only kicked may knock again.

// ErrNotHost is returned when a host-only action is attempted by a member.
var ErrNotHost = errors.New("only the host can do that")

// KickMember removes a member from the room. With ban it cannot come back while the room
// exists.
func (n *P2PNode) KickMember(id string, ban bool) error {
	n.mu.Lock()
	if !n.IsHost || !n.IsConnected {
		n.mu.Unlock()
		return ErrNotHost
	}
	peer := n.Peers[id]
	if peer == nil || id == n.LocalID {
		n.mu.Unlock()
		return fmt.Errorf("no member %q in the room", id)
	}
	ts := time.Now().UnixMilli()
	pkt := P2PPacket{
		Type:      PacketKick,
		RoomCode:  n.roomID,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		TargetID:  id,
		Payload:   n.kickPayloadLocked(id, ban, ts),
		Timestamp: ts,
	}
	nick := peer.Nickname
	n.mu.Unlock()

	// Tell the room (the target included) while the target is still a member, so the notice
	// is sealed with a key everyone holds and reaches it over the paths we know.
	n.sendToRoom(&pkt, protocol.FrameReliable)

	n.mu.Lock()
	if ban {
		n.banLocked(id, peer)
	}
	n.removeMemberLocked(id, ban)
	if n.rekeyTimer != nil {
		n.rekeyTimer.Stop()
		n.rekeyTimer = nil
	}
	n.mu.Unlock()

	n.sendRelaySignal(protocol.Signal{Type: protocol.SigKick, Target: id, Ban: ban})
	go n.rotateGroupKey("member removed")
	if ban {
		n.log(fmt.Sprintf("[HOST] %s was banned from the room.", nick))
	} else {
		n.log(fmt.Sprintf("[HOST] %s was removed from the room.", nick))
	}
	return nil
}

// kickPayloadLocked builds [ban flag] + a proof for every member we hold a key for.
func (n *P2PNode) kickPayloadLocked(target string, ban bool, ts int64) []byte {
	flag := byte(0)
	if ban {
		flag = 1
	}
	payload := []byte{flag}
	if n.identity == nil {
		return payload
	}
	members := make([]e2ee.MemberKey, 0, len(n.memberKeys))
	for id, key := range n.memberKeys {
		members = append(members, e2ee.MemberKey{ID: id, Key: key})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	proof, err := n.identity.KickProof(n.roomID, n.LocalID, target, ban, ts, members)
	if err != nil {
		return payload
	}
	return append(payload, proof...)
}

// banLocked remembers a banned member's ID and addresses. Host only.
func (n *P2PNode) banLocked(id string, peer *PeerInfo) {
	if n.bannedIDs == nil {
		n.bannedIDs = make(map[string]bool)
	}
	if n.bannedIPs == nil {
		n.bannedIPs = make(map[string]bool)
	}
	n.bannedIDs[id] = true
	if peer == nil {
		return
	}
	if peer.Addr != nil && !peer.Addr.IP.IsLoopback() {
		n.bannedIPs[peer.Addr.IP.String()] = true
	}
	for _, ip := range []string{peer.Endpoint.LocalIP, peer.Endpoint.PublicIP} {
		if parsed := net.ParseIP(ip); parsed != nil && !parsed.IsLoopback() {
			n.bannedIPs[parsed.String()] = true
		}
	}
}

// isBannedLocked reports whether a joiner is refused by a ban. addr may be nil.
func (n *P2PNode) isBannedLocked(id string, addr *net.UDPAddr) bool {
	if n.bannedIDs[id] {
		return true
	}
	return addr != nil && n.bannedIPs[addr.IP.String()]
}

// removeMemberLocked drops a member the host removed, so stray packets it still sends with
// the old group key do not bring it back until the host admits it again.
func (n *P2PNode) removeMemberLocked(id string, ban bool) {
	if n.removed == nil {
		n.removed = make(map[string]bool)
	}
	n.removed[id] = true
	peer := n.Peers[id]
	if peer == nil {
		n.forgetMemberLocked(id)
		return
	}
	wasSharing := peer.IsSharingScreen
	delete(n.Peers, id)
	n.forgetMemberLocked(id)
	if n.audio != nil {
		n.audio.RemovePeer(id)
	}
	if !n.IsHost && ban {
		n.log(fmt.Sprintf("[-] The host banned %s.", peer.Nickname))
	} else if !n.IsHost {
		n.log(fmt.Sprintf("[-] The host removed %s.", peer.Nickname))
	}
	if n.OnPeerEvent != nil {
		go n.OnPeerEvent("leave", peer)
	}
	if wasSharing && n.IsWatchingScreen && (n.WatchingPeerID == id || n.WatchingPeerID == "") {
		go func() { _ = n.StopWatchingScreen() }()
	}
}

// handleKickLocked applies a removal announced by the host.
func (n *P2PNode) handleKickLocked(pkt *P2PPacket) {
	if n.IsHost || pkt.SenderID != n.HostID || len(pkt.Payload) < 1 || n.identity == nil {
		return
	}
	hostKey, ok := n.memberKeys[n.HostID]
	if !ok {
		return
	}
	ban := pkt.Payload[0] == 1
	if !n.identity.CheckKick(n.roomID, n.LocalID, n.HostID, pkt.TargetID, hostKey, ban, pkt.Timestamp, pkt.Payload[1:]) {
		n.writeToFileLog(fmt.Sprintf("[SECURITY] Ignored an unauthenticated removal of %s", pkt.TargetID))
		return
	}
	if !n.ctrlDedup.ShouldProcess(pkt.SenderID, pkt.Type, pkt.Seq, pkt.Timestamp) {
		return
	}
	if pkt.TargetID == n.LocalID {
		n.kickedLocked(ban)
		return
	}
	n.removeMemberLocked(pkt.TargetID, ban)
}

// kickedLocked handles our own removal, from the host's notice or from the relay.
func (n *P2PNode) kickedLocked(ban bool) {
	if !n.IsConnected || n.kickedHandled {
		return
	}
	n.kickedHandled = true
	host := n.HostNick
	if ban {
		n.log(fmt.Sprintf("[SECURITY] %s banned you from the room.", host))
	} else {
		n.log(fmt.Sprintf("[SECURITY] %s removed you from the room.", host))
	}
	cb := n.OnKicked
	go func() {
		n.LeaveRoom()
		if cb != nil {
			cb(host, ban)
		}
	}()
}

// ToggleKnockToJoin switches knock-to-join and reports whether it is now on.
func (n *P2PNode) ToggleKnockToJoin() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.KnockToJoin = !n.KnockToJoin
	return n.KnockToJoin
}

// HopEpoch returns the current port hopping epoch.
func (n *P2PNode) HopEpoch() uint32 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.currentEpoch
}
