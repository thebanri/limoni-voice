package p2p

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/thebanri/limoni-voice/internal/nat"
	"github.com/thebanri/limoni-voice/internal/protocol"
)

const (
	punchWaveInterval = 50 * time.Millisecond
	punchSprayPorts   = 512
	punchExtraSockets = 64
)

// DiscoverPublicEndpoint sends STUN binding requests from the main UDP socket to several
// servers to learn the public mapping and classify the NAT (cone vs symmetric).
func (n *P2PNode) DiscoverPublicEndpoint() {
	n.mu.RLock()
	conn := n.Conn
	lanOnly := n.LanOnly
	n.mu.RUnlock()
	n.refreshIPv6Candidates()
	if conn == nil || lanOnly {
		return
	}

	n.stun.StartRound()
	for _, srv := range n.stunServers {
		addr, err := net.ResolveUDPAddr("udp4", srv)
		if err != nil {
			continue
		}
		req, tx := nat.NewBindingRequest()
		n.stun.Track(tx, addr.String())
		_, _ = conn.WriteToUDP(req, addr)
	}
}

func (n *P2PNode) handleSTUNResponse(data []byte) {
	tx, mapped, err := nat.ParseBindingResponse(data)
	if err != nil || mapped.IP.To4() == nil {
		return
	}
	if !n.stun.Observe(tx, mapped) {
		return
	}
	natType, public := n.stun.Result()
	if public == nil {
		return
	}

	n.mu.Lock()
	n.PublicIP = public.IP.String()
	n.PublicPort = public.Port
	isRelay := n.isRelayConnected
	inRoom := n.roomID != "" && (n.IsConnected || n.Connecting)
	var relayPeers []string
	for id, p := range n.Peers {
		if p.ViaRelay {
			relayPeers = append(relayPeers, id)
		}
	}
	localPort := n.Port
	n.mu.Unlock()

	n.debugLog(fmt.Sprintf("🎯 [STUN] Public endpoint %s:%d (local :%d, NAT: %s)", public.IP, public.Port, localPort, natType.Describe()))
	if isRelay && inRoom {
		n.sendRelaySignal(protocol.Signal{Type: protocol.SigPortUpdate, Endpoint: n.localEndpoint()})
	}
	for _, id := range relayPeers {
		go n.punchPeer(id, false)
	}
}

// refreshIPv6Candidates records the global IPv6 addresses peers can use to reach our v6 socket.
func (n *P2PNode) refreshIPv6Candidates() {
	n.mu.RLock()
	port6 := n.Port6
	hasV6 := n.Conn6 != nil
	n.mu.RUnlock()
	var cands []string
	if hasV6 {
		for _, ip := range nat.GlobalIPv6Addrs() {
			cands = append(cands, net.JoinHostPort(ip.String(), strconv.Itoa(port6)))
		}
	}
	n.mu.Lock()
	n.ipv6Addrs = cands
	n.mu.Unlock()
}

