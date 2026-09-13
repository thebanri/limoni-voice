// Package relay implements the Limoni Voice relay: WebSocket signalling with host-approved
// admission (the relay forwards opaque PAKE frames and never learns room secrets), a
// low-latency UDP datagram relay, WebSocket fallback forwarding, per-IP abuse limits and
// Prometheus metrics.
package relay

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thebanri/limoni-voice/internal/protocol"
)

// Config configures a relay Server.
type Config struct {
	AuthToken      string        // optional shared secret required from clients
	UDPPublicAddr  string        // advertised host:port for the UDP relay (optional)
	UDPPort        int           // advertised UDP port on the WebSocket host when UDPPublicAddr is empty
	MaxRoomMembers int           // default 4
	GracePeriod    time.Duration // member reconnect grace (default 30s)
	PendingTimeout time.Duration // how long a joiner may wait for host approval (default 30s)
	TrustProxy     bool          // always trust CF-Connecting-IP / X-Forwarded-For
	Logger         *slog.Logger
}

const (
	maxSignalSize     = 16 * 1024
	maxFrameSize      = 64 * 1024
	maxPakeSize       = 512
	maxPendingPerRoom = 8
	maxConnsPerIP     = 16
)

// Server is a relay instance. Create with New, serve Handler() over HTTP and
// optionally attach a UDP socket with ServeUDP.
type Server struct {
	cfg      Config
	log      *slog.Logger
	upgrader websocket.Upgrader
	metrics  *Metrics

	mu        sync.Mutex
	rooms     map[string]*room
	udpTokens map[[protocol.UDPTokenSize]byte]*client
	ips       map[string]*ipState

	udpConn atomic.Pointer[net.UDPConn]
}

type room struct {
	code        string
	hostID      string
	hostToken   string
	locked      bool
	pinRequired bool
	created     time.Time
	emptySince  time.Time

	members map[string]*member
	order   []string // join order, used for host migration
	pending map[string]*client

	// connected member snapshot for lock-free forwarding
	snapshot atomic.Pointer[[]*client]
}

type member struct {
	id          string
	nickname    string
	token       string
	endpoint    protocol.Endpoint
	client      *client // nil while disconnected within the grace period
	graceTimer  *time.Timer
	joinedOrder int
}

type ipState struct {
	conns        int
	windowStart  time.Time
	roomCreates  int
	joins        int
	pakes        int
	failedAdmits int
}

// New creates a relay server.
func New(cfg Config) *Server {
	if cfg.MaxRoomMembers <= 0 {
		cfg.MaxRoomMembers = 4
	}
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 30 * time.Second
	}
	if cfg.PendingTimeout <= 0 {
		cfg.PendingTimeout = 30 * time.Second
	}
	cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		cfg:     cfg,
		log:     logger,
		metrics: newMetrics(),
		upgrader: websocket.Upgrader{
			CheckOrigin:     func(r *http.Request) bool { return true },
			ReadBufferSize:  64 * 1024,
			WriteBufferSize: 64 * 1024,
		},
		rooms:     make(map[string]*room),
		udpTokens: make(map[[protocol.UDPTokenSize]byte]*client),
		ips:       make(map[string]*ipState),
	}
}

// Metrics returns the server metrics.
func (s *Server) Metrics() *Metrics { return s.metrics }

// Handler returns the HTTP handler serving /ws, /health and /metrics.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.metrics.serveHTTP(s))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Limoni Voice Relay Server v2\n"))
	})
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	roomCount := len(s.rooms)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":        "ok",
		"rooms":         roomCount,
		"auth_required": s.cfg.AuthToken != "",
		"proto":         protocol.SignalVersion,
		"udp":           s.udpConn.Load() != nil,
	})
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	direct := net.ParseIP(host)
	trusted := s.cfg.TrustProxy || (direct != nil && (direct.IsLoopback() || direct.IsPrivate()))
	if trusted {
		if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); net.ParseIP(cf) != nil {
			return cf
		}
		if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(xrip) != nil {
			return xrip
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
	}
	return host
}

