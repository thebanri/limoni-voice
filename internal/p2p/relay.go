package p2p

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thebanri/limoni-voice/internal/e2ee"
	"github.com/thebanri/limoni-voice/internal/protocol"
)

// UpdateRelaySettings dynamically updates the relay server URL and authentication token,
// reconnecting to the new relay server if a room session is currently active.
func (n *P2PNode) UpdateRelaySettings(newURL, newToken string) {
	n.mu.Lock()
	n.RelayURL = NormalizeRelayURL(newURL)
	n.RelayToken = strings.TrimSpace(newToken)
	n.LanOnly = n.RelayURL == ""
	isActiveRoom := n.RoomCode != ""
	currentRoom := n.RoomCode
	isHost := n.IsHost
	n.mu.Unlock()

	if isActiveRoom {
		action := "join"
		if isHost {
			action = "host"
		}
		n.connectRelay(action, currentRoom)
	}
}

// connectRelay connects to the relay in the background and sends the initial host/join message.
// If the connection drops while the room is active, it automatically reconnects.
func (n *P2PNode) connectRelay(action string, roomCode string) {
	n.mu.Lock()
	relayURL := n.RelayURL
	if n.LanOnly || relayURL == "" || strings.EqualFold(relayURL, "none") || strings.EqualFold(relayURL, "off") {
		n.mu.Unlock()
		return
	}
	if n.wsCancel != nil {
		close(n.wsCancel)
		n.wsCancel = nil
	}
	if n.wsConn != nil {
		n.wsConn.Close()
		n.wsConn = nil
		n.isRelayConnected = false
	}
	wsCancel := make(chan struct{})
	n.wsCancel = wsCancel
	n.mu.Unlock()

	go n.relayConnectionSupervisor(relayURL, action, wsCancel)
}

func relayDialURL(relayURL, token string) (string, http.Header) {
	headers := http.Header{}
	target := relayURL
	if token != "" {
		headers.Set("X-Auth-Token", token)
		if !strings.Contains(target, "token=") {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target = fmt.Sprintf("%s%stoken=%s", target, sep, url.QueryEscape(token))
		}
	}
	return target, headers
}

