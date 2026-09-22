package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/thebanri/limoni-voice/internal/e2ee"
	"github.com/thebanri/limoni-voice/internal/protocol"
)

// Join handshake message kinds, carried in relay "pake" signals and LAN handshake frames.
const (
	hsHello     byte = 1
	hsChallenge byte = 2
	hsAuth      byte = 3
	hsResult    byte = 4
)

const (
	lanTagSize             = 16
	maxJoinSessions        = 16
	joinSessionTTL         = 45 * time.Second
	handshakeFailureWindow = time.Minute
	handshakeFailureLimit  = 10
	handshakePause         = 30 * time.Second
)

// joinSession is the host-side state of one joiner handshake.
type joinSession struct {
	server   *e2ee.JoinServer
	hello    []byte
	auth     []byte
	result   []byte
	status   byte
	nickname string
	lanAddr  *net.UDPAddr
	viaRelay bool

	// Knock-to-join: a verified joiner waiting for the host's decision.
	pin      string
	identity e2ee.PublicKey
}

// knockWindow is how long the host has to let a knocking joiner in. The public relay drops
// joiners it has not seen admitted after 30 s, so the decision has to come before that.
const knockWindow = 25 * time.Second

func lanHandshakeTag(roomID string) []byte {
	m := hmac.New(sha256.New, []byte("limoni-lan-handshake-v2"))
	m.Write([]byte(roomID))
	return m.Sum(nil)[:lanTagSize]
}

func appendShort(dst []byte, s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	dst = append(dst, byte(len(s)))
	return append(dst, s...)
}

func readShort(b []byte) (string, []byte, bool) {
	if len(b) < 1 || int(b[0]) > len(b)-1 {
		return "", nil, false
	}
	return string(b[1 : 1+int(b[0])]), b[1+int(b[0]):], true
}

// --- joiner side ---

func (n *P2PNode) ensureJoinClientLocked() error {
	if n.joinClient != nil {
		return nil
	}
	if n.roomID == "" || n.roomSecret == "" {
		return errors.New("no room code")
	}
	if n.identity == nil {
		n.identity = e2ee.NewIdentity()
	}
	client, _, err := e2ee.NewJoinClient(n.roomID, n.roomSecret, n.LocalID, n.identity.Public())
	if err != nil {
		return err
	}
	n.joinClient = client
	return nil
}

// handleJoinPending starts the handshake once the relay accepted our join request.
func (n *P2PNode) handleJoinPending(msg protocol.Signal) {
	if msg.Proto < protocol.SignalVersion {
		n.failJoin("Relay server is outdated: please update the relay server")
		return
	}
	n.mu.Lock()
	if !n.Connecting || n.IsConnected {
		n.mu.Unlock()
		return
	}
	if err := n.ensureJoinClientLocked(); err != nil {
		n.mu.Unlock()
		n.failJoin("Invalid room key")
		return
	}
	n.joinHostID = msg.SenderID
	n.HostNick = msg.Nickname
	hello := n.joinClient.Hello()
	n.mu.Unlock()

	n.debugLog(fmt.Sprintf("[E2EE] Relay accepted join request, verifying room key with host %s", msg.Nickname))
	n.sendRelaySignal(protocol.Signal{Type: protocol.SigPake, Target: msg.SenderID, Data: append([]byte{hsHello}, hello...)})
}

// processChallenge verifies the host knows the room secret and returns our auth message.
func (n *P2PNode) processChallenge(hostID, hostNick string, challenge []byte, lanAddr *net.UDPAddr) ([]byte, bool) {
	n.mu.Lock()
	if !n.Connecting || n.IsConnected || n.joinClient == nil {
		n.mu.Unlock()
		return nil, false
	}
	if n.joinAuth != nil {
		auth := n.joinAuth
		sameHost := n.joinHostID == hostID
		n.mu.Unlock()
		return auth, sameHost
	}
	auth, err := n.joinClient.HandleChallenge(challenge, hostID, n.RoomPIN, n.Nickname)
	if err != nil {
		n.mu.Unlock()
		if errors.Is(err, e2ee.ErrWrongCode) {
			n.failJoin("Wrong room key: the host could not verify it")
		} else {
			n.failJoin("Handshake with host failed")
		}
		return nil, false
	}
	n.joinHostID = hostID
	if hostNick != "" {
		n.HostNick = hostNick
	}
	n.joinHostAddr = lanAddr
	n.joinAuth = auth
	n.mu.Unlock()
	return auth, true
}

