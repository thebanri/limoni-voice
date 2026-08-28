package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const MaxPeers = 4

// DefaultRelayURL is the default public WebSocket relay server URL
const DefaultRelayURL = "wss://limoni-voice.onrender.com/ws"

// MagicPrefix identifies authentic Limoni Voice Secure v1 packets
var MagicPrefix = []byte("LVS1")

type PacketType byte

const (
	PacketHello PacketType = iota + 1
	PacketWelcome
	PacketPing
	PacketPong
	PacketAudio
	PacketMuteState
	PacketLeave
	PacketJoinRequest // Client probes network to request joining an open host's room
	PacketRoomFull    // Host rejects join request because room reached 4-peer capacity
)

type P2PPacket struct {
	Type       PacketType
	RoomCode   string
	SenderID   string
	Nickname   string
	IsMuted    bool
	IsDeafened bool
	Speaking   bool
	RMS        float64
	Seq        uint32
	Timestamp  int64
	Payload    []byte
	Peers      []PeerSummary // for Welcome message
}

type PeerSummary struct {
	ID         string
	Nickname   string
	AddrStr    string
	IsMuted    bool
	IsDeafened bool
}

type PeerInfo struct {
	ID         string
	Nickname   string
	Addr       *net.UDPAddr
	PingMs     int64
	LastSeen   time.Time
	IsMuted    bool
	IsDeafened bool
	Speaking   bool
	RMS        float64
}

// RelayControlMessage represents control JSON payloads sent to/from the relay server
type RelayControlMessage struct {
	Type     string      `json:"type"`
	RoomCode string      `json:"room_code,omitempty"`
	SenderID string      `json:"sender_id,omitempty"`
	Nickname string      `json:"nickname,omitempty"`
	Message  string      `json:"message,omitempty"`
	Peers    []RelayPeer `json:"peers,omitempty"`
}

type RelayPeer struct {
	SenderID string `json:"sender_id"`
	Nickname string `json:"nickname"`
}

type P2PNode struct {
	mu                sync.RWMutex
	LocalID           string
	Nickname          string
	RoomCode          string
	RoomKey           []byte
	aead              cipher.AEAD
	Conn              *net.UDPConn
	BroadcastConn     *net.UDPConn
	Port              int
	Peers             map[string]*PeerInfo
	IsConnected       bool
	IsHost            bool
	HostID            string
	HostNick          string
	Connecting        bool
	ConnectTargetRoom string
	connectCancel     chan struct{}
	OnJoinSuccess     func(hostNick string)
	OnJoinFailed      func(reason string)
	audio             *AudioEngine
	seqCounter        uint32
	OnLog             func(msg string)
	OnPeerEvent       func(event string, peer *PeerInfo)
	stopChan          chan struct{}
	// Dedicated broadcast sender socket with SO_BROADCAST (works on Windows too)
	bcastSendConn *net.UDPConn
	// WebSocket Relay for internet-wide P2P forwarding
	RelayURL         string
	wsConn           *websocket.Conn
	wsSendCh         chan []byte
	wsMu             sync.Mutex
	isRelayConnected bool
	wsCancel         chan struct{}
}

func NewP2PNode(localID, nickname string, audio *AudioEngine) *P2PNode {
	relayURL := os.Getenv("LIMONI_RELAY_URL")
	if relayURL == "" {
		relayURL = DefaultRelayURL
	}
	// Create a dedicated UDP socket for sending broadcasts (avoids SO_BROADCAST issues on Windows)
	bcastConn, _ := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	return &P2PNode{
		LocalID:       localID,
		Nickname:      nickname,
		RelayURL:      relayURL,
		Peers:         make(map[string]*PeerInfo),
		audio:         audio,
		stopChan:      make(chan struct{}),
		bcastSendConn: bcastConn,
	}
}

// deriveRoomKey securely derives a 256-bit AES encryption key from the room code
func deriveRoomKey(roomCode string) []byte {
	clean := NormalizeCode(roomCode)
	mac := hmac.New(sha256.New, []byte("limoni-voice-e2ee-master-salt-v1"))
	mac.Write([]byte(clean))
	return mac.Sum(nil)
}

// Start binds a local UDP socket trying predictable ports 50000-50050 first
func (n *P2PNode) Start() error {
	var conn *net.UDPConn
	var chosenPort int

	// 1. Try binding sequentially to predictable ports 50000-50050 for reliable local discovery
	for p := 50000; p <= 50050; p++ {
		laddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("0.0.0.0:%d", p))
		if err == nil {
			c, err := net.ListenUDP("udp4", laddr)
			if err == nil {
				conn = c
				chosenPort = p
				break
			}
		}
	}

	// 2. Fallback to OS ephemeral port if 50000-50050 are all busy
	if conn == nil {
		laddr, err := net.ResolveUDPAddr("udp4", "0.0.0.0:0")
		if err != nil {
			return err
		}
		c, err := net.ListenUDP("udp4", laddr)
		if err != nil {
			return err
		}
		conn = c
		chosenPort = conn.LocalAddr().(*net.UDPAddr).Port
	}

	n.Conn = conn
	n.Port = chosenPort

	// Setup broadcast listener on port 45454
	baddr, err := net.ResolveUDPAddr("udp4", ":45454")
	if err == nil {
		bConn, err := net.ListenUDP("udp4", baddr)
		if err == nil {
			n.BroadcastConn = bConn
			go n.listenBroadcastLoop()
		}
	}

	go n.listenLoop()
	go n.heartbeatLoop()

	return nil
}

