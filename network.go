package main

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thebanri/limoni-voice/internal/e2ee"
	"github.com/thebanri/limoni-voice/internal/nat"
	"github.com/thebanri/limoni-voice/internal/protocol"
	"github.com/thebanri/limoni-voice/internal/voice"
	"github.com/thebanri/limoni-voice/screenshare"
)

type P2PNode struct {
	mu                sync.RWMutex
	LocalID           string
	Nickname          string
	RoomCode          string // full normalized room code (shown to the user)
	roomID            string // public room identifier (relay room / LAN tag)
	roomSecret        string // handshake secret derived from the room code
	keyring           *e2ee.Keyring
	Conn              *net.UDPConn
	Conn6             *net.UDPConn
	BroadcastConn     *net.UDPConn
	Port              int
	Port6             int
	PublicIP          string
	PublicPort        int
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
	audioSeq          uint32
	pingSeq           uint32
	OnLog             func(msg string)
	OnPeerEvent       func(event string, peer *PeerInfo)
	OnRoomCodeChanged func(newCode string)
	stopChan          chan struct{}
	stopOnce          sync.Once
	// Dedicated broadcast sender socket with SO_BROADCAST (works on Windows too)
	bcastSendConn *net.UDPConn
	// Optional direct LAN target peer address
	TargetPeerAddr *net.UDPAddr
	lastSweepTime  time.Time

	// NAT traversal
	stun        *nat.Classifier
	stunServers []string
	ipv6Addrs   []string
	punchConns  []*net.UDPConn
	lastPunchAt map[string]time.Time

	// WebSocket / UDP relay
	RelayURL         string
	RelayToken       string
	LanOnly          bool
	wsConn           *websocket.Conn
	wsPriorityCh     chan []byte // realtime frames: audio, ping/pong, control (never delayed by video)
	wsReliableCh     chan []byte // file transfer chunks and rekeys
	wsVideoCh        chan []byte // video stream channel with bounded queue to eliminate bufferbloat
	wsMu             sync.Mutex
	isRelayConnected bool
	wsCancel         chan struct{}
	relayProto       int
	memberToken      string
	relayRTT         time.Duration
	relayPingSent    time.Time
	udpRelay         *udpRelayClient

	// Join handshake & admission
	joinClient       *e2ee.JoinClient
	joinHostID       string
	joinHostAddr     *net.UDPAddr
	joinAuth         []byte
	joinSessions     map[string]*joinSession
	handshakeFails   []time.Time
	handshakePauseTo time.Time

	// Group key rotation
	rekeyAcks  map[string]bool
	rekeyEpoch uint32
	rekeyKey   e2ee.GroupKey
	rekeyTimer *time.Timer

	// Anti-Tracking & Dynamic Port Hopping
	AntiTrackingEnabled bool
	hopInterval         time.Duration
	lastHopTime         time.Time
	nextHopTime         time.Time
	currentEpoch        uint32
	hopCancel           chan struct{}
	OnPortHopped        func(newPort int, epoch uint32)

	// Voice codec
	voiceEnc     *voice.Encoder
	voiceEncOnce sync.Once

	// Screen Sharing State (see network_screen.go)
	IsSharingScreen      bool
	ActiveScreenShareFPS int
	IsWatchingScreen     bool
	WatchingPeerID       string
	WatchingPeerNick     string
	ScreenSharePort      int
	ScreenPreset         int  // index into screenshare.Presets
	ShareSystemAudio     bool // include system audio when sharing
	screenTx             *screenTx
	screenRx             *screenRx
	relayTargeted        bool // relay forwards frames to single members
	audioDedup           AudioDeduplicator
	chatDedup            ChatDeduplicator
	ctrlDedup            ControlDeduplicator
	silenceHangover      int
	audioPreRoll         []audioPreRollFrame
	OnScreenShare        func(peerID string, isSharing bool, videoPort int)
	OnChatMessage        func(senderID string, nickname string, text string, ts time.Time)
	OnDebugLog           func(msg string)

	// Room Security (Lock & PIN Protection)
	IsLocked     bool
	RoomPIN      string
	hostToken    string
	OnRoomLocked func(isLocked bool, pin string)

	// P2P E2EE Direct File & Code Sharing
	incomingTransfers      map[string]*IncomingFileTransfer
	OnFileTransferProgress func(transferID string, fileName string, transferred int64, total int64, speed float64, isUpload bool, done bool, err error)
	OnFileReceived         func(transferID string, fileName string, filePath string, isCode bool, content string)
	OnFileOfferReceived    func(offer *FileOffer)
}

func NewP2PNode(localID, nickname string, audio *AudioEngine) *P2PNode {
	relayURL := os.Getenv("LIMONI_RELAY_URL")
	if relayURL == "" {
		if testing.Testing() {
			relayURL = ""
		} else {
			relayURL = DefaultRelayURL
		}
	}
	relayToken := os.Getenv("LIMONI_RELAY_TOKEN")
	if relayToken == "" {
		relayToken = os.Getenv("RELAY_AUTH_TOKEN")
	}
	lanOnly := false
	if testing.Testing() && os.Getenv("LIMONI_RELAY_URL") == "" {
		lanOnly = true
	}
	if val := strings.ToLower(os.Getenv("LIMONI_LAN_ONLY")); val == "1" || val == "true" || val == "yes" {
		lanOnly = true
	}
	if val := strings.ToLower(os.Getenv("LIMONI_OFFLINE")); val == "1" || val == "true" || val == "yes" {
		lanOnly = true
	}
	hopInterval := 30 * time.Minute
	if hopEnv := os.Getenv("LIMONI_HOP_INTERVAL"); hopEnv != "" {
		if dur, err := time.ParseDuration(hopEnv); err == nil && dur > 0 {
			hopInterval = dur
		}
	}

	if audio == nil {
		audio = NewAudioEngine()
	}

	// Create a dedicated UDP socket for sending broadcasts (avoids SO_BROADCAST issues on Windows)
	bcastConn, _ := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	node := &P2PNode{
		LocalID:             localID,
		Nickname:            nickname,
		RelayURL:            relayURL,
		RelayToken:          strings.TrimSpace(relayToken),
		LanOnly:             lanOnly,
		Peers:               make(map[string]*PeerInfo),
		audio:               audio,
		stopChan:            make(chan struct{}),
		bcastSendConn:       bcastConn,
		AntiTrackingEnabled: true,
		hopInterval:         hopInterval,
		incomingTransfers:   make(map[string]*IncomingFileTransfer),
		stun:                nat.NewClassifier(),
		stunServers:         nat.DefaultSTUNServers,
		joinSessions:        make(map[string]*joinSession),
		lastPunchAt:         make(map[string]time.Time),
	}
	if peerEnv := os.Getenv("LIMONI_PEER"); peerEnv != "" {
		node.SetTargetPeer(peerEnv)
	}
	return node
}

// SetTargetPeer sets an optional direct target IP/host for cross-subnet LAN P2P
func (n *P2PNode) SetTargetPeer(addrStr string) {
	addrStr = strings.TrimSpace(addrStr)
	if addrStr == "" {
		n.mu.Lock()
		n.TargetPeerAddr = nil
		n.mu.Unlock()
		return
	}
	if _, _, err := net.SplitHostPort(addrStr); err != nil {
		addrStr = net.JoinHostPort(strings.Trim(addrStr, "[]"), "50000")
	}
	uaddr, err := net.ResolveUDPAddr("udp", addrStr)
	if err == nil {
		n.mu.Lock()
		n.TargetPeerAddr = uaddr
		n.mu.Unlock()
		n.log(fmt.Sprintf("[DIRECT] Configured direct LAN peer: %s", uaddr.String()))
	}
}

