package p2p

import (
	"crypto/hmac"
	"fmt"
	"net"
	"time"

	"github.com/thebanri/limoni-voice/internal/e2ee"
	"github.com/thebanri/limoni-voice/internal/protocol"
)

// Group key recovery.
//
// The host rotates the room key on a schedule and after membership changes, and promotes the
// new key even when a member never acknowledged it. A member that missed the rotation can no
// longer read the room, and the host stops reading that member once the previous key's grace
// ends: both sides see the other as reconnecting, forever, because every packet they exchange
// is sealed with a key the other side does not hold.
//
// A member that cannot open what arrives therefore asks the host for the current key. The
// request travels in the plaintext room-tagged envelope the LAN handshake uses, since the
// group key is exactly what the member is missing; the answer is a key grant sealed to the
// member's identity key, so only that member can read it and no one else can forge one.
const (
	hsKeyRequest byte = 5
	hsKeyGrant   byte = 6
)

const (
	// keyRequestInterval is the shortest gap between two key requests, and on the host the
	// shortest gap between two answers to the same member.
	keyRequestInterval = 2 * time.Second
	// undecryptableRun is how many unreadable packets in a row count as "we lost the key"
	// rather than a stray packet from a member that is mid-rotation.
	undecryptableRun = 5
)

// noteUndecryptable records a packet that could not be opened. Voice alone delivers fifty
// packets a second, so the counters are atomic and the slow path runs at most every couple
// of seconds.
func (n *P2PNode) noteUndecryptable() {
	if n.undecryptable.Add(1) < undecryptableRun {
		return
	}
	now := time.Now()
	last := n.keyRequestAt.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) < keyRequestInterval {
		return
	}
	if !n.keyRequestAt.CompareAndSwap(last, now.UnixNano()) {
		return
	}

	n.mu.RLock()
	host, isHost, connected := n.HostID, n.IsHost, n.IsConnected
	n.mu.RUnlock()
	if !connected {
		return
	}
	if isHost || host == "" || host == n.LocalID {
		// The host holds the key; a member that lost it asks for it itself.
		n.debugLog("[E2EE] Dropping packets we cannot open: a member is sealing with another group key")
		return
	}
	n.log("[E2EE] Cannot open room packets: asking the host for the current group key")
	n.requestGroupKey(host)
}

// noteDecrypted clears the run of unreadable packets after one opens.
func (n *P2PNode) noteDecrypted() {
	if n.undecryptable.Load() != 0 {
		n.undecryptable.Store(0)
	}
}

// requestGroupKey asks the host to send the current group key again.
func (n *P2PNode) requestGroupKey(host string) {
	n.mu.RLock()
	frame := n.lanFrameLocked(hsKeyRequest, n.LocalID, nil)
	roomID := n.roomID
	n.mu.RUnlock()
	if roomID == "" {
		return
	}
	n.sendRawToMember(host, frame)
}

// sendRawToMember sends an unsealed frame to one member over whichever paths are known: the
// direct one, and the relay, which forwards frames without reading them.
func (n *P2PNode) sendRawToMember(id string, frame []byte) {
	n.mu.RLock()
	var addr *net.UDPAddr
	var conn *net.UDPConn
	viaRelay := true
	if peer, ok := n.Peers[id]; ok {
		addr, conn = peer.Addr, peer.conn
		viaRelay = peer.ViaRelay || peer.Addr == nil
	}
	isRelay, targeted := n.isRelayConnected, n.relayTargeted
	n.mu.RUnlock()

	if addr != nil {
		n.writeUDP(frame, addr, conn)
	}
	if viaRelay && isRelay {
		if targeted {
			n.sendRelayTo(protocol.FrameReliable, id, frame)
		} else {
			n.sendRelayFrame(protocol.FrameReliable, frame) // other members ignore it
		}
	}
}