func (s *Server) authorized(r *http.Request) bool {
	if s.cfg.AuthToken == "" {
		return true
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Auth-Token")
	}
	if token == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			token = strings.TrimSpace(auth[7:])
		}
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AuthToken)) == 1
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.authorized(r) {
		s.metrics.authFailures.Add(1)
		s.log.Warn("unauthorized connection rejected", "ip", ip)
		http.Error(w, "Unauthorized: Invalid or missing relay authentication token", http.StatusUnauthorized)
		return
	}
	if !s.acquireConn(ip) {
		s.metrics.rateLimited.Add(1)
		http.Error(w, "Too many connections", http.StatusTooManyRequests)
		return
	}
	defer s.releaseConn(ip)

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrameSize)
	if tcp, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetWriteBuffer(256 * 1024)
		_ = tcp.SetReadBuffer(256 * 1024)
	}

	c := newClient(s, conn, ip)
	s.metrics.connections.Add(1)
	defer s.metrics.connections.Add(-1)
	go c.writePump()
	defer func() {
		s.onDisconnect(c)
		c.close()
	}()

	conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		return nil
	})

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		switch msgType {
		case websocket.TextMessage:
			s.metrics.wsBytesIn.Add(uint64(len(data)))
			if len(data) > maxSignalSize {
				continue
			}
			var msg protocol.Signal
			if err := json.Unmarshal(data, &msg); err != nil {
				c.sendSignal(protocol.Signal{Type: protocol.SigError, Message: "Invalid JSON message"})
				continue
			}
			s.handleSignal(c, msg)
		case websocket.BinaryMessage:
			s.metrics.wsBytesIn.Add(uint64(len(data)))
			if len(data) < 2 {
				continue
			}
			s.forward(c, data[0], data[1:], false)
		}
	}
}

func (s *Server) acquireConn(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ipStateLocked(ip)
	if st.conns >= maxConnsPerIP {
		return false
	}
	st.conns++
	return true
}

func (s *Server) releaseConn(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.ips[ip]; ok {
		st.conns--
		if st.conns <= 0 && time.Since(st.windowStart) > time.Minute {
			delete(s.ips, ip)
		}
	}
}

func (s *Server) ipStateLocked(ip string) *ipState {
	st, ok := s.ips[ip]
	if !ok {
		st = &ipState{windowStart: time.Now()}
		s.ips[ip] = st
	}
	if time.Since(st.windowStart) > time.Minute {
		st.windowStart = time.Now()
		st.roomCreates, st.joins, st.pakes, st.failedAdmits = 0, 0, 0, 0
	}
	return st
}

// allowLocked applies a per-IP, per-minute budget. Must hold s.mu.
func (s *Server) allowLocked(ip string, kind string) bool {
	st := s.ipStateLocked(ip)
	var n *int
	var limit int
	switch kind {
	case "create":
		n, limit = &st.roomCreates, 10
	case "join":
		n, limit = &st.joins, 30
	case "pake":
		n, limit = &st.pakes, 120
	default:
		return true
	}
	if *n >= limit {
		s.metrics.rateLimited.Add(1)
		return false
	}
	*n++
	return true
}

func (s *Server) handleSignal(c *client, msg protocol.Signal) {
	switch msg.Type {
	case protocol.SigPing:
		c.sendSignal(protocol.Signal{Type: protocol.SigPong})
		return
	case protocol.SigHostRoom, protocol.SigJoinRoom:
		if msg.Proto < protocol.SignalVersion {
			c.sendSignal(protocol.Signal{Type: protocol.SigError, Message: "CLIENT_OUTDATED: please update Limoni Voice to connect to this relay"})
			return
		}
		if msg.RoomCode == "" || msg.SenderID == "" || len(msg.RoomCode) > 64 || len(msg.SenderID) > 64 {
			c.sendSignal(protocol.Signal{Type: protocol.SigError, Message: "Room code and user ID required"})
			return
		}
		if len(msg.Nickname) > 64 {
			msg.Nickname = msg.Nickname[:64]
		}
		msg.Endpoint = sanitizeEndpoint(msg.Endpoint)
		if msg.Type == protocol.SigHostRoom {
			s.hostRoom(c, msg)
		} else {
			s.joinRoom(c, msg)
		}
	case protocol.SigPake:
		s.forwardPake(c, msg)
	case protocol.SigAdmit:
		s.admit(c, msg.Target)
	case protocol.SigReject:
		s.reject(c, msg.Target, msg.Message)
	case protocol.SigLockRoom, protocol.SigUnlockRoom:
		s.setLock(c, msg.Type == protocol.SigLockRoom, msg.PinRequired)
	case protocol.SigPortUpdate:
		s.portUpdate(c, sanitizeEndpoint(msg.Endpoint))
	case protocol.SigLeave:
		s.leave(c)
	}
}

