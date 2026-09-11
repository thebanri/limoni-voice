package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thebanri/limoni-voice/screenshare"
)

const MaxPeers = 4

// DefaultRelayURL is the default public WebSocket relay server URL
const DefaultRelayURL = "wss://limoni-voice-production.up.railway.app/ws"

// File transfer limits preventing DoS and memory exhaustion
const (
	MaxFileTransferSize    = 50 * 1024 * 1024 // 50 MB safety limit
	MaxFileChunks          = 2000             // Max chunks per file (at 32KB chunk size)
	MaxConcurrentTransfers = 10               // Max simultaneous incoming transfers
	TransferExpiryDuration = 5 * time.Minute  // Stale transfer timeout
)

func init() {
	gob.Register(P2PPacket{})
	gob.Register(FileMetadata{})
	gob.Register(PeerSummary{})
}

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

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
	PacketScreenShareStart
	PacketScreenShareStop
	PacketScreenShareData
	PacketChatMessage
	PacketPortHop
	PacketRoomLocked // Host rejects join request because room is locked or PIN is invalid
	PacketFileHeader // Initiates an E2EE direct file or code snippet transfer
	PacketFileChunk  // Chunks of encrypted file data
	PacketFileAck    // Transfer delivery completion or cancellation
)

type FileMetadata struct {
	TransferID  string `json:"transfer_id"`
	FileName    string `json:"file_name"`
	FileSize    int64  `json:"file_size"`
	TotalChunks int    `json:"total_chunks"`
	ChunkIndex  int    `json:"chunk_index"`
	IsCode      bool   `json:"is_code"`
	Checksum    string `json:"checksum"`
}

var DangerousFileExtensions = map[string]bool{
	".exe": true, ".bat": true, ".cmd": true, ".sh": true,
	".vbs": true, ".scr": true, ".msi": true, ".jar": true,
	".bin": true, ".elf": true, ".com": true, ".ps1": true,
	".apk": true, ".appimage": true, ".pif": true, ".hta": true,
	".cpl": true, ".reg": true, ".wsf": true, ".vb": true,
	".so": true, ".dll": true,
	// Additional dangerous formats across platforms
	".desktop": true, ".command": true, ".lnk": true, ".url": true,
	".py": true, ".js": true, ".wsh": true, ".gadget": true,
	".msp": true, ".msc": true, ".iso": true, ".img": true,
	".vhd": true, ".dylib": true,
}

type P2PPacket struct {
	Type            PacketType    `json:"type"`
	RoomCode        string        `json:"room_code"`
	SenderID        string        `json:"sender_id"`
	Nickname        string        `json:"nickname"`
	IsMuted         bool          `json:"is_muted"`
	IsDeafened      bool          `json:"is_deafened"`
	Speaking        bool          `json:"speaking"`
	RMS             float64       `json:"rms"`
	Seq             uint32        `json:"seq"`
	Timestamp       int64         `json:"timestamp"`
	Payload         []byte        `json:"payload"`
	IsSharingScreen bool          `json:"is_sharing_screen"`
	VideoPort       int           `json:"video_port"` // Port used for UDP screen streaming
	LocalPort       int           `json:"local_port"` // Local listening UDP port of the sender
	PIN             string        `json:"pin,omitempty"`
	IsLocked        bool          `json:"is_locked,omitempty"`
	FileMeta        *FileMetadata `json:"file_meta,omitempty"`
	Peers           []PeerSummary `json:"peers,omitempty"` // for Welcome message
	Padding         []byte        `json:"padding,omitempty"`   // Anti-DPI randomized padding
}

type PeerSummary struct {
	ID              string
	Nickname        string
	AddrStr         string
	LocalPort       int
	IsMuted         bool
	IsDeafened      bool
	IsSharingScreen bool
	VideoPort       int
}

type PeerInfo struct {
	ID              string
	Nickname        string
	Addr            *net.UDPAddr
	LocalPort       int
	PingMs          int64
	LastSeen        time.Time
	IsMuted         bool
	IsDeafened      bool
	Speaking        bool
	RMS             float64
	IsSharingScreen bool
	VideoPort       int
	ViaRelay        bool      // True if routing through WebSocket relay, false if direct P2P/LAN
	LastDirectSeen  time.Time // Last time a direct UDP packet arrived from this peer
}

// RelayControlMessage represents control JSON payloads sent to/from the relay server
type RelayControlMessage struct {
	Type       string      `json:"type"`
	RoomCode   string      `json:"room_code,omitempty"`
	SenderID   string      `json:"sender_id,omitempty"`
	Nickname   string      `json:"nickname,omitempty"`
	Message    string      `json:"message,omitempty"`
	Port       int         `json:"port,omitempty"`
	PublicIP   string      `json:"public_ip,omitempty"`
	PublicPort int         `json:"public_port,omitempty"`
	YourIP     string      `json:"your_ip,omitempty"`
	PIN        string      `json:"pin,omitempty"`
	IsLocked   bool        `json:"is_locked,omitempty"`
	Peers      []RelayPeer `json:"peers,omitempty"`
	HostToken  string      `json:"host_token,omitempty"`
}