func handshakeStatusReason(status byte) string {
	switch status {
	case e2ee.StatusPinRequired:
		return "Room is protected by PIN (join with code:PIN)"
	case e2ee.StatusLocked:
		return "Room is locked by host"
	case e2ee.StatusFull:
		return "This room is full! (Maximum 4 people)"
	case e2ee.StatusBusy:
		return "Host is temporarily refusing joins after repeated failed attempts, try again shortly"
	case e2ee.StatusDenied:
		return "The host declined your join request"
	case e2ee.StatusOutdated:
		return "Your Limoni Voice is older than the host's: update to the latest version"
	default:
		return "The host rejected the join request"
	}
}

// processResult installs the group key delivered by the host.
func (n *P2PNode) processResult(hostID string, result []byte, lanAddr *net.UDPAddr) {
	if len(result) == 0 {
		return
	}
	if result[0] == e2ee.StatusWaiting {
		n.mu.Lock()
		first := n.Connecting && !n.IsConnected && n.joinWaitUntil.IsZero()
		if first {
			n.joinWaitUntil = time.Now().Add(knockWindow + 5*time.Second)
		}
		cb := n.OnJoinWaiting
		n.mu.Unlock()
		if first {
			n.log("[E2EE] Room key verified; waiting for the host to let you in")
			if cb != nil {
				go cb()
			}
		}
		return
	}
	if result[0] != e2ee.StatusOK {
		n.failJoin(handshakeStatusReason(result[0]))
		return
	}
	n.mu.Lock()
	if !n.Connecting || n.IsConnected || n.joinClient == nil || (n.joinHostID != "" && n.joinHostID != hostID) {
		n.mu.Unlock()
		return
	}
	if n.keyring != nil {
		n.mu.Unlock()
		return // duplicate result
	}
	_, grant, err := n.joinClient.HandleResult(result)
	if errors.Is(err, e2ee.ErrOutdatedPeer) {
		n.mu.Unlock()
		n.failJoin("The host runs an older Limoni Voice: both sides need the latest version")
		return
	}
	if err != nil {
		n.mu.Unlock()
		n.failJoin("Handshake with host failed")
		return
	}
	keyring, err := e2ee.NewKeyring(grant.Epoch, grant.Key)
	if err != nil {
		n.mu.Unlock()
		n.failJoin("Handshake with host failed")
		return
	}
	n.setMemberKeysLocked(grant.Members)
	if _, ok := n.memberKeys[hostID]; !ok {
		n.mu.Unlock()
		n.failJoin("Handshake with host failed")
		return
	}
	if len(grant.Vouch) > 0 {
		n.joinVouch, _ = e2ee.MarshalVouch(e2ee.Vouch{HostID: hostID, Key: n.identity.Public(), Tags: grant.Vouch})
		n.joinVouchUntil = time.Now().Add(joinVouchPeriod)
	}
	n.keyring = keyring
	lan := lanAddr != nil
	n.mu.Unlock()

	n.log("[E2EE] Room key verified with host, group key received")
	if lan {
		n.broadcastJoinRequest(lanAddr)
	}
}

// --- host side ---

func (n *P2PNode) recordHandshakeFailureLocked(why string) {
	now := time.Now()
	kept := n.handshakeFails[:0]
	for _, t := range n.handshakeFails {
		if now.Sub(t) < handshakeFailureWindow {
			kept = append(kept, t)
		}
	}
	n.handshakeFails = append(kept, now)
	n.writeToFileLog("[SECURITY] Join handshake failed: " + why)
	if len(n.handshakeFails) >= handshakeFailureLimit && now.After(n.handshakePauseTo) {
		n.handshakePauseTo = now.Add(handshakePause)
		n.handshakeFails = nil
		if n.OnLog != nil {
			go n.OnLog("[SECURITY] ⚠️ Repeated wrong room key / PIN attempts: new joins paused for 30s")
		}
	}
}