func sanitizeEndpoint(ep protocol.Endpoint) protocol.Endpoint {
	if net.ParseIP(ep.LocalIP) == nil {
		ep.LocalIP = ""
	}
	if net.ParseIP(ep.PublicIP) == nil {
		ep.PublicIP = ""
	}
	if ep.LocalPort < 0 || ep.LocalPort > 65535 {
		ep.LocalPort = 0
	}
	if ep.PublicPort < 0 || ep.PublicPort > 65535 {
		ep.PublicPort = 0
	}
	var v6 []string
	for _, a := range ep.IPv6 {
		if host, _, err := net.SplitHostPort(a); err == nil && net.ParseIP(host) != nil && len(v6) < 4 {
			v6 = append(v6, a)
		}
	}
	ep.IPv6 = v6
	if ep.NAT != "eim" && ep.NAT != "edm" {
		ep.NAT = ""
	}
	return ep
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Server) hostRoom(c *client, msg protocol.Signal) {
	s.mu.Lock()
	previous := s.detachLocked(c, true)
	s.mu.Unlock()
	previous.send()

	s.mu.Lock()
	if !s.allowLocked(c.ip, "create") {
		s.mu.Unlock()
		c.sendSignal(protocol.Signal{Type: protocol.SigError, Message: "Too many room creation requests. Please wait a moment."})
		return
	}

	endpoint := msg.Endpoint
	if endpoint.PublicIP == "" {
		endpoint.PublicIP = c.ip
	}
	c.id, c.nickname = msg.SenderID, msg.Nickname

	rm, exists := s.rooms[msg.RoomCode]
	if exists {
		if rm.hostToken == "" || msg.HostToken == "" || subtle.ConstantTimeCompare([]byte(rm.hostToken), []byte(msg.HostToken)) != 1 {
			s.mu.Unlock()
			s.metrics.hijackBlocked.Add(1)
			s.log.Warn("room reclaim rejected", "room", msg.RoomCode, "ip", c.ip)
			c.sendSignal(protocol.Signal{Type: protocol.SigError, Message: "ROOM_IN_USE: Room code already in use"})
			return
		}
		m := rm.members[msg.SenderID]
		if m == nil {
			m = &member{id: msg.SenderID, token: randomHex(16), joinedOrder: len(rm.order)}
			rm.members[msg.SenderID] = m
			rm.order = append(rm.order, msg.SenderID)
		}
		if m.client != nil && m.client != c {
			m.client.roomCode = ""
			m.client.close()
		}
		if m.graceTimer != nil {
			m.graceTimer.Stop()
			m.graceTimer = nil
		}
		m.client, m.nickname, m.endpoint = c, msg.Nickname, endpoint
		rm.hostID = msg.SenderID
		rm.locked = msg.IsLocked
		rm.pinRequired = msg.PinRequired
		rm.emptySince = time.Time{}
		c.roomCode = rm.code
		s.assignUDPTokenLocked(c)
		s.refreshSnapshotLocked(rm)
		reply := s.roomCreatedLocked(rm, c, m)
		others := s.connectedExceptLocked(rm, c.id)
		s.mu.Unlock()

		s.log.Info("host reconnected", "room", rm.code)
		c.sendSignal(reply)
		notify := protocol.Signal{Type: protocol.SigPeerJoined, SenderID: c.id, Nickname: c.nickname, Endpoint: endpoint}
		for _, o := range others {
			o.sendSignal(notify)
		}
		return
	}

	rm = &room{
		code:        msg.RoomCode,
		hostID:      msg.SenderID,
		hostToken:   randomHex(32),
		locked:      msg.IsLocked,
		pinRequired: msg.PinRequired,
		created:     time.Now(),
		members:     make(map[string]*member),
		pending:     make(map[string]*client),
	}
	m := &member{id: msg.SenderID, nickname: msg.Nickname, token: randomHex(16), endpoint: endpoint, client: c}
	rm.members[m.id] = m
	rm.order = []string{m.id}
	s.rooms[rm.code] = rm
	c.roomCode = rm.code
	s.assignUDPTokenLocked(c)
	s.refreshSnapshotLocked(rm)
	reply := s.roomCreatedLocked(rm, c, m)
	s.mu.Unlock()

	s.metrics.roomsCreated.Add(1)
	s.log.Info("room created", "room", rm.code, "ip", c.ip)
	c.sendSignal(reply)
}