// punchPeer tries to open a direct path to a relay-routed peer using the strategy that fits
// both NAT types. fresh selects a longer, more aggressive attempt (new peer / new endpoint).
func (n *P2PNode) punchPeer(peerID string, fresh bool) {
	n.mu.Lock()
	peer, ok := n.Peers[peerID]
	if !ok || !n.IsConnected || n.LanOnly || n.keyring == nil || (!peer.ViaRelay && peer.Addr != nil) {
		n.mu.Unlock()
		return
	}
	if peer.PunchState == "probing" {
		n.mu.Unlock()
		return
	}
	ep := peer.Endpoint
	localNAT, _ := n.stun.Result()
	strategy := nat.ChooseStrategy(localNAT, nat.Type(peer.NAT))
	peer.PunchState = "probing"
	n.lastPunchAt[peerID] = time.Now()
	n.mu.Unlock()

	candidates := nat.BuildCandidates(ep.PublicIP, ep.PublicPort, ep.LocalIP, ep.LocalPort, ep.IPv6, false)
	defer func() {
		n.mu.Lock()
		if p, ok := n.Peers[peerID]; ok && p.PunchState == "probing" {
			if p.ViaRelay {
				if strategy == nat.StrategyRelayOnly {
					p.PunchState = "relay-only"
				} else {
					p.PunchState = "relay"
				}
			} else {
				p.PunchState = "direct"
			}
		}
		n.mu.Unlock()
	}()
	if len(candidates) == 0 {
		return
	}

	waves := 10
	if fresh {
		waves = 60
	}
	if strategy == nat.StrategyRelayOnly {
		waves = max(waves/3, 5) // still try IPv6 and the chance our classification was wrong
	}
	n.debugLog(fmt.Sprintf("[P2P] Hole punching %s (strategy %s, %d candidates)", peerID, strategy, len(candidates)))

	var extra []*net.UDPConn
	if strategy == nat.StrategyManySockets && fresh {
		extra = n.openPunchSockets(punchExtraSockets)
	}
	var sprayPorts []int
	var sprayIP net.IP
	if strategy == nat.StrategySprayRemote {
		if ip := net.ParseIP(ep.PublicIP); ip != nil && ip.To4() != nil {
			sprayIP = ip
			sprayPorts = nat.RandomPorts(punchSprayPorts, ep.PublicPort)
		}
	}

	for wave := 0; wave < waves; wave++ {
		if n.peerIsDirect(peerID) {
			return
		}
		probe, err := n.sealedPing(peerID)
		if err != nil {
			return
		}
		for _, c := range candidates {
			n.writeUDP(probe, c.Addr, nil)
			for _, conn := range extra {
				if c.Addr.IP.To4() != nil {
					_, _ = conn.WriteToUDP(probe, c.Addr)
				}
			}
		}
		if sprayIP != nil {
			batch := 32
			for i := 0; i < batch && len(sprayPorts) > 0; i++ {
				port := sprayPorts[0]
				sprayPorts = append(sprayPorts[1:], port) // rotate: cover every port, then repeat
				n.writeUDP(probe, &net.UDPAddr{IP: sprayIP, Port: port}, nil)
			}
		}
		time.Sleep(punchWaveInterval)
	}
}

func (n *P2PNode) peerIsDirect(peerID string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	p, ok := n.Peers[peerID]
	return !ok || (!p.ViaRelay && p.Addr != nil)
}

// sealedPing builds a fresh encrypted ping (unique sequence so every wave gets answered).
func (n *P2PNode) sealedPing(peerID string) ([]byte, error) {
	n.mu.RLock()
	keyring := n.keyring
	roomID := n.roomID
	port := n.Port
	n.mu.RUnlock()
	pkt := P2PPacket{
		Type:      PacketPing,
		RoomCode:  roomID,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		LocalPort: port,
		Seq:       atomic.AddUint32(&n.pingSeq, 1),
		Timestamp: time.Now().UnixMilli(),
	}
	return sealPacket(&pkt, keyring)
}

// openPunchSockets opens extra IPv4 sockets that create additional NAT mappings (symmetric NAT
// birthday punching). Sockets not adopted by a peer are closed after a grace period.
func (n *P2PNode) openPunchSockets(count int) []*net.UDPConn {
	var out []*net.UDPConn
	for i := 0; i < count; i++ {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
		if err != nil {
			break
		}
		out = append(out, c)
		go n.listenLoopOnConn(c)
	}
	n.mu.Lock()
	n.punchConns = append(n.punchConns, out...)
	n.mu.Unlock()

	time.AfterFunc(15*time.Second, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		inUse := map[*net.UDPConn]bool{}
		for _, p := range n.Peers {
			if p.conn != nil {
				inUse[p.conn] = true
			}
		}
		kept := n.punchConns[:0]
		for _, c := range n.punchConns {
			if slices.Contains(out, c) && !inUse[c] {
				_ = c.Close()
				continue
			}
			kept = append(kept, c)
		}
		n.punchConns = kept
	})
	return out
}

// notePunchSocket adopts an extra punching socket once a peer answered through it.
func (n *P2PNode) notePunchSocket(senderID string, via *net.UDPConn) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if via == n.Conn || via == n.Conn6 || via == n.BroadcastConn {
		return
	}
	if !slices.Contains(n.punchConns, via) {
		return
	}
	if p, ok := n.Peers[senderID]; ok && p.conn != via {
		p.conn = via
		n.debugLog(fmt.Sprintf("[P2P] Symmetric NAT traversal succeeded for %s via %s", p.Nickname, via.LocalAddr()))
	}
}

// punchConnFor returns the dedicated socket for a peer address, if any.
func (n *P2PNode) punchConnFor(addr *net.UDPAddr) *net.UDPConn {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, p := range n.Peers {
		if p.conn != nil && p.Addr != nil && p.Addr.Port == addr.Port && p.Addr.IP.Equal(addr.IP) {
			return p.conn
		}
	}
	return nil
}