// HostRoom opens a new room as the authoritative Host
func (n *P2PNode) HostRoom(roomCode string) {
	n.mu.Lock()
	if n.connectCancel != nil {
		close(n.connectCancel)
		n.connectCancel = nil
	}
	n.Connecting = false
	n.IsHost = true
	n.HostID = n.LocalID
	n.HostNick = n.Nickname
	n.RoomCode = NormalizeCode(roomCode)
	n.RoomKey = deriveRoomKey(n.RoomCode)

	block, err := aes.NewCipher(n.RoomKey)
	if err == nil {
		n.aead, _ = cipher.NewGCM(block)
	}

	n.IsConnected = true
	n.Peers = make(map[string]*PeerInfo)
	n.mu.Unlock()

	n.log(fmt.Sprintf("[👑] Oda acildi (HOST): %s (Port: %d | E2EE Guvenli)", n.RoomCode, n.Port))
	n.broadcastHello()
	n.connectRelay("host", n.RoomCode)
}

// RequestJoinRoom searches for an active host and requests admission. Fails if no open room exists.
func (n *P2PNode) RequestJoinRoom(roomCode string, timeout time.Duration, onSuccess func(hostNick string), onFailed func(reason string)) {
	cleanCode := NormalizeCode(roomCode)
	if cleanCode == "" {
		if onFailed != nil {
			onFailed("Gecersiz oda anahtari")
		}
		return
	}

	n.mu.Lock()
	if n.connectCancel != nil {
		close(n.connectCancel)
		n.connectCancel = nil
	}

	cancelChan := make(chan struct{})
	n.connectCancel = cancelChan
	n.Connecting = true
	n.IsConnected = false
	n.IsHost = false
	n.HostID = ""
	n.HostNick = ""
	n.ConnectTargetRoom = cleanCode
	n.RoomCode = cleanCode
	n.RoomKey = deriveRoomKey(cleanCode)

	block, err := aes.NewCipher(n.RoomKey)
	if err == nil {
		n.aead, _ = cipher.NewGCM(block)
	}

	n.Peers = make(map[string]*PeerInfo)
	n.OnJoinSuccess = onSuccess
	n.OnJoinFailed = onFailed
	n.mu.Unlock()

	n.log(fmt.Sprintf("[⏳] '%s' odasi araniyor ve host dogrulaniyor...", cleanCode))

	// Connect to internet relay server for cross-network join
	n.connectRelay("join", cleanCode)

	// Background LAN probe and timeout handler
	go func() {
		probeTicker := time.NewTicker(250 * time.Millisecond)
		defer probeTicker.Stop()

		timeoutTimer := time.NewTimer(timeout)
		defer timeoutTimer.Stop()

		// Initial probe
		n.broadcastJoinRequest()

		for {
			select {
			case <-cancelChan:
				return

			case <-probeTicker.C:
				n.mu.RLock()
				isConn := n.IsConnected
				isConnecting := n.Connecting
				n.mu.RUnlock()

				if !isConnecting || isConn {
					return
				}
				n.broadcastJoinRequest()

			case <-timeoutTimer.C:
				n.mu.Lock()
				if n.Connecting && !n.IsConnected {
					n.Connecting = false
					n.aead = nil
					n.RoomCode = ""
					failedCb := n.OnJoinFailed
					n.mu.Unlock()

					n.log(fmt.Sprintf("❌ '%s' odasi bulunamadi (Host cevrimdisi veya oda acilmamis).", cleanCode))
					if failedCb != nil {
						failedCb("Bu oda su anda acik degil! Arkadasinizin [2] ODA OLUSTUR butonuna basarak odayi actigindan emin olun.")
					}
				} else {
					n.mu.Unlock()
				}
				return
			}
		}
	}()
}

// CancelJoin cancels any pending join discovery
func (n *P2PNode) CancelJoin() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.connectCancel != nil {
		close(n.connectCancel)
		n.connectCancel = nil
	}
	if n.Connecting {
		n.Connecting = false
		n.aead = nil
		n.RoomCode = ""
		n.log("Odaya baglanti istegi iptal edildi.")
	}
}

// JoinRoom is kept for direct connection (e.g. tests or compatibility)
func (n *P2PNode) JoinRoom(roomCode string) {
	n.HostRoom(roomCode)
}

// LeaveRoom sends leave message and clears peer state
func (n *P2PNode) LeaveRoom() {
	n.CancelJoin()

	n.mu.Lock()
	if !n.IsConnected {
		n.mu.Unlock()
		return
	}
	room := n.RoomCode
	wasHost := n.IsHost
	n.IsConnected = false
	n.IsHost = false
	n.HostID = ""
	n.HostNick = ""
	n.RoomCode = ""
	n.RoomKey = nil
	n.aead = nil
	n.mu.Unlock()

	pkt := P2PPacket{
		Type:       PacketLeave,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    n.audio.Muted,
		IsDeafened: n.audio.Deafened,
		Timestamp:  time.Now().UnixMilli(),
	}
	n.broadcastToPeers(&pkt)
	n.closeRelay()

	n.mu.Lock()
	n.Peers = make(map[string]*PeerInfo)
	n.mu.Unlock()

	if wasHost {
		n.log("Odayi kapattiniz (Host ayrildi).")
	} else {
		n.log("Odadan ayrildiniz.")
	}
}