func (s *Server) roomCreatedLocked(rm *room, c *client, m *member) protocol.Signal {
	sig := protocol.Signal{
		Type:        protocol.SigRoomCreated,
		Proto:       protocol.SignalVersion,
		RoomCode:    rm.code,
		SenderID:    rm.hostID,
		YourIP:      c.ip,
		HostToken:   rm.hostToken,
		MemberToken: m.token,
		IsLocked:    rm.locked,
	}
	for _, id := range rm.order {
		if id == c.id {
			continue
		}
		if o := rm.members[id]; o != nil {
			sig.Peers = append(sig.Peers, protocol.SignalPeer{SenderID: o.id, Nickname: o.nickname, Endpoint: o.endpoint})
		}
	}
	s.fillUDPLocked(&sig, c)
	return sig
}

func (s *Server) joinRoom(c *client, msg protocol.Signal) {
	s.mu.Lock()
	previous := s.detachLocked(c, true)
	s.mu.Unlock()
	previous.send()

	s.mu.Lock()
	if !s.allowLocked(c.ip, "join") {
		s.mu.Unlock()
		c.sendSignal(protocol.Signal{Type: protocol.SigError, Message: "Too many join attempts. Please wait a moment."})
		return
	}
	rm, ok := s.rooms[msg.RoomCode]
	if !ok {
		s.mu.Unlock()
		c.sendSignal(protocol.Signal{Type: protocol.SigRoomNotFound, Message: "Room not found or currently closed"})
		return
	}

	endpoint := msg.Endpoint
	if endpoint.PublicIP == "" {
		endpoint.PublicIP = c.ip
	}
	c.id, c.nickname = msg.SenderID, msg.Nickname

	// Reconnect of an admitted member (member token proves prior admission).
	if m := rm.members[msg.SenderID]; m != nil {
		if msg.MemberToken == "" || subtle.ConstantTimeCompare([]byte(m.token), []byte(msg.MemberToken)) != 1 {
			s.mu.Unlock()
			s.metrics.hijackBlocked.Add(1)
			c.sendSignal(protocol.Signal{Type: protocol.SigError, Message: "Unauthorized: member identity already in use"})
			return
		}
		if m.client != nil && m.client != c {
			m.client.roomCode = ""
			m.client.close()
		}
		if m.graceTimer != nil {
			m.graceTimer.Stop()
			m.graceTimer = nil
		}
		m.client, m.nickname, m.endpoint = c, msg.Nickname, endpoint
		c.roomCode = rm.code
		s.assignUDPTokenLocked(c)
		s.refreshSnapshotLocked(rm)
		welcome := s.welcomeLocked(rm, c, m)
		others := s.connectedExceptLocked(rm, c.id)
		s.mu.Unlock()

		c.sendSignal(welcome)
		notify := protocol.Signal{Type: protocol.SigPeerJoined, SenderID: c.id, Nickname: c.nickname, Endpoint: endpoint}
		for _, o := range others {
			o.sendSignal(notify)
		}
		return
	}

	if rm.locked && !rm.pinRequired {
		s.mu.Unlock()
		c.sendSignal(protocol.Signal{Type: protocol.SigRoomLocked, Message: "ROOM_LOCKED"})
		return
	}
	if len(rm.members) >= s.cfg.MaxRoomMembers {
		s.mu.Unlock()
		c.sendSignal(protocol.Signal{Type: protocol.SigRoomFull, Message: "Room is full (Max 4 members)"})
		return
	}
	host := rm.members[rm.hostID]
	if host == nil || host.client == nil {
		s.mu.Unlock()
		c.sendSignal(protocol.Signal{Type: protocol.SigRoomNotFound, Message: "Room host is currently offline"})
		return
	}
	if len(rm.pending) >= maxPendingPerRoom {
		s.mu.Unlock()
		c.sendSignal(protocol.Signal{Type: protocol.SigError, Message: "Too many pending join requests, try again shortly"})
		return
	}
	if old := rm.pending[c.id]; old != nil && old != c {
		old.pendingRoom = ""
		old.close()
	}
	rm.pending[c.id] = c
	c.pendingRoom = rm.code
	c.endpoint = endpoint
	c.pendingSince = time.Now()
	hostClient := host.client
	pendingSig := protocol.Signal{
		Type:        protocol.SigJoinPending,
		Proto:       protocol.SignalVersion,
		RoomCode:    rm.code,
		SenderID:    rm.hostID,
		Nickname:    host.nickname,
		IsLocked:    rm.locked,
		PinRequired: rm.pinRequired,
		YourIP:      c.ip,
	}
	timeout := s.cfg.PendingTimeout
	s.mu.Unlock()

	s.metrics.joinRequests.Add(1)
	c.sendSignal(pendingSig)
	hostClient.sendSignal(protocol.Signal{Type: protocol.SigJoinRequest, SenderID: c.id, Nickname: c.nickname})

	time.AfterFunc(timeout, func() {
		s.mu.Lock()
		expired := false
		if r := s.rooms[pendingSig.RoomCode]; r != nil && r.pending[c.id] == c {
			delete(r.pending, c.id)
			c.pendingRoom = ""
			expired = true
		}
		s.mu.Unlock()
		if expired {
			c.sendSignal(protocol.Signal{Type: protocol.SigRejected, Message: "Host did not respond to the join request"})
		}
	})
}