// Start binds local UDP sockets (IPv4 predictable ports 50000-50050 first, plus IPv6).
func (n *P2PNode) Start() error {
	conn, chosenPort, err := bindVoiceSocket("udp4", 0)
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.Conn = conn
	n.Port = chosenPort
	n.mu.Unlock()

	// IPv6 socket on the same port number when possible (separate v6-only socket).
	if conn6, port6, err := bindVoiceSocket("udp6", chosenPort); err == nil {
		n.mu.Lock()
		n.Conn6 = conn6
		n.Port6 = port6
		n.mu.Unlock()
		go n.listenLoopOnConn(conn6)
	}
	n.refreshIPv6Candidates()

	// Setup broadcast listener on port 45454
	baddr, err := net.ResolveUDPAddr("udp4", ":45454")
	if err == nil {
		bConn, err := net.ListenUDP("udp4", baddr)
		if err == nil {
			n.BroadcastConn = bConn
			go n.listenBroadcastLoop()
		}
	}

	screenshare.LogCallback = n.log

	go n.listenLoopOnConn(conn)
	go n.heartbeatLoop()
	go n.DiscoverPublicEndpoint()

	return nil
}

// bindVoiceSocket binds a UDP socket, preferring preferredPort, then 50000-50050, then any port.
func bindVoiceSocket(network string, preferredPort int) (*net.UDPConn, int, error) {
	anyIP := net.IPv4zero
	if network == "udp6" {
		anyIP = net.IPv6unspecified
	}
	try := func(port int) *net.UDPConn {
		c, err := net.ListenUDP(network, &net.UDPAddr{IP: anyIP, Port: port})
		if err != nil {
			return nil
		}
		return c
	}
	var conn *net.UDPConn
	if preferredPort > 0 {
		conn = try(preferredPort)
	}
	for p := 50000; conn == nil && p <= 50050; p++ {
		conn = try(p)
	}
	if conn == nil {
		c, err := net.ListenUDP(network, &net.UDPAddr{IP: anyIP})
		if err != nil {
			return nil, 0, err
		}
		conn = c
	}
	_ = conn.SetReadBuffer(4 * 1024 * 1024)
	_ = conn.SetWriteBuffer(4 * 1024 * 1024)
	return conn, conn.LocalAddr().(*net.UDPAddr).Port, nil
}

func (n *P2PNode) isLanMode() bool {
	return n.LanOnly || n.RelayURL == "" || strings.EqualFold(n.RelayURL, "none") || strings.EqualFold(n.RelayURL, "off") || strings.EqualFold(n.RelayURL, "lan")
}

// HostRoom opens a new room as the authoritative Host
func (n *P2PNode) HostRoom(roomCode string) {
	n.mu.Lock()
	if n.connectCancel != nil {
		close(n.connectCancel)
		n.connectCancel = nil
	}
	if n.hopCancel != nil {
		close(n.hopCancel)
		n.hopCancel = nil
	}
	n.Connecting = false
	n.IsHost = true
	n.HostID = n.LocalID
	n.HostNick = n.Nickname
	n.RoomCode = NormalizeCode(roomCode)
	n.roomID, n.roomSecret = e2ee.SplitRoomCode(n.RoomCode)
	n.keyring, _ = e2ee.NewKeyring(1, e2ee.NewGroupKey())
	n.currentEpoch = 0
	n.joinClient = nil
	n.joinSessions = make(map[string]*joinSession)
	n.memberToken = ""
	n.lastHopTime = time.Now()
	n.nextHopTime = time.Now().Add(n.hopInterval)
	hopCancel := make(chan struct{})
	n.hopCancel = hopCancel

	n.IsLocked = false
	n.RoomPIN = ""

	n.IsConnected = true
	n.Peers = make(map[string]*PeerInfo)
	n.mu.Unlock()

	go n.portHopSupervisor(hopCancel)

	if n.isLanMode() {
		n.log(fmt.Sprintf("[HOST] Room opened: %s (Port: %d | LAN Mode | E2EE)", n.RoomCode, n.Port))
		n.broadcastHello()
	} else {
		n.log(fmt.Sprintf("[HOST] Room opened: %s (Port: %d | E2EE Secure)", n.RoomCode, n.Port))
		n.connectRelay("host", n.RoomCode)
	}
}

// RequestJoinRoom searches for an active host and requests admission. Fails if no open room exists.
func (n *P2PNode) RequestJoinRoom(roomCode string, timeout time.Duration, onSuccess func(hostNick string), onFailed func(reason string)) {
	// Check if input contains an explicit IP target, e.g. "192.168.1.50/4819-azure-wave" or "4819-azure-wave@192.168.1.50"
	var customPeerIP string
	if strings.Contains(roomCode, "/") {
		parts := strings.SplitN(roomCode, "/", 2)
		customPeerIP = strings.TrimSpace(parts[0])
		roomCode = parts[1]
	} else if strings.Contains(roomCode, "@") {
		parts := strings.SplitN(roomCode, "@", 2)
		roomCode = parts[0]
		customPeerIP = strings.TrimSpace(parts[1])
	}
	if customPeerIP != "" {
		n.SetTargetPeer(customPeerIP)
	}

	// Check if input contains an optional PIN, e.g. "5289-lunar-voice:1234" or "5289-lunar-voice#1234"
	var customPIN string
	if strings.Contains(roomCode, ":") {
		parts := strings.SplitN(roomCode, ":", 2)
		roomCode = parts[0]
		customPIN = strings.TrimSpace(parts[1])
	} else if strings.Contains(roomCode, "#") {
		parts := strings.SplitN(roomCode, "#", 2)
		roomCode = parts[0]
		customPIN = strings.TrimSpace(parts[1])
	}

	cleanCode := NormalizeCode(roomCode)
	if cleanCode == "" {
		if onFailed != nil {
			onFailed("Invalid room key")
		}
		return
	}

	n.mu.Lock()
	if n.connectCancel != nil {
		close(n.connectCancel)
		n.connectCancel = nil
	}
	cancelChan := make(chan struct{})
	hopCancel := make(chan struct{})
	n.hopCancel = hopCancel
	n.connectCancel = cancelChan

	n.RoomPIN = customPIN
	n.ConnectTargetRoom = cleanCode
	n.RoomCode = cleanCode
	n.roomID, n.roomSecret = e2ee.SplitRoomCode(cleanCode)
	n.keyring = nil
	n.joinClient = nil
	n.joinHostID = ""
	n.joinHostAddr = nil
	n.joinAuth = nil
	n.memberToken = ""
	n.currentEpoch = 0
	n.lastHopTime = time.Now()
	n.nextHopTime = time.Now().Add(n.hopInterval)
	n.Connecting = true
	n.IsConnected = false
	n.IsHost = false
	n.HostID = ""
	n.HostNick = ""
	n.Peers = make(map[string]*PeerInfo)
	n.OnJoinSuccess = onSuccess
	n.OnJoinFailed = onFailed
	lan := n.isLanMode()
	targetPeer := n.TargetPeerAddr
	n.mu.Unlock()

	go n.portHopSupervisor(hopCancel)

	if lan {
		n.log(fmt.Sprintf("[CONNECT] Searching room '%s' on local network (LAN)...", cleanCode))
	} else {
		n.log(fmt.Sprintf("[CONNECT] Searching room '%s' and verifying host...", cleanCode))
		n.connectRelay("join", cleanCode)
	}

	// Only probe local LAN via UDP broadcast if explicitly in LAN mode or if TargetPeerAddr is configured
	probeLAN := lan || targetPeer != nil
	go func() {
		probeTicker := time.NewTicker(250 * time.Millisecond)
		defer probeTicker.Stop()

		timeoutTimer := time.NewTimer(timeout)
		defer timeoutTimer.Stop()

		if probeLAN {
			n.lanJoinProbe()
		}

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
				if probeLAN {
					n.lanJoinProbe()
				}

			case <-timeoutTimer.C:
				n.mu.Lock()
				if n.Connecting && !n.IsConnected {
					failedCb := n.OnJoinFailed
					n.resetJoinStateLocked()
					n.mu.Unlock()

					if lan {
						n.log(fmt.Sprintf("[ERROR] Room '%s' not found on LAN (Host offline or room not created).", cleanCode))
						if failedCb != nil {
							failedCb("Room not found on local network! Make sure your friend has opened the room.")
						}
					} else {
						n.log(fmt.Sprintf("[ERROR] Room '%s' not found on relay (Host offline or room not created).", cleanCode))
						if failedCb != nil {
							failedCb("Room not found via relay! Make sure your friend has opened the room and both are connected to the relay server.")
						}
					}
				} else {
					n.mu.Unlock()
				}
				return
			}
		}
	}()
}