// connectRelay connects to the WebSocket relay server in the background and sends the initial host/join message.
// If the connection drops while the room is active, it automatically reconnects.
func (n *P2PNode) connectRelay(action string, roomCode string) {
	n.mu.Lock()
	relayURL := n.RelayURL
	if relayURL == "" {
		n.mu.Unlock()
		return
	}
	// Close existing WS connection if any
	if n.wsConn != nil {
		if n.wsCancel != nil {
			close(n.wsCancel)
		}
		n.wsConn.Close()
		n.wsConn = nil
		n.isRelayConnected = false
	}
	wsCancel := make(chan struct{})
	n.wsCancel = wsCancel
	n.mu.Unlock()

	go n.relayConnectionSupervisor(relayURL, action, roomCode, wsCancel)
}

func (n *P2PNode) relayConnectionSupervisor(relayURL, action, roomCode string, cancel chan struct{}) {
	firstConnect := true
	for {
		select {
		case <-cancel:
			return
		default:
		}

		wsSendCh := make(chan []byte, 128)
		n.mu.Lock()
		n.wsSendCh = wsSendCh
		n.mu.Unlock()

		dialer := websocket.Dialer{
			HandshakeTimeout: 6 * time.Second,
		}
		conn, _, err := dialer.Dial(relayURL, nil)
		if err != nil {
			if firstConnect {
				n.log(fmt.Sprintf("[☁️] Relay sunucusuna baglanilamadi (%v). LAN modu devrede.", err))
				firstConnect = false
			}
			// Wait before retry
			select {
			case <-cancel:
				return
			case <-time.After(3 * time.Second):
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
		n.isRelayConnected = true
		n.mu.Unlock()

		if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
		}

		if firstConnect {
			n.log(fmt.Sprintf("[☁️] Relay sunucusuna baglanildi (%s | Internet Aktif)", relayURL))
			firstConnect = false
		} else {
			n.log("[☁️] Relay baglantisi otomatik olarak yeniden kuruldu.")
		}

		connCancel := make(chan struct{})

		// Start write pump
		go n.relayWritePump(conn, wsSendCh, connCancel)

		// Send initial action (host or join)
		if action == "host" {
			n.sendRelayControl(RelayControlMessage{
				Type:     "host_room",
				RoomCode: roomCode,
				SenderID: n.LocalID,
				Nickname: n.Nickname,
			})
		} else if action == "join" {
			n.sendRelayControl(RelayControlMessage{
				Type:     "join_room",
				RoomCode: roomCode,
				SenderID: n.LocalID,
				Nickname: n.Nickname,
			})
		}

		// Run listen loop (blocks until disconnect or cancel)
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

		// Wait briefly before reconnecting
		select {
		case <-cancel:
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (n *P2PNode) closeRelay() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.wsCancel != nil {
		close(n.wsCancel)
		n.wsCancel = nil
	}
	if n.wsConn != nil {
		n.sendRelayControlLocked(RelayControlMessage{Type: "leave"})
		n.wsConn.Close()
		n.wsConn = nil
	}
	n.isRelayConnected = false
}

func (n *P2PNode) sendRelayControl(msg RelayControlMessage) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sendRelayControlLocked(msg)
}

func (n *P2PNode) sendRelayControlLocked(msg RelayControlMessage) {
	if n.wsConn == nil {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	n.wsMu.Lock()
	defer n.wsMu.Unlock()
	n.wsConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	n.wsConn.WriteMessage(websocket.TextMessage, data)
}

func (n *P2PNode) relayWritePump(conn *websocket.Conn, sendCh chan []byte, cancel chan struct{}) {
	ticker := time.NewTicker(20 * time.Second)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()

	for {
		select {
		case <-cancel:
			return
		case data, ok := <-sendCh:
			if !ok {
				return
			}
			n.wsMu.Lock()
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err := conn.WriteMessage(websocket.BinaryMessage, data)
			n.wsMu.Unlock()
			if err != nil {
				return
			}
		case <-ticker.C:
			n.wsMu.Lock()
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
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

	// 60-second read deadline for robust keepalive
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		n.wsMu.Lock()
		err := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
		n.wsMu.Unlock()
		return err
	})

	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
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

		// Refresh read deadline on every valid packet received
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		switch msgType {
		case websocket.TextMessage:
			var msg RelayControlMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			n.handleRelayControl(msg)

		case websocket.BinaryMessage:
			n.mu.RLock()
			aead := n.aead
			active := n.IsConnected || n.Connecting
			n.mu.RUnlock()

			if !active || aead == nil {
				continue
			}

			var pkt P2PPacket
			if err := decryptAndDecodePacket(data, &pkt, aead); err != nil {
				continue
			}

			n.handlePacket(&pkt, nil)
		}
	}
}

func (n *P2PNode) handleRelayControl(msg RelayControlMessage) {
	n.mu.Lock()
	defer n.mu.Unlock()

	switch msg.Type {
	case "room_created":
		n.log(fmt.Sprintf("[☁️] Relay uzerinde '%s' odasi acildi (Internet E2EE)", msg.RoomCode))

	case "welcome":
		if n.Connecting && !n.IsConnected {
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.Connecting = false
			n.IsConnected = true
			n.IsHost = false
			n.HostID = msg.SenderID
			n.HostNick = msg.Nickname
			n.RoomCode = n.ConnectTargetRoom

			hostPeer := &PeerInfo{
				ID:       msg.SenderID,
				Nickname: msg.Nickname,
				LastSeen: time.Now(),
			}
			n.Peers[msg.SenderID] = hostPeer

			for _, p := range msg.Peers {
				if p.SenderID != n.LocalID && n.Peers[p.SenderID] == nil {
					n.Peers[p.SenderID] = &PeerInfo{
						ID:       p.SenderID,
						Nickname: p.Nickname,
						LastSeen: time.Now(),
					}
				}
			}

			n.log(fmt.Sprintf("[☁️] %s odasina baglanildi! (Host: %s | Internet E2EE)", n.RoomCode, msg.Nickname))
			successCb := n.OnJoinSuccess
			if successCb != nil {
				go successCb(msg.Nickname)
			}
			if n.OnPeerEvent != nil {
				go n.OnPeerEvent("join", hostPeer)
			}
		}

	case "peer_joined":
		if n.IsConnected && msg.SenderID != n.LocalID {
			peer, exists := n.Peers[msg.SenderID]
			if !exists {
				peer = &PeerInfo{
					ID:       msg.SenderID,
					Nickname: msg.Nickname,
					LastSeen: time.Now(),
				}
				n.Peers[msg.SenderID] = peer
				n.log(fmt.Sprintf("[+] %s odaya katildi! (Internet E2EE)", msg.Nickname))
				if n.OnPeerEvent != nil {
					go n.OnPeerEvent("join", peer)
				}
			} else {
				peer.Nickname = msg.Nickname
				peer.LastSeen = time.Now()
			}
		}

	case "peer_left":
		if peer, exists := n.Peers[msg.SenderID]; exists {
			delete(n.Peers, msg.SenderID)
			n.log(fmt.Sprintf("[-] %s ayrildi.", peer.Nickname))
			if n.OnPeerEvent != nil {
				go n.OnPeerEvent("leave", peer)
			}
		}

	case "host_left":
		n.log("❌ Host ayrildi, oda kapandi.")
		if n.OnPeerEvent != nil {
			if host, ok := n.Peers[n.HostID]; ok {
				go n.OnPeerEvent("leave", host)
			}
		}

	case "room_full":
		if n.Connecting && !n.IsConnected {
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.Connecting = false
			n.aead = nil
			n.RoomCode = ""
			failedCb := n.OnJoinFailed
			n.log("❌ Odaya katilim reddedildi: Oda dolu (Maks 4 kisi).")
			if failedCb != nil {
				go failedCb("Bu oda dolu! (Maksimum 4 kisi)")
			}
		}
	}
}

func (n *P2PNode) SendAudio(rms float64, speaking bool, pcm []byte) {
	n.mu.RLock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.RUnlock()
		return
	}
	room := n.RoomCode
	n.seqCounter++
	seq := n.seqCounter
	n.mu.RUnlock()

	pkt := P2PPacket{
		Type:       PacketAudio,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    n.audio.Muted,
		IsDeafened: n.audio.Deafened,
		Speaking:   speaking,
		RMS:        rms,
		Seq:        seq,
		Timestamp:  time.Now().UnixMilli(),
		Payload:    pcm,
	}

	n.broadcastToPeers(&pkt)
}

func (n *P2PNode) SendMuteState(isMuted bool) {
	n.mu.RLock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.RUnlock()
		return
	}
	room := n.RoomCode
	n.mu.RUnlock()

	pkt := P2PPacket{
		Type:       PacketMuteState,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    isMuted,
		IsDeafened: n.audio.Deafened,
		Timestamp:  time.Now().UnixMilli(),
	}
	n.broadcastToPeers(&pkt)
}

