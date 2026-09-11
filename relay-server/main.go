package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const MaxRoomMembers = 4

// ControlMessage is a JSON message exchanged between client and relay for room management and P2P hole-punching
type ControlMessage struct {
	Type       string     `json:"type"`
	RoomCode   string     `json:"room_code,omitempty"`
	SenderID   string     `json:"sender_id,omitempty"`
	Nickname   string     `json:"nickname,omitempty"`
	Message    string     `json:"message,omitempty"`
	Port       int        `json:"port,omitempty"`        // Client's local UDP port
	LocalIP    string     `json:"local_ip,omitempty"`    // Client's internal LAN IP
	PublicIP   string     `json:"public_ip,omitempty"`    // Sender's observed public IP
	PublicPort int        `json:"public_port,omitempty"`  // Sender's observed port
	YourIP     string     `json:"your_ip,omitempty"`      // Client's own detected public IP
	PIN        string     `json:"pin,omitempty"`          // Room password / PIN
	IsLocked   bool       `json:"is_locked,omitempty"`    // Room locked state
	Peers      []PeerInfo `json:"peers,omitempty"`
	HostToken  string     `json:"host_token,omitempty"`   // Cryptographic secret token to prevent host hijacking
}

type PeerInfo struct {
	SenderID   string `json:"sender_id"`
	Nickname   string `json:"nickname"`
	LocalIP    string `json:"local_ip,omitempty"`
	PublicIP   string `json:"public_ip,omitempty"`
	LocalPort  int    `json:"local_port,omitempty"`
	PublicPort int    `json:"public_port,omitempty"`
}

type Client struct {
	conn              *websocket.Conn
	senderID          string
	nickname          string
	localPort         int
	localIP           string
	publicIP          string
	publicPort        int
	room              *Room
	prioritySendCh    chan []byte // high-priority channel for Voice, Ping, Pong, Control (never delayed)
	videoSendCh       chan []byte // bounded channel for video chunks with safe backpressure
	mu                sync.Mutex
	explicitLeave     bool
	isDisconnected    bool
	disconnectTimer   *time.Timer
	failedPINAttempts  int
	lastPINAttempt     time.Time
	roomCreationCount  int
	roomCreationWindow time.Time
}

type Room struct {
	Code      string
	HostID    string
	HostToken string // Secret random token known only to the host
	PIN       string
	IsLocked  bool
	Members   map[string]*Client // senderID -> Client
	mu        sync.RWMutex
	created   time.Time
}

func generateHostToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

type RelayServer struct {
	authToken string
	rooms     map[string]*Room
	mu        sync.RWMutex
	upgrader  websocket.Upgrader
}

func NewRelayServer(authTokens ...string) *RelayServer {
	token := ""
	if len(authTokens) > 0 {
		token = strings.TrimSpace(authTokens[0])
	}
	return &RelayServer{
		authToken: token,
		rooms:     make(map[string]*Room),
		upgrader: websocket.Upgrader{
			CheckOrigin:     func(r *http.Request) bool { return true },
			ReadBufferSize:  65536,
			WriteBufferSize: 65536,
		},
	}
}