func (n *P2PNode) decideAdmissionLocked(pin string) byte {
	switch {
	case n.IsLocked && n.RoomPIN == "":
		return e2ee.StatusLocked
	case n.IsLocked && pin != n.RoomPIN:
		return e2ee.StatusPinRequired
	case len(n.Peers) >= MaxPeers-1:
		return e2ee.StatusFull
	default:
		return e2ee.StatusOK
	}
}

func (n *P2PNode) expireJoinSessionsLocked(now time.Time) {
	for id, s := range n.joinSessions {
		if now.Sub(s.server.Created) > joinSessionTTL {
			delete(n.joinSessions, id)
		}
	}
}

// acceptHello answers a joiner hello with a challenge (or a busy result while paused).
func (n *P2PNode) acceptHello(joinerID, nickname string, hello []byte, lanAddr *net.UDPAddr) (kind byte, reply []byte, ok bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.IsHost || !n.IsConnected || n.roomSecret == "" || joinerID == "" || joinerID == n.LocalID {
		return 0, nil, false
	}
	if s := n.joinSessions[joinerID]; s != nil && bytes.Equal(s.hello, hello) {
		if lanAddr != nil {
			s.lanAddr = lanAddr
		}
		return hsChallenge, s.server.Challenge(), true
	}
	if time.Now().Before(n.handshakePauseTo) {
		return hsResult, []byte{e2ee.StatusBusy}, true
	}
	if len(n.joinSessions) >= maxJoinSessions {
		var oldestID string
		var oldest time.Time
		for id, s := range n.joinSessions {
			if oldestID == "" || s.server.Created.Before(oldest) {
				oldestID, oldest = id, s.server.Created
			}
		}
		delete(n.joinSessions, oldestID)
	}
	server, challenge, err := e2ee.AcceptJoin(n.roomID, n.roomSecret, n.LocalID, joinerID, hello)
	if err != nil {
		return 0, nil, false
	}
	n.joinSessions[joinerID] = &joinSession{server: server, hello: append([]byte(nil), hello...), nickname: nickname, lanAddr: lanAddr}
	return hsChallenge, challenge, true
}

// processAuth verifies the joiner's key confirmation and decides admission.
// verified=false means the joiner does not know the room secret.
func (n *P2PNode) processAuth(joinerID string, auth []byte) (result []byte, status byte, verified bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.joinSessions[joinerID]
	if s == nil || !n.IsHost {
		return nil, 0, false
	}
	if s.result != nil && bytes.Equal(s.auth, auth) {
		return s.result, s.status, true
	}
	ja, err := s.server.VerifyAuth(auth)
	if err != nil {
		delete(n.joinSessions, joinerID)
		n.recordHandshakeFailureLocked("wrong room key")
		return nil, 0, false
	}
	if ja.Nickname != "" {
		s.nickname = ja.Nickname
	}
	if ja.HasKey {
		status = n.decideAdmissionLocked(ja.PIN)
	} else {
		status = e2ee.StatusOutdated
	}
	if status == e2ee.StatusPinRequired {
		n.recordHandshakeFailureLocked("wrong PIN")
	}
	s.auth = append([]byte(nil), auth...)
	s.pin, s.identity = ja.PIN, ja.Identity
	if status == e2ee.StatusOK && n.KnockToJoin {
		// Verified, but the host decides; the key is only released by ApproveJoin.
		s.result, s.status = []byte{e2ee.StatusWaiting}, e2ee.StatusWaiting
		if cb := n.OnKnock; cb != nil {
			go cb(joinerID, s.nickname)
		}
		return s.result, s.status, true
	}
	if err := n.finishAdmissionLocked(joinerID, s, status); err != nil {
		return nil, 0, false
	}
	return s.result, s.status, true
}