// resetJoinStateLocked abandons an in-progress join. Caller holds n.mu.
func (n *P2PNode) resetJoinStateLocked() {
	if n.connectCancel != nil {
		close(n.connectCancel)
		n.connectCancel = nil
	}
	n.Connecting = false
	n.keyring = nil
	n.RoomCode = ""
	n.roomID = ""
	n.roomSecret = ""
	n.joinClient = nil
	n.joinHostID = ""
	n.joinHostAddr = nil
	n.joinAuth = nil
}

// failJoin aborts a pending join with a user-visible reason.
func (n *P2PNode) failJoin(reason string) {
	n.mu.Lock()
	if !n.Connecting || n.IsConnected {
		n.mu.Unlock()
		return
	}
	failedCb := n.OnJoinFailed
	n.resetJoinStateLocked()
	n.RoomPIN = ""
	lan := n.isLanMode()
	n.mu.Unlock()
	n.log(fmt.Sprintf("[ERROR] Room join rejected: %s", reason))
	if !lan {
		n.closeRelay()
	}
	if failedCb != nil {
		go failedCb(reason)
	}
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
		n.resetJoinStateLocked()
		n.RoomPIN = ""
		n.log("Room join request cancelled.")
	}
}

// JoinRoom is kept for direct connection (e.g. tests or compatibility)
func (n *P2PNode) JoinRoom(roomCode string) {
	n.HostRoom(roomCode)
}

// LeaveRoom sends leave message and clears peer state
func (n *P2PNode) LeaveRoom() {
	n.CancelJoin()
	_ = n.StopScreenShare()
	_ = n.StopWatchingScreen()

	n.mu.Lock()
	if n.hopCancel != nil {
		close(n.hopCancel)
		n.hopCancel = nil
	}
	if n.rekeyTimer != nil {
		n.rekeyTimer.Stop()
		n.rekeyTimer = nil
	}
	n.hostToken = ""
	n.memberToken = ""
	if !n.IsConnected && !n.Connecting {
		n.RoomCode = ""
		n.roomID = ""
		n.roomSecret = ""
		n.keyring = nil
		n.mu.Unlock()
		return
	}
	room := n.roomID
	wasHost := n.IsHost
	keyring := n.keyring
	n.IsConnected = false
	n.Connecting = false
	n.IsHost = false
	n.HostID = ""
	n.HostNick = ""
	n.RoomCode = ""
	n.roomID = ""
	n.roomSecret = ""
	n.IsLocked = false
	n.RoomPIN = ""
	n.nextHopTime = time.Time{}
	n.lastHopTime = time.Time{}
	peers := make([]*PeerInfo, 0, len(n.Peers))
	for _, p := range n.Peers {
		peers = append(peers, p)
	}
	n.closePunchSocketsLocked()
	if n.audio != nil {
		n.audio.ClearAllPeers()
	}
	n.mu.Unlock()

	muted, deafened := n.audioState()
	pkt := P2PPacket{
		Type:       PacketLeave,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    muted,
		IsDeafened: deafened,
		Timestamp:  time.Now().UnixMilli(),
	}
	if keyring != nil {
		if data, err := sealPacket(&pkt, keyring); err == nil {
			for _, peer := range peers {
				if peer.Addr != nil {
					n.writeUDP(data, peer.Addr, peer.conn)
				}
			}
		}
	}
	n.closeRelay()

	n.mu.Lock()
	n.keyring = nil
	n.joinSessions = make(map[string]*joinSession)
	n.Peers = make(map[string]*PeerInfo)
	n.mu.Unlock()

	if wasHost {
		n.log("Closed room (Host left).")
	} else {
		n.log("Left the room.")
	}
}

// Close gracefully terminates all active network listeners, screen shares, and leaves any room
func (n *P2PNode) Close() {
	n.stopOnce.Do(func() {
		close(n.stopChan)
	})
	n.LeaveRoom()
	n.closeRelay()
	_ = n.StopScreenShare()
	_ = n.StopWatchingScreen()
	n.mu.Lock()
	for _, c := range []*net.UDPConn{n.Conn, n.Conn6, n.BroadcastConn, n.bcastSendConn} {
		if c != nil {
			_ = c.Close()
		}
	}
	n.Conn, n.Conn6, n.BroadcastConn, n.bcastSendConn = nil, nil, nil, nil
	n.mu.Unlock()
}

func (n *P2PNode) audioState() (muted, deafened bool) {
	if n.audio == nil {
		return false, false
	}
	n.audio.mu.RLock()
	defer n.audio.mu.RUnlock()
	return n.audio.Muted, n.audio.Deafened
}

// sealPacket encodes and encrypts a packet with the room keyring: [nonce][ciphertext+tag].
func sealPacket(pkt *P2PPacket, keyring *e2ee.Keyring) ([]byte, error) {
	if keyring == nil {
		return nil, e2ee.ErrNoKey
	}
	plain, err := pkt.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return keyring.Seal(plain)
}

// openPacket authenticates, decrypts and decodes a sealed packet.
func openPacket(data []byte, pkt *P2PPacket, keyring *e2ee.Keyring) error {
	if keyring == nil {
		return e2ee.ErrNoKey
	}
	plain, err := keyring.Open(data)
	if err != nil {
		return err
	}
	return pkt.UnmarshalBinary(plain)
}

// writeUDP sends a datagram on the socket matching the address family (or a dedicated punch socket).
func (n *P2PNode) writeUDP(data []byte, addr *net.UDPAddr, via *net.UDPConn) {
	if addr == nil {
		return
	}
	conn := via
	if conn == nil {
		n.mu.RLock()
		if addr.IP.To4() == nil {
			conn = n.Conn6
		} else {
			conn = n.Conn
		}
		n.mu.RUnlock()
	}
	if conn != nil {
		_, _ = conn.WriteToUDP(data, addr)
	}
}