type RelayPeer struct {
	SenderID  string `json:"sender_id"`
	Nickname  string `json:"nickname"`
	PublicIP  string `json:"public_ip,omitempty"`
	LocalPort int    `json:"local_port,omitempty"`
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
	pingSeq           uint32
	OnLog             func(msg string)
	OnPeerEvent       func(event string, peer *PeerInfo)
	stopChan          chan struct{}
	stopOnce          sync.Once
	// Dedicated broadcast sender socket with SO_BROADCAST (works on Windows too)
	bcastSendConn *net.UDPConn
	// Optional direct LAN target peer address
	TargetPeerAddr *net.UDPAddr
	lastSweepTime  time.Time
	// WebSocket Relay for internet-wide P2P forwarding
	RelayURL         string
	RelayToken       string
	LanOnly          bool
	wsConn           *websocket.Conn
	wsPriorityCh     chan []byte // dedicated real-time channel for Audio, Ping, Pong & Control (never delayed by video)
	wsVideoCh        chan []byte // video stream channel with bounded queue to eliminate bufferbloat
	wsMu             sync.Mutex
	isRelayConnected bool
	wsCancel         chan struct{}

	// Anti-Tracking & Dynamic Port/IP Hopping
	AntiTrackingEnabled bool
	hopInterval         time.Duration
	lastHopTime         time.Time
	nextHopTime         time.Time
	currentEpoch        uint32
	prevAead            cipher.AEAD
	hopCancel           chan struct{}
	OnPortHopped        func(newPort int, epoch uint32)

	// Screen Sharing State & Subprocesses
	IsSharingScreen  bool
	IsWatchingScreen bool
	WatchingPeerID   string
	WatchingPeerNick string
	ScreenSharePort    int
	screenSession      *screenshare.Session
	receiverSession    *screenshare.Session
	videoCaptureConn   *net.UDPConn
	videoTCPListener   net.Listener
	videoTCPConn       net.Conn
	videoPreBuf        [][]byte
	videoReorder       VideoReorderBuffer
	audioDedup         AudioDeduplicator
	chatDedup          ChatDeduplicator
	ctrlDedup          ControlDeduplicator
	silenceHangover    int
	audioPreRoll       []audioPreRollFrame
	lastVideoChunkTime time.Time
	OnScreenShare      func(peerID string, isSharing bool, videoPort int)
	OnChatMessage      func(senderID string, nickname string, text string, ts time.Time)
	OnDebugLog         func(msg string)

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

type FileOffer struct {
	TransferID string
	SenderID   string
	SenderNick string
	FileName   string
	FileSize   int64
	IsCode     bool
	Checksum   string
	Data       []byte
}

type IncomingFileTransfer struct {
	TransferID  string
	FileName    string
	FileSize    int64
	TotalChunks int
	Received    int64
	Chunks      map[int][]byte
	StartTime   time.Time
	IsCode      bool
	Checksum    string
}

// AudioDeduplicator prevents duplicate audio packets from being played
// when packets arrive over both direct UDP and WebSocket relay transports.
type AudioDeduplicator struct {
	mu    sync.Mutex
	peers map[string]*audioSeqTracker
}

type audioSeqTracker struct {
	maxSeq uint32
	seqs   [2048]uint32
}

func (d *AudioDeduplicator) ShouldProcess(senderID string, seq uint32) bool {
	if seq == 0 || senderID == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.peers == nil {
		d.peers = make(map[string]*audioSeqTracker)
	}

	tracker, exists := d.peers[senderID]
	if !exists {
		tracker = &audioSeqTracker{}
		d.peers[senderID] = tracker
	}

	idx := seq % 2048
	if tracker.seqs[idx] == seq {
		// Already processed this exact sequence number!
		return false
	}
	// Drop old packets that are too far behind maxSeq (accounting for uint32 wrap-around)
	if tracker.maxSeq > 1500 && seq < tracker.maxSeq-1500 {
		return false
	}
	tracker.seqs[idx] = seq
	if seq > tracker.maxSeq || (tracker.maxSeq > 0xFFFFFF00 && seq < 0x00000FFF) {
		tracker.maxSeq = seq
	}
	return true
}

func (d *AudioDeduplicator) Reset(senderID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.peers != nil {
		delete(d.peers, senderID)
	}
}

// ChatDeduplicator prevents duplicate text chat messages from appearing
// when packets arrive over both direct UDP and WebSocket relay transports.
type ChatDeduplicator struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (d *ChatDeduplicator) ShouldProcess(senderID string, seq uint32, timestamp int64, text string) bool {
	if senderID == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.seen == nil {
		d.seen = make(map[string]time.Time)
	}

	// Clean up entries older than 30 seconds
	now := time.Now()
	for k, exp := range d.seen {
		if now.After(exp) {
			delete(d.seen, k)
		}
	}

	key := fmt.Sprintf("%s:%d:%d:%s", senderID, seq, timestamp, text)
	if _, exists := d.seen[key]; exists {
		return false
	}

	d.seen[key] = now.Add(30 * time.Second)
	return true
}

// ControlDeduplicator prevents duplicate or replayed control packets from executing
type ControlDeduplicator struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (d *ControlDeduplicator) ShouldProcess(senderID string, pktType PacketType, seq uint32, timestamp int64) bool {
	if senderID == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.seen == nil {
		d.seen = make(map[string]time.Time)
	}

	now := time.Now()
	// Periodic garbage collection of expired keys
	if len(d.seen) > 256 {
		for k, exp := range d.seen {
			if now.After(exp) {
				delete(d.seen, k)
			}
		}
	}

	key := fmt.Sprintf("%s:%d:%d:%d", senderID, pktType, seq, timestamp)
	if exp, exists := d.seen[key]; exists && now.Before(exp) {
		return false
	}
	d.seen[key] = now.Add(35 * time.Second)
	return true
}

func (d *ControlDeduplicator) Reset(senderID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen != nil {
		prefix := senderID + ":"
		for k := range d.seen {
			if strings.HasPrefix(k, prefix) {
				delete(d.seen, k)
			}
		}
	}
}

// sanitizeFilename cleans and validates file names received from peers.
// Prevents path traversal, shell injection characters, control characters,
// and reserved Windows device names.
func sanitizeFilename(rawName string, isCode bool) string {
	base := filepath.Base(filepath.Clean(rawName))
	base = strings.ReplaceAll(base, "\\", "")
	base = strings.ReplaceAll(base, "/", "")
	base = strings.TrimSpace(base)

	var b strings.Builder
	for _, r := range base {
		if r < 32 || r == 127 { // Control characters
			continue
		}
		// Disallow shell operators and risky characters
		switch r {
		case '&', '|', ';', '$', '`', '"', '\'', '<', '>', '(', ')', '{', '}', '!', '%', '*', '?', '[', ']', '^', '~', ':', '\r', '\n':
			continue
		default:
			b.WriteRune(r)
		}
	}
	clean := strings.TrimSpace(b.String())
	clean = strings.TrimLeft(clean, ".")

	if clean == "" {
		if isCode {
			return "snippet.txt"
		}
		return "received_file.bin"
	}

	stem := strings.ToUpper(strings.TrimSuffix(clean, filepath.Ext(clean)))
	reservedWindowsNames := map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true,
		"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
		"LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}
	if reservedWindowsNames[stem] {
		clean = "file_" + clean
	}

	if len(clean) > 200 {
		ext := filepath.Ext(clean)
		clean = clean[:200-len(ext)] + ext
	}

	return clean
}

// audioPreRollFrame stores 20ms audio frames during silence periods so word onsets
// (such as unvoiced fricatives 's', 'p', 't', 'k') are never clipped when VAD triggers.
type audioPreRollFrame struct {
	rms float64
	pcm []byte
	ts  int64
}

// VideoReorderBuffer ensures video packets are delivered to the player in strictly sequential order.
// It discards stale/duplicate packets and buffers out-of-order packets (up to 24 items / ~20ms window)
// so MPEG-TS / H.264 streams never suffer from macroblocking, packet loss or green screen tear.
type VideoReorderBuffer struct {
	mu          sync.Mutex
	expectedSeq uint32
	pending     map[uint32][]byte
	firstPkt    bool
}

func (b *VideoReorderBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expectedSeq = 0
	b.pending = make(map[uint32][]byte)
	b.firstPkt = true
}

func (b *VideoReorderBuffer) Push(seq uint32, payload []byte) [][]byte {
	if len(payload) == 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.pending == nil {
		b.pending = make(map[uint32][]byte)
	}

	if b.firstPkt || seq == 0 {
		b.firstPkt = false
		b.expectedSeq = seq
		b.pending[seq] = payload
	} else {
		// Detect wrap-around or old stale packet
		diff := int32(seq - b.expectedSeq)
		if diff < 0 && diff > -3000 {
			// Stale packet that already passed its sequence window, discard!
			return nil
		}
		// If packet sequence jumped forward by more than 1000 (possible new stream), re-sync
		if diff > 1000 || diff < -3000 {
			b.expectedSeq = seq
			b.pending = make(map[uint32][]byte)
		}
		b.pending[seq] = payload
	}

	// Drain all contiguous sequential packets from pending
	var ready [][]byte
	for {
		chunk, ok := b.pending[b.expectedSeq]
		if !ok {
			break
		}
		delete(b.pending, b.expectedSeq)
		ready = append(ready, chunk)
		b.expectedSeq++
		if b.expectedSeq == 0 {
			b.expectedSeq = 1
		}
	}

	// If pending buffer grows too large (> 4 packets ~15ms), force advance to avoid stalling
	if len(b.pending) > 4 {
		var minSeq uint32
		var found bool
		for s := range b.pending {
			if !found || (int32(s-minSeq) < 0) {
				minSeq = s
				found = true
			}
		}
		if found {
			b.expectedSeq = minSeq
			for {
				chunk, ok := b.pending[b.expectedSeq]
				if !ok {
					break
				}
				delete(b.pending, b.expectedSeq)
				ready = append(ready, chunk)
				b.expectedSeq++
				if b.expectedSeq == 0 {
					b.expectedSeq = 1
				}
			}
		}
	}

	return ready
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
	if !strings.Contains(addrStr, ":") {
		addrStr = addrStr + ":50000"
	}
	uaddr, err := net.ResolveUDPAddr("udp4", addrStr)
	if err == nil {
		n.mu.Lock()
		n.TargetPeerAddr = uaddr
		n.mu.Unlock()
		n.log(fmt.Sprintf("[DIRECT] Configured direct LAN peer: %s", uaddr.String()))
	}
}

// deriveRoomKey securely derives a 256-bit AES encryption key from the room code
func deriveRoomKey(roomCode string) []byte {
	clean := NormalizeCode(roomCode)
	mac := hmac.New(sha256.New, []byte("limoni-voice-e2ee-master-salt-v1"))
	mac.Write([]byte(clean))
	return mac.Sum(nil)
}

// deriveRoomCipher securely derives an AES-256-GCM AEAD cipher from the room key
func deriveRoomCipher(roomKey []byte) (cipher.AEAD, error) {
	if len(roomKey) == 0 {
		return nil, errors.New("empty room key")
	}
	block, err := aes.NewCipher(roomKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// deriveEpochCipher securely derives an AES-256-GCM AEAD cipher for the room
func deriveEpochCipher(roomKey []byte, epoch uint32) (cipher.AEAD, error) {
	return deriveRoomCipher(roomKey)
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

	_ = conn.SetReadBuffer(4 * 1024 * 1024)
	_ = conn.SetWriteBuffer(4 * 1024 * 1024)

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

	screenshare.LogCallback = n.log

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
	if n.hopCancel != nil {
		close(n.hopCancel)
		n.hopCancel = nil
	}
	n.Connecting = false
	n.IsHost = true
	n.HostID = n.LocalID
	n.HostNick = n.Nickname
	n.RoomCode = NormalizeCode(roomCode)
	n.RoomKey = deriveRoomKey(n.RoomCode)
	n.currentEpoch = 0
	n.prevAead = nil
	n.aead, _ = deriveRoomCipher(n.RoomKey)
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

	if n.LanOnly || n.RelayURL == "" || strings.EqualFold(n.RelayURL, "none") || strings.EqualFold(n.RelayURL, "off") {
		n.log(fmt.Sprintf("[HOST] Room opened: %s (Port: %d | LAN Mode | Anti-Tracking Active)", n.RoomCode, n.Port))
	} else {
		n.log(fmt.Sprintf("[HOST] Room opened: %s (Port: %d | E2EE Secure | Anti-Tracking Active)", n.RoomCode, n.Port))
	}
	n.broadcastHello()
	n.connectRelay("host", n.RoomCode)
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
	if n.hopCancel != nil {
		close(n.hopCancel)
		n.hopCancel = nil
	}

	cancelChan := make(chan struct{})
	n.connectCancel = cancelChan
	n.Connecting = true
	n.IsConnected = false
	n.IsHost = false
	n.HostID = ""
	n.HostNick = ""
	n.RoomPIN = customPIN
	n.ConnectTargetRoom = cleanCode
	n.RoomCode = cleanCode
	n.RoomKey = deriveRoomKey(cleanCode)
	n.currentEpoch = 0
	n.prevAead = nil
	n.aead, _ = deriveRoomCipher(n.RoomKey)
	n.lastHopTime = time.Now()
	n.nextHopTime = time.Now().Add(n.hopInterval)
	hopCancel := make(chan struct{})
	n.hopCancel = hopCancel

	n.Peers = make(map[string]*PeerInfo)
	n.OnJoinSuccess = onSuccess
	n.OnJoinFailed = onFailed
	n.mu.Unlock()

	go n.portHopSupervisor(hopCancel)

	if n.LanOnly || n.RelayURL == "" || strings.EqualFold(n.RelayURL, "none") || strings.EqualFold(n.RelayURL, "off") {
		n.log(fmt.Sprintf("[CONNECT] Searching room '%s' on local network (LAN)...", cleanCode))
	} else {
		n.log(fmt.Sprintf("[CONNECT] Searching room '%s' and verifying host...", cleanCode))
	}

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

					n.log(fmt.Sprintf("[ERROR] Room '%s' not found (Host offline or room not created).", cleanCode))
					if failedCb != nil {
						failedCb("This room is not currently open! Make sure your friend has opened the room by clicking [2] CREATE ROOM.")
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
	n.hostToken = ""
	if !n.IsConnected && !n.Connecting {
		n.RoomCode = ""
		n.RoomKey = nil
		n.mu.Unlock()
		return
	}
	room := n.RoomCode
	wasHost := n.IsHost
	aead := n.aead
	n.IsConnected = false
	n.Connecting = false
	n.IsHost = false
	n.HostID = ""
	n.HostNick = ""
	n.RoomCode = ""
	n.RoomKey = nil
	n.IsLocked = false
	n.RoomPIN = ""
	n.nextHopTime = time.Time{}
	n.lastHopTime = time.Time{}
	peers := make([]*PeerInfo, 0, len(n.Peers))
	for _, p := range n.Peers {
		peers = append(peers, p)
	}
	if n.audio != nil {
		n.audio.ClearAllPeers()
	}
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
	if aead != nil {
		data, err := encodeAndEncryptPacket(&pkt, aead)
		if err == nil {
			for _, peer := range peers {
				if peer.Addr != nil && n.Conn != nil {
					n.Conn.WriteToUDP(data, peer.Addr)
				}
			}
		}
	}
	n.closeRelay()

	n.mu.Lock()
	n.aead = nil
	n.prevAead = nil
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
	if n.Conn != nil {
		_ = n.Conn.Close()
		n.Conn = nil
	}
	if n.BroadcastConn != nil {
		_ = n.BroadcastConn.Close()
		n.BroadcastConn = nil
	}
	if n.bcastSendConn != nil {
		_ = n.bcastSendConn.Close()
		n.bcastSendConn = nil
	}
	n.mu.Unlock()
}

// UpdateRelaySettings dynamically updates the relay server URL and authentication token,
// reconnecting to the new relay server if a room session is currently active.
func (n *P2PNode) UpdateRelaySettings(newURL, newToken string) {
	n.mu.Lock()
	n.RelayURL = strings.TrimSpace(newURL)
	n.RelayToken = strings.TrimSpace(newToken)
	if strings.EqualFold(n.RelayURL, "none") || strings.EqualFold(n.RelayURL, "off") {
		n.LanOnly = true
		n.RelayURL = ""
	} else if n.RelayURL != "" {
		n.LanOnly = false
	}
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

// connectRelay connects to the WebSocket relay server in the background and sends the initial host/join message.
// If the connection drops while the room is active, it automatically reconnects.
func (n *P2PNode) connectRelay(action string, roomCode string) {
	n.mu.Lock()
	relayURL := n.RelayURL
	if n.LanOnly || relayURL == "" || strings.EqualFold(relayURL, "none") || strings.EqualFold(relayURL, "off") {
		n.mu.Unlock()
		return
	}
	// Close existing WS connection and supervisor if any
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

		wsPriorityCh := make(chan []byte, 256)
		wsVideoCh := make(chan []byte, 32)
		n.mu.Lock()
		n.wsPriorityCh = wsPriorityCh
		n.wsVideoCh = wsVideoCh
		n.mu.Unlock()

		targetURL := relayURL
		headers := http.Header{}
		if n.RelayToken != "" {
			headers.Set("X-Auth-Token", n.RelayToken)
			if !strings.Contains(targetURL, "token=") {
				sep := "?"
				if strings.Contains(targetURL, "?") {
					sep = "&"
				}
				targetURL = fmt.Sprintf("%s%stoken=%s", targetURL, sep, url.QueryEscape(n.RelayToken))
			}
		}

		dialer := websocket.Dialer{
			HandshakeTimeout: 8 * time.Second,
		}
		conn, resp, err := dialer.Dial(targetURL, headers)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusUnauthorized {
				if firstConnect {
					n.log("[RELAY] ⛔ Relay connection rejected (401 Unauthorized): Invalid or missing token. Check --relay-token or LIMONI_RELAY_TOKEN.")
					firstConnect = false
				}
			} else if firstConnect {
				n.log(fmt.Sprintf("[RELAY] Failed to connect to relay server (%v). LAN mode active.", err))
				firstConnect = false
			}
			// Fast retry when disconnected (e.g. switching to VPN)
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
		n.isRelayConnected = true
		n.mu.Unlock()

		if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
			_ = tcpConn.SetWriteBuffer(64 * 1024) // Cap kernel socket send buffer to 64KB (strictly prevents TCP bufferbloat & 1000ms spikes)
			_ = tcpConn.SetReadBuffer(64 * 1024)
		}

		if firstConnect {
			n.log(fmt.Sprintf("[RELAY] Connected to relay server (%s | Internet Active)", relayURL))
			firstConnect = false
		} else {
			n.log("[RELAY] Relay connection automatically re-established.")
		}

		connCancel := make(chan struct{})

		// Start write pump with dedicated priority vs video scheduling
		go n.relayWritePump(conn, wsPriorityCh, wsVideoCh, connCancel)

		n.mu.RLock()
		localPort := n.Port
		currentPIN := n.RoomPIN
		currentLocked := n.IsLocked
		token := n.hostToken
		n.mu.RUnlock()

		if action == "host" {
			n.sendRelayControl(RelayControlMessage{
				Type:      "host_room",
				RoomCode:  roomCode,
				SenderID:  n.LocalID,
				Nickname:  n.Nickname,
				Port:      localPort,
				PIN:       currentPIN,
				IsLocked:  currentLocked || currentPIN != "",
				HostToken: token,
			})
		} else if action == "join" {
			n.sendRelayControl(RelayControlMessage{
				Type:     "join_room",
				RoomCode: roomCode,
				SenderID: n.LocalID,
				Nickname: n.Nickname,
				Port:     localPort,
				PIN:      currentPIN,
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

		// Fast reconnect after drop
		select {
		case <-cancel:
			return
		case <-time.After(500 * time.Millisecond):
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
		return "Connected"
	}
	if n.IsConnected || n.Connecting {
		return "Offline (LAN Mode)"
	}
	return "Disconnected"
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

func (n *P2PNode) relayWritePump(conn *websocket.Conn, priorityCh, videoCh chan []byte, cancel chan struct{}) {
	ticker := time.NewTicker(20 * time.Second)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()

	writeMsg := func(data []byte) error {
		n.wsMu.Lock()
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err := conn.WriteMessage(websocket.BinaryMessage, data)
		n.wsMu.Unlock()
		return err
	}

	for {
		// Priority 1: Drain ALL pending priority packets (Voice, Ping, Pong, Mute, Control) first!
		// Ping and Voice will NEVER wait behind video packets!
		for {
			select {
			case data, ok := <-priorityCh:
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
		case <-cancel:
			return
		case data, ok := <-priorityCh:
			if !ok {
				return
			}
			if writeMsg(data) != nil {
				return
			}
		case data, ok := <-videoCh:
			if !ok {
				return
			}
			if writeMsg(data) != nil {
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

	conn.SetReadDeadline(time.Now().Add(45 * time.Second))

	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		n.wsMu.Lock()
		err := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
		n.wsMu.Unlock()
		return err
	})

	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(45 * time.Second))
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

		conn.SetReadDeadline(time.Now().Add(45 * time.Second))

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
			prevAead := n.prevAead
			active := n.IsConnected || n.Connecting
			n.mu.RUnlock()

			if !active || aead == nil {
				continue
			}

			var pkt P2PPacket
			if err := decryptAndDecodePacket(data, &pkt, aead); err != nil {
				if prevAead != nil {
					if err2 := decryptAndDecodePacket(data, &pkt, prevAead); err2 != nil {
						continue
					}
				} else {
					continue
				}
			}

			// Fast-path: feed video chunks directly to sequential reorder buffer
			if pkt.Type == PacketScreenShareData {
				n.forwardVideoChunk(pkt.SenderID, pkt.Payload, pkt.Seq, pkt.Nickname)
				continue
			}

			n.handlePacket(&pkt, nil)
		}
	}
}

// punchPeerUDP sends direct UDP probe waves to punch through NAT and establish zero-latency P2P
func (n *P2PNode) punchPeerUDP(publicIP string, localPort int) {
	if publicIP == "" {
		return
	}

	pkt := P2PPacket{
		Type:       PacketHello,
		RoomCode:   n.RoomCode,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    n.audio.Muted,
		IsDeafened: n.audio.Deafened,
		Timestamp:  time.Now().UnixMilli(),
	}

	targetPorts := []int{localPort}
	for p := 50000; p <= 50008; p++ {
		if p != localPort {
			targetPorts = append(targetPorts, p)
		}
	}
	targetPorts = append(targetPorts, 45454)

	// Send 3 probe waves spaced by 100ms to open bidirectional NAT mapping reliably
	go func() {
		for wave := 0; wave < 3; wave++ {
			for _, p := range targetPorts {
				if p > 0 {
					raddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", publicIP, p))
					if err == nil && n.Conn != nil {
						n.sendDirectUDPPacket(raddr, &pkt)
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

func (n *P2PNode) sendDirectUDPPacket(addr *net.UDPAddr, pkt *P2PPacket) {
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
	if err == nil && n.Conn != nil {
		n.Conn.WriteToUDP(data, addr)
	}
}

func (n *P2PNode) handleRelayControl(msg RelayControlMessage) {
	n.mu.Lock()
	defer n.mu.Unlock()

	switch msg.Type {
	case "room_created":
		if msg.HostToken != "" {
			n.hostToken = msg.HostToken
		}
		n.log(fmt.Sprintf("[RELAY] Room '%s' created on relay (Internet E2EE)", msg.RoomCode))

	case "welcome":
		if !n.IsConnected && n.Connecting {
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.IsConnected = true
			n.Connecting = false
			n.IsHost = false
			n.HostID = msg.SenderID
			n.HostNick = msg.Nickname

			var hostAddr *net.UDPAddr
			if msg.PublicIP != "" && msg.Port > 0 {
				hostAddr, _ = net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", msg.PublicIP, msg.Port))
			}

			if existingHost, ok := n.Peers[msg.SenderID]; ok {
				existingHost.Nickname = msg.Nickname
				existingHost.LastSeen = time.Now()
				if hostAddr != nil && (existingHost.Addr == nil || (!existingHost.Addr.IP.IsPrivate() && !existingHost.Addr.IP.IsLoopback())) {
					existingHost.Addr = hostAddr
				}
			} else {
				hostPeer := &PeerInfo{
					ID:       msg.SenderID,
					Nickname: msg.Nickname,
					Addr:     hostAddr,
					LastSeen: time.Now(),
					ViaRelay: true,
				}
				n.Peers[msg.SenderID] = hostPeer
			}

			// Trigger direct UDP hole-punching to Host
			if msg.PublicIP != "" {
				go n.punchPeerUDP(msg.PublicIP, msg.Port)
			}

			for _, p := range msg.Peers {
				if p.SenderID != n.LocalID {
					var pAddr *net.UDPAddr
					if p.PublicIP != "" && p.LocalPort > 0 {
						pAddr, _ = net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", p.PublicIP, p.LocalPort))
					}
					if existingPeer, ok := n.Peers[p.SenderID]; ok {
						existingPeer.Nickname = p.Nickname
						existingPeer.LastSeen = time.Now()
						if pAddr != nil && (existingPeer.Addr == nil || (!existingPeer.Addr.IP.IsPrivate() && !existingPeer.Addr.IP.IsLoopback())) {
							existingPeer.Addr = pAddr
						}
					} else {
						n.Peers[p.SenderID] = &PeerInfo{
							ID:       p.SenderID,
							Nickname: p.Nickname,
							Addr:     pAddr,
							LastSeen: time.Now(),
							ViaRelay: true,
						}
					}
					if p.PublicIP != "" {
						go n.punchPeerUDP(p.PublicIP, p.LocalPort)
					}
				}
			}

			if msg.PIN != "" {
				n.RoomPIN = msg.PIN
				n.IsLocked = true
			} else if msg.IsLocked {
				n.IsLocked = true
			}

			n.log(fmt.Sprintf("[RELAY] Connected to room %s! (Host: %s | Internet E2EE)", n.RoomCode, msg.Nickname))
			successCb := n.OnJoinSuccess
			if successCb != nil {
				go successCb(msg.Nickname)
			}
		} else if n.IsConnected {
			for _, p := range msg.Peers {
				if p.SenderID != n.LocalID {
					var pAddr *net.UDPAddr
					if p.PublicIP != "" && p.LocalPort > 0 {
						pAddr, _ = net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", p.PublicIP, p.LocalPort))
					}
					if existingPeer, ok := n.Peers[p.SenderID]; ok {
						existingPeer.Nickname = p.Nickname
						existingPeer.LastSeen = time.Now()
						if pAddr != nil && (existingPeer.Addr == nil || (!existingPeer.Addr.IP.IsPrivate() && !existingPeer.Addr.IP.IsLoopback())) {
							existingPeer.Addr = pAddr
						}
					} else {
						n.Peers[p.SenderID] = &PeerInfo{
							ID:       p.SenderID,
							Nickname: p.Nickname,
							Addr:     pAddr,
							LastSeen: time.Now(),
							ViaRelay: true,
						}
					}
					if p.PublicIP != "" {
						go n.punchPeerUDP(p.PublicIP, p.LocalPort)
					}
				}
			}
		}

	case "peer_joined":
		if n.IsConnected && msg.SenderID != n.LocalID {
			var peerAddr *net.UDPAddr
			if msg.PublicIP != "" && msg.Port > 0 {
				peerAddr, _ = net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", msg.PublicIP, msg.Port))
			}

			peer, exists := n.Peers[msg.SenderID]
			if !exists {
				peer = &PeerInfo{
					ID:       msg.SenderID,
					Nickname: msg.Nickname,
					Addr:     peerAddr,
					LastSeen: time.Now(),
				}
				n.Peers[msg.SenderID] = peer
				n.log(fmt.Sprintf("[+] %s joined the room! (Internet E2EE)", msg.Nickname))
				if n.OnPeerEvent != nil {
					go n.OnPeerEvent("join", peer)
				}
				go n.sendPingToPeer(peer)
			} else {
				peer.Nickname = msg.Nickname
				peer.LastSeen = time.Now()
				if peerAddr != nil && (peer.Addr == nil || (!peer.Addr.IP.IsPrivate() && !peer.Addr.IP.IsLoopback())) {
					peer.Addr = peerAddr
				}
			}

			// Trigger direct UDP hole-punching to the new/reconnected joiner
			if msg.PublicIP != "" {
				go n.punchPeerUDP(msg.PublicIP, msg.Port)
			}
		}

	case "peer_port_updated":
		if peer, exists := n.Peers[msg.SenderID]; exists {
			if msg.Port > 0 {
				peer.LocalPort = msg.Port
				if peer.Addr != nil {
					peer.Addr = &net.UDPAddr{IP: peer.Addr.IP, Port: msg.Port}
				} else if msg.PublicIP != "" {
					ip := net.ParseIP(msg.PublicIP)
					if ip != nil {
						peer.Addr = &net.UDPAddr{IP: ip, Port: msg.Port}
					}
				}
			}
			peer.LastSeen = time.Now()
			n.log(fmt.Sprintf("[SECURITY] Peer %s rotated endpoint via relay: :%d", msg.Nickname, msg.Port))

			// Trigger direct UDP hole-punching to the rotated port
			pubIP := msg.PublicIP
			if pubIP == "" && peer.Addr != nil {
				pubIP = peer.Addr.IP.String()
			}
			if pubIP != "" {
				go n.punchPeerUDP(pubIP, msg.Port)
			}
		}

	case "peer_left":
		if peer, exists := n.Peers[msg.SenderID]; exists {
			wasSharing := peer.IsSharingScreen
			delete(n.Peers, msg.SenderID)
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
		}

	case "new_host":
		n.HostID = msg.SenderID
		n.HostNick = msg.Nickname
		if msg.SenderID == n.LocalID {
			n.IsHost = true
			if msg.HostToken != "" {
				n.hostToken = msg.HostToken
			}
			if msg.PIN != "" {
				n.RoomPIN = msg.PIN
				n.IsLocked = true
			} else if msg.IsLocked {
				n.IsLocked = true
			}
			if n.RoomPIN != "" {
				n.log(fmt.Sprintf("[HOST] Former host left, you are now the room HOST! (Room PIN: %s)", n.RoomPIN))
			} else {
				n.log("[HOST] Former host left, you are now the room HOST!")
			}
			if n.OnRoomLocked != nil && n.IsLocked {
				go n.OnRoomLocked(true, n.RoomPIN)
			}
		} else {
			n.IsHost = false
			if msg.IsLocked {
				n.IsLocked = true
			}
			n.log(fmt.Sprintf("[HOST] New room HOST: %s", msg.Nickname))
		}

	case "room_locked":
		if n.Connecting && !n.IsConnected {
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.Connecting = false
			n.aead = nil
			n.RoomCode = ""
			n.RoomPIN = ""
			failedCb := n.OnJoinFailed
			reason := "Room is locked by host"
			if msg.Message == "PIN_REQUIRED" || strings.Contains(strings.ToUpper(msg.Message), "PIN") {
				reason = "Room is protected by PIN (join with code:PIN)"
			}
			n.log(fmt.Sprintf("[ERROR] Room join rejected: %s", reason))
			if failedCb != nil {
				go failedCb(reason)
			}
		}

	case "host_left":
		// Only close if no new host was elected
		if n.HostID == msg.SenderID && !n.IsHost {
			n.log("[ERROR] Host left, room closed.")
			if n.IsWatchingScreen {
				go func() {
					_ = n.StopWatchingScreen()
				}()
			}
			if n.OnPeerEvent != nil {
				if host, ok := n.Peers[n.HostID]; ok {
					go n.OnPeerEvent("leave", host)
				}
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
			n.log("[ERROR] Room join rejected: Room full (Max 4 people).")
			if failedCb != nil {
				go failedCb("This room is full! (Maximum 4 people)")
			}
		}

	case "room_not_found":
		if n.Connecting && !n.IsConnected {
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.Connecting = false
			n.aead = nil
			n.RoomCode = ""
			failedCb := n.OnJoinFailed
			msgText := msg.Message
			if msgText == "" {
				msgText = "This room is not currently open! Make sure your friend has opened the room by clicking [2] CREATE ROOM."
			}
			n.log(fmt.Sprintf("[ERROR] Failed to join room: %s", msgText))
			if failedCb != nil {
				go failedCb(msgText)
			}
		}

	case "error":
		if n.Connecting && !n.IsConnected {
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.Connecting = false
			n.aead = nil
			n.RoomCode = ""
			failedCb := n.OnJoinFailed
			msgText := msg.Message
			if msgText == "" {
				msgText = "Server connection error occurred."
			}
			n.log(fmt.Sprintf("[ERROR] Server error: %s", msgText))
			if failedCb != nil {
				go failedCb(msgText)
			}
		}
	}
}

func (n *P2PNode) SendAudio(rms float64, speaking bool, pcm []byte) {
	n.mu.Lock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.Unlock()
		return
	}
	room := n.RoomCode

	// DTX (Discontinuous Transmission) with Pre-Roll Lookback Cushion:
	// When silent, maintain a 3-frame (~60ms) ring buffer.
	// When speech begins, flush the pre-buffered frames immediately so word onsets
	// (like the 'S' in "Selam") are never clipped or truncated.
	var flushedPreRoll []audioPreRollFrame
	if speaking {
		if n.silenceHangover == 0 && len(n.audioPreRoll) > 0 {
			flushedPreRoll = n.audioPreRoll
			n.audioPreRoll = nil
		}
		n.silenceHangover = 25
	} else {
		if n.silenceHangover > 0 {
			n.silenceHangover--
		} else {
			// In silence: buffer up to 3 frames (~60ms lookback)
			pcmCopy := make([]byte, len(pcm))
			copy(pcmCopy, pcm)
			n.audioPreRoll = append(n.audioPreRoll, audioPreRollFrame{
				rms: rms,
				pcm: pcmCopy,
				ts:  time.Now().UnixMilli(),
			})
			if len(n.audioPreRoll) > 3 {
				n.audioPreRoll = n.audioPreRoll[len(n.audioPreRoll)-3:]
			}
			n.mu.Unlock()
			return
		}
	}

	var pktsToSend []P2PPacket
	if len(flushedPreRoll) > 0 {
		for _, pre := range flushedPreRoll {
			n.seqCounter++
			pktsToSend = append(pktsToSend, P2PPacket{
				Type:       PacketAudio,
				RoomCode:   room,
				SenderID:   n.LocalID,
				Nickname:   n.Nickname,
				IsMuted:    n.audio.Muted,
				IsDeafened: n.audio.Deafened,
				Speaking:   true,
				RMS:        pre.rms,
				Seq:        n.seqCounter,
				Timestamp:  pre.ts,
				Payload:    pre.pcm,
			})
		}
	}

	n.seqCounter++
	pktsToSend = append(pktsToSend, P2PPacket{
		Type:       PacketAudio,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		IsMuted:    n.audio.Muted,
		IsDeafened: n.audio.Deafened,
		Speaking:   speaking,
		RMS:        rms,
		Seq:        n.seqCounter,
		Timestamp:  time.Now().UnixMilli(),
		Payload:    pcm,
	})
	n.mu.Unlock()

	for i := range pktsToSend {
		n.sendAudioToPeers(&pktsToSend[i])
	}
}

// sendAudioToPeers routes audio packets through the high-priority channel
func (n *P2PNode) sendAudioToPeers(pkt *P2PPacket) {
	n.mu.RLock()
	aead := n.aead
	wsPriorityCh := n.wsPriorityCh
	isRelay := n.isRelayConnected
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	if aead == nil || (len(n.Peers) == 0 && !isRelay) {
		n.mu.RUnlock()
		return
	}

	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		n.mu.RUnlock()
		return
	}

	// 1. Forward via dedicated high-priority WebSocket channel (never queued behind video)
	if isRelay && wsPriorityCh != nil {
		select {
		case wsPriorityCh <- data:
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

func (n *P2PNode) SendScreenShareState(isSharing bool, videoPort int) {
	n.mu.RLock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.RUnlock()
		return
	}
	room := n.RoomCode
	n.mu.RUnlock()

	pktType := PacketScreenShareStop
	if isSharing {
		pktType = PacketScreenShareStart
	}

	pkt := P2PPacket{
		Type:            pktType,
		RoomCode:        room,
		SenderID:        n.LocalID,
		Nickname:        n.Nickname,
		LocalPort:       n.Port,
		IsSharingScreen: isSharing,
		VideoPort:       videoPort,
		Timestamp:       time.Now().UnixMilli(),
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
	room := n.RoomCode
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

func (n *P2PNode) sweepSubnets(pkt *P2PPacket) {
	n.mu.Lock()
	if n.IsConnected || !n.Connecting {
		n.mu.Unlock()
		return
	}
	if time.Since(n.lastSweepTime) < 2500*time.Millisecond {
		n.mu.Unlock()
		return
	}
	n.lastSweepTime = time.Now()
	aead := n.aead
	conn := n.Conn
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	n.mu.Unlock()

	if aead == nil || conn == nil {
		return
	}

	// Pre-encrypt packet ONCE for the entire sweep
	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		return
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}

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
			// Unicast sweep across local /24 subnet on primary Limoni ports (50000 & 50001)
			for host := 1; host <= 254; host++ {
				if byte(host) == ip[3] {
					continue // skip self
				}
				targetIP := net.IPv4(ip[0], ip[1], ip[2], byte(host))
				uaddr1 := &net.UDPAddr{IP: targetIP, Port: 50000}
				conn.WriteToUDP(data, uaddr1)
				uaddr2 := &net.UDPAddr{IP: targetIP, Port: 50001}
				conn.WriteToUDP(data, uaddr2)
			}
		}
	}
}

func (n *P2PNode) broadcastJoinRequest() {
	n.mu.RLock()
	room := n.ConnectTargetRoom
	isConnecting := n.Connecting
	localPort := n.Port
	targetPeer := n.TargetPeerAddr
	pin := n.RoomPIN
	n.mu.RUnlock()

	if !isConnecting || room == "" {
		return
	}

	pkt := P2PPacket{
		Type:       PacketJoinRequest,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		LocalPort:  localPort,
		IsMuted:    n.audio.Muted,
		IsDeafened: n.audio.Deafened,
		PIN:        pin,
		Timestamp:  time.Now().UnixMilli(),
	}

	// 1. Direct Target Peer if configured (instant unicast)
	if targetPeer != nil {
		n.sendDirectUDPPacket(targetPeer, &pkt)
	}

	// 2. Send via local port range 50000-50010 on loopback (instant multi-instance discovery)
	for p := 50000; p <= 50010; p++ {
		if p != n.Port {
			raddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.1:%d", p))
			if err == nil {
				n.sendDirectUDPPacket(raddr, &pkt)
			}
		}
	}

	// 3. Send via LAN broadcast 255.255.255.255 to common ports
	for p := 50000; p <= 50005; p++ {
		n.sendBroadcastPacket(&pkt, p)
	}
	n.sendBroadcastPacket(&pkt, 45454)

	// 4. Send to specific subnet broadcast addresses of active interfaces
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
							n.sendDirectUDPPacket(baddr, &pkt)
						}
					}
					baddr45454, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", bcast.String(), 45454))
					if err == nil {
						n.sendDirectUDPPacket(baddr45454, &pkt)
					}
				}
			}
		}
	}

	// 5. Active Subnet Unicast Sweep (guaranteed delivery, rate-limited and pre-encrypted)
	go n.sweepSubnets(&pkt)
}

func (n *P2PNode) broadcastHello() {
	n.mu.RLock()
	room := n.RoomCode
	isConnected := n.IsConnected
	localPort := n.Port
	targetPeer := n.TargetPeerAddr
	n.mu.RUnlock()

	if !isConnected || room == "" {
		return
	}

	pkt := P2PPacket{
		Type:       PacketHello,
		RoomCode:   room,
		SenderID:   n.LocalID,
		Nickname:   n.Nickname,
		LocalPort:  localPort,
		IsMuted:    n.audio.Muted,
		IsDeafened: n.audio.Deafened,
		Timestamp:  time.Now().UnixMilli(),
	}

	// 1. Direct Target Peer if configured
	if targetPeer != nil {
		n.sendDirectUDPPacket(targetPeer, &pkt)
	}

	// 2. Send via local port range 50000-50010 on loopback (instant multi-instance discovery)
	for p := 50000; p <= 50010; p++ {
		if p != n.Port {
			raddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.1:%d", p))
			if err == nil {
				n.sendDirectUDPPacket(raddr, &pkt)
			}
		}
	}

	// 3. Send via LAN broadcast 255.255.255.255 to common ports (best-effort)
	for p := 50000; p <= 50005; p++ {
		n.sendBroadcastPacket(&pkt, p)
	}
	n.sendBroadcastPacket(&pkt, 45454)

	// 4. Send to specific subnet broadcast addresses of active interfaces
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
							n.sendDirectUDPPacket(baddr, &pkt)
						}
					}
					baddr45454, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", bcast.String(), 45454))
					if err == nil {
						n.sendDirectUDPPacket(baddr45454, &pkt)
					}
				}
			}
		}
	}
}

func (n *P2PNode) sendBroadcastPacket(pkt *P2PPacket, port int) {
	n.mu.RLock()
	aead := n.aead
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
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
	wsPriorityCh := n.wsPriorityCh
	isRelay := n.isRelayConnected
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	n.mu.RUnlock()

	if aead == nil {
		return
	}

	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		return
	}

	// 1. Forward via high-priority WebSocket channel (Ping, Pong & Control with 0ms queue delay)
	if isRelay && wsPriorityCh != nil {
		select {
		case wsPriorityCh <- data:
		default:
		}
	}

	// 2. Also send directly via UDP if destination endpoint is reachable (LAN / P2P hole-punched)
	if addr != nil && n.Conn != nil {
		n.Conn.WriteToUDP(data, addr)
	}
}