func (n *P2PNode) relayConnectionSupervisor(relayURL, action string, cancel chan struct{}) {
	firstConnect := true
	for {
		select {
		case <-cancel:
			return
		default:
		}

		priorityCh := make(chan []byte, 256)
		reliableCh := make(chan []byte, 512)
		// Size video channel to hold a 1080p 120 FPS keyframe burst without bufferbloat.
		videoCh := make(chan []byte, 128)

		targetURL, headers := relayDialURL(relayURL, n.RelayToken)
		dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second}
		conn, resp, err := dialer.Dial(targetURL, headers)
		if err != nil {
			if firstConnect {
				if resp != nil && resp.StatusCode == http.StatusUnauthorized {
					n.log("[RELAY] ⛔ Relay connection rejected (401 Unauthorized): Invalid or missing token. Check --relay-token or LIMONI_RELAY_TOKEN.")
				} else {
					n.log(fmt.Sprintf("[RELAY] Failed to connect to relay server (%v). LAN mode active.", err))
				}
				firstConnect = false
			}
			select {
			case <-cancel:
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		n.mu.Lock()
		select {
		case <-cancel:
			conn.Close()
			n.mu.Unlock()
			return
		default:
		}
		n.wsConn = conn
		n.wsPriorityCh = priorityCh
		n.wsReliableCh = reliableCh
		n.wsVideoCh = videoCh
		n.isRelayConnected = true
		n.mu.Unlock()

		if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
			_ = tcpConn.SetWriteBuffer(64 * 1024)
			_ = tcpConn.SetReadBuffer(64 * 1024)
		}

		if firstConnect {
			n.log(fmt.Sprintf("[RELAY] Connected to relay server (%s | Internet Active)", relayURL))
			firstConnect = false
		} else {
			n.log("[RELAY] Relay connection automatically re-established.")
		}

		connCancel := make(chan struct{})
		go n.relayWritePump(conn, priorityCh, reliableCh, videoCh, connCancel)
		go n.relayPingLoop(connCancel)

		endpoint := n.localEndpoint()
		n.mu.RLock()
		sig := protocol.Signal{
			Proto:       protocol.SignalVersion,
			RoomCode:    n.roomID,
			SenderID:    n.LocalID,
			Nickname:    n.Nickname,
			Endpoint:    endpoint,
			MemberToken: n.memberToken,
		}
		if action == "host" || n.IsHost {
			sig.Type = protocol.SigHostRoom
			sig.HostToken = n.hostToken
			sig.IsLocked = n.IsLocked
			sig.PinRequired = n.IsLocked && n.RoomPIN != ""
		} else {
			sig.Type = protocol.SigJoinRoom
		}
		n.mu.RUnlock()
		n.sendRelaySignal(sig)

		// Blocks until disconnect or cancel
		n.relayListenLoop(conn, connCancel)

		close(connCancel)
		conn.Close()

		n.mu.Lock()
		if n.wsConn == conn {
			n.wsConn = nil
			n.isRelayConnected = false
		}
		isActive := n.IsConnected || n.Connecting
		n.mu.Unlock()

		if !isActive {
			return
		}

		select {
		case <-cancel:
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (n *P2PNode) relayPingLoop(cancel chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return
		case <-ticker.C:
			n.mu.Lock()
			n.relayPingSent = time.Now()
			n.mu.Unlock()
			n.sendRelaySignal(protocol.Signal{Type: protocol.SigPing})
		}
	}
}

func (n *P2PNode) closeRelay() {
	n.mu.Lock()
	if n.wsCancel != nil {
		close(n.wsCancel)
		n.wsCancel = nil
	}
	conn := n.wsConn
	n.wsConn = nil
	n.isRelayConnected = false
	ur := n.udpRelay
	n.udpRelay = nil
	n.mu.Unlock()

	if ur != nil {
		ur.stop()
	}
	if conn != nil {
		if data, err := json.Marshal(protocol.Signal{Type: protocol.SigLeave}); err == nil {
			n.wsMu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = conn.WriteMessage(websocket.TextMessage, data)
			n.wsMu.Unlock()
		}
		conn.Close()
	}
}

// IsRelayConnected returns whether the node has an active WebSocket relay connection
func (n *P2PNode) IsRelayConnected() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.isRelayConnected
}

// RelayStatus returns human-readable status of the relay server connection
func (n *P2PNode) RelayStatus() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.LanOnly || n.RelayURL == "" || strings.EqualFold(n.RelayURL, "none") || strings.EqualFold(n.RelayURL, "off") {
		return "LAN Mode"
	}
	if n.isRelayConnected {
		if n.udpRelay != nil && n.udpRelay.isActive() {
			return "Connected (UDP)"
		}
		return "Connected"
	}
	if n.IsConnected || n.Connecting {
		return "Connecting..."
	}
	return "Disconnected"
}