func extractClientIP(r *http.Request) string {
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return strings.TrimSpace(cf)
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return strings.TrimSpace(xrip)
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *RelayServer) handleWS(w http.ResponseWriter, r *http.Request) {
	clientIP := extractClientIP(r)

	// Validate server authentication token if configured
	if s.authToken != "" {
		clientToken := r.URL.Query().Get("token")
		if clientToken == "" {
			clientToken = r.Header.Get("X-Auth-Token")
		}
		if clientToken == "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				clientToken = strings.TrimSpace(authHeader[7:])
			}
		}

		if subtle.ConstantTimeCompare([]byte(clientToken), []byte(s.authToken)) != 1 {
			log.Printf("[SECURITY] Unauthorized connection rejected from %s (invalid or missing auth token)", clientIP)
			http.Error(w, "Unauthorized: Invalid or missing relay authentication token", http.StatusUnauthorized)
			return
		}
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	// Enforce 64 KB max frame limit to prevent memory exhaustion DoS / OOM attacks
	conn.SetReadLimit(65536)

	if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}

	remoteAddr := r.RemoteAddr
	log.Printf("[🌐] New connection from IP: %s (remote: %s)", clientIP, remoteAddr)

	if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
		_ = tcpConn.SetWriteBuffer(256 * 1024)
		_ = tcpConn.SetReadBuffer(256 * 1024)
	}

	client := &Client{
		conn:           conn,
		publicIP:       clientIP,
		prioritySendCh: make(chan []byte, 128),
		videoSendCh:    make(chan []byte, 128),
	}

	// Start write pump
	go client.writePump()

	defer func() {
		log.Printf("[🔌] Connection closed for %s (%s)", client.nickname, remoteAddr)
		if client.explicitLeave {
			s.removeClientImmediate(client)
		} else {
			s.handleDisconnect(client)
		}
		conn.Close()
	}()

	// Set read deadline and handlers for keepalive
	conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		client.mu.Lock()
		err := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
		client.mu.Unlock()
		return err
	})
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		return nil
	})

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("Client disconnected: %v", err)
			}
			return
		}

		// Refresh deadline on valid message
		conn.SetReadDeadline(time.Now().Add(45 * time.Second))

		switch msgType {
		case websocket.TextMessage:
			// Control message (JSON)
			s.handleControlMessage(client, data)

		case websocket.BinaryMessage:
			// Encrypted audio/data packet — forward to all other room members
			s.relayBinaryData(client, data)
		}
	}
}

func (s *RelayServer) handleControlMessage(client *Client, data []byte) {
	var msg ControlMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		sendControlMessage(client, ControlMessage{Type: "error", Message: "Invalid JSON message"})
		return
	}

	log.Printf("[📩] Control message '%s' from %s (%s) for room '%s' (port: %d)", msg.Type, msg.Nickname, msg.SenderID, msg.RoomCode, msg.Port)

	switch msg.Type {
	case "host_room":
		s.handleHostRoom(client, msg)
	case "join_room":
		s.handleJoinRoom(client, msg)
	case "lock_room":
		if client.room != nil {
			client.room.mu.Lock()
			if client.room.HostID == client.senderID {
				client.room.IsLocked = true
				client.room.PIN = strings.TrimSpace(msg.PIN)
				log.Printf("[SECURITY] Room %s locked by host %s (PIN: %s)", client.room.Code, client.nickname, client.room.PIN)
			}
			client.room.mu.Unlock()
		}
	case "unlock_room":
		if client.room != nil {
			client.room.mu.Lock()
			if client.room.HostID == client.senderID {
				client.room.IsLocked = false
				client.room.PIN = ""
				log.Printf("[SECURITY] Room %s unlocked by host %s", client.room.Code, client.nickname)
			}
			client.room.mu.Unlock()
		}
	case "port_update":
		client.localPort = msg.Port
		if msg.PublicPort > 0 {
			client.publicPort = msg.PublicPort
		}
		if msg.PublicIP != "" {
			client.publicIP = msg.PublicIP
		}
		if msg.LocalIP != "" {
			client.localIP = msg.LocalIP
		}
		log.Printf("[🛡️] Client %s (%s) rotated endpoint: local=%s:%d, public=%s:%d", client.nickname, client.senderID, client.localIP, client.localPort, client.publicIP, client.publicPort)
		if client.room != nil {
			client.room.mu.RLock()
			for _, m := range client.room.Members {
				if m != client && !m.isDisconnected {
					sendControlMessage(m, ControlMessage{
						Type:       "peer_port_updated",
						SenderID:   client.senderID,
						Nickname:   client.nickname,
						Port:       client.localPort,
						LocalIP:    client.localIP,
						PublicPort: client.publicPort,
						PublicIP:   client.publicIP,
					})
				}
			}
			client.room.mu.RUnlock()
		}
	case "leave":
		client.explicitLeave = true
		s.removeClientImmediate(client)
	case "ping":
		sendControlMessage(client, ControlMessage{Type: "pong"})
	}
}

