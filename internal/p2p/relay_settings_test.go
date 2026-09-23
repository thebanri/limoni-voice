package p2p

import (
	"net"
	"testing"
	"time"
)

func TestP2PNodeUpdateRelaySettings(t *testing.T) {
	node := &P2PNode{
		RelayURL: DefaultRelayURL,
	}

	// Update to custom URL and token
	node.UpdateRelaySettings("wss://my-relay.domain.com/ws", "my-secret-token")
	if node.RelayURL != "wss://my-relay.domain.com/ws" {
		t.Fatalf("Expected updated relay URL, got %s", node.RelayURL)
	}
	if node.RelayToken != "my-secret-token" {
		t.Fatalf("Expected updated relay token, got %s", node.RelayToken)
	}
	if node.LanOnly {
		t.Fatalf("Expected LanOnly to be false")
	}

	// Setting to "none" should enable LanOnly
	node.UpdateRelaySettings("none", "")
	if !node.LanOnly {
		t.Fatalf("Expected LanOnly to be true after setting URL to 'none'")
	}
	if node.RelayURL != "" {
		t.Fatalf("Expected empty RelayURL when LanOnly, got %s", node.RelayURL)
	}
}

func TestPingPongDeduplicationAndSmoothing(t *testing.T) {
	node := &P2PNode{
		LocalID:     "node_local",
		RoomCode:    "test-room",
		roomID:      "test-room",
		IsConnected: true,
		Peers:       make(map[string]*PeerInfo),
	}
	peer := &PeerInfo{
		ID:       "peer_remote",
		Nickname: "RemoteUser",
		LastSeen: time.Now(),
		PingMs:   0,
	}
	node.Peers["peer_remote"] = peer

	now := time.Now().UnixMilli()
	sendTimestamp := now - 2 // 2ms RTT

	// 1. First Pong arrives (e.g. via direct UDP)
	pong1 := P2PPacket{
		Type:      PacketPong,
		RoomCode:  "test-room",
		SenderID:  "peer_remote",
		Seq:       1,
		Timestamp: sendTimestamp,
	}
	node.handlePacket(&pong1, nil)

	if peer.PingMs <= 0 || peer.PingMs > 10 {
		t.Fatalf("Expected PingMs to be around 2ms, got %d", peer.PingMs)
	}
	recordedPing := peer.PingMs

	// 2. Delayed duplicate Pong arrives 300ms later (via WebSocket Relay) with identical seq & timestamp
	pong2 := P2PPacket{
		Type:      PacketPong,
		RoomCode:  "test-room",
		SenderID:  "peer_remote",
		Seq:       1,
		Timestamp: sendTimestamp,
	}
	// Simulate arrival after 300ms delay by temporarily modifying time or just passing it
	node.handlePacket(&pong2, nil)

	// PingMs must NOT have been updated or overwritten by the delayed packet!
	if peer.PingMs != recordedPing {
		t.Fatalf("Expected PingMs to remain %d, but was overwritten to %d by duplicate pong!", recordedPing, peer.PingMs)
	}
}

func TestPeerViaRelayAndDirectTracking(t *testing.T) {
	node := testRoomNode("node_local", "room-test")
	node.LanOnly = false

	// 1. Peer packet arrives via WebSocket relay (raddr == nil)
	pkt1 := P2PPacket{
		Type:      PacketPing,
		RoomCode:  "room-test",
		SenderID:  "peer_1",
		Nickname:  "Alice",
		LocalPort: 50002,
		Timestamp: time.Now().UnixMilli(),
	}
	node.handlePacket(&pkt1, nil)

	peer, exists := node.Peers["peer_1"]
	if !exists {
		t.Fatalf("Expected peer_1 to be registered")
	}
	if !peer.ViaRelay {
		t.Fatalf("Expected ViaRelay to be true for relay-only packet")
	}

	// 2. Private LAN packet arrives while !LanOnly -> must NOT hijack into LAN mode (ViaRelay remains true)
	lanAddr := &net.UDPAddr{IP: net.ParseIP("192.168.1.50"), Port: 50002}
	pkt2 := P2PPacket{
		Type:      PacketPing,
		RoomCode:  "room-test",
		SenderID:  "peer_1",
		Nickname:  "Alice",
		LocalPort: 50002,
		Timestamp: time.Now().UnixMilli(),
	}
	node.handlePacket(&pkt2, lanAddr)
	if !peer.ViaRelay {
		t.Fatalf("Expected ViaRelay to remain true for LAN packet when !LanOnly")
	}

	// 3. Direct WAN UDP packet arrives (public IP) -> switches to direct P2P (ViaRelay = false)
	wanAddr := &net.UDPAddr{IP: net.ParseIP("203.0.113.50"), Port: 50002}
	pkt3 := P2PPacket{
		Type:      PacketPing,
		RoomCode:  "room-test",
		SenderID:  "peer_1",
		Nickname:  "Alice",
		LocalPort: 50002,
		Timestamp: time.Now().UnixMilli(),
	}
	node.handlePacket(&pkt3, wanAddr)
	if peer.ViaRelay {
		t.Fatalf("Expected ViaRelay to be false after receiving direct WAN UDP packet")
	}
	if peer.LastDirectSeen.IsZero() {
		t.Fatalf("Expected LastDirectSeen to be recorded")
	}

	// 4. LAN-only mode accepts private IP
	node.mu.Lock()
	node.LanOnly = true
	peer.ViaRelay = true
	node.mu.Unlock()
	node.handlePacket(&pkt2, lanAddr)
	if peer.ViaRelay {
		t.Fatalf("Expected ViaRelay to be false for LAN packet when LanOnly is true")
	}
}

func TestNormalizeRelayURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://voice.thebanri.dpdns.org", "wss://voice.thebanri.dpdns.org/ws"},
		{"voice.thebanri.dpdns.org", "wss://voice.thebanri.dpdns.org/ws"},
		{"https://voice.thebanri.dpdns.org/", "wss://voice.thebanri.dpdns.org/ws"},
		{"https://voice.thebanri.dpdns.org/ws", "wss://voice.thebanri.dpdns.org/ws"},
		{"voice.thebanri.dpdns.org/ws", "wss://voice.thebanri.dpdns.org/ws"},
		{"http://voice.thebanri.dpdns.org", "ws://voice.thebanri.dpdns.org/ws"},
		{"http://192.168.1.3:27850", "ws://192.168.1.3:27850/ws"},
		{"192.168.1.3:27850", "ws://192.168.1.3:27850/ws"},
		{"localhost:27850", "ws://localhost:27850/ws"},
		{"127.0.0.1:27850", "ws://127.0.0.1:27850/ws"},
		{"none", ""},
		{"off", ""},
		{"lan", ""},
		{"local", ""},
		{"", DefaultRelayURL},
		{"default", DefaultRelayURL},
		{"reset", DefaultRelayURL},
		{"wss://custom.relay.com/ws", "wss://custom.relay.com/ws"},
		{"ws://custom.relay.com/ws", "ws://custom.relay.com/ws"},
		{"https://relay.example.com:8443/custompath?token=123", "wss://relay.example.com:8443/custompath?token=123"},
	}

	for _, tc := range tests {
		got := NormalizeRelayURL(tc.input)
		if got != tc.expected {
			t.Errorf("NormalizeRelayURL(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}