func (n *P2PNode) sendDirectUDPPacket(addr *net.UDPAddr, pkt *P2PPacket) {
	if addr == nil {
		return
	}
	n.mu.RLock()
	keyring := n.keyring
	n.mu.RUnlock()
	data, err := sealPacket(pkt, keyring)
	if err == nil {
		n.writeUDP(data, addr, nil)
	}
}

// SendMuteState broadcasts mute/deafen state to the room.
func (n *P2PNode) SendMuteState(isMuted bool) {
	n.mu.RLock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.RUnlock()
		return
	}
	room := n.roomID
	n.mu.RUnlock()

	_, deafened := n.audioState()
	pkt := P2PPacket{
		Type:       PacketMuteState,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    isMuted,
		IsDeafened: deafened,
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
	room := n.roomID
	n.mu.RUnlock()

	muted, _ := n.audioState()
	pkt := P2PPacket{
		Type:       PacketMuteState,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    muted,
		IsDeafened: isDeafened,
		Timestamp:  time.Now().UnixMilli(),
	}
	n.broadcastToPeers(&pkt)
}

func (n *P2PNode) SendChatMessage(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(text) > 16384 {
		text = text[:16384]
	}
	n.mu.Lock()
	if !n.IsConnected || (len(n.Peers) == 0 && !n.isRelayConnected) {
		n.mu.Unlock()
		return
	}
	room := n.roomID
	n.seqCounter++
	seq := n.seqCounter
	n.mu.Unlock()

	pkt := P2PPacket{
		Type:      PacketChatMessage,
		RoomCode:  room,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		LocalPort: n.Port,
		Seq:       seq,
		Payload:   []byte(text),
		Timestamp: time.Now().UnixMilli(),
	}
	n.broadcastToPeers(&pkt)
}

// sendPacketTo sends a packet directly to addr, or through the relay when addr is nil.
func (n *P2PNode) sendPacketTo(addr *net.UDPAddr, pkt *P2PPacket) {
	n.mu.RLock()
	keyring := n.keyring
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	n.mu.RUnlock()

	data, err := sealPacket(pkt, keyring)
	if err != nil {
		return
	}
	if addr != nil {
		n.writeUDP(data, addr, n.punchConnFor(addr))
		return
	}
	n.sendRelayFrame(protocol.FrameRealtime, data)
}

// broadcastToPeers delivers a packet to every peer: directly where possible and through the
// relay when any peer is relay-only (or while still discovering peers).
func (n *P2PNode) broadcastToPeers(pkt *P2PPacket) {
	n.sendToRoom(pkt, protocol.FrameRealtime)
}

func (n *P2PNode) sendToRoom(pkt *P2PPacket, relayClass byte) {
	n.mu.RLock()
	keyring := n.keyring
	isRelay := n.isRelayConnected
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	if keyring == nil || (len(n.Peers) == 0 && !isRelay) {
		n.mu.RUnlock()
		return
	}
	type target struct {
		addr *net.UDPAddr
		conn *net.UDPConn
	}
	targets := make([]target, 0, len(n.Peers))
	hasRelayPeer := len(n.Peers) == 0
	for _, peer := range n.Peers {
		if peer.ViaRelay || peer.Addr == nil {
			hasRelayPeer = true
		}
		if peer.Addr != nil {
			targets = append(targets, target{peer.Addr, peer.conn})
		}
	}
	n.mu.RUnlock()

	data, err := sealPacket(pkt, keyring)
	if err != nil {
		return
	}
	for _, t := range targets {
		n.writeUDP(data, t.addr, t.conn)
	}
	if hasRelayPeer && isRelay {
		n.sendRelayFrame(relayClass, data)
	}
}

// GetPeer returns a snapshot of a peer (or nil)
func (n *P2PNode) GetPeer(id string) *PeerInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if p, ok := n.Peers[id]; ok {
		cp := *p
		return &cp
	}
	return nil
}

// GetPeersList returns snapshots of all peers sorted by ID.
func (n *P2PNode) GetPeersList() []*PeerInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()

	list := make([]*PeerInfo, 0, len(n.Peers))
	for _, p := range n.Peers {
		cp := *p
		list = append(list, &cp)
	}
	// Deterministic sorting by ID to eliminate position flickering across render cycles
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list
}

func (n *P2PNode) heartbeatLoop() {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	var tickCount int
	for {
		select {
		case <-n.stopChan:
			return
		case <-ticker.C:
		}

		tickCount++
		if tickCount%20 == 0 {
			go n.DiscoverPublicEndpoint()
		}

		n.mu.Lock()
		if !n.IsConnected {
			n.mu.Unlock()
			continue
		}

		now := time.Now()
		n.expireJoinSessionsLocked(now)

		// If no peers connected yet, keep announcing presence to discover peers
		if len(n.Peers) == 0 {
			n.mu.Unlock()
			n.broadcastHello()
			continue
		}

		// Check timeouts & send pings (45s timeout for resilient internet connections)
		var toPing, toPunch []*PeerInfo
		removed := false
		for id, peer := range n.Peers {
			if now.Sub(peer.LastSeen) > 45*time.Second {
				wasSharing := peer.IsSharingScreen
				delete(n.Peers, id)
				removed = true
				if n.audio != nil {
					n.audio.RemovePeer(id)
				}
				n.log(fmt.Sprintf("[-] %s timed out.", peer.Nickname))
				if n.OnPeerEvent != nil {
					go n.OnPeerEvent("leave", peer)
				}
				if (wasSharing || len(n.Peers) == 0) && n.IsWatchingScreen {
					go func() {
						_ = n.StopWatchingScreen()
					}()
				}
				continue
			}
			toPing = append(toPing, peer)
			if peer.ViaRelay && !n.LanOnly && now.Sub(n.lastPunchAt[id]) > 15*time.Second {
				n.lastPunchAt[id] = now
				toPunch = append(toPunch, peer)
			}
		}

		// If all peers disconnected, stop watching
		if n.IsWatchingScreen && len(n.Peers) == 0 {
			go func() {
				_ = n.StopWatchingScreen()
			}()
		}
		if removed && n.IsHost {
			n.scheduleRekeyLocked("member timed out")
		}
		n.mu.Unlock()

		for _, peer := range toPing {
			go n.sendPingToPeer(peer.ID)
		}
		for _, peer := range toPunch {
			go n.punchPeer(peer.ID, false)
		}
	}
}

// sendPingToPeer transmits a latency measurement packet to a specific peer
func (n *P2PNode) sendPingToPeer(peerID string) {
	n.mu.RLock()
	peer, ok := n.Peers[peerID]
	if !n.IsConnected || !ok {
		n.mu.RUnlock()
		return
	}
	roomID := n.roomID
	sharing := n.IsSharingScreen
	videoPort := n.ScreenSharePort
	videoFPS := n.ActiveScreenShareFPS
	peerAddr := peer.Addr
	viaRelay := peer.ViaRelay
	n.mu.RUnlock()

	muted, deafened := n.audioState()
	pingSeq := atomic.AddUint32(&n.pingSeq, 1)
	pingPkt := P2PPacket{
		Type:            PacketPing,
		RoomCode:        roomID,
		SenderID:        n.LocalID,
		Nickname:        n.Nickname,
		Seq:             pingSeq,
		IsMuted:         muted,
		IsDeafened:      deafened,
		IsSharingScreen: sharing,
		VideoPort:       videoPort,
		VideoFPS:        videoFPS,
		Timestamp:       time.Now().UnixMilli(),
	}
	if viaRelay || peerAddr == nil {
		n.sendPacketTo(nil, &pingPkt)
		// keep probing the direct path so it can take over once NAT mappings open
		if peerAddr != nil {
			probe := pingPkt
			n.sendPacketTo(peerAddr, &probe)
		}
	} else {
		n.sendPacketTo(peerAddr, &pingPkt)
	}
}