func (s *RelayServer) handleHostRoom(client *Client, msg ControlMessage) {
	if msg.RoomCode == "" || msg.SenderID == "" {
		sendControlMessage(client, ControlMessage{Type: "error", Message: "Room code and user ID required"})
		return
	}

	// Rate limit room creation: max 10 rooms per minute per connection
	now := time.Now()
	if now.Sub(client.roomCreationWindow) > time.Minute {
		client.roomCreationCount = 0
		client.roomCreationWindow = now
	}
	client.roomCreationCount++
	if client.roomCreationCount > 10 {
		log.Printf("[SECURITY] Rate limit exceeded: client %s attempted too many room creations", client.publicIP)
		sendControlMessage(client, ControlMessage{
			Type:    "error",
			Message: "Too many room creation requests. Please wait a moment.",
		})
		return
	}

	// Remove from any previous room
	s.removeClient(client)

	client.senderID = msg.SenderID
	client.nickname = msg.Nickname
	client.localPort = msg.Port
	if msg.LocalIP != "" {
		client.localIP = msg.LocalIP
	}
	if msg.PublicPort > 0 {
		client.publicPort = msg.PublicPort
	}
	if msg.PublicIP != "" {
		client.publicIP = msg.PublicIP
	}

	s.mu.Lock()

	// Check if room already exists
	if existing, ok := s.rooms[msg.RoomCode]; ok {
		existing.mu.Lock()
		existingHostID := existing.HostID
		memberCount := len(existing.Members)

		// Allow reclaiming if same host reconnecting or room is empty
		if existingHostID == msg.SenderID || memberCount == 0 {
			// Cryptographic Host Token Verification to prevent host hijacking
			if existing.HostToken != "" {
				if msg.HostToken == "" || subtle.ConstantTimeCompare([]byte(existing.HostToken), []byte(msg.HostToken)) != 1 {
					existing.mu.Unlock()
					s.mu.Unlock()
					log.Printf("[SECURITY] Unauthorized host reclaim attempt for room %s by sender %s (invalid or missing host token)", msg.RoomCode, msg.SenderID)
					sendControlMessage(client, ControlMessage{Type: "error", Message: "Unauthorized room management: Invalid or missing Host Token"})
					return
				}
			}

			if oldClient, ok := existing.Members[msg.SenderID]; ok {
				if oldClient.disconnectTimer != nil {
					oldClient.disconnectTimer.Stop()
				}
			}
			existing.HostID = msg.SenderID
			if msg.PIN != "" {
				existing.PIN = strings.TrimSpace(msg.PIN)
				existing.IsLocked = true
			} else if msg.IsLocked {
				existing.IsLocked = true
			}
			existing.Members[msg.SenderID] = client
			client.room = existing
			hostToken := existing.HostToken
			existing.mu.Unlock()
			s.mu.Unlock()

			log.Printf("[~] Host reconnected to room: %s by %s (%s)", msg.RoomCode, msg.Nickname, msg.SenderID)
			sendControlMessage(client, ControlMessage{
				Type:      "room_created",
				RoomCode:  msg.RoomCode,
				YourIP:    client.publicIP,
				HostToken: hostToken,
			})
			return
		}

		existing.mu.Unlock()
		s.mu.Unlock()
		sendControlMessage(client, ControlMessage{Type: "error", Message: "Room code already in use"})
		return
	}

	pin := strings.TrimSpace(msg.PIN)
	isLocked := msg.IsLocked || pin != ""
	hostToken := generateHostToken()
	room := &Room{
		Code:      msg.RoomCode,
		HostID:    msg.SenderID,
		HostToken: hostToken,
		PIN:       pin,
		IsLocked:  isLocked,
		Members:   map[string]*Client{msg.SenderID: client},
		created:   time.Now(),
	}
	s.rooms[msg.RoomCode] = room
	client.room = room
	s.mu.Unlock()

	log.Printf("[+] Room created: %s by %s (%s, IP: %s:%d, Locked: %v, PIN: %s)", msg.RoomCode, msg.Nickname, msg.SenderID, client.publicIP, client.localPort, isLocked, pin)
	sendControlMessage(client, ControlMessage{
		Type:      "room_created",
		RoomCode:  msg.RoomCode,
		YourIP:    client.publicIP,
		HostToken: hostToken,
	})
}

