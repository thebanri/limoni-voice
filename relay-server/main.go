package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const MaxRoomMembers = 4

// ControlMessage is a JSON message exchanged between client and relay for room management
type ControlMessage struct {
	Type     string `json:"type"`
	RoomCode string `json:"room_code,omitempty"`
	SenderID string `json:"sender_id,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Message  string `json:"message,omitempty"`
	// For welcome message: list of existing peers
	Peers []PeerInfo `json:"peers,omitempty"`
}

type PeerInfo struct {
	SenderID string `json:"sender_id"`
	Nickname string `json:"nickname"`
}

type Client struct {
	conn     *websocket.Conn
	senderID string
	nickname string
	room     *Room
	sendCh   chan []byte // buffered channel for outgoing messages
	mu       sync.Mutex
}

type Room struct {
	Code    string
	HostID  string
	Members map[string]*Client // senderID -> Client
	mu      sync.RWMutex
	created time.Time
}

type RelayServer struct {
	rooms    map[string]*Room
	mu       sync.RWMutex
	upgrader websocket.Upgrader
}

func NewRelayServer() *RelayServer {
	return &RelayServer{
		rooms: make(map[string]*Room),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
	}
}

func (s *RelayServer) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	client := &Client{
		conn:   conn,
		sendCh: make(chan []byte, 64),
	}

	// Start write pump
	go client.writePump()

	defer func() {
		s.removeClient(client)
		conn.Close()
	}()

	// Set read deadline and pong handler for keepalive
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("Client disconnected unexpectedly: %v", err)
			}
			return
		}

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
		sendControlMessage(client, ControlMessage{Type: "error", Message: "Gecersiz JSON mesaji"})
		return
	}

	switch msg.Type {
	case "host_room":
		s.handleHostRoom(client, msg)
	case "join_room":
		s.handleJoinRoom(client, msg)
	case "leave":
		s.removeClient(client)
	case "ping":
		sendControlMessage(client, ControlMessage{Type: "pong"})
	}
}

func (s *RelayServer) handleHostRoom(client *Client, msg ControlMessage) {
	if msg.RoomCode == "" || msg.SenderID == "" {
		sendControlMessage(client, ControlMessage{Type: "error", Message: "Oda kodu ve kullanici ID gerekli"})
		return
	}

	// Remove from any existing room
	s.removeClient(client)

	client.senderID = msg.SenderID
	client.nickname = msg.Nickname

	s.mu.Lock()

	// Check if room already exists
	if existing, ok := s.rooms[msg.RoomCode]; ok {
		existing.mu.RLock()
		memberCount := len(existing.Members)
		existing.mu.RUnlock()

		if memberCount > 0 {
			s.mu.Unlock()
			sendControlMessage(client, ControlMessage{Type: "error", Message: "Bu oda kodu zaten kullaniliyor"})
			return
		}
		// Empty room, clean it up
		delete(s.rooms, msg.RoomCode)
	}

	room := &Room{
		Code:    msg.RoomCode,
		HostID:  msg.SenderID,
		Members: map[string]*Client{msg.SenderID: client},
		created: time.Now(),
	}
	s.rooms[msg.RoomCode] = room
	client.room = room
	s.mu.Unlock()

	log.Printf("[+] Room created: %s by %s (%s)", msg.RoomCode, msg.Nickname, msg.SenderID)
	sendControlMessage(client, ControlMessage{Type: "room_created", RoomCode: msg.RoomCode})
}

func (s *RelayServer) handleJoinRoom(client *Client, msg ControlMessage) {
	if msg.RoomCode == "" || msg.SenderID == "" {
		sendControlMessage(client, ControlMessage{Type: "error", Message: "Oda kodu ve kullanici ID gerekli"})
		return
	}

	// Remove from any existing room
	s.removeClient(client)

	client.senderID = msg.SenderID
	client.nickname = msg.Nickname

	s.mu.RLock()
	room, exists := s.rooms[msg.RoomCode]
	s.mu.RUnlock()

	if !exists {
		sendControlMessage(client, ControlMessage{Type: "room_not_found", Message: "Bu oda su anda acik degil"})
		return
	}

	room.mu.Lock()
	if len(room.Members) >= MaxRoomMembers {
		room.mu.Unlock()
		sendControlMessage(client, ControlMessage{Type: "room_full", Message: "Oda dolu (Maks 4 kisi)"})
		return
	}

	// Build peer list for welcome message
	peers := make([]PeerInfo, 0, len(room.Members))
	existingMembers := make([]*Client, 0, len(room.Members))
	for _, m := range room.Members {
		peers = append(peers, PeerInfo{SenderID: m.senderID, Nickname: m.nickname})
		existingMembers = append(existingMembers, m)
	}

	room.Members[msg.SenderID] = client
	client.room = room
	hostID := room.HostID

	// Find host nickname
	hostNick := ""
	if host, ok := room.Members[hostID]; ok {
		hostNick = host.nickname
	}
	room.mu.Unlock()

	log.Printf("[+] %s (%s) joined room %s", msg.Nickname, msg.SenderID, msg.RoomCode)

	// Send welcome to joiner with peer list
	sendControlMessage(client, ControlMessage{
		Type:     "welcome",
		RoomCode: msg.RoomCode,
		SenderID: hostID,
		Nickname: hostNick,
		Peers:    peers,
	})

	// Notify existing members about new peer
	joinNotify := ControlMessage{
		Type:     "peer_joined",
		SenderID: msg.SenderID,
		Nickname: msg.Nickname,
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

	for id, member := range room.Members {
		if id != sender.senderID {
			// Non-blocking send via buffered channel
			select {
			case member.sendCh <- data:
			default:
				// Drop packet if send buffer is full (prevents slow client from blocking)
			}
		}
	}
}

func (s *RelayServer) removeClient(client *Client) {
	if client.room == nil {
		return
	}

	room := client.room
	senderID := client.senderID
	nickname := client.nickname
	client.room = nil

	room.mu.Lock()
	delete(room.Members, senderID)
	remainingMembers := make([]*Client, 0, len(room.Members))
	for _, m := range room.Members {
		remainingMembers = append(remainingMembers, m)
	}
	isEmpty := len(room.Members) == 0
	isHost := room.HostID == senderID
	room.mu.Unlock()

	if isEmpty {
		s.mu.Lock()
		delete(s.rooms, room.Code)
		s.mu.Unlock()
		log.Printf("[-] Room %s closed (empty)", room.Code)
		return
	}

	log.Printf("[-] %s (%s) left room %s", nickname, senderID, room.Code)

	// Notify remaining members
	leaveMsg := ControlMessage{
		Type:     "peer_left",
		SenderID: senderID,
		Nickname: nickname,
	}

	// If host left, notify everyone that room is closing
	if isHost {
		leaveMsg.Type = "host_left"
		leaveMsg.Message = "Host odadan ayrildi, oda kapaniyor"
	}

	for _, m := range remainingMembers {
		sendControlMessage(m, leaveMsg)
	}

	// If host left, close the room and disconnect everyone
	if isHost {
		room.mu.Lock()
		for _, m := range room.Members {
			m.room = nil
			m.conn.Close()
		}
		room.Members = make(map[string]*Client)
		room.mu.Unlock()

		s.mu.Lock()
		delete(s.rooms, room.Code)
		s.mu.Unlock()
		log.Printf("[-] Room %s closed (host left)", room.Code)
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
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case data, ok := <-c.sendCh:
			if !ok {
				return
			}
			c.mu.Lock()
			c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err := c.conn.WriteMessage(websocket.BinaryMessage, data)
			c.mu.Unlock()
			if err != nil {
				return
			}

		case <-ticker.C:
			c.mu.Lock()
			c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
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
		port = "8080"
	}

	server := NewRelayServer()
	go server.cleanupLoop()

	http.HandleFunc("/ws", server.handleWS)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		server.mu.RLock()
		roomCount := len(server.rooms)
		server.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"rooms":  roomCount,
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
