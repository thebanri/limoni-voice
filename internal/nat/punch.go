package nat

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"strconv"
)

// Strategy describes how two peers should attempt a direct path.
type Strategy int

const (
	// StrategyDirect: at least one side has endpoint-independent mapping (or unknown);
	// both sides probe each other's known endpoints simultaneously.
	StrategyDirect Strategy = iota
	// StrategySprayRemote: we are EIM, the remote is EDM. We probe many random ports on the
	// remote public IP while the remote opens many mappings towards our endpoint.
	StrategySprayRemote
	// StrategyManySockets: we are EDM, the remote is EIM. We open many local sockets towards
	// the remote endpoint so that one of the remote's random probes hits a mapping.
	StrategyManySockets
	// StrategyRelayOnly: both sides are EDM; birthday attacks are impractical.
	StrategyRelayOnly
)

// ChooseStrategy picks the punching strategy for the local and remote NAT types.
func ChooseStrategy(local, remote Type) Strategy {
	switch {
	case local == TypeEDM && remote == TypeEDM:
		return StrategyRelayOnly
	case local == TypeEIM && remote == TypeEDM:
		return StrategySprayRemote
	case local == TypeEDM && remote == TypeEIM:
		return StrategyManySockets
	default:
		return StrategyDirect
	}
}

func (s Strategy) String() string {
	switch s {
	case StrategySprayRemote:
		return "spray"
	case StrategyManySockets:
		return "many-sockets"
	case StrategyRelayOnly:
		return "relay-only"
	default:
		return "direct"
	}
}

// RandomPorts returns n distinct random ports in [1024, 65535], excluding skip.
func RandomPorts(n int, skip int) []int {
	seen := make(map[int]bool, n)
	out := make([]int, 0, n)
	var b [2]byte
	for len(out) < n {
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		p := 1024 + int(binary.BigEndian.Uint16(b[:]))%(65536-1024)
		if p == skip || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// Candidate is a remote transport address worth probing.
type Candidate struct {
	Addr *net.UDPAddr
	Kind string // "v4", "v6", "lan"
}

// BuildCandidates assembles probe targets from a peer's signalled endpoint.
// LAN addresses are only included when includeLAN is set.
func BuildCandidates(publicIP string, publicPort int, localIP string, localPort int, ipv6 []string, includeLAN bool) []Candidate {
	var out []Candidate
	add := func(ip string, port int, kind string) {
		parsed := net.ParseIP(ip)
		if parsed == nil || port <= 0 || port > 65535 {
			return
		}
		for _, c := range out {
			if c.Addr.IP.Equal(parsed) && c.Addr.Port == port {
				return
			}
		}
		out = append(out, Candidate{Addr: &net.UDPAddr{IP: parsed, Port: port}, Kind: kind})
	}
	if ip := net.ParseIP(publicIP); ip != nil {
		kind := "v4"
		if ip.To4() == nil {
			kind = "v6"
		}
		if includeLAN || (!ip.IsPrivate() && !ip.IsLoopback()) {
			port := publicPort
			if port == 0 {
				port = localPort
			}
			add(publicIP, port, kind)
		}
	}
	for _, hp := range ipv6 {
		host, portStr, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
			add(host, port, "v6")
		}
	}
	if includeLAN && localIP != "" && localIP != publicIP {
		add(localIP, localPort, "lan")
	}
	return out
}

// GlobalIPv6Addrs returns the host's global unicast IPv6 addresses (no ULA / link-local).
func GlobalIPv6Addrs() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			if ip.To4() != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				continue
			}
			out = append(out, ip)
			if len(out) >= 3 {
				return out
			}
		}
	}
	return out
}