// finishAdmissionLocked builds the verdict for a verified joiner and, when it is admitted,
// the key grant it carries.
func (n *P2PNode) finishAdmissionLocked(joinerID string, s *joinSession, status byte) error {
	grant := e2ee.KeyGrant{}
	if status == e2ee.StatusOK {
		n.memberKeys[joinerID] = s.identity
		grant.Epoch, grant.Key = n.keyring.Current()
		if staged, ok := n.keyring.StagedEpoch(); ok && n.rekeyEpoch == staged {
			grant.Epoch, grant.Key = staged, n.rekeyKey
		}
		grant.Members = n.memberDirectoryLocked(n.rekeyRecipientsLocked())
		// Let the joiner introduce its key to the other members itself, so it is not
		// stranded if we leave before our next rekey reaches them.
		var err error
		grant.Vouch, err = n.identity.VouchTags(n.roomID, n.LocalID, joinerID, s.identity, grant.Members)
		if err != nil {
			return err
		}
	}
	result, err := s.server.Result(status, grant)
	if err != nil {
		return err
	}
	s.result, s.status = result, status
	if status == e2ee.StatusOK {
		// Rotate so the other members learn the joiner's identity key (they need it should
		// they become host) and the joiner cannot read traffic captured before it joined.
		n.scheduleRekeyLocked("member joined")
	}
	return nil
}

// ApproveJoin lets a knocking joiner in (allow) or turns it away. It reports whether the
// joiner was still waiting.
func (n *P2PNode) ApproveJoin(joinerID string, allow bool) bool {
	n.mu.Lock()
	s := n.joinSessions[joinerID]
	if s == nil || !n.IsHost || s.status != e2ee.StatusWaiting {
		n.mu.Unlock()
		return false
	}
	status := byte(e2ee.StatusDenied)
	if allow {
		status = n.decideAdmissionLocked(s.pin) // the room may have filled or locked meanwhile
	}
	if err := n.finishAdmissionLocked(joinerID, s, status); err != nil {
		n.mu.Unlock()
		return false
	}
	result, lanAddr, viaRelay := s.result, s.lanAddr, s.viaRelay
	var frame []byte
	if lanAddr != nil {
		frame = n.lanFrameLocked(hsResult, n.LocalID, result)
	}
	n.mu.Unlock()

	if viaRelay {
		n.sendRelaySignal(protocol.Signal{Type: protocol.SigPake, Target: joinerID, Data: append([]byte{hsResult}, result...)})
		if status == e2ee.StatusOK {
			n.sendRelaySignal(protocol.Signal{Type: protocol.SigAdmit, Target: joinerID})
		} else {
			n.sendRelaySignal(protocol.Signal{Type: protocol.SigReject, Target: joinerID, Message: handshakeStatusReason(status)})
		}
	}
	if frame != nil {
		n.writeUDP(frame, lanAddr, nil)
	}
	return true
}

// handleRelayPake dispatches handshake frames forwarded by the relay.
func (n *P2PNode) handleRelayPake(msg protocol.Signal) {
	if len(msg.Data) < 2 {
		return
	}
	kind, body := msg.Data[0], msg.Data[1:]
	switch kind {
	case hsHello:
		replyKind, reply, ok := n.acceptHello(msg.SenderID, "", body, nil)
		if !ok {
			return
		}
		n.sendRelaySignal(protocol.Signal{Type: protocol.SigPake, Target: msg.SenderID, Data: append([]byte{replyKind}, reply...)})
		if replyKind == hsResult {
			n.sendRelaySignal(protocol.Signal{Type: protocol.SigReject, Target: msg.SenderID, Message: handshakeStatusReason(reply[0])})
		}
	case hsChallenge:
		n.mu.RLock()
		hostNick := n.HostNick
		n.mu.RUnlock()
		auth, ok := n.processChallenge(msg.SenderID, hostNick, body, nil)
		if ok {
			n.sendRelaySignal(protocol.Signal{Type: protocol.SigPake, Target: msg.SenderID, Data: append([]byte{hsAuth}, auth...)})
		}
	case hsAuth:
		n.mu.Lock()
		if s := n.joinSessions[msg.SenderID]; s != nil {
			s.viaRelay = true
		}
		n.mu.Unlock()
		result, status, verified := n.processAuth(msg.SenderID, body)
		if !verified {
			n.sendRelaySignal(protocol.Signal{Type: protocol.SigReject, Target: msg.SenderID, Message: "Wrong room key"})
			return
		}
		n.sendRelaySignal(protocol.Signal{Type: protocol.SigPake, Target: msg.SenderID, Data: append([]byte{hsResult}, result...)})
		if status == e2ee.StatusWaiting {
			return // ApproveJoin sends the verdict
		}
		if status == e2ee.StatusOK {
			n.sendRelaySignal(protocol.Signal{Type: protocol.SigAdmit, Target: msg.SenderID})
		} else {
			n.sendRelaySignal(protocol.Signal{Type: protocol.SigReject, Target: msg.SenderID, Message: handshakeStatusReason(status)})
		}
	case hsResult:
		n.processResult(msg.SenderID, body, nil)
	}
}