func (n *P2PNode) writeToFileLog(msg string) {
	if f, err := os.OpenFile("limoni-voice.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		_, _ = f.WriteString(time.Now().Format("15:04:05.000 ") + msg + "\n")
		_ = f.Close()
	}
}

func (n *P2PNode) debugLog(msg string) {
	n.writeToFileLog(msg)
	if n.OnDebugLog != nil {
		n.OnDebugLog(msg)
	}
}

func (n *P2PNode) log(msg string) {
	n.writeToFileLog(msg)
	if n.OnLog != nil {
		n.OnLog(msg)
	}
}

// LockRoom locks the current room against new joiners, optionally requiring a 4-digit PIN (Host only).
// The PIN is enforced by the host during the join handshake and is never sent to the relay.
func (n *P2PNode) LockRoom(pin string) {
	n.mu.Lock()
	if !n.IsHost {
		n.mu.Unlock()
		n.log("[SECURITY] Non-host attempted to lock room - ignored.")
		return
	}
	n.IsLocked = true
	n.RoomPIN = strings.TrimSpace(pin)
	roomPIN := n.RoomPIN
	if n.RoomPIN != "" {
		n.writeToFileLog("[SECURITY] Room locked with a PIN.")
		if n.OnLog != nil {
			n.OnLog(fmt.Sprintf("[SECURITY] Room locked with 4-digit PIN: %s", n.RoomPIN))
		}
	} else {
		n.log("[SECURITY] Room locked. No new members can join.")
	}
	n.mu.Unlock()

	n.sendRelaySignal(protocol.Signal{Type: protocol.SigLockRoom, IsLocked: true, PinRequired: roomPIN != ""})

	if n.OnRoomLocked != nil {
		go n.OnRoomLocked(true, roomPIN)
	}
}

// UnlockRoom unlocks the room for open joining (Host only).
func (n *P2PNode) UnlockRoom() {
	n.mu.Lock()
	if !n.IsHost {
		n.mu.Unlock()
		n.log("[SECURITY] Non-host attempted to unlock room - ignored.")
		return
	}
	n.IsLocked = false
	n.RoomPIN = ""
	n.log("[SECURITY] Room unlocked. Open for new members.")
	n.mu.Unlock()

	n.sendRelaySignal(protocol.Signal{Type: protocol.SigUnlockRoom})

	if n.OnRoomLocked != nil {
		go n.OnRoomLocked(false, "")
	}
}

func (n *P2PNode) listenLoopOnConn(conn *net.UDPConn) {
	if conn == nil {
		return
	}
	buf := make([]byte, 65535)
	for {
		readBytes, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		n.handleDatagram(buf[:readBytes], raddr, conn)
	}
}

func (n *P2PNode) listenBroadcastLoop() {
	n.mu.RLock()
	bConn := n.BroadcastConn
	n.mu.RUnlock()
	if bConn == nil {
		return
	}
	buf := make([]byte, 65535)
	for {
		readBytes, raddr, err := bConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		n.handleDatagram(buf[:readBytes], raddr, nil)
	}
}

// handleDatagram demultiplexes STUN answers, UDP relay traffic, LAN handshake frames and
// encrypted room packets arriving on any socket.
func (n *P2PNode) handleDatagram(data []byte, raddr *net.UDPAddr, via *net.UDPConn) {
	if nat.IsBindingResponse(data) {
		n.handleSTUNResponse(data)
		return
	}

	fromRelay := false
	if ur := n.currentUDPRelay(); ur != nil && ur.matches(raddr) {
		payload, isData := ur.handleDatagram(data)
		if !isData {
			return
		}
		data = payload
		fromRelay = true
	}

	if !fromRelay && n.handleLANHandshake(data, raddr) {
		return
	}

	n.mu.RLock()
	keyring := n.keyring
	active := n.IsConnected || n.Connecting
	n.mu.RUnlock()
	if !active || keyring == nil {
		return
	}

	var pkt P2PPacket
	if err := openPacket(data, &pkt, keyring); err != nil {
		return
	}

	if fromRelay {
		raddr = nil
	} else if via != nil && pkt.SenderID != "" {
		n.notePunchSocket(pkt.SenderID, via)
	}

	if n.handleScreenPacket(&pkt) {
		return
	}
	n.handlePacket(&pkt, raddr)
}

func (n *P2PNode) welcomeSummariesLocked(exceptID string) []PeerSummary {
	summaries := make([]PeerSummary, 0, len(n.Peers))
	for _, p := range n.Peers {
		if p.ID == exceptID {
			continue
		}
		addrStr := ""
		if p.Addr != nil {
			addrStr = p.Addr.String()
		}
		summaries = append(summaries, PeerSummary{
			ID:              p.ID,
			Nickname:        p.Nickname,
			AddrStr:         addrStr,
			LocalPort:       p.LocalPort,
			IsMuted:         p.IsMuted,
			IsDeafened:      p.IsDeafened,
			IsSharingScreen: p.IsSharingScreen,
			VideoPort:       p.VideoPort,
			VideoFPS:        p.VideoFPS,
		})
	}
	return summaries
}

func (n *P2PNode) welcomePacketLocked(exceptID string) P2PPacket {
	muted, deafened := false, false
	if n.audio != nil {
		muted, deafened = n.audio.Muted, n.audio.Deafened
	}
	return P2PPacket{
		Type:            PacketWelcome,
		RoomCode:        n.roomID,
		SenderID:        n.LocalID,
		Nickname:        n.Nickname,
		LocalPort:       n.Port,
		IsMuted:         muted,
		IsDeafened:      deafened,
		IsSharingScreen: n.IsSharingScreen,
		VideoPort:       n.ScreenSharePort,
		VideoFPS:        n.ActiveScreenShareFPS,
		Peers:           n.welcomeSummariesLocked(exceptID),
		IsLocked:        n.IsLocked,
		Timestamp:       time.Now().UnixMilli(),
	}
}

