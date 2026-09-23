package p2p

import (
	"fmt"
	"strings"
	"time"
)

// PeerDiagnostics describes the connection quality to one peer.
type PeerDiagnostics struct {
	ID            string
	Nickname      string
	Path          string
	RemoteAddr    string
	NAT           string
	PunchState    string
	PingMs        int64
	LossPct       float64 // loss we see on their audio
	JitterMs      float64
	RemoteLossPct float64 // loss they report for our audio
	LastDirect    time.Duration
}

// NetDiagnostics is a snapshot of the local network situation for the debug panel.
type NetDiagnostics struct {
	LocalPort    int
	PublicAddr   string
	NATType      string
	STUNMappings []string
	IPv6         []string
	RelayURL     string
	RelayStatus  string
	RelayRTT     time.Duration
	UDPRelay     string
	GroupEpoch   uint32
	EncoderLoss  int
	VoiceBitrate int
	Screen       ScreenStats
	Peers        []PeerDiagnostics
}

// Diagnostics returns the current network diagnostics snapshot.
func (n *P2PNode) Diagnostics() NetDiagnostics {
	natType, public := n.stun.Result()
	obs := n.stun.Observations()
	relayStatus := n.RelayStatus()

	n.mu.RLock()
	d := NetDiagnostics{
		LocalPort:   n.Port,
		NATType:     natType.Describe(),
		IPv6:        append([]string(nil), n.ipv6Addrs...),
		RelayURL:    n.RelayURL,
		RelayStatus: relayStatus,
		RelayRTT:    n.relayRTT,
		UDPRelay:    "unavailable",
	}
	if n.LanOnly {
		d.NATType = "n/a (LAN mode)"
	}
	if public != nil {
		d.PublicAddr = public.String()
	}
	for _, o := range obs {
		d.STUNMappings = append(d.STUNMappings, fmt.Sprintf("%s → %s", o.Server, o.Mapped))
	}
	relayUDP := false
	if ur := n.udpRelay; ur != nil {
		if ur.isActive() {
			d.UDPRelay = fmt.Sprintf("active (%s)", ur.addr.Load())
			relayUDP = true
		} else if addr := ur.addr.Load(); addr != nil {
			d.UDPRelay = fmt.Sprintf("unreachable (%s) → WebSocket fallback", addr)
		} else {
			d.UDPRelay = "resolving"
		}
	}
	if n.keyring != nil {
		d.GroupEpoch, _ = n.keyring.Current()
	}
	if n.voiceEnc != nil {
		d.EncoderLoss = n.voiceEnc.PacketLoss()
		d.VoiceBitrate = n.voiceEnc.Bitrate()
	}
	for _, p := range n.Peers {
		pd := PeerDiagnostics{
			ID:            p.ID,
			Nickname:      p.Nickname,
			Path:          p.Path(relayUDP),
			NAT:           p.NAT,
			PunchState:    p.PunchState,
			PingMs:        p.PingMs,
			RemoteLossPct: p.RemoteLossPct,
		}
		if p.Addr != nil {
			pd.RemoteAddr = p.Addr.String()
		}
		if !p.LastDirectSeen.IsZero() {
			pd.LastDirect = time.Since(p.LastDirectSeen)
		}
		d.Peers = append(d.Peers, pd)
	}
	audio := n.audio
	n.mu.RUnlock()
	d.Screen = n.ScreenStats()

	if audio != nil {
		for i := range d.Peers {
			d.Peers[i].LossPct, d.Peers[i].JitterMs = audio.PeerReceiveQuality(d.Peers[i].ID)
		}
	}
	return d
}

// PeerPath returns the transport label for a peer snapshot.
func (n *P2PNode) PeerPath(p *PeerInfo) string {
	n.mu.RLock()
	relayUDP := n.udpRelay != nil && n.udpRelay.isActive()
	n.mu.RUnlock()
	return p.Path(relayUDP)
}

// Lines renders the diagnostics as text for the debug panel / clipboard.
func (d NetDiagnostics) Lines() []string {
	natDesc := func(s string) string {
		switch s {
		case "eim":
			return "Cone"
		case "edm":
			return "Symmetric"
		default:
			return "?"
		}
	}
	lines := []string{
		fmt.Sprintf("Local UDP :%d  Public %s  NAT %s", d.LocalPort, OrDash(d.PublicAddr), d.NATType),
	}
	if len(d.IPv6) > 0 {
		lines = append(lines, "IPv6: "+strings.Join(d.IPv6, ", "))
	} else {
		lines = append(lines, "IPv6: none (global address not available)")
	}
	if len(d.STUNMappings) > 0 {
		lines = append(lines, "STUN: "+strings.Join(d.STUNMappings, " | "))
	}
	rtt := "-"
	if d.RelayRTT > 0 {
		rtt = fmt.Sprintf("%dms", d.RelayRTT.Milliseconds())
	}
	lines = append(lines, fmt.Sprintf("Relay: %s (%s) RTT %s  UDP relay: %s", d.RelayStatus, OrDash(d.RelayURL), rtt, d.UDPRelay))
	lines = append(lines, fmt.Sprintf("E2EE group key epoch %d  Opus %d kbps, FEC loss hint %d%%", d.GroupEpoch, d.VoiceBitrate/1000, d.EncoderLoss))
	if sc := d.Screen; sc.Sharing {
		audio := ""
		if sc.Audio {
			audio = " + system audio"
		}
		lines = append(lines, fmt.Sprintf("Screen share: %s at %d kbps%s, %d viewer(s), uplink queue %dms, %d retransmits",
			sc.Preset, sc.Kbps, audio, sc.Watchers, sc.QueueDelay.Milliseconds(), sc.Retransmits))
		if sc.AudioStatus != "" {
			lines = append(lines, "System audio: "+sc.AudioStatus)
		}
	}
	if sc := d.Screen; sc.Watching {
		lines = append(lines, fmt.Sprintf("Watching screen: residual loss %.2f%%, gap wait %dms", sc.LossPct, sc.MaxWait.Milliseconds()))
	}
	if len(d.Peers) == 0 {
		lines = append(lines, "No peers connected")
	}
	for _, p := range d.Peers {
		direct := "never"
		if p.LastDirect > 0 {
			direct = fmt.Sprintf("%.0fs ago", p.LastDirect.Seconds())
		}
		why := ""
		if p.Path == PathRelayWS || p.Path == PathRelayUDP {
			switch p.PunchState {
			case "relay-only":
				why = " (both NATs symmetric: direct path impossible)"
			case "probing":
				why = " (hole punching in progress)"
			case "relay":
				why = " (hole punching failed)"
			}
		}
		lines = append(lines, fmt.Sprintf("• %s via %s%s  ping %dms  rx loss %.1f%%  jitter %.0fms  tx loss %.0f%%  NAT %s  addr %s  last direct %s",
			p.Nickname, p.Path, why, p.PingMs, p.LossPct, p.JitterMs, p.RemoteLossPct, natDesc(p.NAT), OrDash(p.RemoteAddr), direct))
	}
	return lines
}

// OrDash returns s, or "-" when it is empty.
func OrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