// --- LAN handshake ---

// lanJoinProbe advertises a join attempt on the local network: the handshake hello until the
// group key is obtained, then the encrypted join request.
func (n *P2PNode) lanJoinProbe() {
	n.mu.Lock()
	if !n.Connecting || n.IsConnected {
		n.mu.Unlock()
		return
	}
	if n.keyring != nil {
		hostAddr := n.joinHostAddr
		n.mu.Unlock()
		n.broadcastJoinRequest(hostAddr)
		return
	}
	if n.joinAuth != nil && n.joinHostAddr != nil {
		// Challenge answered; retransmit auth until the result arrives.
		frame := n.lanFrameLocked(hsAuth, n.LocalID, n.joinAuth)
		addr := n.joinHostAddr
		n.mu.Unlock()
		n.writeUDP(frame, addr, nil)
		return
	}
	if err := n.ensureJoinClientLocked(); err != nil {
		n.mu.Unlock()
		return
	}
	body := appendShort(nil, n.Nickname)
	body = append(body, n.joinClient.Hello()...)
	frame := n.lanFrameLocked(hsHello, n.LocalID, body)
	n.mu.Unlock()
	n.broadcastRaw(frame, true)
}

func (n *P2PNode) lanFrameLocked(kind byte, id string, body []byte) []byte {
	frame := make([]byte, 0, lanTagSize+2+len(id)+len(body))
	frame = append(frame, lanHandshakeTag(n.roomID)...)
	frame = append(frame, kind)
	frame = appendShort(frame, id)
	return append(frame, body...)
}

// handleLANHandshake consumes plaintext LAN handshake frames for our room. It returns true
// when the datagram was a handshake frame.
func (n *P2PNode) handleLANHandshake(data []byte, raddr *net.UDPAddr) bool {
	if len(data) < lanTagSize+2 || raddr == nil {
		return false
	}
	n.mu.RLock()
	roomID := n.roomID
	n.mu.RUnlock()
	if roomID == "" || !hmac.Equal(data[:lanTagSize], lanHandshakeTag(roomID)) {
		return false
	}
	kind := data[lanTagSize]
	id, body, ok := readShort(data[lanTagSize+1:])
	if !ok || id == n.LocalID {
		return true
	}

	switch kind {
	case hsHello:
		nick, hello, ok := readShort(body)
		if !ok {
			return true
		}
		replyKind, reply, ok := n.acceptHello(id, nick, hello, raddr)
		if !ok {
			return true
		}
		n.mu.RLock()
		var frame []byte
		if replyKind == hsChallenge {
			frame = n.lanFrameLocked(hsChallenge, n.LocalID, append(appendShort(nil, n.Nickname), reply...))
		} else {
			frame = n.lanFrameLocked(hsResult, n.LocalID, reply)
		}
		n.mu.RUnlock()
		n.writeUDP(frame, raddr, nil)

	case hsChallenge:
		hostNick, challenge, ok := readShort(body)
		if !ok {
			return true
		}
		auth, ok := n.processChallenge(id, hostNick, challenge, raddr)
		if !ok {
			return true
		}
		n.mu.RLock()
		frame := n.lanFrameLocked(hsAuth, n.LocalID, auth)
		n.mu.RUnlock()
		n.writeUDP(frame, raddr, nil)

	case hsAuth:
		result, _, verified := n.processAuth(id, body)
		if !verified {
			return true
		}
		n.mu.RLock()
		frame := n.lanFrameLocked(hsResult, n.LocalID, result)
		n.mu.RUnlock()
		n.writeUDP(frame, raddr, nil)

	case hsResult:
		n.processResult(id, body, raddr)
	}
	return true
}

// --- group key rotation ---

const (
	rekeyDebounce   = 2 * time.Second
	rekeyRetry      = 700 * time.Millisecond
	rekeyMaxRetries = 6
	rekeyPromote    = 1500 * time.Millisecond
)