func (n *P2PNode) sendRelaySignal(sig protocol.Signal) {
	n.mu.RLock()
	conn := n.wsConn
	n.mu.RUnlock()
	if conn == nil {
		return
	}
	data, err := json.Marshal(sig)
	if err != nil {
		return
	}
	n.wsMu.Lock()
	defer n.wsMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

// sendRelayFrame forwards a sealed packet to the room through the relay: over the UDP relay
// when it is reachable (audio, video, pings), otherwise over the WebSocket with class scheduling.
func (n *P2PNode) sendRelayFrame(class byte, data []byte) {
	n.mu.RLock()
	isRelay := n.isRelayConnected
	ur := n.udpRelay
	n.mu.RUnlock()
	if !isRelay {
		return
	}

	if ur != nil && class != protocol.FrameReliable && ur.isActive() && len(data) <= 1400 {
		kind := protocol.UDPKindData
		if class == protocol.FrameBulk {
			kind = protocol.UDPKindBulk
		}
		if ur.send(kind, data) {
			return
		}
	}

	frame := make([]byte, 1+len(data))
	frame[0] = class
	copy(frame[1:], data)
	n.queueRelayFrame(class, frame)
}

// sendRelayTo sends a sealed packet through the relay to a single member (screen share).
// Callers check relayTargeted first; older relays would broadcast the frame to the room.
func (n *P2PNode) sendRelayTo(class byte, member string, data []byte) {
	n.mu.RLock()
	isRelay := n.isRelayConnected
	ur := n.udpRelay
	n.mu.RUnlock()
	if !isRelay || len(member) == 0 || len(member) > 255 {
		return
	}
	if ur != nil && class != protocol.FrameReliable && ur.isActive() {
		payload := protocol.AppendTarget([]byte{class}, member, data)
		if len(payload) <= 1400 && ur.send(protocol.UDPKindTo, payload) {
			return
		}
	}
	frame := protocol.AppendTarget([]byte{class | protocol.FrameTargetFlag}, member, data)
	n.queueRelayFrame(class, frame)
}

// queueRelayFrame schedules a WebSocket binary frame on the queue for its class.
func (n *P2PNode) queueRelayFrame(class byte, frame []byte) {
	n.mu.RLock()
	priorityCh, reliableCh, videoCh := n.wsPriorityCh, n.wsReliableCh, n.wsVideoCh
	n.mu.RUnlock()
	switch class {
	case protocol.FrameBulk:
		if videoCh == nil {
			return
		}
		select {
		case videoCh <- frame:
		default:
			// Channel congested: drain older stale packets to snap latency back to real-time
			for i := len(videoCh) / 2; i > 0; i-- {
				select {
				case <-videoCh:
				default:
				}
			}
			select {
			case videoCh <- frame:
			default:
			}
		}
	case protocol.FrameReliable:
		if reliableCh == nil {
			return
		}
		select {
		case reliableCh <- frame:
		case <-time.After(2 * time.Second):
		}
	default:
		if priorityCh == nil {
			return
		}
		select {
		case priorityCh <- frame:
		default:
		}
	}
}

func (n *P2PNode) relayWritePump(conn *websocket.Conn, priorityCh, reliableCh, videoCh chan []byte, cancel chan struct{}) {
	ticker := time.NewTicker(20 * time.Second)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()

	writeMsg := func(data []byte) error {
		n.wsMu.Lock()
		defer n.wsMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteMessage(websocket.BinaryMessage, data)
	}

	for {
		// Priority 1: drain all pending realtime frames (voice, ping, pong, control) first.
		select {
		case data := <-priorityCh:
			if writeMsg(data) != nil {
				return
			}
			continue
		default:
		}

		select {
		case <-cancel:
			return
		case data := <-priorityCh:
			if writeMsg(data) != nil {
				return
			}
		case data := <-reliableCh:
			if writeMsg(data) != nil {
				return
			}
		case data := <-videoCh:
			if writeMsg(data) != nil {
				return
			}
		case <-ticker.C:
			n.wsMu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err := conn.WriteMessage(websocket.PingMessage, nil)
			n.wsMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (n *P2PNode) relayListenLoop(conn *websocket.Conn, cancel chan struct{}) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		return nil
	})

	for {
		select {
		case <-cancel:
			return
		default:
		}

		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))

		switch msgType {
		case websocket.TextMessage:
			var msg protocol.Signal
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			n.handleRelaySignal(msg)

		case websocket.BinaryMessage:
			if len(data) < 2 {
				continue
			}
			n.handleRelayPacket(data[1:])
		}
	}
}