func (n *P2PNode) broadcastToPeers(pkt *P2PPacket) {
	n.mu.RLock()
	aead := n.aead
	wsPriorityCh := n.wsPriorityCh
	isRelay := n.isRelayConnected
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	if aead == nil || (len(n.Peers) == 0 && !isRelay) {
		n.mu.RUnlock()
		return
	}

	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		n.mu.RUnlock()
		return
	}

	// 1. Forward via high-priority WebSocket Relay
	if isRelay && wsPriorityCh != nil {
		select {
		case wsPriorityCh <- data:
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

// broadcastVideoPacket routes screen share chunks through the paced video channel with bounded queue
func (n *P2PNode) broadcastVideoPacket(pkt *P2PPacket) {
	n.mu.RLock()
	aead := n.aead
	wsVideoCh := n.wsVideoCh
	isRelay := n.isRelayConnected
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	if aead == nil || (len(n.Peers) == 0 && !isRelay) {
		n.mu.RUnlock()
		return
	}

	data, err := encodeAndEncryptPacket(pkt, aead)
	if err != nil {
		n.mu.RUnlock()
		return
	}

	// 1. Guaranteed delivery to all room members via WebSocket Relay
	if isRelay && wsVideoCh != nil {
		select {
		case wsVideoCh <- data:
		default:
			// If buffer has more than 8 chunks, drop oldest chunks immediately to prevent queue buildup
			for len(wsVideoCh) > 8 {
				select {
				case <-wsVideoCh:
				default:
					break
				}
			}
			select {
			case wsVideoCh <- data:
			default:
			}
		}
	}

	// 2. Also send via direct UDP to known peer addresses
	for _, peer := range n.Peers {
		if peer.Addr != nil && n.Conn != nil {
			n.Conn.WriteToUDP(data, peer.Addr)
		}
	}
	n.mu.RUnlock()
}

func (n *P2PNode) listenLoop() {
	n.mu.RLock()
	c := n.Conn
	n.mu.RUnlock()
	if c != nil {
		n.listenLoopOnConn(c)
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

		n.mu.RLock()
		aead := n.aead
		prevAead := n.prevAead
		active := n.IsConnected || n.Connecting
		n.mu.RUnlock()

		if !active || aead == nil {
			continue
		}

		var pkt P2PPacket
		if err := decryptAndDecodePacket(buf[:readBytes], &pkt, aead); err != nil {
			if prevAead != nil {
				if err2 := decryptAndDecodePacket(buf[:readBytes], &pkt, prevAead); err2 != nil {
					continue
				}
			} else {
				continue
			}
		}

		if pkt.Type == PacketScreenShareData {
			n.forwardVideoChunk(pkt.SenderID, pkt.Payload, pkt.Seq, pkt.Nickname)
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
		prevAead := n.prevAead
		active := n.IsConnected || n.Connecting
		n.mu.RUnlock()

		if !active || aead == nil {
			continue
		}

		var pkt P2PPacket
		if err := decryptAndDecodePacket(buf[:readBytes], &pkt, aead); err != nil {
			if prevAead != nil {
				if err2 := decryptAndDecodePacket(buf[:readBytes], &pkt, prevAead); err2 != nil {
					continue
				}
			} else {
				continue
			}
		}

		if pkt.Type == PacketScreenShareData {
			n.forwardVideoChunk(pkt.SenderID, pkt.Payload, pkt.Seq, pkt.Nickname)
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

	// Only process if matching room (case-insensitive)
	if NormalizeCode(pkt.RoomCode) != NormalizeCode(targetRoom) || (!n.IsConnected && !n.Connecting) {
		return
	}

	// Verify packet timestamp freshness and deduplication for state control packets to prevent Replay Attacks
	switch pkt.Type {
	case PacketLeave, PacketRoomLocked, PacketRoomFull, PacketScreenShareStart, PacketScreenShareStop, PacketPortHop, PacketJoinRequest, PacketMuteState:
		if pkt.Timestamp > 0 {
			nowMs := time.Now().UnixMilli()
			diff := nowMs - pkt.Timestamp
			if diff < -15000 || diff > 30000 { // Allow 15s future clock skew, 30s past delay
				return // Drop stale or replayed packet!
			}
		}
		if !n.ctrlDedup.ShouldProcess(pkt.SenderID, pkt.Type, pkt.Seq, pkt.Timestamp) {
			return // Drop duplicate replayed packet!
		}
	}

	var peerAddr *net.UDPAddr
	if raddr != nil {
		peerPort := raddr.Port
		if pkt.LocalPort > 0 {
			peerPort = pkt.LocalPort
		}
		peerAddr = &net.UDPAddr{IP: raddr.IP, Port: peerPort}
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
						RoomCode:  n.RoomCode,
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
					var lastDirect time.Time
					isRelayed := (raddr == nil && peerAddr == nil)
					if raddr != nil {
						lastDirect = time.Now()
					}
					peer = &PeerInfo{
						ID:             pkt.SenderID,
						Nickname:       nick,
						Addr:           peerAddr,
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
					go n.sendPingToPeer(peer)
				}
			} else {
				if peerAddr != nil {
					peer.Addr = peerAddr
				}
				peer.LastSeen = time.Now()
				if raddr != nil {
					peer.LastDirectSeen = time.Now()
					peer.ViaRelay = false
				} else if peer.Addr == nil || time.Since(peer.LastDirectSeen) > 3*time.Second {
					peer.ViaRelay = true
				}
				if pkt.Nickname != "" {
					peer.Nickname = pkt.Nickname
				}
				if pkt.LocalPort > 0 {
					peer.LocalPort = pkt.LocalPort
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

		// Check Room Lock & PIN Protection
		if n.IsLocked {
			if n.RoomPIN != "" && strings.TrimSpace(pkt.PIN) != n.RoomPIN {
				lockPkt := P2PPacket{
					Type:      PacketRoomLocked,
					RoomCode:  n.RoomCode,
					SenderID:  n.LocalID,
					Nickname:  n.Nickname,
					Payload:   []byte("PIN_REQUIRED"),
					Timestamp: time.Now().UnixMilli(),
				}
				if peerAddr != nil {
					go n.sendDirectUDPPacket(peerAddr, &lockPkt)
				}
				return
			} else if n.RoomPIN == "" {
				lockPkt := P2PPacket{
					Type:      PacketRoomLocked,
					RoomCode:  n.RoomCode,
					SenderID:  n.LocalID,
					Nickname:  n.Nickname,
					Payload:   []byte("ROOM_LOCKED"),
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
				RoomCode:  n.RoomCode,
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

		// Reply with Welcome containing current peers, PIN, and lock status
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
					LocalPort:  p.LocalPort,
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
			LocalPort:  n.Port,
			IsMuted:    n.audio.Muted,
			IsDeafened: n.audio.Deafened,
			Peers:      summaries,
			PIN:        n.RoomPIN,
			IsLocked:   n.IsLocked,
			Timestamp:  time.Now().UnixMilli(),
		}
		if peerAddr != nil {
			go n.sendDirectUDPPacket(peerAddr, &welcomePkt)
		}

	case PacketHello:
		// If we're a Joiner still searching and we see a Hello from an active host
		// in our target room → send a JoinRequest DIRECTLY to that host (unicast, bypasses broadcast issues)
		if n.Connecting && !n.IsConnected {
			room := n.ConnectTargetRoom
			pin := n.RoomPIN
			if NormalizeCode(pkt.RoomCode) == room {
				joinPkt := P2PPacket{
					Type:       PacketJoinRequest,
					RoomCode:   room,
					SenderID:   n.LocalID,
					Nickname:   n.Nickname,
					LocalPort:  n.Port,
					IsMuted:    n.audio.Muted,
					IsDeafened: n.audio.Deafened,
					PIN:        pin,
					Timestamp:  time.Now().UnixMilli(),
				}
				if peerAddr != nil {
					go n.sendDirectUDPPacket(peerAddr, &joinPkt)
				}
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
					LocalPort:  p.LocalPort,
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
			LocalPort:  n.Port,
			IsMuted:    n.audio.Muted,
			IsDeafened: n.audio.Deafened,
			Peers:      summaries,
			PIN:        n.RoomPIN,
			IsLocked:   n.IsLocked,
			Timestamp:  time.Now().UnixMilli(),
		}
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
			if pkt.PIN != "" {
				n.RoomPIN = pkt.PIN
				n.IsLocked = true
			} else if pkt.IsLocked {
				n.IsLocked = true
			}

			var lastDirect time.Time
			isRelayed := (raddr == nil && peerAddr == nil)
			if raddr != nil {
				lastDirect = time.Now()
			}
			hostPeer := &PeerInfo{
				ID:             pkt.SenderID,
				Nickname:       pkt.Nickname,
				Addr:           peerAddr,
				LocalPort:      pkt.LocalPort,
				LastSeen:       time.Now(),
				LastDirectSeen: lastDirect,
				ViaRelay:       isRelayed,
				IsMuted:        pkt.IsMuted,
				IsDeafened:     pkt.IsDeafened,
			}
			n.Peers[pkt.SenderID] = hostPeer
			go n.sendPingToPeer(hostPeer)
			n.log(fmt.Sprintf("[+] Connected to room %s (Host: %s | E2EE Secure)", n.RoomCode, pkt.Nickname))

			// Connect to other peers reported in Welcome packet (mesh topology)
			for _, pSum := range pkt.Peers {
				if pSum.ID != n.LocalID && n.Peers[pSum.ID] == nil && len(n.Peers) < MaxPeers-1 {
					var pAddr *net.UDPAddr
					if pSum.AddrStr != "" {
						pAddr, _ = net.ResolveUDPAddr("udp4", pSum.AddrStr)
					} else if peerAddr != nil && pSum.LocalPort > 0 {
						pAddr = &net.UDPAddr{IP: peerAddr.IP, Port: pSum.LocalPort}
					}
					var pDirect time.Time
					if pAddr != nil {
						pDirect = time.Now()
					}
					newPeer := &PeerInfo{
						ID:             pSum.ID,
						Nickname:       pSum.Nickname,
						Addr:           pAddr,
						LocalPort:      pSum.LocalPort,
						LastSeen:       time.Now(),
						LastDirectSeen: pDirect,
						ViaRelay:       (pAddr == nil),
						IsMuted:        pSum.IsMuted,
						IsDeafened:     pSum.IsDeafened,
					}
					n.Peers[pSum.ID] = newPeer
					if pAddr != nil {
						helloPkt := P2PPacket{
							Type:       PacketHello,
							RoomCode:   n.RoomCode,
							SenderID:   n.LocalID,
							Nickname:   n.Nickname,
							LocalPort:  n.Port,
							IsMuted:    n.audio.Muted,
							IsDeafened: n.audio.Deafened,
							Timestamp:  time.Now().UnixMilli(),
						}
						go n.sendDirectUDPPacket(pAddr, &helloPkt)
					}
				}
			}

			successCb := n.OnJoinSuccess
			if successCb != nil {
				go successCb(pkt.Nickname)
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
			n.log("❌ Room join rejected: Room full (Max 4 people).")
			if failedCb != nil {
				go failedCb("This room is full! (Maximum 4 people)")
			}
		}

	case PacketPing:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.IsSharingScreen = pkt.IsSharingScreen
			peer.VideoPort = pkt.VideoPort
		}
		// Dedup incoming ping: if we already received and answered this exact ping seq/timestamp, don't echo again
		if pkt.Timestamp > 0 && !n.ctrlDedup.ShouldProcess(pkt.SenderID, PacketPing, pkt.Seq, pkt.Timestamp) {
			return
		}
		var destAddr *net.UDPAddr = raddr
		if destAddr == nil {
			if peer, ok := n.Peers[pkt.SenderID]; ok {
				destAddr = peer.Addr
			}
		}
		var isMuted, isDeafened bool
		if n.audio != nil {
			isMuted = n.audio.Muted
			isDeafened = n.audio.Deafened
		}
		pong := P2PPacket{
			Type:            PacketPong,
			RoomCode:        n.RoomCode,
			SenderID:        n.LocalID,
			Nickname:        n.Nickname,
			Seq:             pkt.Seq,
			IsMuted:         isMuted,
			IsDeafened:      isDeafened,
			IsSharingScreen: n.IsSharingScreen,
			VideoPort:       n.ScreenSharePort,
			Timestamp:       pkt.Timestamp, // Echo timestamp
		}
		go n.sendPacketTo(destAddr, &pong)

	case PacketPong:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.IsSharingScreen = pkt.IsSharingScreen
			peer.VideoPort = pkt.VideoPort

			if raddr != nil {
				peer.LastDirectSeen = time.Now()
				peer.ViaRelay = false
			} else if peer.Addr == nil || time.Since(peer.LastDirectSeen) > 3*time.Second {
				peer.ViaRelay = true
			}

			// Dedup incoming pong: only accept the earliest/fastest pong for this ping sequence.
			// Drops delayed redundant copies arriving from alternate transport (e.g. WebSocket Relay vs Direct UDP).
			if pkt.Timestamp > 0 && !n.ctrlDedup.ShouldProcess(pkt.SenderID, PacketPong, pkt.Seq, pkt.Timestamp) {
				return
			}

			nowMs := time.Now().UnixMilli()
			rtt := nowMs - pkt.Timestamp
			if rtt <= 0 {
				rtt = 1
			}
			if rtt < 3000 {
				if peer.PingMs <= 0 {
					peer.PingMs = rtt
				} else {
					// Exponential Moving Average (EMA) with 70% history, 30% new sample
					// Eliminates sudden jitter spikes while keeping display smooth and responsive
					peer.PingMs = int64(math.Round(float64(peer.PingMs)*0.70 + float64(rtt)*0.30))
					if peer.PingMs <= 0 {
						peer.PingMs = 1
					}
				}
			}
		}

	case PacketAudio:
		if !n.audioDedup.ShouldProcess(pkt.SenderID, pkt.Seq) {
			return // Ignore duplicate packet received over redundant transport
		}
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.Speaking = pkt.Speaking
			peer.RMS = pkt.RMS
			n.audio.PlayPeerPCM(pkt.SenderID, pkt.Payload, pkt.RMS, pkt.Speaking)
		}

	case PacketMuteState:
		// handled by auto-register / refresh at top

	case PacketScreenShareStart:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.IsSharingScreen = true
			peer.VideoPort = pkt.VideoPort
			n.log(fmt.Sprintf("[SCREEN] %s started screen sharing (Port: %d)", peer.Nickname, pkt.VideoPort))
			if n.OnScreenShare != nil {
				go n.OnScreenShare(pkt.SenderID, true, pkt.VideoPort)
			}
		}

	case PacketScreenShareStop:
		if peer, exists := n.Peers[pkt.SenderID]; exists {
			peer.IsSharingScreen = false
			peer.VideoPort = 0
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

	case PacketScreenShareData:
		go n.forwardVideoChunk(pkt.SenderID, pkt.Payload, pkt.Seq, pkt.Nickname)

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
					peer.Addr = &net.UDPAddr{IP: raddr.IP, Port: pkt.LocalPort}
				} else if peer.Addr != nil {
					peer.Addr = &net.UDPAddr{IP: peer.Addr.IP, Port: pkt.LocalPort}
				}
			}
			peer.LastSeen = time.Now()
			n.log(fmt.Sprintf("[SECURITY] Peer %s rotated endpoint to port :%d (Epoch %d)", pkt.Nickname, pkt.LocalPort, pkt.Seq))

			// Immediately respond with a PacketPong so NAT hole-punching succeeds bidirectionally
			if peer.Addr != nil {
				pong := P2PPacket{
					Type:            PacketPong,
					RoomCode:        n.RoomCode,
					SenderID:        n.LocalID,
					Nickname:        n.Nickname,
					IsMuted:         n.audio.Muted,
					IsDeafened:      n.audio.Deafened,
					IsSharingScreen: n.IsSharingScreen,
					VideoPort:       n.ScreenSharePort,
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
			if n.connectCancel != nil {
				close(n.connectCancel)
				n.connectCancel = nil
			}
			n.Connecting = false
			n.aead = nil
			n.RoomCode = ""
			n.RoomPIN = ""
			failedCb := n.OnJoinFailed
			n.log(fmt.Sprintf("[SECURITY] %s", reason))
			if failedCb != nil {
				go failedCb(reason)
			}
		}

	case PacketFileHeader:
		meta := pkt.FileMeta
		if meta != nil && meta.TransferID != "" {
			// DoS Protection: Size and chunk count limits
			if meta.FileSize <= 0 || meta.FileSize > MaxFileTransferSize || meta.TotalChunks <= 0 || meta.TotalChunks > MaxFileChunks {
				n.log(fmt.Sprintf("[SECURITY] Rejected file transfer %s: size %d bytes or %d chunks exceeds limit", meta.TransferID, meta.FileSize, meta.TotalChunks))
				return
			}

			if n.incomingTransfers == nil {
				n.incomingTransfers = make(map[string]*IncomingFileTransfer)
			}

			// Clean expired transfers (TTL cleanup)
			now := time.Now()
			for id, tr := range n.incomingTransfers {
				if now.Sub(tr.StartTime) > TransferExpiryDuration {
					delete(n.incomingTransfers, id)
				}
			}

			// Limit concurrent active transfers
			if len(n.incomingTransfers) >= MaxConcurrentTransfers {
				n.log("[SECURITY] Rejected file transfer: max concurrent incoming transfers reached")
				return
			}

			safeName := sanitizeFilename(meta.FileName, meta.IsCode)
			transfer := &IncomingFileTransfer{
				TransferID:  meta.TransferID,
				FileName:    safeName,
				FileSize:    meta.FileSize,
				TotalChunks: meta.TotalChunks,
				Chunks:      make(map[int][]byte),
				StartTime:   time.Now(),
				IsCode:      meta.IsCode,
				Checksum:    meta.Checksum,
			}
			n.incomingTransfers[meta.TransferID] = transfer
			if n.OnFileTransferProgress != nil {
				go n.OnFileTransferProgress(meta.TransferID, safeName, 0, meta.FileSize, 0, false, false, nil)
			}
		}

	case PacketFileChunk:
		meta := pkt.FileMeta
		if meta != nil && meta.TransferID != "" {
			if n.incomingTransfers == nil {
				return
			}
			transfer, exists := n.incomingTransfers[meta.TransferID]
			if !exists {
				return // Reject chunks for non-existent or expired transfers
			}

			// Validate chunk index bounds
			if meta.ChunkIndex < 0 || meta.ChunkIndex >= transfer.TotalChunks {
				return
			}

			// Reject oversized individual chunk payload (> 64KB)
			if len(pkt.Payload) > 65536 {
				return
			}

			if _, already := transfer.Chunks[meta.ChunkIndex]; !already {
				transfer.Chunks[meta.ChunkIndex] = pkt.Payload
				transfer.Received += int64(len(pkt.Payload))
			}

			elapsed := time.Since(transfer.StartTime).Seconds()
			var speed float64
			if elapsed > 0.05 {
				speed = float64(transfer.Received) / elapsed
			}

			isDone := len(transfer.Chunks) >= transfer.TotalChunks || transfer.Received >= transfer.FileSize
			if isDone {
				// Extract transfer data under lock, then delete from map
				delete(n.incomingTransfers, meta.TransferID)
				totalChunks := transfer.TotalChunks
				chunksCopy := transfer.Chunks
				checksum := transfer.Checksum
				fileName := transfer.FileName
				transferID := meta.TransferID
				isCode := transfer.IsCode
				senderID := pkt.SenderID
				senderNick := pkt.Nickname
				fileSize := transfer.FileSize
				received := transfer.Received

				// Release lock before assembling and verifying checksum in background to prevent audio/ping glitch!
				go func() {
					var assembled bytes.Buffer
					for idx := 0; idx < totalChunks; idx++ {
						if chunk, ok := chunksCopy[idx]; ok {
							assembled.Write(chunk)
						}
					}
					fullData := assembled.Bytes()

					// 1. Verify Checksum
					if checksum != "" {
						sum := sha256.Sum256(fullData)
						actualChecksum := hex.EncodeToString(sum[:])
						if actualChecksum != checksum {
							if n.OnFileTransferProgress != nil {
								n.OnFileTransferProgress(transferID, fileName, received, fileSize, 0, false, true, fmt.Errorf("checksum mismatch (corrupted or tampered)"))
							}
							return
						}
					}

					safeName := sanitizeFilename(fileName, isCode)
					offer := &FileOffer{
						TransferID: transferID,
						SenderID:   senderID,
						SenderNick: senderNick,
						FileName:   safeName,
						FileSize:   int64(len(fullData)),
						IsCode:     isCode,
						Checksum:   checksum,
						Data:       fullData,
					}

					if n.OnFileOfferReceived != nil {
						n.OnFileOfferReceived(offer)
					} else if n.OnFileReceived != nil {
						savedPath, err := SaveAcceptedFile(offer)
						if err == nil {
							n.OnFileReceived(transferID, safeName, savedPath, isCode, string(fullData))
						}
					}

					if n.OnFileTransferProgress != nil {
						n.OnFileTransferProgress(transferID, safeName, int64(len(fullData)), fileSize, speed, false, true, nil)
					}
				}()
				return
			}

			if n.OnFileTransferProgress != nil {
				go n.OnFileTransferProgress(meta.TransferID, transfer.FileName, transfer.Received, transfer.FileSize, speed, false, isDone, nil)
			}
		}

	case PacketFileAck:
		// Transfer acknowledgment received
	}
}

func (n *P2PNode) forwardVideoChunk(senderID string, payload []byte, seq uint32, nickname string) {
	if len(payload) == 0 {
		return
	}

	n.mu.Lock()
	if peer, exists := n.Peers[senderID]; exists {
		peer.LastSeen = time.Now()
		peer.IsSharingScreen = true
	} else if senderID != "" {
		for _, p := range n.Peers {
			if p.Nickname == nickname {
				p.LastSeen = time.Now()
				p.IsSharingScreen = true
			}
		}
	}

	watching := n.IsWatchingScreen
	watchingPeerID := n.WatchingPeerID

	// If we are watching a specific peer, ignore stream packets from other broadcasters
	if watching && watchingPeerID != "" && senderID != "" && senderID != watchingPeerID {
		n.mu.Unlock()
		return
	}

	n.lastVideoChunkTime = time.Now()
	tcpConn := n.videoTCPConn
	n.mu.Unlock()

	readyChunks := n.videoReorder.Push(seq, payload)
	if len(readyChunks) == 0 {
		return
	}

	n.mu.Lock()
	// Keep prebuffer of recent video chunks (~30 chunks = ~35KB) so player gets headers immediately on connect
	// Zero-allocation ring buffer: reuses slice backing array instead of append(slice[1:], chunk)
	for _, chunk := range readyChunks {
		if len(chunk) > 0 {
			if len(n.videoPreBuf) < 30 {
				n.videoPreBuf = append(n.videoPreBuf, chunk)
			} else {
				copy(n.videoPreBuf, n.videoPreBuf[1:])
				n.videoPreBuf[len(n.videoPreBuf)-1] = chunk
			}
		}
	}
	tcpConn = n.videoTCPConn
	watching = n.IsWatchingScreen
	n.mu.Unlock()

	if watching && tcpConn != nil {
		for _, chunk := range readyChunks {
			if len(chunk) > 0 {
				if _, err := tcpConn.Write(chunk); err != nil {
					n.mu.Lock()
					if n.videoTCPConn == tcpConn {
						n.videoTCPConn = nil
						_ = tcpConn.Close()
						n.debugLog(fmt.Sprintf("⚠️ [WATCH] Player TCP disconnected: %v", err))
					}
					n.mu.Unlock()
					return
				}
			}
		}
	}
}

// StartScreenShare starts capturing screen via ffmpeg/gpu-screen-recorder and streams MPEG-TS chunks over Internet E2EE
func (n *P2PNode) StartScreenShare(targetIP string, targetPort int, customOpts ...screenshare.BroadcastOptions) error {
	n.mu.Lock()
	if !n.IsConnected {
		n.mu.Unlock()
		return errors.New("cannot share screen while disconnected")
	}
	if n.IsSharingScreen && n.screenSession != nil {
		n.mu.Unlock()
		return nil
	}
	n.mu.Unlock()

	// 1. Listen on a dynamic free local UDP port (port 0 = OS assigns available port)
	captureAddr, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	captureConn, err := net.ListenUDP("udp4", captureAddr)
	if err != nil {
		return fmt.Errorf("failed to open screen capture port: %w", err)
	}
	_ = captureConn.SetReadBuffer(4 * 1024 * 1024)
	_ = captureConn.SetWriteBuffer(4 * 1024 * 1024)
	localAssignedPort := captureConn.LocalAddr().(*net.UDPAddr).Port

	opts := screenshare.DefaultBroadcastOptions()
	if len(customOpts) > 0 {
		opts = customOpts[0]
	}
	session, err := screenshare.StartBroadcasting(context.Background(), "127.0.0.1", localAssignedPort, opts)
	if err != nil {
		_ = captureConn.Close()
		return err
	}

	n.mu.Lock()
	n.ScreenSharePort = localAssignedPort
	n.screenSession = session
	n.videoCaptureConn = captureConn
	n.IsSharingScreen = true
	roomCode := n.RoomCode
	localID := n.LocalID
	nickname := n.Nickname
	n.mu.Unlock()

	startPkt := P2PPacket{
		Type:            PacketScreenShareStart,
		RoomCode:        roomCode,
		SenderID:        localID,
		Nickname:        nickname,
		IsSharingScreen: true,
		VideoPort:       localAssignedPort,
	}
	n.broadcastToPeers(&startPkt)
	n.log("[SCREEN] Screen share started (1080p 60 FPS - Internet)")

	// 2. Read raw MPEG-TS video chunks and broadcast to all room peers over WebSocket Relay (Internet)
	go func() {
		buf := make([]byte, 65535)
		var seq uint32
		var totalPackets int
		var totalBytes int64

		for {
			nBytes, _, err := captureConn.ReadFromUDP(buf)
			if err != nil || nBytes <= 0 {
				n.debugLog(fmt.Sprintf("[WARN] [SHARE] UDP capture read ended: %v", err))
				break
			}

			n.mu.RLock()
			sharing := n.IsSharingScreen
			currentRoom := n.RoomCode
			n.mu.RUnlock()

			if !sharing {
				break
			}

			seq++
			if seq == 0 {
				seq = 1
			}

			totalPackets++
			totalBytes += int64(nBytes)
			if totalPackets == 1 {
				n.debugLog(fmt.Sprintf("[SCREEN] [SHARE] First video chunk captured (%d bytes)! Broadcasting...", nBytes))
			} else if totalPackets%120 == 0 {
				n.debugLog(fmt.Sprintf("[SCREEN] [SHARE] Stream active: %d chunks (%d KB) sent", totalPackets, totalBytes/1024))
			}

			chunk := make([]byte, nBytes)
			copy(chunk, buf[:nBytes])

			vidPkt := P2PPacket{
				Type:     PacketScreenShareData,
				RoomCode: currentRoom,
				SenderID: localID,
				Nickname: nickname,
				Seq:      seq,
				Payload:  chunk,
			}
			n.broadcastVideoPacket(&vidPkt)
		}
	}()

	// 3. Monitor session lifecycle
	go func() {
		select {
		case err := <-session.Err():
			n.log(fmt.Sprintf("[WARN] Screen stream closed: %v", err))
		case <-session.Done():
			n.log("[INFO] Screen stream ended.")
		}

		_ = captureConn.Close()

		n.mu.Lock()
		n.IsSharingScreen = false
		n.screenSession = nil
		n.videoCaptureConn = nil
		n.mu.Unlock()

		stopPkt := P2PPacket{
			Type:            PacketScreenShareStop,
			RoomCode:        roomCode,
			SenderID:        localID,
			Nickname:        nickname,
			IsSharingScreen: false,
			VideoPort:       0,
		}
		n.broadcastToPeers(&stopPkt)
	}()

	return nil
}

// StopScreenShare stops active broadcasting
func (n *P2PNode) StopScreenShare() error {
	n.mu.Lock()
	if !n.IsSharingScreen || n.screenSession == nil {
		n.mu.Unlock()
		return nil
	}

	session := n.screenSession
	conn := n.videoCaptureConn
	n.screenSession = nil
	n.videoCaptureConn = nil
	n.IsSharingScreen = false
	roomCode := n.RoomCode
	localID := n.LocalID
	nickname := n.Nickname
	n.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	if session != nil {
		_ = session.Stop()
	}

	stopPkt := P2PPacket{
		Type:            PacketScreenShareStop,
		RoomCode:        roomCode,
		SenderID:        localID,
		Nickname:        nickname,
		IsSharingScreen: false,
		VideoPort:       0,
	}
	n.broadcastToPeers(&stopPkt)
	n.log("[SCREEN] Screen share stopped.")
	return nil
}

// StartWatchingScreen launches native hardware-accelerated video receiver (mpv/ffplay) and feeds it decrypted stream chunks over dynamic local TCP server
func (n *P2PNode) StartWatchingScreen(peerID string, port int, opts ...screenshare.ReceiverOptions) error {
	n.mu.Lock()
	if n.receiverSession != nil {
		prevSession := n.receiverSession
		prevLn := n.videoTCPListener
		prevConn := n.videoTCPConn
		n.receiverSession = nil
		n.videoTCPListener = nil
		n.videoTCPConn = nil
		n.mu.Unlock()
		if prevConn != nil {
			_ = prevConn.Close()
		}
		if prevLn != nil {
			_ = prevLn.Close()
		}
		_ = prevSession.Stop()
		n.mu.Lock()
	}

	n.WatchingPeerID = peerID
	if p, ok := n.Peers[peerID]; ok {
		n.WatchingPeerNick = p.Nickname
	} else {
		n.WatchingPeerNick = ""
	}

	var opt screenshare.ReceiverOptions
	if len(opts) > 0 {
		opt = opts[0]
	} else {
		opt = screenshare.DefaultReceiverOptions()
	}
	n.videoReorder.Reset()
	n.videoPreBuf = nil
	n.mu.Unlock()

	// 1. Open a local TCP listener on a dynamic free port (127.0.0.1:0)
	tcpLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to open local TCP player socket: %w", err)
	}
	assignedTCPPort := tcpLn.Addr().(*net.TCPAddr).Port

	n.mu.Lock()
	n.IsWatchingScreen = true
	n.videoTCPListener = tcpLn
	n.lastVideoChunkTime = time.Now()
	n.mu.Unlock()

	// 2. Start accepting incoming TCP connection from player in background immediately
	go func() {
		conn, err := tcpLn.Accept()
		if err != nil {
			n.debugLog(fmt.Sprintf("[WARN] [WATCH] Player TCP accept error: %v", err))
			return
		}
		n.debugLog(fmt.Sprintf("[VIEWER] [WATCH] Player connected to internal TCP port %d", assignedTCPPort))
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetNoDelay(true)
			_ = tcp.SetWriteBuffer(4 * 1024 * 1024)
			_ = tcp.SetReadBuffer(4 * 1024 * 1024)
		}
		n.mu.Lock()
		if n.IsWatchingScreen {
			n.videoTCPConn = conn
			// Immediately flush pre-buffered chunks so player receives sync frames instantly
			for _, chunk := range n.videoPreBuf {
				if len(chunk) > 0 {
					_, _ = conn.Write(chunk)
				}
			}
		} else {
			_ = conn.Close()
		}
		n.mu.Unlock()
	}()

	// 3. Start MPV connecting to tcp://127.0.0.1:assignedTCPPort
	session, err := screenshare.StartReceiving(context.Background(), assignedTCPPort, opt)
	if err != nil {
		n.mu.Lock()
		n.IsWatchingScreen = false
		n.WatchingPeerID = ""
		n.WatchingPeerNick = ""
		n.videoTCPListener = nil
		n.mu.Unlock()
		_ = tcpLn.Close()
		return err
	}

	n.mu.Lock()
	n.receiverSession = session
	n.mu.Unlock()

	n.log("[VIEWER] Live screen stream viewer window opened (HD 60 FPS).")

	// 4. Monitor receiver session lifecycle
	go func(curSession *screenshare.Session) {
		select {
		case err := <-curSession.Err():
			n.log(fmt.Sprintf("[WARN] Screen viewer closed/error: %v", err))
		case <-curSession.Done():
			n.log("[INFO] Screen viewer window closed.")
		}

		n.mu.Lock()
		if n.receiverSession == curSession {
			n.IsWatchingScreen = false
			n.WatchingPeerID = ""
			n.WatchingPeerNick = ""
			n.receiverSession = nil
			if n.videoTCPConn != nil {
				_ = n.videoTCPConn.Close()
				n.videoTCPConn = nil
			}
			if n.videoTCPListener != nil {
				_ = n.videoTCPListener.Close()
				n.videoTCPListener = nil
			}
		}
		n.mu.Unlock()
	}(session)

	return nil
}

// StopWatchingScreen stops the active mpv receiver
func (n *P2PNode) StopWatchingScreen() error {
	n.mu.Lock()
	session := n.receiverSession
	conn := n.videoTCPConn
	ln := n.videoTCPListener
	n.receiverSession = nil
	n.videoTCPConn = nil
	n.videoTCPListener = nil
	n.IsWatchingScreen = false
	n.WatchingPeerID = ""
	n.WatchingPeerNick = ""
	n.lastVideoChunkTime = time.Time{}
	n.videoPreBuf = nil
	n.videoReorder.Reset()
	n.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if session != nil {
		_ = session.Stop()
	}

	n.log("[VIEWER] Screen viewer closed.")
	return nil
}

// GetPeer returns a peer info pointer by peer ID (or nil)
func (n *P2PNode) GetPeer(id string) *PeerInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if p, ok := n.Peers[id]; ok {
		return p
	}
	return nil
}

func (n *P2PNode) heartbeatLoop() {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-n.stopChan:
			return
		case <-ticker.C:
		}

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

		// Check timeouts & send pings (45s timeout for resilient internet connections)
		for id, peer := range n.Peers {
			if now.Sub(peer.LastSeen) > 45*time.Second {
				wasSharing := peer.IsSharingScreen
				delete(n.Peers, id)
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

			go n.sendPingToPeer(peer)
		}

		// If all peers disconnected, stop watching
		if n.IsWatchingScreen && len(n.Peers) == 0 {
			go func() {
				_ = n.StopWatchingScreen()
			}()
		}
		n.mu.Unlock()
	}
}

// sendPingToPeer transmits a high-priority latency measurement packet to a specific peer
func (n *P2PNode) sendPingToPeer(peer *PeerInfo) {
	if peer == nil {
		return
	}
	n.mu.RLock()
	if !n.IsConnected {
		n.mu.RUnlock()
		return
	}
	roomCode := n.RoomCode
	localID := n.LocalID
	nickname := n.Nickname
	muted := false
	deafened := false
	if n.audio != nil {
		muted = n.audio.Muted
		deafened = n.audio.Deafened
	}
	sharing := n.IsSharingScreen
	videoPort := n.ScreenSharePort
	peerAddr := peer.Addr
	n.mu.RUnlock()

	pingSeq := atomic.AddUint32(&n.pingSeq, 1)
	pingPkt := P2PPacket{
		Type:            PacketPing,
		RoomCode:        roomCode,
		SenderID:        localID,
		Nickname:        nickname,
		Seq:             pingSeq,
		IsMuted:         muted,
		IsDeafened:      deafened,
		IsSharingScreen: sharing,
		VideoPort:       videoPort,
		Timestamp:       time.Now().UnixMilli(),
	}
	n.sendPacketTo(peerAddr, &pingPkt)
}

func (n *P2PNode) GetPeersList() []*PeerInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()

	list := make([]*PeerInfo, 0, len(n.Peers))
	for _, p := range n.Peers {
		list = append(list, p)
	}
	// Deterministic sorting by ID to eliminate position flickering across render cycles
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list
}