// scheduleRekeyLocked rotates the room group key shortly (debounced). Host only.
func (n *P2PNode) scheduleRekeyLocked(reason string) {
	if !n.IsHost || n.keyring == nil || n.rekeyTimer != nil {
		return
	}
	n.rekeyTimer = time.AfterFunc(rekeyDebounce, func() { n.rotateGroupKey(reason) })
}

// rekeyRecipientsLocked lists the members a rekey goes to: current peers plus joiners that
// were just admitted but have not been registered as peers yet.
func (n *P2PNode) rekeyRecipientsLocked() []string {
	ids := make([]string, 0, len(n.Peers)+1)
	for id := range n.Peers {
		ids = append(ids, id)
	}
	for id, s := range n.joinSessions {
		if s.status == e2ee.StatusOK && n.Peers[id] == nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// memberDirectoryLocked returns the identity keys of this node and the given members.
func (n *P2PNode) memberDirectoryLocked(ids []string) []e2ee.MemberKey {
	dir := []e2ee.MemberKey{{ID: n.LocalID, Key: n.identity.Public()}}
	for _, id := range ids {
		if key, ok := n.memberKeys[id]; ok {
			dir = append(dir, e2ee.MemberKey{ID: id, Key: key})
		}
	}
	return dir
}

// setMemberKeysLocked replaces the member key directory with one vouched for by the host.
func (n *P2PNode) setMemberKeysLocked(members []e2ee.MemberKey) {
	n.memberKeys = make(map[string]e2ee.PublicKey, len(members))
	for _, m := range members {
		if m.ID != n.LocalID {
			n.memberKeys[m.ID] = m.Key
			delete(n.departed, m.ID) // re-admitted by the host
		}
	}
}

const (
	joinVouchPeriod = time.Minute      // how long a new member presents its host vouch
	prevHostTrust   = 30 * time.Second // how long vouches from the previous host stay valid
)

func (n *P2PNode) resetMemberTrackingLocked() {
	n.joinVouch = nil
	n.joinVouchUntil = time.Time{}
	n.prevHostID = ""
	n.prevHostKey = e2ee.PublicKey{}
	n.prevHostUntil = time.Time{}
	n.departed = make(map[string]time.Time)
}

// rememberHostLocked keeps the key of a host that is being replaced, so members it admitted
// just before leaving can still prove its vouch to us for a short while.
func (n *P2PNode) rememberHostLocked(hostID string) {
	if hostID == "" || hostID == n.LocalID {
		return
	}
	if key, ok := n.memberKeys[hostID]; ok {
		n.prevHostID, n.prevHostKey = hostID, key
		n.prevHostUntil = time.Now().Add(prevHostTrust)
	}
}

// forgetMemberLocked drops a departed member's key and admission state.
func (n *P2PNode) forgetMemberLocked(id string) {
	if id == n.HostID {
		n.rememberHostLocked(id)
	}
	delete(n.memberKeys, id)
	delete(n.joinSessions, id)
	if n.departed != nil {
		n.departed[id] = time.Now()
	}
}

// acceptVouchLocked learns a member's identity key from its host vouch. A vouch is accepted
// from the current host or, briefly, the previous one, and never for a member that left.
// A host that learns a key this way rekeys so the member receives the current group key.
func (n *P2PNode) acceptVouchLocked(memberID string, raw []byte) {
	if n.identity == nil || n.memberKeys == nil || memberID == "" || memberID == n.LocalID {
		return
	}
	if _, known := n.memberKeys[memberID]; known {
		return
	}
	if _, gone := n.departed[memberID]; gone {
		return
	}
	v, err := e2ee.UnmarshalVouch(raw)
	if err != nil || v.HostID == n.LocalID {
		return
	}
	var hostKey e2ee.PublicKey
	switch {
	case v.HostID == n.HostID:
		key, ok := n.memberKeys[v.HostID]
		if !ok {
			return
		}
		hostKey = key
	case v.HostID == n.prevHostID && time.Now().Before(n.prevHostUntil):
		hostKey = n.prevHostKey
	default:
		return
	}
	if !n.identity.CheckVouch(n.roomID, n.LocalID, memberID, hostKey, v) {
		n.writeToFileLog(fmt.Sprintf("[SECURITY] Rejected identity key vouch for %s", memberID))
		return
	}
	n.memberKeys[memberID] = v.Key
	n.debugLog(fmt.Sprintf("[E2EE] Learned identity key of %s from its host vouch", memberID))
	if n.IsHost {
		n.scheduleRekeyLocked("member key vouched")
	}
}

// leaveProofLocked authenticates our leave to every member whose key we know.
func (n *P2PNode) leaveProofLocked(timestamp int64) []byte {
	if n.identity == nil || len(n.memberKeys) == 0 {
		return nil
	}
	peers := make([]e2ee.MemberKey, 0, len(n.memberKeys))
	for id, key := range n.memberKeys {
		peers = append(peers, e2ee.MemberKey{ID: id, Key: key})
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	proof, err := n.identity.LeaveProof(n.roomID, n.LocalID, timestamp, peers)
	if err != nil {
		return nil
	}
	return proof
}

// verifyLeaveLocked reports whether a Leave really comes from its sender. A member whose key
// we do not know yet cannot be verified; it times out instead.
func (n *P2PNode) verifyLeaveLocked(pkt *P2PPacket) bool {
	key, ok := n.memberKeys[pkt.SenderID]
	if !ok || n.identity == nil {
		return false
	}
	return n.identity.CheckLeave(n.roomID, n.LocalID, pkt.SenderID, key, pkt.Timestamp, pkt.Payload)
}

// sealGrantsLocked seals a grant of the given group key for every rekey recipient, each with
// its own pairwise key. Host only.
func (n *P2PNode) sealGrantsLocked(epoch uint32, key e2ee.GroupKey) map[string][]byte {
	recipients := n.rekeyRecipientsLocked()
	grant := e2ee.KeyGrant{Epoch: epoch, Key: key, Members: n.memberDirectoryLocked(recipients)}
	sealed := make(map[string][]byte, len(recipients))
	for _, id := range recipients {
		memberKey, ok := n.memberKeys[id]
		if !ok {
			// Only possible after a host change that raced a join: we never learned this
			// member's key, so it cannot receive the new group key and will drop out.
			n.writeToFileLog(fmt.Sprintf("[E2EE] No identity key for %s, it cannot receive the new group key", id))
			continue
		}
		payload, err := n.identity.SealGrant(n.roomID, n.LocalID, id, memberKey, grant)
		if err != nil {
			continue
		}
		sealed[id] = payload
	}
	return sealed
}

// rotateGroupKey distributes a fresh group key to the members, sealed separately for each
// one with its pairwise identity key: departed members cannot read it (they only hold old
// group keys) and members cannot forge one, because only the host and the recipient know
// the pairwise key.
func (n *P2PNode) rotateGroupKey(reason string) {
	n.mu.Lock()
	n.rekeyTimer = nil
	if !n.IsHost || !n.IsConnected || n.keyring == nil || n.identity == nil {
		n.mu.Unlock()
		return
	}
	epoch, _ := n.keyring.Current()
	if staged, ok := n.keyring.StagedEpoch(); ok && staged > epoch {
		epoch = staged // a rotation is still in flight: supersede it
	}
	newEpoch := epoch + 1
	newKey := e2ee.NewGroupKey()
	if err := n.keyring.Stage(newEpoch, newKey); err != nil {
		n.mu.Unlock()
		return
	}
	sealed := n.sealGrantsLocked(newEpoch, newKey)
	n.rekeyEpoch = newEpoch
	n.rekeyKey = newKey
	n.rekeyAcks = make(map[string]bool, len(sealed))
	for id := range sealed {
		n.rekeyAcks[id] = false
	}
	roomID := n.roomID
	keyring := n.keyring
	n.mu.Unlock()

	finish := func() {
		n.mu.Lock()
		if n.keyring == keyring && n.rekeyEpoch == newEpoch {
			keyring.Promote()
		}
		n.mu.Unlock()
		n.debugLog(fmt.Sprintf("[E2EE] Group key rotated to epoch %d (%s)", newEpoch, reason))
	}
	if len(sealed) == 0 {
		finish()
		return
	}

	go func() {
		start := time.Now()
		for attempt := 0; attempt < rekeyMaxRetries; attempt++ {
			n.mu.RLock()
			var pending []string
			for id, acked := range n.rekeyAcks {
				if !acked {
					pending = append(pending, id)
				}
			}
			stillCurrent := n.keyring == keyring && n.rekeyEpoch == newEpoch
			n.mu.RUnlock()
			if !stillCurrent {
				return
			}
			if len(pending) == 0 {
				break
			}
			for _, id := range pending {
				pkt := P2PPacket{
					Type:      PacketRekey,
					RoomCode:  roomID,
					SenderID:  n.LocalID,
					Nickname:  n.Nickname,
					TargetID:  id,
					Epoch:     newEpoch,
					Payload:   sealed[id],
					Timestamp: time.Now().UnixMilli(),
				}
				n.sendToMember(id, &pkt, protocol.FrameReliable)
			}
			time.Sleep(rekeyRetry)
		}
		if wait := rekeyPromote - time.Since(start); wait > 0 {
			time.Sleep(wait)
		}
		finish()
	}()
}

// sendToMember seals a packet with the group key and sends it to one member: directly when
// a path is known, and through the relay (targeted when supported) when the member is
// relay-only. Admitted joiners that are not peers yet are reached through their handshake
// address or the relay.
func (n *P2PNode) sendToMember(id string, pkt *P2PPacket, class byte) {
	n.mu.RLock()
	keyring := n.keyring
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	var addr *net.UDPAddr
	var conn *net.UDPConn
	viaRelay := true
	if peer, ok := n.Peers[id]; ok {
		addr, conn = peer.Addr, peer.conn
		viaRelay = peer.ViaRelay || peer.Addr == nil
	} else if s := n.joinSessions[id]; s != nil && s.lanAddr != nil {
		addr, viaRelay = s.lanAddr, false
	}
	isRelay := n.isRelayConnected
	targeted := n.relayTargeted
	n.mu.RUnlock()

	data, err := sealPacket(pkt, keyring)
	if err != nil {
		return
	}
	if addr != nil {
		n.writeUDP(data, addr, conn)
	}
	if viaRelay && isRelay {
		if targeted {
			n.sendRelayTo(class, id, data)
		} else {
			n.sendRelayFrame(class, data) // other members ignore it: TargetID and sealing
		}
	}
}

// handleRekeyLocked installs a group key sealed for us by the host and acknowledges it.
func (n *P2PNode) handleRekeyLocked(pkt *P2PPacket, raddr *net.UDPAddr) {
	if n.keyring == nil || n.identity == nil || n.IsHost || pkt.SenderID != n.HostID || pkt.TargetID != n.LocalID {
		return
	}
	hostKey, ok := n.memberKeys[n.HostID]
	if !ok {
		return
	}
	grant, err := n.identity.OpenGrant(n.roomID, n.HostID, n.LocalID, hostKey, pkt.Payload)
	if err != nil {
		n.writeToFileLog(fmt.Sprintf("[SECURITY] Dropped rekey from %s that is not sealed for us: %v", pkt.Nickname, err))
		return
	}
	cur, _ := n.keyring.Current()
	if grant.Epoch > cur {
		if staged, ok := n.keyring.StagedEpoch(); !ok || staged < grant.Epoch {
			if err := n.keyring.Stage(grant.Epoch, grant.Key); err != nil {
				return
			}
			n.setMemberKeysLocked(grant.Members)
			keyring, epoch := n.keyring, grant.Epoch
			time.AfterFunc(rekeyPromote, func() {
				n.mu.Lock()
				defer n.mu.Unlock()
				if n.keyring == keyring {
					if staged, ok := keyring.StagedEpoch(); ok && staged == epoch {
						keyring.Promote()
					}
				}
			})
		}
	}
	ack := P2PPacket{
		Type:      PacketRekeyAck,
		RoomCode:  n.roomID,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		Epoch:     grant.Epoch,
		Timestamp: time.Now().UnixMilli(),
	}
	go n.sendPacketTo(raddr, &ack)
}

func (n *P2PNode) handleRekeyAckLocked(pkt *P2PPacket) {
	if !n.IsHost || pkt.Epoch != n.rekeyEpoch || n.rekeyAcks == nil {
		return
	}
	if _, ok := n.rekeyAcks[pkt.SenderID]; ok {
		n.rekeyAcks[pkt.SenderID] = true
	}
}