// handleRelayPacket processes a sealed room packet delivered by the relay.
func (n *P2PNode) handleRelayPacket(sealed []byte) {
	n.mu.RLock()
	keyring := n.keyring
	active := n.IsConnected || n.Connecting
	n.mu.RUnlock()
	if !active || keyring == nil {
		return
	}
	if n.handleKeyFrame(sealed, nil) {
		return
	}

	var pkt P2PPacket
	if err := openPacket(sealed, &pkt, keyring); err != nil {
		n.noteUndecryptable()
		return
	}
	n.noteDecrypted()
	if n.handleScreenPacket(&pkt) {
		return
	}
	n.handlePacket(&pkt, nil)
}

// signalPeerAddr picks the address used for direct traffic from a signalled endpoint.
func (n *P2PNode) signalPeerAddr(ep protocol.Endpoint) *net.UDPAddr {
	port := ep.LocalPort
	if ep.PublicPort > 0 {
		port = ep.PublicPort
	}
	if ep.PublicIP != "" && port > 0 {
		if ip := net.ParseIP(ep.PublicIP); ip != nil {
			return &net.UDPAddr{IP: ip, Port: port}
		}
	}
	if ep.LocalIP != "" && ep.LocalPort > 0 && n.LanOnly {
		if ip := net.ParseIP(ep.LocalIP); ip != nil {
			return &net.UDPAddr{IP: ip, Port: ep.LocalPort}
		}
	}
	return nil
}

// upsertSignalPeerLocked registers or refreshes a member announced by the relay. Caller holds n.mu.
func (n *P2PNode) upsertSignalPeerLocked(id, nickname string, ep protocol.Endpoint) (*PeerInfo, bool) {
	addr := n.signalPeerAddr(ep)
	if peer, ok := n.Peers[id]; ok {
		peer.Nickname = nickname
		peer.LastSeen = time.Now()
		peer.Endpoint = ep
		peer.NAT = ep.NAT
		if addr != nil && (peer.Addr == nil || (!peer.Addr.IP.IsPrivate() && !peer.Addr.IP.IsLoopback())) && peer.ViaRelay {
			peer.Addr = addr
			peer.conn = nil
		}
		return peer, false
	}
	peer := &PeerInfo{
		ID:       id,
		Nickname: nickname,
		Addr:     addr,
		LastSeen: time.Now(),
		ViaRelay: true,
		Endpoint: ep,
		NAT:      ep.NAT,
	}
	n.Peers[id] = peer
	return peer, true
}