func (n *P2PNode) writeToFileLog(msg string) {
	if f, err := os.OpenFile("limoni-voice.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
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

// NextHopRemaining returns the duration remaining until the next scheduled port hop
func (n *P2PNode) NextHopRemaining() time.Duration {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.nextHopTime.IsZero() {
		return 0
	}
	rem := time.Until(n.nextHopTime)
	if rem < 0 {
		return 0
	}
	return rem
}

// RotatePort dynamically binds a new random UDP socket, announces the new port to peers,
// and gracefully switches over with zero packet loss.
func (n *P2PNode) RotatePort() error {
	n.mu.Lock()
	if !n.IsConnected && !n.Connecting {
		n.mu.Unlock()
		return errors.New("node is not in a room")
	}
	currentPort := n.Port
	oldConn := n.Conn
	roomCode := n.RoomCode
	senderID := n.LocalID
	nickname := n.Nickname
	roomKey := n.RoomKey
	aead := n.aead
	n.currentEpoch++
	newEpoch := n.currentEpoch
	n.mu.Unlock()

	if aead == nil && len(roomKey) > 0 {
		var err error
		aead, err = deriveRoomCipher(roomKey)
		if err != nil {
			return err
		}
		n.mu.Lock()
		n.aead = aead
		n.mu.Unlock()
	}

	// 1. Find and bind a new random UDP port (50000-59999)
	var newConn *net.UDPConn
	var newPort int
	for attempt := 0; attempt < 25; attempt++ {
		var randBytes [2]byte
		_, _ = rand.Read(randBytes[:])
		p := 50000 + (int(binary.BigEndian.Uint16(randBytes[:])) % 10000)
		if p == currentPort {
			continue
		}
		laddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("0.0.0.0:%d", p))
		if err == nil {
			c, err := net.ListenUDP("udp4", laddr)
			if err == nil {
				newConn = c
				newPort = p
				break
			}
		}
	}

	if newConn == nil {
		laddr, err := net.ResolveUDPAddr("udp4", "0.0.0.0:0")
		if err != nil {
			return err
		}
		c, err := net.ListenUDP("udp4", laddr)
		if err != nil {
			return err
		}
		newConn = c
		newPort = newConn.LocalAddr().(*net.UDPAddr).Port
	}

	_ = newConn.SetReadBuffer(4 * 1024 * 1024)
	_ = newConn.SetWriteBuffer(4 * 1024 * 1024)

	n.mu.Lock()
	n.Conn = newConn
	n.Port = newPort
	n.lastHopTime = time.Now()
	n.nextHopTime = time.Now().Add(n.hopInterval)
	peers := make([]*PeerInfo, 0, len(n.Peers))
	for _, p := range n.Peers {
		peers = append(peers, p)
	}
	onHopCb := n.OnPortHopped
	n.mu.Unlock()

	// 2. Start listener goroutine on the new UDP socket
	go n.listenLoopOnConn(newConn)

	// 3. Send PacketPortHop announcement to all connected peers
	hopPkt := P2PPacket{
		Type:      PacketPortHop,
		RoomCode:  roomCode,
		SenderID:  senderID,
		Nickname:  nickname,
		LocalPort: newPort,
		Seq:       newEpoch,
		Timestamp: time.Now().UnixMilli(),
	}

	go func() {
		// Send via new UDP to all known peer addresses (multiple bursts for UDP reliability)
		for _, peer := range peers {
			if peer.Addr != nil {
				for burst := 0; burst < 3; burst++ {
					go n.sendDirectUDPPacket(peer.Addr, &hopPkt)
					if oldConn != nil {
						data, err := encodeAndEncryptPacket(&hopPkt, aead)
						if err == nil {
							_, _ = oldConn.WriteToUDP(data, peer.Addr)
						}
					}
					time.Sleep(30 * time.Millisecond)
				}
			}
		}
		// Send via WebSocket Relay
		n.mu.RLock()
		wsCh := n.wsPriorityCh
		isRelay := n.isRelayConnected
		n.mu.RUnlock()
		if isRelay && wsCh != nil {
			data, err := encodeAndEncryptPacket(&hopPkt, aead)
			if err == nil {
				select {
				case wsCh <- data:
				default:
				}
			}
		}
		// Update relay server of new local port
		n.sendRelayControl(RelayControlMessage{
			Type:     "port_update",
			RoomCode: roomCode,
			SenderID: senderID,
			Port:     newPort,
		})
	}()

	n.log(fmt.Sprintf("[SECURITY] Port rotated: :%d -> :%d (Epoch %d). Session keys & obfuscation renewed.", currentPort, newPort, newEpoch))

	if onHopCb != nil {
		onHopCb(newPort, newEpoch)
	}

	// 4. Grace period: Keep old socket alive for 10 seconds to receive in-flight packets, then close
	if oldConn != nil {
		go func() {
			time.Sleep(10 * time.Second)
			_ = oldConn.Close()
		}()
	}

	return nil
}

func (n *P2PNode) portHopSupervisor(cancel chan struct{}) {
	n.mu.RLock()
	interval := n.hopInterval
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	n.mu.RUnlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-cancel:
			return
		case <-ticker.C:
			n.mu.RLock()
			active := (n.IsConnected || n.Connecting) && n.AntiTrackingEnabled
			n.mu.RUnlock()

			if active {
				_ = n.RotatePort()
				n.cycleRelayConnection()
			}
		}
	}
}