func (n *P2PNode) SendDeafenState(isDeafened bool) {
	n.mu.RLock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.RUnlock()
		return
	}
	room := n.RoomCode
	n.mu.RUnlock()

	pkt := P2PPacket{
		Type:       PacketMuteState,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    n.audio.Muted,
		IsDeafened: isDeafened,
		Timestamp:  time.Now().UnixMilli(),
	}
	n.broadcastToPeers(&pkt)
}

func (n *P2PNode) broadcastJoinRequest() {
	n.mu.RLock()
	room := n.ConnectTargetRoom
	isConnecting := n.Connecting
	n.mu.RUnlock()

	if !isConnecting || room == "" {
		return
	}

	pkt := P2PPacket{
		Type:       PacketJoinRequest,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    n.audio.Muted,
		IsDeafened: n.audio.Deafened,
		Timestamp:  time.Now().UnixMilli(),
	}

	// 1. Send via local port range 50000-50050 on loopback (instant multi-instance discovery)
	for p := 50000; p <= 50050; p++ {
		if p != n.Port {
			raddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.1:%d", p))
			if err == nil {
				n.sendPacketTo(raddr, &pkt)
			}
		}
	}

	// 2. Send via LAN broadcast 255.255.255.255 to common ports
	for p := 50000; p <= 50010; p++ {
		n.sendBroadcastPacket(&pkt, p)
	}
	n.sendBroadcastPacket(&pkt, 45454)

	// 3. Send to specific subnet broadcast addresses of active interfaces
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if !ok || ipnet.IP.To4() == nil {
					continue
				}
				ip := ipnet.IP.To4()
				mask := ipnet.Mask
				if len(mask) == 4 {
					bcast := net.IPv4(
						ip[0]|^mask[0],
						ip[1]|^mask[1],
						ip[2]|^mask[2],
						ip[3]|^mask[3],
					)
					for p := 50000; p <= 50005; p++ {
						baddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", bcast.String(), p))
						if err == nil {
							n.sendPacketTo(baddr, &pkt)
						}
					}
				}
			}
		}
	}
}