func (n *P2PNode) closePunchSocketsLocked() {
	for _, c := range n.punchConns {
		_ = c.Close()
	}
	n.punchConns = nil
	for _, p := range n.Peers {
		p.conn = nil
	}
}

// --- LAN discovery fan-out ---

// broadcastRaw sends a datagram to every LAN discovery target: the configured direct peer,
// local instances on loopback, the limited and subnet broadcast addresses and optionally a
// rate limited /24 unicast sweep.
func (n *P2PNode) broadcastRaw(data []byte, sweep bool) {
	n.mu.RLock()
	targetPeer := n.TargetPeerAddr
	port := n.Port
	conn := n.Conn
	bcast := n.bcastSendConn
	n.mu.RUnlock()
	if conn == nil {
		return
	}

	if targetPeer != nil {
		n.writeUDP(data, targetPeer, nil)
	}
	for p := 50000; p <= 50010; p++ {
		if p != port {
			_, _ = conn.WriteToUDP(data, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p})
		}
	}
	ports := []int{50000, 50001, 50002, 50003, 50004, 50005, 45454}
	for _, p := range ports {
		addr := &net.UDPAddr{IP: net.IPv4bcast, Port: p}
		if bcast != nil {
			_, _ = bcast.WriteToUDP(data, addr)
		}
		_, _ = conn.WriteToUDP(data, addr)
	}

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
				if !ok || ipnet.IP.To4() == nil || len(ipnet.Mask) != 4 {
					continue
				}
				ip := ipnet.IP.To4()
				mask := ipnet.Mask
				broadcast := net.IPv4(ip[0]|^mask[0], ip[1]|^mask[1], ip[2]|^mask[2], ip[3]|^mask[3])
				for _, p := range ports {
					_, _ = conn.WriteToUDP(data, &net.UDPAddr{IP: broadcast, Port: p})
				}
			}
		}
	}

	if sweep {
		go n.sweepSubnets(data)
	}
}

// sweepSubnets unicasts data across local /24 subnets on the primary Limoni ports
// (for networks that filter broadcast). Rate limited.
func (n *P2PNode) sweepSubnets(data []byte) {
	n.mu.Lock()
	if n.IsConnected || !n.Connecting || time.Since(n.lastSweepTime) < 2500*time.Millisecond {
		n.mu.Unlock()
		return
	}
	n.lastSweepTime = time.Now()
	conn := n.Conn
	n.mu.Unlock()
	if conn == nil {
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
			for host := 1; host <= 254; host++ {
				if byte(host) == ip[3] {
					continue
				}
				target := net.IPv4(ip[0], ip[1], ip[2], byte(host))
				_, _ = conn.WriteToUDP(data, &net.UDPAddr{IP: target, Port: 50000})
				_, _ = conn.WriteToUDP(data, &net.UDPAddr{IP: target, Port: 50001})
			}
		}
	}
}

// broadcastJoinRequest sends the encrypted join request (after the handshake) to the host,
// or to all LAN discovery targets when the host address is unknown.
func (n *P2PNode) broadcastJoinRequest(hostAddr *net.UDPAddr) {
	n.mu.RLock()
	isConnecting := n.Connecting
	keyring := n.keyring
	pkt := P2PPacket{
		Type:      PacketJoinRequest,
		RoomCode:  n.roomID,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		LocalPort: n.Port,
		PIN:       n.RoomPIN,
		Timestamp: time.Now().UnixMilli(),
	}
	n.mu.RUnlock()
	if !isConnecting || keyring == nil || pkt.RoomCode == "" {
		return
	}
	pkt.IsMuted, pkt.IsDeafened = n.audioState()
	data, err := sealPacket(&pkt, keyring)
	if err != nil {
		return
	}
	if hostAddr != nil {
		n.writeUDP(data, hostAddr, nil)
		return
	}
	n.broadcastRaw(data, true)
}

func (n *P2PNode) broadcastHello() {
	n.mu.RLock()
	isConnected := n.IsConnected
	keyring := n.keyring
	pkt := P2PPacket{
		Type:      PacketHello,
		RoomCode:  n.roomID,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		LocalPort: n.Port,
		Timestamp: time.Now().UnixMilli(),
	}
	n.mu.RUnlock()
	if !isConnected || keyring == nil || pkt.RoomCode == "" {
		return
	}
	pkt.IsMuted, pkt.IsDeafened = n.audioState()
	data, err := sealPacket(&pkt, keyring)
	if err != nil {
		return
	}
	n.broadcastRaw(data, false)
}