func (n *P2PNode) cycleRelayConnection() {
	n.mu.Lock()
	if n.wsConn == nil || n.LanOnly || n.RoomCode == "" {
		n.mu.Unlock()
		return
	}
	roomCode := n.RoomCode
	isHost := n.IsHost
	action := "join"
	if isHost {
		action = "host"
	}
	n.mu.Unlock()

	n.connectRelay(action, roomCode)
}

// encodeAndEncryptPacket serializes the packet and encrypts it with AES-256-GCM AEAD
func encodeAndEncryptPacket(pkt *P2PPacket, aead cipher.AEAD) ([]byte, error) {
	// Add randomized 16-48 byte anti-DPI padding if not already populated
	if len(pkt.Padding) == 0 {
		var padLenBuf [1]byte
		_, _ = rand.Read(padLenBuf[:])
		padLen := 16 + int(padLenBuf[0]%33)
		pkt.Padding = make([]byte, padLen)
		_, _ = rand.Read(pkt.Padding)
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	enc := gob.NewEncoder(buf)
	if err := enc.Encode(pkt); err != nil {
		return nil, err
	}

	plaintext := buf.Bytes()

	// 12-byte random nonce on stack (zero heap allocation)
	var nonce [12]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, err
	}

	prefixLen := len(MagicPrefix)
	nonceLen := len(nonce)
	overhead := aead.Overhead()
	totalCap := prefixLen + nonceLen + len(plaintext) + overhead

	// Preallocate exact capacity and write MagicPrefix + Nonce
	out := make([]byte, prefixLen+nonceLen, totalCap)
	copy(out, MagicPrefix)
	copy(out[prefixLen:], nonce[:])

	// Seal appends [Ciphertext + AuthTag] directly to out, zero extra heap allocation or copy!
	out = aead.Seal(out, nonce[:], plaintext, nil)

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

	dec := gob.NewDecoder(bytes.NewReader(plaintext))
	return dec.Decode(pkt)
}