// handleKeyFrame consumes a key request or a key grant. It reports whether the datagram was
// one; raddr is nil for frames that arrived through the relay.
func (n *P2PNode) handleKeyFrame(data []byte, raddr *net.UDPAddr) bool {
	if len(data) < lanTagSize+2 {
		return false
	}
	kind := data[lanTagSize]
	if kind != hsKeyRequest && kind != hsKeyGrant {
		return false
	}
	n.mu.RLock()
	roomID := n.roomID
	n.mu.RUnlock()
	if roomID == "" || !hmac.Equal(data[:lanTagSize], lanHandshakeTag(roomID)) {
		return false
	}
	id, body, ok := readShort(data[lanTagSize+1:])
	if !ok || id == n.LocalID {
		return true
	}
	if kind == hsKeyRequest {
		n.answerKeyRequest(id, raddr)
	} else {
		n.installKeyGrant(id, body)
	}
	return true
}

// keyGrantFrameLocked seals the group key the room is sealing with for one member. It refuses
// members the host does not know, and answers each member at most once every couple of seconds.
func (n *P2PNode) keyGrantFrameLocked(memberID string) ([]byte, uint32, bool) {
	if !n.IsHost || !n.IsConnected || n.keyring == nil || n.identity == nil {
		return nil, 0, false
	}
	if last, ok := n.keyAnswers[memberID]; ok && time.Since(last) < keyRequestInterval {
		return nil, 0, false
	}
	memberKey, known := n.memberKeys[memberID]
	if !known {
		return nil, 0, false
	}
	// Hand out the key that is about to be active when a rotation is still in flight, so the
	// member does not have to ask again moments later.
	epoch, key := n.keyring.Current()
	if staged, ok := n.keyring.StagedEpoch(); ok && staged > epoch {
		epoch, key = n.keyring.StagedKey()
	}
	grant := e2ee.KeyGrant{Epoch: epoch, Key: key, Members: n.memberDirectoryLocked(n.rekeyRecipientsLocked())}
	sealed, err := n.identity.SealGrant(n.roomID, n.LocalID, memberID, memberKey, grant)
	if err != nil {
		return nil, 0, false
	}
	if n.keyAnswers == nil {
		n.keyAnswers = map[string]time.Time{}
	}
	n.keyAnswers[memberID] = time.Now()
	return n.lanFrameLocked(hsKeyGrant, n.LocalID, append(appendShort(nil, memberID), sealed...)), epoch, true
}

// answerKeyRequest re-sends the current group key to a member that lost it.
func (n *P2PNode) answerKeyRequest(memberID string, raddr *net.UDPAddr) {
	n.mu.Lock()
	frame, epoch, ok := n.keyGrantFrameLocked(memberID)
	nick := memberID
	if peer, found := n.Peers[memberID]; found {
		nick = peer.Nickname
	}
	n.mu.Unlock()
	if !ok {
		return
	}

	if raddr != nil {
		n.writeUDP(frame, raddr, nil)
	}
	n.sendRawToMember(memberID, frame)
	n.log(fmt.Sprintf("[E2EE] %s lost the group key; sent it the current one (epoch %d)", nick, epoch))
}

// installKeyGrant adopts a group key the host sent after a request.
func (n *P2PNode) installKeyGrant(hostID string, body []byte) {
	target, sealed, ok := readShort(body)
	if !ok || target != n.LocalID || len(sealed) == 0 {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.IsHost || n.keyring == nil || n.identity == nil || hostID != n.HostID {
		return
	}
	hostKey, known := n.memberKeys[hostID]
	if !known {
		return
	}
	grant, err := n.identity.OpenGrant(n.roomID, hostID, n.LocalID, hostKey, sealed)
	if err != nil {
		n.writeToFileLog(fmt.Sprintf("[SECURITY] Dropped a group key that is not sealed for us: %v", err))
		return
	}
	cur, _ := n.keyring.Current()
	if grant.Epoch <= cur {
		return
	}
	if err := n.keyring.Stage(grant.Epoch, grant.Key); err != nil {
		return
	}
	// The room already moved on, so take the key up at once rather than after a grace.
	n.keyring.Promote()
	n.setMemberKeysLocked(grant.Members)
	n.undecryptable.Store(0)
	n.log(fmt.Sprintf("[E2EE] Group key recovered from the host (epoch %d)", grant.Epoch))
}