func (n *P2PNode) handleRelaySignal(msg protocol.Signal) {
	switch msg.Type {
	case protocol.SigPong:
		n.mu.Lock()
		if !n.relayPingSent.IsZero() {
			rtt := time.Since(n.relayPingSent)
			if n.relayRTT == 0 {
				n.relayRTT = rtt
			} else {
				n.relayRTT = (n.relayRTT*7 + rtt*3) / 10
			}
		}
		n.mu.Unlock()
		return
	case protocol.SigPake:
		n.handleRelayPake(msg)
		return
	case protocol.SigJoinPending:
		n.handleJoinPending(msg)
		return
	}

	if (msg.Type == protocol.SigRoomCreated || msg.Type == protocol.SigWelcome) && msg.Proto < protocol.SignalVersion {
		n.log("[ERROR] Relay server is outdated (signalling protocol v1). Update the relay server to use this version of Limoni Voice.")
		n.failJoin("Relay server is outdated: please update the relay server")
		return
	}

	n.mu.Lock()
	var punch []string
	defer func() {
		n.mu.Unlock()
		for _, id := range punch {
			go n.punchPeer(id, true)
		}
	}()

	switch msg.Type {
	case protocol.SigRoomCreated:
		n.hostToken = msg.HostToken
		n.memberToken = msg.MemberToken
		n.relayProto = msg.Proto
		n.relayTargeted = msg.HasFeature(protocol.FeatureTargeted)
		n.startUDPRelayLocked(msg)
		for _, p := range msg.Peers {
			if p.SenderID != n.LocalID {
				n.upsertSignalPeerLocked(p.SenderID, p.Nickname, p.Endpoint)
				punch = append(punch, p.SenderID)
			}
		}
		n.log(fmt.Sprintf("[RELAY] Room '%s' created on relay (Internet E2EE)", n.RoomCode))

	case protocol.SigJoinRequest:
		if n.IsHost {
			n.debugLog(fmt.Sprintf("[JOIN] %s is requesting to join, waiting for handshake", msg.Nickname))
		}

	case protocol.SigWelcome:
		n.memberToken = msg.MemberToken
		n.relayProto = msg.Proto
		n.relayTargeted = msg.HasFeature(protocol.FeatureTargeted)
		n.startUDPRelayLocked(msg)

		if n.Connecting && !n.IsConnected {
			if n.keyring == nil {
				// Admitted without completing the handshake: cannot decrypt anything.
				go n.failJoin("Handshake with host did not complete")
				return
			}
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.IsConnected = true
			n.Connecting = false
			n.IsHost = false
			n.HostID = msg.SenderID
			n.HostNick = msg.Nickname
			n.joinClient = nil
			if msg.IsLocked {
				n.IsLocked = true
			}
			n.upsertSignalPeerLocked(msg.SenderID, msg.Nickname, msg.Endpoint)
			punch = append(punch, msg.SenderID)
			for _, p := range msg.Peers {
				if p.SenderID != n.LocalID && p.SenderID != msg.SenderID {
					n.upsertSignalPeerLocked(p.SenderID, p.Nickname, p.Endpoint)
					punch = append(punch, p.SenderID)
				}
			}
			n.log(fmt.Sprintf("[RELAY] Connected to room %s! (Host: %s | Internet E2EE)", n.RoomCode, msg.Nickname))
			if successCb := n.OnJoinSuccess; successCb != nil {
				go successCb(msg.Nickname)
			}
			for _, id := range punch {
				go n.sendPingToPeer(id)
			}
		} else if n.IsConnected {
			for _, p := range msg.Peers {
				if p.SenderID != n.LocalID {
					n.upsertSignalPeerLocked(p.SenderID, p.Nickname, p.Endpoint)
					punch = append(punch, p.SenderID)
				}
			}
		}

	case protocol.SigPeerJoined:
		if !n.IsConnected || msg.SenderID == n.LocalID {
			return
		}
		peer, isNew := n.upsertSignalPeerLocked(msg.SenderID, msg.Nickname, msg.Endpoint)
		if isNew {
			n.log(fmt.Sprintf("[+] %s joined the room! (Internet E2EE)", msg.Nickname))
			if n.OnPeerEvent != nil {
				cp := *peer
				go n.OnPeerEvent("join", &cp)
			}
		}
		punch = append(punch, msg.SenderID)
		go n.sendPingToPeer(msg.SenderID)

	case protocol.SigPeerPortUpdated:
		if peer, exists := n.Peers[msg.SenderID]; exists {
			peer.Endpoint = msg.Endpoint
			peer.NAT = msg.NAT
			if addr := n.signalPeerAddr(msg.Endpoint); addr != nil && (peer.ViaRelay || peer.Addr == nil) {
				peer.Addr = addr
				peer.conn = nil
			}
			peer.LastSeen = time.Now()
			n.debugLog(fmt.Sprintf("[SECURITY] Peer %s rotated endpoint via relay", msg.Nickname))
			punch = append(punch, msg.SenderID)
		}

	case protocol.SigPeerLeft:
		if peer, exists := n.Peers[msg.SenderID]; exists {
			wasSharing := peer.IsSharingScreen
			delete(n.Peers, msg.SenderID)
			n.forgetMemberLocked(msg.SenderID)
			if n.audio != nil {
				n.audio.RemovePeer(msg.SenderID)
			}
			n.log(fmt.Sprintf("[-] %s left.", peer.Nickname))
			if n.OnPeerEvent != nil {
				go n.OnPeerEvent("leave", peer)
			}
			if (wasSharing || len(n.Peers) == 0) && n.IsWatchingScreen {
				go func() {
					_ = n.StopWatchingScreen()
				}()
			}
			if n.IsHost {
				n.scheduleRekeyLocked("member left")
			}
		}

	case protocol.SigNewHost:
		n.rememberHostLocked(n.HostID)
		n.HostID = msg.SenderID
		n.HostNick = msg.Nickname
		if msg.SenderID == n.LocalID {
			n.IsHost = true
			if msg.HostToken != "" {
				n.hostToken = msg.HostToken
			}
			if msg.IsLocked {
				n.IsLocked = true
			}
			n.log("[HOST] Former host left, you are now the room HOST!")
			if n.OnRoomLocked != nil && n.IsLocked {
				go n.OnRoomLocked(true, n.RoomPIN)
			}
			n.scheduleRekeyLocked("host changed")
		} else {
			n.IsHost = false
			if msg.IsLocked {
				n.IsLocked = true
			}
			n.log(fmt.Sprintf("[HOST] New room HOST: %s", msg.Nickname))
		}

	case protocol.SigKicked:
		n.kickedLocked(msg.Ban)

	case protocol.SigRoomLocked:
		reason := "Room is locked by host"
		if msg.Message == "PIN_REQUIRED" {
			reason = "Room is protected by PIN (join with code:PIN)"
		}
		go n.failJoin(reason)

	case protocol.SigRoomFull:
		go n.failJoin("This room is full! (Maximum 4 people)")

	case protocol.SigRoomNotFound:
		msgText := msg.Message
		if msgText == "" {
			msgText = "This room is not currently open! Make sure your friend has opened the room."
		}
		go n.failJoin(msgText)

	case protocol.SigRejected:
		msgText := msg.Message
		if msgText == "" {
			msgText = "The host rejected the join request"
		}
		go n.failJoin(msgText)

	case protocol.SigError:
		msgText := msg.Message
		if msgText == "" {
			msgText = "Server connection error occurred."
		}
		if strings.HasPrefix(msgText, "ROOM_IN_USE") && n.IsHost && n.IsConnected && e2ee.IsStrongCode(n.RoomCode) {
			// Another room already owns this numeric ID on the relay: pick a new one, keep the words.
			_, words, _ := strings.Cut(n.RoomCode, "-")
			newCode := fmt.Sprintf("%d-%s", 1000+time.Now().UnixNano()%9000, words)
			n.RoomCode = newCode
			n.roomID, n.roomSecret = e2ee.SplitRoomCode(newCode)
			n.hostToken = ""
			n.log(fmt.Sprintf("[RELAY] Room ID was in use, switched room key to %s", newCode))
			if cb := n.OnRoomCodeChanged; cb != nil {
				go cb(newCode)
			}
			go n.connectRelay("host", newCode)
			return
		}
		n.log(fmt.Sprintf("[ERROR] Server error: %s", msgText))
		go n.failJoin(msgText)
	}
}