func (s *Server) forwardPake(c *client, msg protocol.Signal) {
	if len(msg.Data) == 0 || len(msg.Data) > maxPakeSize {
		return
	}
	s.mu.Lock()
	if !s.allowLocked(c.ip, "pake") {
		s.mu.Unlock()
		return
	}
	var target *client
	if c.pendingRoom != "" {
		if rm := s.rooms[c.pendingRoom]; rm != nil && rm.pending[c.id] == c && msg.Target == rm.hostID {
			if host := rm.members[rm.hostID]; host != nil {
				target = host.client
			}
		}
	} else if c.roomCode != "" {
		if rm := s.rooms[c.roomCode]; rm != nil && rm.hostID == c.id {
			target = rm.pending[msg.Target]
		}
	}
	s.mu.Unlock()
	if target == nil {
		return
	}
	s.metrics.pakeForwarded.Add(1)
	target.sendSignal(protocol.Signal{Type: protocol.SigPake, SenderID: c.id, Data: msg.Data})
}

func (s *Server) admit(c *client, target string) {
	s.mu.Lock()
	rm := s.rooms[c.roomCode]
	if rm == nil || rm.hostID != c.id {
		s.mu.Unlock()
		return
	}
	joiner := rm.pending[target]
	if joiner == nil {
		s.mu.Unlock()
		return
	}
	delete(rm.pending, target)
	joiner.pendingRoom = ""
	if len(rm.members) >= s.cfg.MaxRoomMembers {
		s.mu.Unlock()
		joiner.sendSignal(protocol.Signal{Type: protocol.SigRoomFull, Message: "Room is full (Max 4 members)"})
		return
	}
	m := &member{id: joiner.id, nickname: joiner.nickname, token: randomHex(16), endpoint: joiner.endpoint, client: joiner, joinedOrder: len(rm.order)}
	rm.members[m.id] = m
	rm.order = append(rm.order, m.id)
	joiner.roomCode = rm.code
	s.assignUDPTokenLocked(joiner)
	s.refreshSnapshotLocked(rm)
	welcome := s.welcomeLocked(rm, joiner, m)
	others := s.connectedExceptLocked(rm, joiner.id)
	s.mu.Unlock()

	s.metrics.admitted.Add(1)
	s.log.Info("member admitted", "room", rm.code, "ip", joiner.ip)
	joiner.sendSignal(welcome)
	notify := protocol.Signal{Type: protocol.SigPeerJoined, SenderID: m.id, Nickname: m.nickname, Endpoint: m.endpoint}
	for _, o := range others {
		o.sendSignal(notify)
	}
}