// LockRoom locks the current room against new joiners, optionally requiring a 4-digit PIN (Host only).
func (n *P2PNode) LockRoom(pin string) {
	n.mu.Lock()
	if !n.IsHost {
		n.mu.Unlock()
		n.log("[SECURITY] Non-host attempted to lock room - ignored.")
		return
	}
	n.IsLocked = true
	n.RoomPIN = strings.TrimSpace(pin)
	roomCode := n.RoomCode
	roomPIN := n.RoomPIN
	if n.RoomPIN != "" {
		n.log(fmt.Sprintf("[SECURITY] Room locked with 4-digit PIN: %s", n.RoomPIN))
	} else {
		n.log("[SECURITY] Room locked. No new members can join.")
	}
	n.mu.Unlock()

	n.sendRelayControl(RelayControlMessage{
		Type:     "lock_room",
		RoomCode: roomCode,
		PIN:      roomPIN,
		IsLocked: true,
	})

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
	roomCode := n.RoomCode
	n.log("[SECURITY] Room unlocked. Open for new members.")
	n.mu.Unlock()

	n.sendRelayControl(RelayControlMessage{
		Type:     "unlock_room",
		RoomCode: roomCode,
		IsLocked: false,
	})

	if n.OnRoomLocked != nil {
		go n.OnRoomLocked(false, "")
	}
}