func (s *RelayServer) handleJoinRoom(client *Client, msg ControlMessage) {
	if msg.RoomCode == "" || msg.SenderID == "" {
		sendControlMessage(client, ControlMessage{Type: "error", Message: "Room code and user ID required"})
		return
	}

	// Remove from any previous room
	s.removeClient(client)

	client.senderID = msg.SenderID
	client.nickname = msg.Nickname
	client.localPort = msg.Port
	if msg.LocalIP != "" {
		client.localIP = msg.LocalIP
	}
	if msg.PublicPort > 0 {
		client.publicPort = msg.PublicPort
	}
	if msg.PublicIP != "" {
		client.publicIP = msg.PublicIP
	}

	s.mu.RLock()
	room, exists := s.rooms[msg.RoomCode]
	s.mu.RUnlock()

	if !exists {
		sendControlMessage(client, ControlMessage{Type: "room_not_found", Message: "Room not found or currently closed"})
		return
	}

	room.mu.Lock()
	if room.IsLocked {
		if room.PIN != "" && strings.TrimSpace(msg.PIN) != room.PIN {
			client.failedPINAttempts++
			failedAttempts := client.failedPINAttempts
			client.lastPINAttempt = time.Now()
			room.mu.Unlock()

			log.Printf("[SECURITY] Join rejected for %s to room %s: invalid or missing PIN (attempt #%d)", msg.Nickname, msg.RoomCode, failedAttempts)

			// Progressive backoff and lockout after 10 failed attempts
			if failedAttempts >= 10 {
				sendControlMessage(client, ControlMessage{
					Type:    "error",
					Message: "Too many failed PIN attempts. Connection closed for security.",
				})
				client.mu.Lock()
				_ = client.conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "Too many failed attempts"),
					time.Now().Add(time.Second))
				_ = client.conn.Close()
				client.mu.Unlock()
				return
			}

			if failedAttempts >= 5 {
				time.Sleep(500 * time.Millisecond)
			}

			sendControlMessage(client, ControlMessage{
				Type:    "room_locked",
				Message: "PIN_REQUIRED",
			})
			return
		} else if room.PIN == "" {
			room.mu.Unlock()
			log.Printf("[SECURITY] Join rejected for %s to room %s: room is locked", msg.Nickname, msg.RoomCode)
			sendControlMessage(client, ControlMessage{
				Type:    "room_locked",
				Message: "ROOM_LOCKED",
			})
			return
		}
		// Reset counter on successful PIN verification
		client.failedPINAttempts = 0
	}

	if len(room.Members) >= MaxRoomMembers && room.Members[msg.SenderID] == nil {
		room.mu.Unlock()
		sendControlMessage(client, ControlMessage{Type: "room_full", Message: "Room is full (Max 4 members)"})
		return
	}

	// Build peer list for welcome message (including public IP, local IP and port for P2P UDP hole punching)
	peers := make([]PeerInfo, 0, len(room.Members))
	existingMembers := make([]*Client, 0, len(room.Members))
	for _, m := range room.Members {
		if m.senderID != msg.SenderID {
			peers = append(peers, PeerInfo{
				SenderID:   m.senderID,
				Nickname:   m.nickname,
				LocalIP:    m.localIP,
				PublicIP:   m.publicIP,
				LocalPort:  m.localPort,
				PublicPort: m.publicPort,
			})
			existingMembers = append(existingMembers, m)
		}
	}

	// Cancel any active grace disconnect timer for reconnecting member
	if oldClient, ok := room.Members[msg.SenderID]; ok {
		if oldClient.disconnectTimer != nil {
			oldClient.disconnectTimer.Stop()
		}
	}

	room.Members[msg.SenderID] = client
	client.room = room
	hostID := room.HostID

	// Find host nickname & info
	hostNick := ""
	hostLocalIP := ""
	hostIP := ""
	hostPort := 0
	hostPubPort := 0
	if host, ok := room.Members[hostID]; ok {
		hostNick = host.nickname
		hostLocalIP = host.localIP
		hostIP = host.publicIP
		hostPort = host.localPort
		hostPubPort = host.publicPort
	}
	roomLocked := room.IsLocked
	room.mu.Unlock()

	log.Printf("[+] %s (%s, IP: %s:%d, PubPort: %d) joined room %s", msg.Nickname, msg.SenderID, client.publicIP, client.localPort, client.publicPort, msg.RoomCode)

	// Send welcome to joiner with peer list, direct P2P endpoint info, and room locked state (never leak room PIN)
	sendControlMessage(client, ControlMessage{
		Type:       "welcome",
		RoomCode:   msg.RoomCode,
		SenderID:   hostID,
		Nickname:   hostNick,
		LocalIP:    hostLocalIP,
		PublicIP:   hostIP,
		Port:       hostPort,
		PublicPort: hostPubPort,
		YourIP:     client.publicIP,
		Peers:      peers,
		IsLocked:   roomLocked,
	})

	// Notify existing members about new/reconnected peer with direct IP info for hole-punching
	joinNotify := ControlMessage{
		Type:       "peer_joined",
		SenderID:   msg.SenderID,
		Nickname:   msg.Nickname,
		LocalIP:    client.localIP,
		PublicIP:   client.publicIP,
		Port:       client.localPort,
		PublicPort: client.publicPort,
	}
	for _, m := range existingMembers {
		sendControlMessage(m, joinNotify)
	}
}