// localEndpoint describes how peers can reach this node directly.
func (n *P2PNode) localEndpoint() protocol.Endpoint {
	localIP := getLocalPrivateIP()
	natType, public := n.stun.Result()
	n.mu.Lock()
	defer n.mu.Unlock()
	ep := protocol.Endpoint{
		LocalIP:   localIP,
		LocalPort: n.Port,
		IPv6:      append([]string(nil), n.ipv6Addrs...),
		NAT:       string(natType),
	}
	if public != nil {
		ep.PublicIP = public.IP.String()
		ep.PublicPort = public.Port
		n.PublicIP, n.PublicPort = ep.PublicIP, ep.PublicPort
	}
	return ep
}

// --- UDP relay client ---

type udpRelayClient struct {
	n       *P2PNode
	addr    atomic.Pointer[net.UDPAddr]
	token   [protocol.UDPTokenSize]byte
	active  atomic.Bool
	lastAck atomic.Int64
	done    chan struct{}
	once    sync.Once
}

func (u *udpRelayClient) isActive() bool {
	return u.active.Load() && time.Since(time.Unix(0, u.lastAck.Load())) < 20*time.Second
}

func (u *udpRelayClient) matches(raddr *net.UDPAddr) bool {
	addr := u.addr.Load()
	return raddr != nil && addr != nil && raddr.Port == addr.Port && raddr.IP.Equal(addr.IP)
}

