package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"
)

const MaxPeers = 4

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

type P2PNode struct {
	mu            sync.RWMutex
	LocalID       string
	Nickname      string
	RoomCode      string
	RoomKey       []byte
	aead          cipher.AEAD
	Conn          *net.UDPConn
	BroadcastConn *net.UDPConn
	Port          int
	Peers         map[string]*PeerInfo
	IsConnected   bool
	audio         *AudioEngine
	seqCounter    uint32
	OnLog         func(msg string)
	OnPeerEvent   func(event string, peer *PeerInfo)
	stopChan      chan struct{}
}

func NewP2PNode(localID, nickname string, audio *AudioEngine) *P2PNode {
	return &P2PNode{
		LocalID:  localID,
		Nickname: nickname,
		Peers:    make(map[string]*PeerInfo),
		audio:    audio,
		stopChan: make(chan struct{}),
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

// JoinRoom sets the active room code, initializes AES-256-GCM cipher, announces presence and connects
func (n *P2PNode) JoinRoom(roomCode string) {
	n.mu.Lock()
	n.RoomCode = NormalizeCode(roomCode)
	n.RoomKey = deriveRoomKey(n.RoomCode)

	block, err := aes.NewCipher(n.RoomKey)
	if err == nil {
		n.aead, _ = cipher.NewGCM(block)
	}

	n.IsConnected = true
	n.Peers = make(map[string]*PeerInfo)
	n.mu.Unlock()

	n.log(fmt.Sprintf("Odaya baglanildi: %s (Port: %d | E2EE Aktif)", n.RoomCode, n.Port))
	n.broadcastHello()
}

// LeaveRoom sends leave message and clears peer state
func (n *P2PNode) LeaveRoom() {
	n.mu.Lock()
	if !n.IsConnected {
		n.mu.Unlock()
		return
	}
	room := n.RoomCode
	n.IsConnected = false
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

	n.mu.Lock()
	n.Peers = make(map[string]*PeerInfo)
	n.mu.Unlock()

	n.log("Odadan ayrildiniz.")
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
				// Calculate IPv4 broadcast
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
	if err == nil {
		n.Conn.WriteToUDP(data, baddr)
	}
}

func (n *P2PNode) sendPacketTo(addr *net.UDPAddr, pkt *P2PPacket) {
	if addr == nil || n.Conn == nil {
		return
	}

	n.mu.RLock()
	aead := n.aead
	n.mu.RUnlock()

	if aead == nil {
		return
	}

	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		return
	}
	n.Conn.WriteToUDP(data, addr)
}

func (n *P2PNode) broadcastToPeers(pkt *P2PPacket) {
	n.mu.RLock()
	aead := n.aead
	if aead == nil || len(n.Peers) == 0 {
		n.mu.RUnlock()
		return
	}

	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		n.mu.RUnlock()
		return
	}

	for _, peer := range n.Peers {
		if peer.Addr != nil {
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
		isConnected := n.IsConnected
		n.mu.RUnlock()

		if !isConnected || aead == nil {
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
		isConnected := n.IsConnected
		n.mu.RUnlock()

		if !isConnected || aead == nil {
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

	// Only process if matching room
	if NormalizeCode(pkt.RoomCode) != n.RoomCode || !n.IsConnected {
		return
	}

	// Replay and stale packet validation (max 15 seconds window)
	nowMs := time.Now().UnixMilli()
	if math.Abs(float64(nowMs-pkt.Timestamp)) > 15000 {
		return
	}

	switch pkt.Type {
	case PacketHello:
		// Check peer limit
		if len(n.Peers) >= MaxPeers-1 && n.Peers[pkt.SenderID] == nil {
			n.log(fmt.Sprintf("Oda dolu! (%s katilamadi)", pkt.Nickname))
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
			peer.Addr = raddr
			peer.LastSeen = time.Now()
			peer.Nickname = pkt.Nickname
			peer.IsMuted = pkt.IsMuted
			peer.IsDeafened = pkt.IsDeafened
		}

		// Reply with Welcome and existing peers list
		summaries := make([]PeerSummary, 0, len(n.Peers))
		for _, p := range n.Peers {
			if p.ID != pkt.SenderID {
				summaries = append(summaries, PeerSummary{
					ID:         p.ID,
					Nickname:   p.Nickname,
					AddrStr:    p.Addr.String(),
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
		peer, exists := n.Peers[pkt.SenderID]
		if !exists {
			if len(n.Peers) < MaxPeers-1 {
				peer = &PeerInfo{
					ID:         pkt.SenderID,
					Nickname:   pkt.Nickname,
					Addr:       raddr,
					LastSeen:   time.Now(),
					IsMuted:    pkt.IsMuted,
					IsDeafened: pkt.IsDeafened,
				}
				n.Peers[pkt.SenderID] = peer
				n.log(fmt.Sprintf("[+] %s ile baglanti kuruldu. (E2EE Guvenli)", pkt.Nickname))
				if n.OnPeerEvent != nil {
					go n.OnPeerEvent("join", peer)
				}
			}
		} else {
			peer.Addr = raddr
			peer.LastSeen = time.Now()
			peer.IsMuted = pkt.IsMuted
			peer.IsDeafened = pkt.IsDeafened
		}

		// Connect to other peers reported in Welcome packet (mesh topology)
		for _, pSum := range pkt.Peers {
			if pSum.ID != n.LocalID && n.Peers[pSum.ID] == nil && len(n.Peers) < MaxPeers-1 {
				pAddr, err := net.ResolveUDPAddr("udp4", pSum.AddrStr)
				if err == nil {
					newPeer := &PeerInfo{
						ID:         pSum.ID,
						Nickname:   pSum.Nickname,
						Addr:       pAddr,
						LastSeen:   time.Now(),
						IsMuted:    pSum.IsMuted,
						IsDeafened: pSum.IsDeafened,
					}
					n.Peers[pSum.ID] = newPeer
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

	case PacketPing:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.LastSeen = time.Now()
			peer.IsMuted = pkt.IsMuted
			peer.IsDeafened = pkt.IsDeafened
		}
		pong := P2PPacket{
			Type:       PacketPong,
			RoomCode:   n.RoomCode,
			SenderID:   n.LocalID,
			IsMuted:    n.audio.Muted,
			IsDeafened: n.audio.Deafened,
			Timestamp:  pkt.Timestamp, // Echo timestamp
		}
		go n.sendPacketTo(raddr, &pong)

	case PacketPong:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.LastSeen = time.Now()
			peer.IsMuted = pkt.IsMuted
			peer.IsDeafened = pkt.IsDeafened
			peer.PingMs = time.Now().UnixMilli() - pkt.Timestamp
			if peer.PingMs < 0 {
				peer.PingMs = 0
			}
		}

	case PacketAudio:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.LastSeen = time.Now()
			peer.IsMuted = pkt.IsMuted
			peer.IsDeafened = pkt.IsDeafened
			peer.Speaking = pkt.Speaking
			peer.RMS = pkt.RMS
			n.audio.PlayPeerPCM(pkt.SenderID, pkt.Payload, pkt.RMS, pkt.Speaking)
		}

	case PacketMuteState:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.IsMuted = pkt.IsMuted
			peer.IsDeafened = pkt.IsDeafened
		}

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
	ticker := time.NewTicker(1200 * time.Millisecond)
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

		// Check timeouts & send pings
		for id, peer := range n.Peers {
			if now.Sub(peer.LastSeen) > 8*time.Second {
				delete(n.Peers, id)
				n.log(fmt.Sprintf("[-] %s zaman asimina ugradi.", peer.Nickname))
				continue
			}

			pingPkt := P2PPacket{
				Type:       PacketPing,
				RoomCode:   n.RoomCode,
				SenderID:   n.LocalID,
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