// SendFile streams a local file to all peers in the room with E2EE chunking
func (n *P2PNode) SendFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	fileName := filepath.Base(filePath)
	return n.SendFileBytes(fileName, data, false)
}

// SendCodeSnippet shares a syntax code snippet with peers in the room
func (n *P2PNode) SendCodeSnippet(title string, codeContent string) error {
	if title == "" {
		title = "snippet.txt"
	}
	return n.SendFileBytes(title, []byte(codeContent), true)
}

// SendFileBytes streams raw file or code bytes to all peers with E2EE chunking
func (n *P2PNode) SendFileBytes(fileName string, data []byte, isCode bool) error {
	n.mu.RLock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.RUnlock()
		return errors.New("cannot transfer: not connected to room or no peers")
	}
	room := n.RoomCode
	senderID := n.LocalID
	nickname := n.Nickname
	n.mu.RUnlock()

	fileSum := sha256.Sum256([]byte(fileName))
	transferID := fmt.Sprintf("tf_%d_%x", time.Now().UnixNano(), fileSum[:4])
	const chunkSize = 16384 // 16 KB per chunk
	fileSize := int64(len(data))
	totalChunks := (len(data) + chunkSize - 1) / chunkSize
	if totalChunks == 0 {
		totalChunks = 1
	}

	h := sha256.New()
	h.Write(data)
	checksum := hex.EncodeToString(h.Sum(nil))

	headerPkt := P2PPacket{
		Type:     PacketFileHeader,
		RoomCode: room,
		SenderID: senderID,
		Nickname: nickname,
		FileMeta: &FileMetadata{
			TransferID:  transferID,
			FileName:    fileName,
			FileSize:    fileSize,
			TotalChunks: totalChunks,
			IsCode:      isCode,
			Checksum:    checksum,
		},
		Timestamp: time.Now().UnixMilli(),
	}

	n.sendAudioToPeers(&headerPkt)

	// Stream chunks in background goroutine
	go func() {
		startTime := time.Now()
		var sentBytes int64

		for i := 0; i < totalChunks; i++ {
			start := i * chunkSize
			end := start + chunkSize
			if end > len(data) {
				end = len(data)
			}
			chunkData := data[start:end]
			sentBytes += int64(len(chunkData))

			chunkPkt := P2PPacket{
				Type:     PacketFileChunk,
				RoomCode: room,
				SenderID: senderID,
				Nickname: nickname,
				FileMeta: &FileMetadata{
					TransferID:  transferID,
					FileName:    fileName,
					FileSize:    fileSize,
					TotalChunks: totalChunks,
					ChunkIndex:  i,
					IsCode:      isCode,
					Checksum:    checksum,
				},
				Payload:   chunkData,
				Timestamp: time.Now().UnixMilli(),
			}

			n.sendAudioToPeers(&chunkPkt)

			elapsed := time.Since(startTime).Seconds()
			var speed float64
			if elapsed > 0.05 {
				speed = float64(sentBytes) / elapsed
			}

			isDone := i == totalChunks-1
			if n.OnFileTransferProgress != nil {
				n.OnFileTransferProgress(transferID, fileName, sentBytes, fileSize, speed, true, isDone, nil)
			}
			time.Sleep(6 * time.Millisecond) // smooth pacing
		}
	}()

	return nil
}