func (s *RelayServer) relayBinaryData(sender *Client, data []byte) {
	if sender.room == nil {
		return
	}

	room := sender.room
	room.mu.RLock()
	defer room.mu.RUnlock()

	isVideo := bytes.HasPrefix(data, []byte("LVV1"))
	for id, member := range room.Members {
		if id != sender.senderID && !member.isDisconnected {
			if isVideo {
				// Dedicated video channel with anti-bufferbloat backpressure:
				// If channel fills beyond 128 packets, client has fallen behind.
				// Drain older backlog to snap stream immediately back to real-time (<50ms latency),
				// rather than accumulating permanent ~1200ms queue delay.
				select {
				case member.videoSendCh <- data:
				default:
					dropCount := len(member.videoSendCh) / 2
					for i := 0; i < dropCount; i++ {
						select {
						case <-member.videoSendCh:
						default:
							break
						}
					}
					select {
					case member.videoSendCh <- data:
					default:
					}
				}
			} else {
				// High-priority channel for Audio (Opus), Ping, Pong & Control
				// Never delayed behind video frames; ping remains ~0ms!
				select {
				case member.prioritySendCh <- data:
				default:
				}
			}
		}
	}
}

// handleDisconnect is invoked when a client connection drops unexpectedly (e.g. WiFi/internet glitch).
// It starts a 10-second grace timer to give the client a chance to reconnect before dropping them.
func (s *RelayServer) handleDisconnect(client *Client) {
	if client.room == nil {
		return
	}

	room := client.room
	senderID := client.senderID
	nickname := client.nickname

	room.mu.Lock()
	if room.Members[senderID] != client {
		room.mu.Unlock()
		return
	}

	client.isDisconnected = true
	log.Printf("[⏳] Connection lost for %s (%s) in room %s. Waiting 30s grace period...", nickname, senderID, room.Code)

	if client.disconnectTimer != nil {
		client.disconnectTimer.Stop()
	}

	client.disconnectTimer = time.AfterFunc(30*time.Second, func() {
		s.removeClientImmediate(client)
	})
	room.mu.Unlock()
}

func (s *RelayServer) removeClient(client *Client) {
	s.removeClientImmediate(client)
}