func (n *P2PNode) handlePacket(pkt *P2PPacket, raddr *net.UDPAddr) {
	// Ignore self
	if pkt.SenderID == n.LocalID {
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	// Packets are authenticated by the group key; the room ID must still match.
	if n.roomID == "" || !strings.EqualFold(pkt.RoomCode, n.roomID) || (!n.IsConnected && !n.Connecting) {
		return
	}

	// Verify packet timestamp freshness and deduplication for state control packets to prevent Replay Attacks
	switch pkt.Type {
	case PacketLeave, PacketRoomLocked, PacketRoomFull, PacketScreenShareStart, PacketScreenShareStop, PacketPortHop, PacketJoinRequest, PacketMuteState, PacketRekey:
		if pkt.Timestamp > 0 {
			nowMs := time.Now().UnixMilli()
			diff := nowMs - pkt.Timestamp
			if diff < -15000 || diff > 30000 { // Allow 15s future clock skew, 30s past delay
				return // Drop stale or replayed packet!
			}
		}
		// Rekeys are idempotent (epoch checked) and must be re-acknowledged on retransmission.
		if pkt.Type != PacketRekey && !n.ctrlDedup.ShouldProcess(pkt.SenderID, pkt.Type, pkt.Seq, pkt.Timestamp) {
			return // Drop duplicate replayed packet!
		}
	}

	var peerAddr *net.UDPAddr
	if raddr != nil {
		peerPort := raddr.Port
		if (raddr.IP.IsPrivate() || raddr.IP.IsLoopback()) && pkt.LocalPort > 0 {
			peerPort = pkt.LocalPort
		}
		peerAddr = &net.UDPAddr{IP: raddr.IP, Port: peerPort}
	}
	muted, deafened := false, false
	if n.audio != nil {
		muted, deafened = n.audio.Muted, n.audio.Deafened
	}

	// Auto-register or refresh peer on any valid authenticated packet from this room
	if pkt.Type != PacketJoinRequest && pkt.Type != PacketRoomFull && pkt.Type != PacketLeave && pkt.Type != PacketRoomLocked {
		if n.IsConnected {
			peer, exists := n.Peers[pkt.SenderID]
			if !exists {
				// Strict PIN & Lock check: If host is locked, reject any unexpected incoming packet from unregistered peer
				if n.IsHost && n.IsLocked {
					lockPkt := P2PPacket{
						Type:      PacketRoomLocked,
						RoomCode:  n.roomID,
						SenderID:  n.LocalID,
						Nickname:  n.Nickname,
						Payload:   []byte("PIN_REQUIRED"),
						Timestamp: time.Now().UnixMilli(),
					}
					if peerAddr != nil {
						go n.sendDirectUDPPacket(peerAddr, &lockPkt)
					}
					return
				}

				if len(n.Peers) < MaxPeers-1 {
					nick := pkt.Nickname
					if nick == "" {
						nick = "User_" + pkt.SenderID[:min(len(pkt.SenderID), 4)]
					}
					isPrivateAddr := (raddr != nil && (raddr.IP.IsPrivate() || raddr.IP.IsLoopback())) ||
						(peerAddr != nil && (peerAddr.IP.IsPrivate() || peerAddr.IP.IsLoopback()))
					isRelayed := (raddr == nil && peerAddr == nil) || (!n.LanOnly && isPrivateAddr)
					effectiveAddr := peerAddr
					if !n.LanOnly && isPrivateAddr {
						effectiveAddr = nil
					}
					var lastDirect time.Time
					if raddr != nil && (n.LanOnly || !isPrivateAddr) {
						lastDirect = time.Now()
					}
					peer = &PeerInfo{
						ID:             pkt.SenderID,
						Nickname:       nick,
						Addr:           effectiveAddr,
						LocalPort:      pkt.LocalPort,
						LastSeen:       time.Now(),
						LastDirectSeen: lastDirect,
						ViaRelay:       isRelayed,
						IsMuted:        pkt.IsMuted,
						IsDeafened:     pkt.IsDeafened,
					}
					n.Peers[pkt.SenderID] = peer
					n.log(fmt.Sprintf("[+] Established connection with %s. (E2EE Secure)", nick))
					if n.OnPeerEvent != nil {
						go n.OnPeerEvent("join", peer)
					}
					go n.sendPingToPeer(peer.ID)
				}
			} else {
				isPrivateAddr := (raddr != nil && (raddr.IP.IsPrivate() || raddr.IP.IsLoopback())) ||
					(peerAddr != nil && (peerAddr.IP.IsPrivate() || peerAddr.IP.IsLoopback()))
				if peerAddr != nil && (n.LanOnly || !isPrivateAddr) {
					peer.Addr = peerAddr
				}
				peer.LastSeen = time.Now()
				if raddr != nil && (n.LanOnly || !isPrivateAddr) {
					if peer.ViaRelay {
						n.debugLog(fmt.Sprintf("[P2P] Direct path to %s established (%s)", peer.Nickname, raddr))
					}
					peer.LastDirectSeen = time.Now()
					peer.ViaRelay = false
					peer.Addr = raddr
					peer.PunchState = "direct"
				} else if peer.Addr == nil || time.Since(peer.LastDirectSeen) > 3*time.Second || (!n.LanOnly && isPrivateAddr) {
					peer.ViaRelay = true
				}
				if pkt.Nickname != "" {
					peer.Nickname = pkt.Nickname
				}
				if pkt.LocalPort > 0 {
					peer.LocalPort = pkt.LocalPort
				}
				if pkt.Type != PacketAudio {
					peer.IsMuted = pkt.IsMuted
					peer.IsDeafened = pkt.IsDeafened
				}
			}
		}
	}

	switch pkt.Type {
	case PacketJoinRequest:
		// Only an active Host of this room can admit joiners (joiner already holds the group key)
		if !n.IsConnected || !n.IsHost {
			return
		}

		// Check Room Lock & PIN Protection
		if n.IsLocked {
			reason := "ROOM_LOCKED"
			if n.RoomPIN != "" {
				reason = "PIN_REQUIRED"
			}
			if n.RoomPIN == "" || strings.TrimSpace(pkt.PIN) != n.RoomPIN {
				lockPkt := P2PPacket{
					Type:      PacketRoomLocked,
					RoomCode:  n.roomID,
					SenderID:  n.LocalID,
					Nickname:  n.Nickname,
					Payload:   []byte(reason),
					Timestamp: time.Now().UnixMilli(),
				}
				if peerAddr != nil {
					go n.sendDirectUDPPacket(peerAddr, &lockPkt)
				}
				return
			}
		}

		// Check peer limit (Max 4 people: Host + 3 peers)
		if len(n.Peers) >= MaxPeers-1 && n.Peers[pkt.SenderID] == nil {
			fullPkt := P2PPacket{
				Type:      PacketRoomFull,
				RoomCode:  n.roomID,
				SenderID:  n.LocalID,
				Nickname:  n.Nickname,
				LocalPort: n.Port,
				Timestamp: time.Now().UnixMilli(),
			}
			if peerAddr != nil {
				go n.sendDirectUDPPacket(peerAddr, &fullPkt)
			}
			return
		}

		peer, exists := n.Peers[pkt.SenderID]
		if !exists {
			peer = &PeerInfo{
				ID:         pkt.SenderID,
				Nickname:   pkt.Nickname,
				Addr:       peerAddr,
				LocalPort:  pkt.LocalPort,
				LastSeen:   time.Now(),
				IsMuted:    pkt.IsMuted,
				IsDeafened: pkt.IsDeafened,
			}
			n.Peers[pkt.SenderID] = peer
			n.log(fmt.Sprintf("[+] %s joined the room! (E2EE Secure)", pkt.Nickname))
			if n.OnPeerEvent != nil {
				go n.OnPeerEvent("join", peer)
			}
		} else {
			if peerAddr != nil {
				peer.Addr = peerAddr
			}
			peer.LastSeen = time.Now()
			peer.Nickname = pkt.Nickname
			if pkt.LocalPort > 0 {
				peer.LocalPort = pkt.LocalPort
			}
			peer.IsMuted = pkt.IsMuted
			peer.IsDeafened = pkt.IsDeafened
		}

		welcomePkt := n.welcomePacketLocked(pkt.SenderID)
		if peerAddr != nil {
			go n.sendDirectUDPPacket(peerAddr, &welcomePkt)
		}

	case PacketHello:
		// A joiner that already holds the group key sees a host Hello → ask to join directly.
		if n.Connecting && !n.IsConnected {
			joinPkt := P2PPacket{
				Type:       PacketJoinRequest,
				RoomCode:   n.roomID,
				SenderID:   n.LocalID,
				Nickname:   n.Nickname,
				LocalPort:  n.Port,
				IsMuted:    muted,
				IsDeafened: deafened,
				PIN:        n.RoomPIN,
				Timestamp:  time.Now().UnixMilli(),
			}
			if peerAddr != nil {
				go n.sendDirectUDPPacket(peerAddr, &joinPkt)
			}
			return
		}
		if !n.IsConnected {
			return
		}

		welcomePkt := n.welcomePacketLocked(pkt.SenderID)
		if peerAddr != nil {
			go n.sendDirectUDPPacket(peerAddr, &welcomePkt)
		}

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
			if pkt.IsLocked {
				n.IsLocked = true
			}

			isPrivateAddr := (raddr != nil && (raddr.IP.IsPrivate() || raddr.IP.IsLoopback())) ||
				(peerAddr != nil && (peerAddr.IP.IsPrivate() || peerAddr.IP.IsLoopback()))
			isRelayed := (raddr == nil && peerAddr == nil) || (!n.LanOnly && isPrivateAddr)
			effectiveHostAddr := peerAddr
			if !n.LanOnly && isPrivateAddr {
				effectiveHostAddr = nil
			}
			var lastDirect time.Time
			if raddr != nil && (n.LanOnly || !isPrivateAddr) {
				lastDirect = time.Now()
			}
			hostPeer := &PeerInfo{
				ID:              pkt.SenderID,
				Nickname:        pkt.Nickname,
				Addr:            effectiveHostAddr,
				LocalPort:       pkt.LocalPort,
				LastSeen:        time.Now(),
				LastDirectSeen:  lastDirect,
				ViaRelay:        isRelayed,
				IsMuted:         pkt.IsMuted,
				IsDeafened:      pkt.IsDeafened,
				IsSharingScreen: pkt.IsSharingScreen,
				VideoPort:       pkt.VideoPort,
				VideoFPS:        pkt.VideoFPS,
			}
			n.Peers[pkt.SenderID] = hostPeer
			go n.sendPingToPeer(hostPeer.ID)
			n.log(fmt.Sprintf("[+] Connected to room %s (Host: %s | E2EE Secure)", n.RoomCode, pkt.Nickname))

			n.introduceWelcomePeersLocked(pkt, peerAddr, muted, deafened)

			successCb := n.OnJoinSuccess
			if successCb != nil {
				go successCb(pkt.Nickname)
			}
			return
		}

		n.introduceWelcomePeersLocked(pkt, nil, muted, deafened)

	case PacketRoomFull:
		if n.Connecting && !n.IsConnected {
			failedCb := n.OnJoinFailed
			n.resetJoinStateLocked()
			n.log("❌ Room join rejected: Room full (Max 4 people).")
			if failedCb != nil {
				go failedCb("This room is full! (Maximum 4 people)")
			}
		}

	case PacketPing:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.IsSharingScreen = pkt.IsSharingScreen
			peer.VideoPort = pkt.VideoPort
			if pkt.VideoFPS > 0 {
				peer.VideoFPS = pkt.VideoFPS
			}
		}
		// Dedup incoming ping: if we already received and answered this exact ping seq/timestamp, don't echo again
		if pkt.Timestamp > 0 && !n.ctrlDedup.ShouldProcess(pkt.SenderID, PacketPing, pkt.Seq, pkt.Timestamp) {
			return
		}
		// Answer on the path the ping arrived on: direct pings open NAT mappings both ways.
		destAddr := raddr
		loss, jitter := 0.0, 0.0
		if n.audio != nil {
			loss, jitter = n.audio.PeerReceiveQuality(pkt.SenderID)
		}
		pong := P2PPacket{
			Type:            PacketPong,
			RoomCode:        n.roomID,
			SenderID:        n.LocalID,
			Nickname:        n.Nickname,
			Seq:             pkt.Seq,
			IsMuted:         muted,
			IsDeafened:      deafened,
			IsSharingScreen: n.IsSharingScreen,
			VideoPort:       n.ScreenSharePort,
			VideoFPS:        n.ActiveScreenShareFPS,
			Timestamp:       pkt.Timestamp, // Echo timestamp
			LossPct:         uint8(math.Round(math.Min(loss, 100))),
			JitterMs:        uint16(math.Round(math.Min(jitter, 65535))),
		}
		go n.sendPacketTo(destAddr, &pong)

	case PacketPong:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.IsSharingScreen = pkt.IsSharingScreen
			peer.VideoPort = pkt.VideoPort
			if pkt.VideoFPS > 0 {
				peer.VideoFPS = pkt.VideoFPS
			}
			peer.RemoteLossPct = float64(pkt.LossPct)

			// Dedup incoming pong: only accept the earliest/fastest pong for this ping sequence.
			// Drops delayed redundant copies arriving from alternate transport (e.g. relay vs direct UDP).
			if pkt.Timestamp > 0 && !n.ctrlDedup.ShouldProcess(pkt.SenderID, PacketPong, pkt.Seq, pkt.Timestamp) {
				return
			}

			rtt := time.Now().UnixMilli() - pkt.Timestamp
			if rtt <= 0 {
				rtt = 1
			}
			if rtt < 3000 {
				if peer.PingMs <= 0 {
					peer.PingMs = rtt
				} else {
					// Exponential Moving Average (EMA) with 70% history, 30% new sample
					peer.PingMs = int64(math.Round(float64(peer.PingMs)*0.70 + float64(rtt)*0.30))
					if peer.PingMs <= 0 {
						peer.PingMs = 1
					}
				}
			}
			go n.adaptEncoderToLoss()
		}

	case PacketAudio:
		if !n.audioDedup.ShouldProcess(pkt.SenderID, pkt.Seq) {
			return // Ignore duplicate packet received over redundant transport
		}
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.Speaking = pkt.Speaking
			peer.RMS = pkt.RMS
			if n.audio != nil {
				n.audio.PlayPeerOpus(pkt.SenderID, pkt.Seq, pkt.Timestamp, pkt.Payload, pkt.RMS, pkt.Speaking)
			}
		}

	case PacketMuteState:
		// handled by auto-register / refresh at top

	case PacketScreenShareStart:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			wasSharing := peer.IsSharingScreen
			peer.IsSharingScreen = true
			peer.VideoPort = pkt.VideoPort
			fps := pkt.VideoFPS
			if fps <= 0 {
				fps = 60
			}
			peer.VideoFPS = fps
			peer.VideoKbps = int(pkt.VideoKbps)
			peer.ScreenAudio = pkt.HasAudio
			if wasSharing {
				break // bitrate / audio update of an ongoing share
			}
			audio := ""
			if pkt.HasAudio {
				audio = " with system audio"
			}
			n.log(fmt.Sprintf("[SCREEN] %s started screen sharing (%d FPS%s)", peer.Nickname, peer.VideoFPS, audio))
			if n.OnScreenShare != nil {
				go n.OnScreenShare(pkt.SenderID, true, pkt.VideoPort)
			}
		}

	case PacketScreenShareStop:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.IsSharingScreen = false
			peer.VideoPort = 0
			peer.VideoFPS = 0
			peer.VideoKbps = 0
			peer.ScreenAudio = false
			n.log(fmt.Sprintf("[SCREEN] %s stopped screen sharing.", peer.Nickname))
			if n.OnScreenShare != nil {
				go n.OnScreenShare(pkt.SenderID, false, 0)
			}
		}
		watchingThisPeer := n.IsWatchingScreen && (n.WatchingPeerID == pkt.SenderID || n.WatchingPeerID == "")
		if watchingThisPeer {
			go func() {
				_ = n.StopWatchingScreen()
			}()
		}

	case PacketChatMessage:
		payload := pkt.Payload
		if len(payload) > 16384 {
			payload = payload[:16384]
		}
		msgText := string(payload)
		if msgText != "" {
			if !n.chatDedup.ShouldProcess(pkt.SenderID, pkt.Seq, pkt.Timestamp, msgText) {
				return
			}
			ts := time.UnixMilli(pkt.Timestamp)
			if pkt.Timestamp == 0 {
				ts = time.Now()
			}
			if n.OnChatMessage != nil {
				go n.OnChatMessage(pkt.SenderID, pkt.Nickname, msgText, ts)
			}
		}

	case PacketPortHop:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			if pkt.LocalPort > 0 {
				peer.LocalPort = pkt.LocalPort
				if raddr != nil {
					pPort := raddr.Port
					if (raddr.IP.IsPrivate() || raddr.IP.IsLoopback()) && pkt.LocalPort > 0 {
						pPort = pkt.LocalPort
					}
					peer.Addr = &net.UDPAddr{IP: raddr.IP, Port: pPort}
				} else if peer.Addr != nil && pkt.LocalPort > 0 {
					peer.Addr = &net.UDPAddr{IP: peer.Addr.IP, Port: pkt.LocalPort}
				}
				peer.conn = nil
			}
			peer.LastSeen = time.Now()
			n.log(fmt.Sprintf("[SECURITY] Peer %s rotated endpoint to port :%d (Epoch %d)", pkt.Nickname, pkt.LocalPort, pkt.Seq))

			// Immediately respond with a PacketPong so NAT hole-punching succeeds bidirectionally
			if peer.Addr != nil {
				pong := P2PPacket{
					Type:            PacketPong,
					RoomCode:        n.roomID,
					SenderID:        n.LocalID,
					Nickname:        n.Nickname,
					IsMuted:         muted,
					IsDeafened:      deafened,
					IsSharingScreen: n.IsSharingScreen,
					VideoPort:       n.ScreenSharePort,
					VideoFPS:        n.ActiveScreenShareFPS,
					Timestamp:       pkt.Timestamp,
				}
				go n.sendDirectUDPPacket(peer.Addr, &pong)
			}
		}

	case PacketLeave:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			wasSharing := peer.IsSharingScreen
			isHostLeaving := (pkt.SenderID == n.HostID)
			delete(n.Peers, pkt.SenderID)
			if n.audio != nil {
				n.audio.RemovePeer(pkt.SenderID)
			}
			n.log(fmt.Sprintf("[-] %s left the room.", peer.Nickname))
			if n.OnPeerEvent != nil {
				go n.OnPeerEvent("leave", peer)
			}
			if isHostLeaving {
				// Deterministic LAN host election among remaining peers
				electedID := n.LocalID
				electedNick := n.Nickname
				for pid, p := range n.Peers {
					if pid < electedID {
						electedID = pid
						electedNick = p.Nickname
					}
				}
				n.HostID = electedID
				n.HostNick = electedNick
				if electedID == n.LocalID {
					n.IsHost = true
					if n.RoomPIN != "" {
						n.IsLocked = true
						n.log(fmt.Sprintf("[HOST] Former host left, you are now the room HOST! (Room PIN: %s)", n.RoomPIN))
					} else {
						n.log("[HOST] Former host left, you are now the room HOST!")
					}
					if n.OnRoomLocked != nil && n.IsLocked {
						go n.OnRoomLocked(true, n.RoomPIN)
					}
				} else {
					n.IsHost = false
					n.log(fmt.Sprintf("[HOST] New room HOST: %s", electedNick))
				}
			}
			if n.IsHost {
				n.scheduleRekeyLocked("member left")
			}
			watchingThisPeer := wasSharing && n.IsWatchingScreen && (n.WatchingPeerID == pkt.SenderID || n.WatchingPeerID == "")
			if watchingThisPeer {
				go func() {
					_ = n.StopWatchingScreen()
				}()
			}
		}

	case PacketRoomLocked:
		reason := "Room is locked by host"
		if string(pkt.Payload) == "PIN_REQUIRED" {
			reason = "Room is protected by PIN (join with code:PIN)"
		}
		if n.Connecting && !n.IsConnected {
			failedCb := n.OnJoinFailed
			n.resetJoinStateLocked()
			n.RoomPIN = ""
			n.log(fmt.Sprintf("[SECURITY] %s", reason))
			if failedCb != nil {
				go failedCb(reason)
			}
		}

	case PacketRekey:
		n.handleRekeyLocked(pkt, raddr)

	case PacketRekeyAck:
		n.handleRekeyAckLocked(pkt)

	case PacketFileHeader, PacketFileChunk:
		n.handleFilePacketLocked(pkt)

	case PacketFileAck:
		// Transfer acknowledgment received
	}
}