// GetLimoniTransfersDir returns the cross-platform path to ~/Downloads/LimoniTransfers/
func GetLimoniTransfersDir() string {
	if testing.Testing() {
		return filepath.Join(os.TempDir(), "LimoniTransfers_test")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		if runtime.GOOS == "windows" {
			homeDir = os.Getenv("USERPROFILE")
			if homeDir == "" {
				homeDir = os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
			}
		} else {
			homeDir = os.Getenv("HOME")
		}
	}
	if homeDir == "" {
		homeDir = "."
	}
	return filepath.Join(homeDir, "Downloads", "LimoniTransfers")
}

// SaveAcceptedFile saves an accepted file transfer or code snippet to the user's Downloads/LimoniTransfers directory.
func SaveAcceptedFile(offer *FileOffer) (string, error) {
	if offer == nil || len(offer.Data) == 0 {
		return "", errors.New("empty file offer data")
	}

	dlDir := GetLimoniTransfersDir()
	if err := os.MkdirAll(dlDir, 0755); err != nil {
		return "", err
	}

	safeName := sanitizeFilename(offer.FileName, offer.IsCode)
	destPath := filepath.Join(dlDir, safeName)
	if _, err := os.Stat(destPath); err == nil {
		ext := filepath.Ext(safeName)
		base := strings.TrimSuffix(safeName, ext)
		for i := 1; i < 1000; i++ {
			altPath := filepath.Join(dlDir, fmt.Sprintf("%s (%d)%s", base, i, ext))
			if _, err := os.Stat(altPath); os.IsNotExist(err) {
				destPath = altPath
				break
			}
		}
	}

	if err := os.WriteFile(destPath, offer.Data, 0644); err != nil {
		return "", err
	}
	return destPath, nil
}
