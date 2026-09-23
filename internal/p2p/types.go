package p2p

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thebanri/limoni-voice/internal/protocol"
)

const MaxPeers = 4

// DefaultRelayURL is the default public WebSocket relay server URL
const DefaultRelayURL = "wss://relay.thebanri.dpdns.org/ws"

// DefaultRelayFallbacks are official relays tried, in order, when DefaultRelayURL cannot be
// reached or does not have the room. Rooms live on one relay, so every client lists them in
// the same order and they only help when the primary is down for everyone.
var DefaultRelayFallbacks []string

// File transfer limits preventing DoS and memory exhaustion
const (
	MaxFileTransferSize    = 50 * 1024 * 1024 // 50 MB safety limit
	MaxFileChunks          = 4000             // Max chunks per file (at 16KB chunk size)
	MaxConcurrentTransfers = 10               // Max simultaneous incoming transfers
	TransferExpiryDuration = 5 * time.Minute  // Stale transfer timeout
)

// Wire types live in internal/protocol; the aliases keep the application code readable.
type (
	P2PPacket    = protocol.Packet
	PacketType   = protocol.PacketType
	FileMetadata = protocol.FileMetadata
	PeerSummary  = protocol.PeerSummary
)

const (
	PacketHello            = protocol.PacketHello
	PacketWelcome          = protocol.PacketWelcome
	PacketPing             = protocol.PacketPing
	PacketPong             = protocol.PacketPong
	PacketAudio            = protocol.PacketAudio
	PacketMuteState        = protocol.PacketMuteState
	PacketLeave            = protocol.PacketLeave
	PacketJoinRequest      = protocol.PacketJoinRequest
	PacketRoomFull         = protocol.PacketRoomFull
	PacketScreenShareStart = protocol.PacketScreenShareStart
	PacketScreenShareStop  = protocol.PacketScreenShareStop
	PacketScreenShareData  = protocol.PacketScreenShareData
	PacketScreenWatch      = protocol.PacketScreenWatch
	PacketScreenUnwatch    = protocol.PacketScreenUnwatch
	PacketScreenNack       = protocol.PacketScreenNack
	PacketScreenAudio      = protocol.PacketScreenAudio
	PacketKick             = protocol.PacketKick
	PacketChatMessage      = protocol.PacketChatMessage
	PacketPortHop          = protocol.PacketPortHop
	PacketRoomLocked       = protocol.PacketRoomLocked
	PacketFileHeader       = protocol.PacketFileHeader
	PacketFileChunk        = protocol.PacketFileChunk
	PacketFileAck          = protocol.PacketFileAck
	PacketRekey            = protocol.PacketRekey
	PacketRekeyAck         = protocol.PacketRekeyAck
)

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

// Transport paths a peer can be reached over, best first.
const (
	PathLAN      = "LAN"
	PathDirectV6 = "P2P-v6"
	PathDirectV4 = "P2P"
	PathRelayUDP = "Relay-UDP"
	PathRelayWS  = "Relay"
)

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
	VideoFPS        int
	VideoKbps       int       // sharer's current video bitrate
	ScreenAudio     bool      // screen share includes system audio
	ViaRelay        bool      // True if routing through the relay, false if direct P2P/LAN
	LastDirectSeen  time.Time // Last time a direct UDP packet arrived from this peer

	// Network diagnostics
	NAT           string            // remote NAT mapping behaviour reported via relay ("eim"/"edm")
	Endpoint      protocol.Endpoint // last signalled endpoint (hole punching candidates)
	LossPct       float64           // audio loss we observe from this peer
	JitterMs      float64           // audio jitter we observe from this peer
	RemoteLossPct float64           // audio loss this peer reports for our stream
	PunchState    string            // "", "probing", "direct", "relay-only"

	conn *net.UDPConn // dedicated socket that reached this peer (symmetric NAT punching)
}

// Path returns the transport currently used for this peer.
func (p *PeerInfo) Path(relayUDP bool) string {
	if !p.ViaRelay && p.Addr != nil {
		if p.Addr.IP.IsPrivate() || p.Addr.IP.IsLoopback() {
			return PathLAN
		}
		if p.Addr.IP.To4() == nil {
			return PathDirectV6
		}
		return PathDirectV4
	}
	if relayUDP {
		return PathRelayUDP
	}
	return PathRelayWS
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
// when packets arrive over both direct UDP and relay transports.
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
// when packets arrive over both direct UDP and relay transports.
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

// audioPreRollFrame stores encoded 20ms frames during silence periods so word onsets
// (such as unvoiced fricatives 's', 'p', 't', 'k') are never clipped when VAD triggers.
type audioPreRollFrame struct {
	rms   float64
	frame []byte
	ts    int64
}

func getLocalPrivateIP() string {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		if udpAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok && udpAddr.IP != nil {
			return udpAddr.IP.String()
		}
	}
	return ""
}
