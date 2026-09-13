package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
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
}

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
	client, _, err := e2ee.NewJoinClient(n.roomID, n.roomSecret, n.LocalID)
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
	default:
		return "The host rejected the join request"
	}
}

// processResult installs the group key delivered by the host.
func (n *P2PNode) processResult(hostID string, result []byte, lanAddr *net.UDPAddr) {
	if len(result) == 0 {
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
	_, epoch, key, err := n.joinClient.HandleResult(result)
	if err != nil {
		n.mu.Unlock()
		n.failJoin("Handshake with host failed")
		return
	}
	keyring, err := e2ee.NewKeyring(epoch, key)
	if err != nil {
		n.mu.Unlock()
		n.failJoin("Handshake with host failed")
		return
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
	pin, nickname, err := s.server.VerifyAuth(auth)
	if err != nil {
		delete(n.joinSessions, joinerID)
		n.recordHandshakeFailureLocked("wrong room key")
		return nil, 0, false
	}
	if nickname != "" {
		s.nickname = nickname
	}
	status = n.decideAdmissionLocked(pin)
	if status == e2ee.StatusPinRequired {
		n.recordHandshakeFailureLocked("wrong PIN")
	}
	epoch, key := n.keyring.Current()
	if staged, ok := n.keyring.StagedEpoch(); ok && n.rekeyEpoch == staged {
		epoch = staged
		key = n.rekeyKey
	}
	result, err = s.server.Result(status, epoch, key)
	if err != nil {
		return nil, 0, false
	}
	s.auth = append([]byte(nil), auth...)
	s.result, s.status = result, status
	return result, status, true
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
		result, status, verified := n.processAuth(msg.SenderID, body)
		if !verified {
			n.sendRelaySignal(protocol.Signal{Type: protocol.SigReject, Target: msg.SenderID, Message: "Wrong room key"})
			return
		}
		n.sendRelaySignal(protocol.Signal{Type: protocol.SigPake, Target: msg.SenderID, Data: append([]byte{hsResult}, result...)})
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

// rotateGroupKey distributes a fresh group key to all members, so departed members and old
// captures can no longer decrypt new traffic.
func (n *P2PNode) rotateGroupKey(reason string) {
	n.mu.Lock()
	n.rekeyTimer = nil
	if !n.IsHost || !n.IsConnected || n.keyring == nil {
		n.mu.Unlock()
		return
	}
	epoch, _ := n.keyring.Current()
	newEpoch := epoch + 1
	newKey := e2ee.NewGroupKey()
	if err := n.keyring.Stage(newEpoch, newKey); err != nil {
		n.mu.Unlock()
		return
	}
	n.rekeyEpoch = newEpoch
	n.rekeyKey = newKey
	n.rekeyAcks = make(map[string]bool, len(n.Peers))
	for id := range n.Peers {
		n.rekeyAcks[id] = false
	}
	members := len(n.Peers)
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
	if members == 0 {
		finish()
		return
	}

	pkt := P2PPacket{
		Type:      PacketRekey,
		RoomCode:  roomID,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		Epoch:     newEpoch,
		Payload:   newKey[:],
		Timestamp: time.Now().UnixMilli(),
	}
	go func() {
		start := time.Now()
		for attempt := 0; attempt < rekeyMaxRetries; attempt++ {
			n.mu.RLock()
			pending := false
			for _, acked := range n.rekeyAcks {
				if !acked {
					pending = true
				}
			}
			stillCurrent := n.keyring == keyring && n.rekeyEpoch == newEpoch
			n.mu.RUnlock()
			if !stillCurrent {
				return
			}
			if !pending && attempt > 0 {
				break
			}
			p := pkt
			n.sendToRoom(&p, protocol.FrameReliable)
			time.Sleep(rekeyRetry)
		}
		if wait := rekeyPromote - time.Since(start); wait > 0 {
			time.Sleep(wait)
		}
		finish()
	}()
}

// handleRekeyLocked installs a group key announced by the host and acknowledges it.
func (n *P2PNode) handleRekeyLocked(pkt *P2PPacket, raddr *net.UDPAddr) {
	if n.keyring == nil || pkt.SenderID != n.HostID || n.IsHost || len(pkt.Payload) != e2ee.KeySize {
		return
	}
	cur, _ := n.keyring.Current()
	if pkt.Epoch > cur {
		if staged, ok := n.keyring.StagedEpoch(); !ok || staged != pkt.Epoch {
			var key e2ee.GroupKey
			copy(key[:], pkt.Payload)
			if err := n.keyring.Stage(pkt.Epoch, key); err != nil {
				return
			}
			keyring, epoch := n.keyring, pkt.Epoch
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
		Epoch:     pkt.Epoch,
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