func (s *Server) welcomeLocked(rm *room, c *client, m *member) protocol.Signal {
	host := rm.members[rm.hostID]
	sig := protocol.Signal{
		Type:        protocol.SigWelcome,
		Proto:       protocol.SignalVersion,
		RoomCode:    rm.code,
		SenderID:    rm.hostID,
		YourIP:      c.ip,
		MemberToken: m.token,
		IsLocked:    rm.locked,
		PinRequired: rm.pinRequired,
	}
	if host != nil {
		sig.Nickname = host.nickname
		sig.Endpoint = host.endpoint
	}
	for _, id := range rm.order {
		if id == c.id {
			continue
		}
		if o := rm.members[id]; o != nil {
			sig.Peers = append(sig.Peers, protocol.SignalPeer{SenderID: o.id, Nickname: o.nickname, Endpoint: o.endpoint})
		}
	}
	s.fillUDPLocked(&sig, c)
	return sig
}

func (s *Server) reject(c *client, target, message string) {
	s.mu.Lock()
	rm := s.rooms[c.roomCode]
	if rm == nil || rm.hostID != c.id {
		s.mu.Unlock()
		return
	}
	joiner := rm.pending[target]
	if joiner != nil {
		delete(rm.pending, target)
		joiner.pendingRoom = ""
		s.ipStateLocked(joiner.ip).failedAdmits++
	}
	s.mu.Unlock()
	if joiner == nil {
		return
	}
	if len(message) > 128 {
		message = message[:128]
	}
	s.metrics.rejected.Add(1)
	joiner.sendSignal(protocol.Signal{Type: protocol.SigRejected, Message: message})
}

func (s *Server) setLock(c *client, locked, pinRequired bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := s.rooms[c.roomCode]
	if rm == nil || rm.hostID != c.id {
		return
	}
	rm.locked = locked
	rm.pinRequired = locked && pinRequired
	s.log.Info("room lock changed", "room", rm.code, "locked", rm.locked, "pin_required", rm.pinRequired)
}

func (s *Server) portUpdate(c *client, ep protocol.Endpoint) {
	s.mu.Lock()
	rm := s.rooms[c.roomCode]
	if rm == nil {
		s.mu.Unlock()
		return
	}
	m := rm.members[c.id]
	if m == nil || m.client != c {
		s.mu.Unlock()
		return
	}
	if ep.PublicIP == "" {
		ep.PublicIP = c.ip
	}
	m.endpoint = ep
	others := s.connectedExceptLocked(rm, c.id)
	s.mu.Unlock()
	notify := protocol.Signal{Type: protocol.SigPeerPortUpdated, SenderID: c.id, Nickname: c.nickname, Endpoint: ep}
	for _, o := range others {
		o.sendSignal(notify)
	}
}

func (s *Server) leave(c *client) {
	s.mu.Lock()
	notices := s.detachLocked(c, true)
	s.mu.Unlock()
	notices.send()
}

func (s *Server) onDisconnect(c *client) {
	s.mu.Lock()
	notices := s.detachLocked(c, false)
	s.mu.Unlock()
	notices.send()
}

type notice struct {
	to  *client
	sig protocol.Signal
}

type notices []notice

func (ns notices) send() {
	for _, n := range ns {
		n.to.sendSignal(n.sig)
	}
}

// detachLocked removes c from its pending or member slot. With explicit=false a member
// keeps its slot for the grace period so it can reconnect with its member token.
func (s *Server) detachLocked(c *client, explicit bool) notices {
	s.releaseUDPTokenLocked(c)
	if c.pendingRoom != "" {
		if rm := s.rooms[c.pendingRoom]; rm != nil && rm.pending[c.id] == c {
			delete(rm.pending, c.id)
		}
		c.pendingRoom = ""
	}
	if c.roomCode == "" {
		return nil
	}
	rm := s.rooms[c.roomCode]
	c.roomCode = ""
	if rm == nil {
		return nil
	}
	m := rm.members[c.id]
	if m == nil || m.client != c {
		return nil
	}
	m.client = nil
	s.refreshSnapshotLocked(rm)
	if !explicit {
		code, id := rm.code, m.id
		m.graceTimer = time.AfterFunc(s.cfg.GracePeriod, func() {
			s.mu.Lock()
			var ns notices
			if r := s.rooms[code]; r != nil {
				if mm := r.members[id]; mm != nil && mm.client == nil {
					ns = s.removeMemberLocked(r, mm)
				}
			}
			s.mu.Unlock()
			ns.send()
		})
		return nil
	}
	return s.removeMemberLocked(rm, m)
}