func (n *P2PNode) broadcastHello() {
	n.mu.RLock()
	room := n.RoomCode
	isConnected := n.IsConnected
	n.mu.RUnlock()

	if !isConnected || room == "" {
		return
	}

	pkt := P2PPacket{
		Type:       PacketHello,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    n.audio.Muted,
		IsDeafened: n.audio.Deafened,
		Timestamp:  time.Now().UnixMilli(),
	}

	// 1. Send via local port range 50000-50050 on loopback (instant multi-instance discovery)
	for p := 50000; p <= 50050; p++ {
		if p != n.Port {
			raddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.1:%d", p))
			if err == nil {
				n.sendPacketTo(raddr, &pkt)
			}
		}
	}

	// 2. Send via LAN broadcast 255.255.255.255 to common ports (best-effort)
	for p := 50000; p <= 50010; p++ {
		n.sendBroadcastPacket(&pkt, p)
	}
	n.sendBroadcastPacket(&pkt, 45454)

	// 3. Send to specific subnet broadcast addresses of active interfaces
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if !ok || ipnet.IP.To4() == nil {
					continue
				}
				ip := ipnet.IP.To4()
				mask := ipnet.Mask
				if len(mask) == 4 {
					bcast := net.IPv4(
						ip[0]|^mask[0],
						ip[1]|^mask[1],
						ip[2]|^mask[2],
						ip[3]|^mask[3],
					)
					for p := 50000; p <= 50005; p++ {
						baddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", bcast.String(), p))
						if err == nil {
							n.sendPacketTo(baddr, &pkt)
						}
					}
				}
			}
		}
	}
}

func (n *P2PNode) sendBroadcastPacket(pkt *P2PPacket, port int) {
	n.mu.RLock()
	aead := n.aead
	n.mu.RUnlock()

	if aead == nil || n.Conn == nil {
		return
	}

	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		return
	}

	baddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", port))
	if err != nil {
		return
	}

	// Use dedicated broadcast socket first (works better on Windows)
	if n.bcastSendConn != nil {
		n.bcastSendConn.WriteToUDP(data, baddr)
	}
	// Also try via main connection as fallback
	n.Conn.WriteToUDP(data, baddr)
}

func (n *P2PNode) sendPacketTo(addr *net.UDPAddr, pkt *P2PPacket) {
	n.mu.RLock()
	aead := n.aead
	wsSendCh := n.wsSendCh
	isRelay := n.isRelayConnected
	n.mu.RUnlock()

	if aead == nil {
		return
	}

	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		return
	}

	if addr != nil && n.Conn != nil {
		n.Conn.WriteToUDP(data, addr)
	} else if isRelay && wsSendCh != nil {
		select {
		case wsSendCh <- data:
		default:
		}
	}
}

func (n *P2PNode) broadcastToPeers(pkt *P2PPacket) {
	n.mu.RLock()
	aead := n.aead
	wsSendCh := n.wsSendCh
	isRelay := n.isRelayConnected
	if aead == nil || (len(n.Peers) == 0 && !isRelay) {
		n.mu.RUnlock()
		return
	}

	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		n.mu.RUnlock()
		return
	}

	// 1. Forward via WebSocket Relay to all members
	if isRelay && wsSendCh != nil {
		select {
		case wsSendCh <- data:
		default:
		}
	}

	// 2. Also send via direct UDP to known LAN peer addresses
	for _, peer := range n.Peers {
		if peer.Addr != nil && n.Conn != nil {
			n.Conn.WriteToUDP(data, peer.Addr)
		}
	}
	n.mu.RUnlock()
}

func (n *P2PNode) listenLoop() {
	buf := make([]byte, 65535)
	for {
		readBytes, raddr, err := n.Conn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		n.mu.RLock()
		aead := n.aead
		active := n.IsConnected || n.Connecting
		n.mu.RUnlock()

		if !active || aead == nil {
			continue
		}

		var pkt P2PPacket
		if err := decryptAndDecodePacket(buf[:readBytes], &pkt, aead); err != nil {
			// Unauthorized packet, wrong key or corrupt data -> silently drop
			continue
		}

		n.handlePacket(&pkt, raddr)
	}
}

