package protocol

// SignalVersion is the relay signalling protocol version. v2 introduced host admission
// (PAKE forwarding), member tokens, UDP relay and removed room PINs from the relay.
const SignalVersion = 2

// Signalling message types exchanged with the relay over the WebSocket text channel.
const (
	// client -> relay
	SigHostRoom   = "host_room"
	SigJoinRoom   = "join_room"
	SigLockRoom   = "lock_room"
	SigUnlockRoom = "unlock_room"
	SigPortUpdate = "port_update"
	SigLeave      = "leave"
	SigPing       = "ping"
	SigPake       = "pake"   // opaque PAKE handshake frame, forwarded between a pending joiner and the host
	SigAdmit      = "admit"  // host approves a pending joiner that completed the handshake
	SigReject     = "reject" // host refuses a pending joiner

	// relay -> client
	SigRoomCreated     = "room_created"
	SigJoinPending     = "join_pending" // joiner: relay accepted the request, handshake with host may begin
	SigJoinRequest     = "join_request" // host: a new joiner is waiting for the handshake
	SigWelcome         = "welcome"
	SigPeerJoined      = "peer_joined"
	SigPeerPortUpdated = "peer_port_updated"
	SigPeerLeft        = "peer_left"
	SigNewHost         = "new_host"
	SigRoomLocked      = "room_locked"
	SigRoomFull        = "room_full"
	SigRoomNotFound    = "room_not_found"
	SigRejected        = "rejected"
	SigError           = "error"
	SigPong            = "pong"
)

// Endpoint describes how a member can be reached directly (hole punching candidates).
type Endpoint struct {
	LocalIP    string   `json:"local_ip,omitempty"`    // internal LAN IP
	PublicIP   string   `json:"public_ip,omitempty"`   // observed / STUN-mapped public IPv4
	LocalPort  int      `json:"local_port,omitempty"`  // bound UDP port
	PublicPort int      `json:"public_port,omitempty"` // STUN-mapped public port
	IPv6       []string `json:"ipv6,omitempty"`        // global IPv6 candidates as host:port
	NAT        string   `json:"nat,omitempty"`         // NAT mapping behaviour: "eim", "edm" or ""
}

// SignalPeer is a room member entry in a welcome message.
type SignalPeer struct {
	SenderID string `json:"sender_id"`
	Nickname string `json:"nickname"`
	Endpoint
}

// Signal is a JSON signalling message. Unused fields are omitted on the wire.
type Signal struct {
	Type     string `json:"type"`
	Proto    int    `json:"proto,omitempty"`
	RoomCode string `json:"room_code,omitempty"` // public room identifier (never the room secret)
	SenderID string `json:"sender_id,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Message  string `json:"message,omitempty"`
	Target   string `json:"target,omitempty"` // addressed member for pake/admit/reject
	Data     []byte `json:"data,omitempty"`   // opaque handshake payload (base64 in JSON)

	Endpoint
	YourIP string `json:"your_ip,omitempty"` // client's public IP as seen by the relay

	IsLocked    bool   `json:"is_locked,omitempty"`
	PinRequired bool   `json:"pin_required,omitempty"`
	HostToken   string `json:"host_token,omitempty"`
	MemberToken string `json:"member_token,omitempty"`

	Peers []SignalPeer `json:"peers,omitempty"`

	// UDP relay parameters (room_created / welcome)
	UDPToken string `json:"udp_token,omitempty"` // hex encoded 8-byte token
	UDPAddr  string `json:"udp_addr,omitempty"`  // explicit host:port, when the relay advertises one
	UDPPort  int    `json:"udp_port,omitempty"`  // UDP port on the WebSocket host otherwise

	// Features advertised by the relay (room_created / welcome), e.g. FeatureTargeted.
	Features []string `json:"features,omitempty"`
}

// FeatureTargeted: the relay forwards targeted frames (FrameTargetFlag / UDPKindTo) to a
// single member instead of the whole room.
const FeatureTargeted = "targeted"

// HasFeature reports whether the signal advertises feature f.
func (s Signal) HasFeature(f string) bool {
	for _, v := range s.Features {
		if v == f {
			return true
		}
	}
	return false
}

// UDP relay datagram framing.
//
//	client -> relay: [8-byte token][kind][payload]
//	relay -> client: [kind][payload]
const (
	UDPTokenSize = 8

	UDPKindKeepalive byte = 0x00 // client keepalive / reachability probe
	UDPKindData      byte = 0x01 // encrypted P2P packet to forward to the room
	UDPKindProbeAck  byte = 0x02 // relay acknowledges a keepalive
	UDPKindBulk      byte = 0x03 // screen share video to the room
	UDPKindTo        byte = 0x04 // payload = AppendTarget(member, class, packet); forwarded to one member
)

// WebSocket binary frame class prefix (client <-> relay). The relay uses it for
// scheduling only; the remainder of the frame is an opaque encrypted packet.
const (
	FrameRealtime byte = 0x01 // audio, ping/pong, control
	FrameBulk     byte = 0x02 // screen share video
	FrameReliable byte = 0x03 // file transfers, rekey (never sent over UDP)

	// FrameTargetFlag marks a client → relay frame addressed to one member:
	// [class|FrameTargetFlag][len][member id][packet]. The member receives [class][packet].
	FrameTargetFlag byte = 0x80
)

// AppendTarget prefixes packet with a member id (used by targeted relay frames).
func AppendTarget(dst []byte, member string, packet []byte) []byte {
	dst = append(dst, byte(len(member)))
	dst = append(dst, member...)
	return append(dst, packet...)
}

// SplitTarget parses a buffer written by AppendTarget.
func SplitTarget(b []byte) (member string, packet []byte, ok bool) {
	if len(b) < 1 || int(b[0]) == 0 || len(b) < 1+int(b[0])+1 {
		return "", nil, false
	}
	n := int(b[0])
	return string(b[1 : 1+n]), b[1+n:], true
}