func (s *Server) removeMemberLocked(rm *room, m *member) notices {
	if m.graceTimer != nil {
		m.graceTimer.Stop()
		m.graceTimer = nil
	}
	delete(rm.members, m.id)
	for i, id := range rm.order {
		if id == m.id {
			rm.order = append(rm.order[:i], rm.order[i+1:]...)
			break
		}
	}
	s.refreshSnapshotLocked(rm)

	var ns notices
	remaining := s.connectedExceptLocked(rm, "")
	for _, o := range remaining {
		ns = append(ns, notice{o, protocol.Signal{Type: protocol.SigPeerLeft, SenderID: m.id, Nickname: m.nickname}})
	}

	if len(rm.members) == 0 {
		for id, p := range rm.pending {
			ns = append(ns, notice{p, protocol.Signal{Type: protocol.SigRejected, Message: "Room closed"}})
			p.pendingRoom = ""
			delete(rm.pending, id)
		}
		rm.emptySince = time.Now()
		code := rm.code
		time.AfterFunc(s.cfg.GracePeriod, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if r := s.rooms[code]; r == rm && len(r.members) == 0 {
				delete(s.rooms, code)
				s.log.Info("room closed", "room", code)
			}
		})
		return ns
	}

	if rm.hostID == m.id {
		var newHost *member
		for _, id := range rm.order {
			if mm := rm.members[id]; mm != nil && mm.client != nil {
				newHost = mm
				break
			}
		}
		if newHost == nil {
			for _, id := range rm.order {
				if mm := rm.members[id]; mm != nil {
					newHost = mm
					break
				}
			}
		}
		rm.hostID = newHost.id
		rm.hostToken = randomHex(32)
		s.metrics.hostMigrations.Add(1)
		for id, p := range rm.pending {
			ns = append(ns, notice{p, protocol.Signal{Type: protocol.SigRejected, Message: "Host changed, please retry"}})
			p.pendingRoom = ""
			delete(rm.pending, id)
		}
		for _, o := range remaining {
			sig := protocol.Signal{Type: protocol.SigNewHost, RoomCode: rm.code, SenderID: newHost.id, Nickname: newHost.nickname, IsLocked: rm.locked, PinRequired: rm.pinRequired}
			if o.id == newHost.id {
				sig.HostToken = rm.hostToken
			}
			ns = append(ns, notice{o, sig})
		}
	}
	return ns
}

func (s *Server) connectedExceptLocked(rm *room, exceptID string) []*client {
	out := make([]*client, 0, len(rm.members))
	for _, id := range rm.order {
		if id == exceptID {
			continue
		}
		if m := rm.members[id]; m != nil && m.client != nil {
			out = append(out, m.client)
		}
	}
	return out
}

func (s *Server) refreshSnapshotLocked(rm *room) {
	snap := s.connectedExceptLocked(rm, "")
	rm.snapshot.Store(&snap)
}

// forward relays an encrypted frame from sender to every other connected member.
func (s *Server) forward(sender *client, class byte, payload []byte, viaUDP bool) {
	if len(payload) == 0 {
		return
	}
	s.mu.Lock()
	rm := s.rooms[sender.roomCode]
	s.mu.Unlock()
	if rm == nil {
		return
	}
	snap := rm.snapshot.Load()
	if snap == nil {
		return
	}
	udp := s.udpConn.Load()
	for _, m := range *snap {
		if m == sender {
			continue
		}
		if udp != nil && class != protocol.FrameReliable && len(payload) <= maxUDPPayload {
			if addr := m.udpPeer(); addr != nil {
				out := make([]byte, 1+len(payload))
				out[0] = protocol.UDPKindData
				copy(out[1:], payload)
				if _, err := udp.WriteToUDP(out, addr); err == nil {
					s.metrics.udpPacketsOut.Add(1)
					s.metrics.udpBytesOut.Add(uint64(len(out)))
					continue
				}
			}
		}
		m.enqueueFrame(class, payload)
	}
	if viaUDP {
		s.metrics.udpForwarded.Add(1)
	} else {
		s.metrics.wsForwarded.Add(1)
	}
}