func (n *P2PNode) listenBroadcastLoop() {
	if n.BroadcastConn == nil {
		return
	}
	buf := make([]byte, 65535)
	for {
		readBytes, raddr, err := n.BroadcastConn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		n.mu.RLock()
		aead := n.aead
		active := n.IsConnected || n.Connecting
		n.mu.RUnlock()

		if !active || aead == nil {
			continue
		}

		var pkt P2PPacket
		if err := decryptAndDecodePacket(buf[:readBytes], &pkt, aead); err != nil {
			// Unauthorized packet, wrong key or corrupt data -> silently drop
			continue
		}

		n.handlePacket(&pkt, raddr)
	}
}

func (n *P2PNode) handlePacket(pkt *P2PPacket, raddr *net.UDPAddr) {
	// Ignore self
	if pkt.SenderID == n.LocalID {
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	targetRoom := n.RoomCode
	if targetRoom == "" && n.Connecting {
		targetRoom = n.ConnectTargetRoom
	}

	// Only process if matching room
	if NormalizeCode(pkt.RoomCode) != targetRoom || (!n.IsConnected && !n.Connecting) {
		return
	}

	// Auto-register or refresh peer on any valid authenticated packet from this room
	if pkt.Type != PacketJoinRequest && pkt.Type != PacketRoomFull && pkt.Type != PacketLeave {
		if n.IsConnected {
			peer, exists := n.Peers[pkt.SenderID]
			if !exists {
				if len(n.Peers) < MaxPeers-1 {
					nick := pkt.Nickname
					if nick == "" {
						nick = "User_" + pkt.SenderID[:min(len(pkt.SenderID), 4)]
					}
					peer = &PeerInfo{
						ID:         pkt.SenderID,
						Nickname:   nick,
						Addr:       raddr,
						LastSeen:   time.Now(),
						IsMuted:    pkt.IsMuted,
						IsDeafened: pkt.IsDeafened,
					}
					n.Peers[pkt.SenderID] = peer
					n.log(fmt.Sprintf("[+] %s ile baglanti kuruldu. (E2EE Guvenli)", nick))
					if n.OnPeerEvent != nil {
						go n.OnPeerEvent("join", peer)
					}
				}
			} else {
				if raddr != nil {
					peer.Addr = raddr
				}
				peer.LastSeen = time.Now()
				if pkt.Nickname != "" {
					peer.Nickname = pkt.Nickname
				}
				peer.IsMuted = pkt.IsMuted
				peer.IsDeafened = pkt.IsDeafened
			}
		}
	}

	switch pkt.Type {
	case PacketJoinRequest:
		// Only an active Host of this exact room code can admit joiners
		if !n.IsConnected || !n.IsHost {
			return
		}

		// Check peer limit (Max 4 people: Host + 3 peers)
		if len(n.Peers) >= MaxPeers-1 && n.Peers[pkt.SenderID] == nil {
			fullPkt := P2PPacket{
				Type:      PacketRoomFull,
				RoomCode:  n.RoomCode,
				SenderID:  n.LocalID,
				Nickname:  n.Nickname,
				Timestamp: time.Now().UnixMilli(),
			}
			go n.sendPacketTo(raddr, &fullPkt)
			return
		}

		peer, exists := n.Peers[pkt.SenderID]
		if !exists {
			peer = &PeerInfo{
				ID:         pkt.SenderID,
				Nickname:   pkt.Nickname,
				Addr:       raddr,
				LastSeen:   time.Now(),
				IsMuted:    pkt.IsMuted,
				IsDeafened: pkt.IsDeafened,
			}
			n.Peers[pkt.SenderID] = peer
			n.log(fmt.Sprintf("[+] %s odaya katildi! (E2EE Guvenli)", pkt.Nickname))
			if n.OnPeerEvent != nil {
				go n.OnPeerEvent("join", peer)
			}
		} else {
			if raddr != nil {
				peer.Addr = raddr
			}
			peer.LastSeen = time.Now()
			peer.Nickname = pkt.Nickname
			peer.IsMuted = pkt.IsMuted
			peer.IsDeafened = pkt.IsDeafened
		}

		// Reply with Welcome containing current peers
		summaries := make([]PeerSummary, 0, len(n.Peers))
		for _, p := range n.Peers {
			if p.ID != pkt.SenderID {
				addrStr := ""
				if p.Addr != nil {
					addrStr = p.Addr.String()
				}
				summaries = append(summaries, PeerSummary{
					ID:         p.ID,
					Nickname:   p.Nickname,
					AddrStr:    addrStr,
					IsMuted:    p.IsMuted,
					IsDeafened: p.IsDeafened,
				})
			}
		}

		welcomePkt := P2PPacket{
			Type:       PacketWelcome,
			RoomCode:   n.RoomCode,
			SenderID:   n.LocalID,
			Nickname:   n.Nickname,
			IsMuted:    n.audio.Muted,
			IsDeafened: n.audio.Deafened,
			Peers:      summaries,
			Timestamp:  time.Now().UnixMilli(),
		}
		go n.sendPacketTo(raddr, &welcomePkt)

	case PacketHello:
		// If we're a Joiner still searching and we see a Hello from an active host
		// in our target room → send a JoinRequest DIRECTLY to that host (unicast, bypasses broadcast issues)
		if n.Connecting && !n.IsConnected {
			room := n.ConnectTargetRoom
			if NormalizeCode(pkt.RoomCode) == room {
				joinPkt := P2PPacket{
					Type:       PacketJoinRequest,
					RoomCode:   room,
					SenderID:   n.LocalID,
					Nickname:   n.Nickname,
					IsMuted:    n.audio.Muted,
					IsDeafened: n.audio.Deafened,
					Timestamp:  time.Now().UnixMilli(),
				}
				go n.sendPacketTo(raddr, &joinPkt)
			}
			return
		}
		if !n.IsConnected {
			return
		}

		// Reply with Welcome and existing peers list
		summaries := make([]PeerSummary, 0, len(n.Peers))
		for _, p := range n.Peers {
			if p.ID != pkt.SenderID {
				addrStr := ""
				if p.Addr != nil {
					addrStr = p.Addr.String()
				}
				summaries = append(summaries, PeerSummary{
					ID:         p.ID,
					Nickname:   p.Nickname,
					AddrStr:    addrStr,
					IsMuted:    p.IsMuted,
					IsDeafened: p.IsDeafened,
				})
			}
		}

		welcomePkt := P2PPacket{
			Type:       PacketWelcome,
			RoomCode:   n.RoomCode,
			SenderID:   n.LocalID,
			Nickname:   n.Nickname,
			IsMuted:    n.audio.Muted,
			IsDeafened: n.audio.Deafened,
			Peers:      summaries,
			Timestamp:  time.Now().UnixMilli(),
		}
		go n.sendPacketTo(raddr, &welcomePkt)

	case PacketWelcome:
		// If client was waiting to connect to an open room:
		if n.Connecting && !n.IsConnected {
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.Connecting = false
			n.IsConnected = true
			n.IsHost = false
			n.HostID = pkt.SenderID
			n.HostNick = pkt.Nickname
			n.RoomCode = n.ConnectTargetRoom

			hostPeer := &PeerInfo{
				ID:         pkt.SenderID,
				Nickname:   pkt.Nickname,
				Addr:       raddr,
				LastSeen:   time.Now(),
				IsMuted:    pkt.IsMuted,
				IsDeafened: pkt.IsDeafened,
			}
			n.Peers[pkt.SenderID] = hostPeer
			n.log(fmt.Sprintf("[+] %s odasina baglanildi (Host: %s | E2EE Guvenli)", n.RoomCode, pkt.Nickname))

			// Connect to other peers reported in Welcome packet (mesh topology)
			for _, pSum := range pkt.Peers {
				if pSum.ID != n.LocalID && n.Peers[pSum.ID] == nil && len(n.Peers) < MaxPeers-1 {
					var pAddr *net.UDPAddr
					if pSum.AddrStr != "" {
						pAddr, _ = net.ResolveUDPAddr("udp4", pSum.AddrStr)
					}
					newPeer := &PeerInfo{
						ID:         pSum.ID,
						Nickname:   pSum.Nickname,
						Addr:       pAddr,
						LastSeen:   time.Now(),
						IsMuted:    pSum.IsMuted,
						IsDeafened: pSum.IsDeafened,
					}
					n.Peers[pSum.ID] = newPeer
					if pAddr != nil {
						helloPkt := P2PPacket{
							Type:       PacketHello,
							RoomCode:   n.RoomCode,
							SenderID:   n.LocalID,
							Nickname:   n.Nickname,
							IsMuted:    n.audio.Muted,
							IsDeafened: n.audio.Deafened,
							Timestamp:  time.Now().UnixMilli(),
						}
						go n.sendPacketTo(pAddr, &helloPkt)
					}
				}
			}

			successCb := n.OnJoinSuccess
			if successCb != nil {
				go successCb(pkt.Nickname)
			}
			if n.OnPeerEvent != nil {
				go n.OnPeerEvent("join", hostPeer)
			}
			return
		}

		// Connect to other peers reported in Welcome packet (mesh topology)
		for _, pSum := range pkt.Peers {
			if pSum.ID != n.LocalID && n.Peers[pSum.ID] == nil && len(n.Peers) < MaxPeers-1 {
				var pAddr *net.UDPAddr
				if pSum.AddrStr != "" {
					pAddr, _ = net.ResolveUDPAddr("udp4", pSum.AddrStr)
				}
				newPeer := &PeerInfo{
					ID:         pSum.ID,
					Nickname:   pSum.Nickname,
					Addr:       pAddr,
					LastSeen:   time.Now(),
					IsMuted:    pSum.IsMuted,
					IsDeafened: pSum.IsDeafened,
				}
				n.Peers[pSum.ID] = newPeer
				if pAddr != nil {
					// Introduce self to this peer
					helloPkt := P2PPacket{
						Type:       PacketHello,
						RoomCode:   n.RoomCode,
						SenderID:   n.LocalID,
						Nickname:   n.Nickname,
						IsMuted:    n.audio.Muted,
						IsDeafened: n.audio.Deafened,
						Timestamp:  time.Now().UnixMilli(),
					}
					go n.sendPacketTo(pAddr, &helloPkt)
				}
			}
		}

	case PacketRoomFull:
		if n.Connecting && !n.IsConnected {
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.Connecting = false
			n.aead = nil
			n.RoomCode = ""
			failedCb := n.OnJoinFailed
			n.log("❌ Odaya katilim reddedildi: Oda dolu (Maks 4 kisi).")
			if failedCb != nil {
				go failedCb("Bu oda dolu! (Maksimum 4 kisi)")
			}
		}

	case PacketPing:
		pong := P2PPacket{
			Type:       PacketPong,
			RoomCode:   n.RoomCode,
			SenderID:   n.LocalID,
			Nickname:   n.Nickname,
			IsMuted:    n.audio.Muted,
			IsDeafened: n.audio.Deafened,
			Timestamp:  pkt.Timestamp, // Echo timestamp
		}
		go n.sendPacketTo(raddr, &pong)

	case PacketPong:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			nowMs := time.Now().UnixMilli()
			rtt := nowMs - pkt.Timestamp
			if rtt >= 0 && rtt < 10000 {
				if peer.PingMs == 0 {
					peer.PingMs = rtt
				} else {
					// Exponential Moving Average filter (70% previous + 30% current)
					peer.PingMs = int64(float64(peer.PingMs)*0.70 + float64(rtt)*0.30)
				}
			}
		}

	case PacketAudio:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.Speaking = pkt.Speaking
			peer.RMS = pkt.RMS
			n.audio.PlayPeerPCM(pkt.SenderID, pkt.Payload, pkt.RMS, pkt.Speaking)
		}

	case PacketMuteState:
		// handled by auto-register / refresh at top

	case PacketLeave:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			delete(n.Peers, pkt.SenderID)
			n.log(fmt.Sprintf("[-] %s odadan ayrildi.", peer.Nickname))
			if n.OnPeerEvent != nil {
				go n.OnPeerEvent("leave", peer)
			}
		}
	}
}