// introduceWelcomePeersLocked registers the other members listed in a Welcome packet (mesh topology).
func (n *P2PNode) introduceWelcomePeersLocked(pkt *P2PPacket, hostAddr *net.UDPAddr, muted, deafened bool) {
	for _, pSum := range pkt.Peers {
		if pSum.ID == n.LocalID || n.Peers[pSum.ID] != nil || len(n.Peers) >= MaxPeers-1 {
			continue
		}
		var pAddr *net.UDPAddr
		if pSum.AddrStr != "" {
			pAddr, _ = net.ResolveUDPAddr("udp", pSum.AddrStr)
		} else if hostAddr != nil && pSum.LocalPort > 0 {
			pAddr = &net.UDPAddr{IP: hostAddr.IP, Port: pSum.LocalPort}
		}
		var pDirect time.Time
		if pAddr != nil {
			pDirect = time.Now()
		}
		n.Peers[pSum.ID] = &PeerInfo{
			ID:              pSum.ID,
			Nickname:        pSum.Nickname,
			Addr:            pAddr,
			LocalPort:       pSum.LocalPort,
			LastSeen:        time.Now(),
			LastDirectSeen:  pDirect,
			ViaRelay:        pAddr == nil,
			IsMuted:         pSum.IsMuted,
			IsDeafened:      pSum.IsDeafened,
			IsSharingScreen: pSum.IsSharingScreen,
			VideoPort:       pSum.VideoPort,
			VideoFPS:        pSum.VideoFPS,
		}
		if pAddr != nil {
			helloPkt := P2PPacket{
				Type:       PacketHello,
				RoomCode:   n.roomID,
				SenderID:   n.LocalID,
				Nickname:   n.Nickname,
				LocalPort:  n.Port,
				IsMuted:    muted,
				IsDeafened: deafened,
				Timestamp:  time.Now().UnixMilli(),
			}
			go n.sendDirectUDPPacket(pAddr, &helloPkt)
		}
	}
}

var errNotInRoom = errors.New("node is not in a room")