// handleDatagram consumes a relay datagram; it returns the sealed payload for data frames.
func (u *udpRelayClient) handleDatagram(data []byte) ([]byte, bool) {
	if len(data) < 1 {
		return nil, false
	}
	switch data[0] {
	case protocol.UDPKindProbeAck:
		u.lastAck.Store(time.Now().UnixNano())
		if !u.active.Swap(true) {
			u.n.debugLog(fmt.Sprintf("[RELAY] UDP relay reachable at %s (low-latency path active)", u.addr.Load()))
		}
		return nil, false
	case protocol.UDPKindData:
		return data[1:], true
	}
	return nil, false
}

func (u *udpRelayClient) send(kind byte, payload []byte) bool {
	out := make([]byte, 0, protocol.UDPTokenSize+1+len(payload))
	out = append(out, u.token[:]...)
	out = append(out, kind)
	out = append(out, payload...)
	addr := u.addr.Load()
	if addr == nil {
		return false
	}
	u.n.mu.RLock()
	conn := u.n.Conn
	if addr.IP.To4() == nil {
		conn = u.n.Conn6
	}
	u.n.mu.RUnlock()
	if conn == nil {
		return false
	}
	_, err := conn.WriteToUDP(out, addr)
	return err == nil
}

func (u *udpRelayClient) stop() {
	u.once.Do(func() { close(u.done) })
}

func (u *udpRelayClient) keepaliveLoop() {
	// Fast probing until the first ack, then a keepalive that also refreshes NAT bindings.
	interval := 300 * time.Millisecond
	for i := 0; ; i++ {
		u.send(protocol.UDPKindKeepalive, nil)
		if u.active.Load() {
			interval = 5 * time.Second
		} else if i > 10 {
			interval = 5 * time.Second
		}
		select {
		case <-u.done:
			return
		case <-time.After(interval):
		}
	}
}

func (n *P2PNode) currentUDPRelay() *udpRelayClient {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.udpRelay
}

// startUDPRelayLocked resolves the relay's UDP endpoint from a room_created / welcome message.
func (n *P2PNode) startUDPRelayLocked(msg protocol.Signal) {
	token, err := hex.DecodeString(msg.UDPToken)
	if err != nil || len(token) != protocol.UDPTokenSize {
		return
	}
	hostPort := msg.UDPAddr
	if hostPort == "" && msg.UDPPort > 0 {
		u, err := url.Parse(n.RelayURL)
		if err != nil || u.Hostname() == "" {
			return
		}
		hostPort = net.JoinHostPort(u.Hostname(), strconv.Itoa(msg.UDPPort))
	}
	if hostPort == "" {
		return
	}
	if n.udpRelay != nil {
		var cur [protocol.UDPTokenSize]byte
		copy(cur[:], token)
		if n.udpRelay.token == cur {
			return
		}
		n.udpRelay.stop()
		n.udpRelay = nil
	}
	ur := &udpRelayClient{n: n, done: make(chan struct{})}
	copy(ur.token[:], token)
	n.udpRelay = ur
	go func() {
		network := "udp4"
		if n.Conn6 != nil {
			network = "udp"
		}
		addr, err := net.ResolveUDPAddr(network, hostPort)
		if err != nil {
			n.debugLog(fmt.Sprintf("[RELAY] UDP relay address %s unresolvable: %v", hostPort, err))
			return
		}
		ur.addr.Store(addr)
		ur.keepaliveLoop()
	}()
}