func (n *P2PNode) heartbeatLoop() {
	ticker := time.NewTicker(1500 * time.Millisecond)
	for range ticker.C {
		n.mu.Lock()
		if !n.IsConnected {
			n.mu.Unlock()
			continue
		}

		now := time.Now()

		// If no peers connected yet, keep announcing presence to discover peers
		if len(n.Peers) == 0 {
			n.mu.Unlock()
			n.broadcastHello()
			continue
		}

		// Check timeouts & send pings (20s timeout for resilient internet connections)
		for id, peer := range n.Peers {
			if now.Sub(peer.LastSeen) > 20*time.Second {
				delete(n.Peers, id)
				n.log(fmt.Sprintf("[-] %s zaman asimina ugradi.", peer.Nickname))
				if n.OnPeerEvent != nil {
					go n.OnPeerEvent("leave", peer)
				}
				continue
			}

			pingPkt := P2PPacket{
				Type:       PacketPing,
				RoomCode:   n.RoomCode,
				SenderID:   n.LocalID,
				Nickname:   n.Nickname,
				IsMuted:    n.audio.Muted,
				IsDeafened: n.audio.Deafened,
				Timestamp:  now.UnixMilli(),
			}
			go n.sendPacketTo(peer.Addr, &pingPkt)
		}
		n.mu.Unlock()
	}
}