func (s *RelayServer) removeClientImmediate(client *Client) {
	if client.room == nil {
		return
	}

	room := client.room
	senderID := client.senderID
	nickname := client.nickname
	client.room = nil

	room.mu.Lock()
	// Only remove from room if THIS client instance is the currently registered one.
	if room.Members[senderID] != client {
		room.mu.Unlock()
		return
	}

	if client.disconnectTimer != nil {
		client.disconnectTimer.Stop()
	}

	isHostLeaving := (room.HostID == senderID)

	delete(room.Members, senderID)
	remainingMembers := make([]*Client, 0, len(room.Members))
	for _, m := range room.Members {
		if !m.isDisconnected {
			remainingMembers = append(remainingMembers, m)
		}
	}
	isEmpty := len(room.Members) == 0

	if isEmpty {
		roomCode := room.Code
		room.mu.Unlock()
		go func() {
			time.Sleep(30 * time.Second)
			s.mu.Lock()
			defer s.mu.Unlock()
			if r, ok := s.rooms[roomCode]; ok {
				r.mu.Lock()
				if len(r.Members) == 0 {
					delete(s.rooms, roomCode)
					log.Printf("[-] Room %s closed after grace period (empty)", roomCode)
				}
				r.mu.Unlock()
			}
		}()
		return
	}

	log.Printf("[-] %s (%s) left room %s", nickname, senderID, room.Code)

	var newHostID, newHostNick, newHostToken string
	roomPIN := room.PIN
	roomLocked := room.IsLocked
	if isHostLeaving && len(remainingMembers) > 0 {
		newHost := remainingMembers[0]
		room.HostID = newHost.senderID
		room.HostToken = generateHostToken()
		newHostID = newHost.senderID
		newHostNick = newHost.nickname
		newHostToken = room.HostToken
		log.Printf("👑 Host migrated in room %s to %s (%s, PIN: %s, Locked: %v)", room.Code, newHost.nickname, newHost.senderID, roomPIN, roomLocked)
	}
	roomCode := room.Code
	room.mu.Unlock()

	// Notify remaining members about peer leaving
	leaveMsg := ControlMessage{
		Type:     "peer_left",
		SenderID: senderID,
		Nickname: nickname,
	}
	for _, m := range remainingMembers {
		sendControlMessage(m, leaveMsg)
	}

	// Automatic Host Migration notification (PIN & HostToken sent only to the newly elected host)
	if isHostLeaving && len(remainingMembers) > 0 && newHostID != "" {
		for _, m := range remainingMembers {
			newHostMsg := ControlMessage{
				Type:     "new_host",
				RoomCode: roomCode,
				SenderID: newHostID,
				Nickname: newHostNick,
				IsLocked: roomLocked,
			}
			if m.senderID == newHostID {
				newHostMsg.PIN = roomPIN
				newHostMsg.HostToken = newHostToken
			}
			sendControlMessage(m, newHostMsg)
		}
	}
}

func sendControlMessage(client *Client, msg ControlMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	client.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	client.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *Client) writePump() {
	ticker := time.NewTicker(20 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	writeMsg := func(data []byte) error {
		c.mu.Lock()
		c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err := c.conn.WriteMessage(websocket.BinaryMessage, data)
		c.mu.Unlock()
		return err
	}

	for {
		// Priority 1: Drain ALL pending priority packets (Voice, Ping, Pong, Control) first!
		for {
			select {
			case data, ok := <-c.prioritySendCh:
				if !ok {
					return
				}
				if writeMsg(data) != nil {
					return
				}
			default:
				goto sendVideoOrWait
			}
		}

	sendVideoOrWait:
		select {
		case data, ok := <-c.prioritySendCh:
			if !ok {
				return
			}
			if writeMsg(data) != nil {
				return
			}
		case data, ok := <-c.videoSendCh:
			if !ok {
				return
			}
			if writeMsg(data) != nil {
				return
			}
		case <-ticker.C:
			c.mu.Lock()
			c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			err := c.conn.WriteMessage(websocket.PingMessage, nil)
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// Periodic cleanup of stale empty rooms
func (s *RelayServer) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		s.mu.Lock()
		for code, room := range s.rooms {
			room.mu.RLock()
			if len(room.Members) == 0 && time.Since(room.created) > 10*time.Minute {
				delete(s.rooms, code)
				log.Printf("[cleanup] Removed stale room: %s", code)
			}
			room.mu.RUnlock()
		}
		s.mu.Unlock()
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "27850"
	}

	authToken := os.Getenv("RELAY_AUTH_TOKEN")
	if authToken == "" {
		authToken = os.Getenv("LIMONI_AUTH_TOKEN")
	}

	server := NewRelayServer(authToken)
	go server.cleanupLoop()

	if server.authToken != "" {
		log.Printf("🔒 [SECURITY] Relay authentication active (RELAY_AUTH_TOKEN is set)")
	} else {
		log.Printf("⚠️  [NOTICE] Relay authentication disabled (public mode - no RELAY_AUTH_TOKEN set)")
	}

	http.HandleFunc("/ws", server.handleWS)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		server.mu.RLock()
		roomCount := len(server.rooms)
		server.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":        "ok",
			"rooms":         roomCount,
			"auth_required": server.authToken != "",
		})
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Limoni Voice Relay Server v1.0\n"))
	})

	log.Printf("🚀 Limoni Voice Relay Server starting on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