// --- port hopping ---

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

// RotatePort binds new UDP sockets, announces the new port to peers and gracefully switches
// over. The host also rotates the room group key.
func (n *P2PNode) RotatePort() error {
	n.mu.Lock()
	if !n.IsConnected && !n.Connecting {
		n.mu.Unlock()
		return errNotInRoom
	}
	currentPort := n.Port
	oldConn := n.Conn
	oldConn6 := n.Conn6
	roomID := n.roomID
	keyring := n.keyring
	n.currentEpoch++
	newEpoch := n.currentEpoch
	n.mu.Unlock()

	// Bind a new random UDP port (50000-59999)
	var newConn *net.UDPConn
	newPort := 0
	for attempt := 0; attempt < 25 && newConn == nil; attempt++ {
		var randBytes [2]byte
		_, _ = rand.Read(randBytes[:])
		p := 50000 + int(binary.BigEndian.Uint16(randBytes[:]))%10000
		if p == currentPort {
			continue
		}
		if c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: p}); err == nil {
			newConn, newPort = c, p
		}
	}
	if newConn == nil {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
		if err != nil {
			return err
		}
		newConn = c
		newPort = c.LocalAddr().(*net.UDPAddr).Port
	}
	_ = newConn.SetReadBuffer(4 * 1024 * 1024)
	_ = newConn.SetWriteBuffer(4 * 1024 * 1024)
	newConn6, newPort6, err6 := bindVoiceSocket("udp6", newPort)

	n.mu.Lock()
	n.Conn = newConn
	n.Port = newPort
	if err6 == nil {
		n.Conn6 = newConn6
		n.Port6 = newPort6
	}
	n.lastHopTime = time.Now()
	n.nextHopTime = time.Now().Add(n.hopInterval)
	peers := make([]*PeerInfo, 0, len(n.Peers))
	for _, p := range n.Peers {
		cp := *p
		peers = append(peers, &cp)
	}
	onHopCb := n.OnPortHopped
	if n.IsHost {
		n.scheduleRekeyLocked("scheduled rotation")
	}
	n.mu.Unlock()
	n.stun.Reset()

	go n.listenLoopOnConn(newConn)
	if err6 == nil {
		go n.listenLoopOnConn(newConn6)
	}
	n.refreshIPv6Candidates()

	hopPkt := P2PPacket{
		Type:      PacketPortHop,
		RoomCode:  roomID,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		LocalPort: newPort,
		Seq:       newEpoch,
		Timestamp: time.Now().UnixMilli(),
	}

	go func() {
		data, err := sealPacket(&hopPkt, keyring)
		if err != nil {
			return
		}
		// Send over the new and the old socket (multiple bursts for UDP reliability)
		for _, peer := range peers {
			if peer.Addr == nil {
				continue
			}
			for burst := 0; burst < 3; burst++ {
				n.writeUDP(data, peer.Addr, nil)
				if oldConn != nil && peer.Addr.IP.To4() != nil {
					_, _ = oldConn.WriteToUDP(data, peer.Addr)
				}
				time.Sleep(30 * time.Millisecond)
			}
		}
		n.sendRelayFrame(protocol.FrameRealtime, data)
		n.DiscoverPublicEndpoint()
		time.Sleep(time.Second)
		n.sendRelaySignal(protocol.Signal{Type: protocol.SigPortUpdate, Endpoint: n.localEndpoint()})
	}()

	n.log(fmt.Sprintf("[SECURITY] Port rotated: :%d -> :%d (Epoch %d).", currentPort, newPort, newEpoch))

	if onHopCb != nil {
		onHopCb(newPort, newEpoch)
	}

	// Grace period: keep old sockets alive for 10 seconds to receive in-flight packets
	go func() {
		time.Sleep(10 * time.Second)
		if oldConn != nil {
			_ = oldConn.Close()
		}
		if oldConn6 != nil && err6 == nil {
			_ = oldConn6.Close()
		}
	}()

	return nil
}

func (n *P2PNode) portHopSupervisor(cancel chan struct{}) {
	n.mu.RLock()
	interval := n.hopInterval
	n.mu.RUnlock()
	if interval <= 0 {
		interval = 30 * time.Minute
	}

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
			}
		}
	}
}