func (n *P2PNode) GetPeersList() []*PeerInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()

	list := make([]*PeerInfo, 0, len(n.Peers))
	for _, p := range n.Peers {
		list = append(list, p)
	}
	return list
}

func (n *P2PNode) log(msg string) {
	if n.OnLog != nil {
		n.OnLog(msg)
	}
}

// encodeAndEncryptPacket serializes the packet and encrypts it with AES-256-GCM AEAD
func encodeAndEncryptPacket(pkt *P2PPacket, aead cipher.AEAD) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(pkt); err != nil {
		return nil, err
	}

	plaintext := buf.Bytes()

	// 12-byte random nonce
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// Seal: [MagicPrefix (4 bytes)][Nonce (12 bytes)][Ciphertext + AuthTag]
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)

	out := make([]byte, 0, len(MagicPrefix)+len(nonce)+len(ciphertext))
	out = append(out, MagicPrefix...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	return out, nil
}

// decryptAndDecodePacket verifies the magic header, authenticates and decrypts with AES-256-GCM
func decryptAndDecodePacket(data []byte, pkt *P2PPacket, aead cipher.AEAD) error {
	headerLen := len(MagicPrefix)
	nonceLen := aead.NonceSize()
	minLen := headerLen + nonceLen + aead.Overhead()

	if len(data) < minLen {
		return errors.New("packet too short")
	}

	// Verify magic prefix
	if !bytes.Equal(data[:headerLen], MagicPrefix) {
		return errors.New("invalid packet magic header")
	}

	nonce := data[headerLen : headerLen+nonceLen]
	ciphertext := data[headerLen+nonceLen:]

	// Authenticate and Decrypt with AES-GCM
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return fmt.Errorf("decryption failed (authentication tag mismatch): %w", err)
	}

	decBuf := bytes.NewBuffer(plaintext)
	dec := gob.NewDecoder(decBuf)
	return dec.Decode(pkt)
}
