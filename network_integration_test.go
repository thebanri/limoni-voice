package main

import (
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/relay"
)

// startTestRelay runs a relay v2 (WebSocket + UDP) on loopback and returns its ws URL.
func startTestRelay(t *testing.T) (string, *relay.Server) {
	t.Helper()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	srv := relay.New(relay.Config{
		UDPPublicAddr: udp.LocalAddr().String(),
		GracePeriod:   2 * time.Second,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	go srv.ServeUDP(udp)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		hs.Close()
		udp.Close()
	})
	return "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws", srv
}

func newRelayNode(t *testing.T, id, nick, relayURL string) *P2PNode {
	t.Helper()
	n := NewP2PNode(id, nick, NewAudioEngine())
	n.LanOnly = false
	n.RelayURL = relayURL
	n.stunServers = nil // no internet access from tests
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	return n
}

type joinResult struct {
	ok     bool
	reason string
	host   string
}

func joinAndWait(t *testing.T, n *P2PNode, code string, timeout time.Duration) joinResult {
	t.Helper()
	done := make(chan joinResult, 1)
	n.RequestJoinRoom(code, timeout, func(host string) {
		done <- joinResult{ok: true, host: host}
	}, func(reason string) {
		done <- joinResult{reason: reason}
	})
	select {
	case r := <-done:
		return r
	case <-time.After(timeout + 2*time.Second):
		t.Fatalf("join callback never fired")
	}
	return joinResult{}
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type chatSink struct {
	mu   sync.Mutex
	msgs []string
}

func (c *chatSink) add(_, _ string, text string, _ time.Time) {
	c.mu.Lock()
	c.msgs = append(c.msgs, text)
	c.mu.Unlock()
}

func (c *chatSink) has(text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if m == text {
			return true
		}
	}
	return false
}

func TestRelayJoinHandshakeAndChat(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "relay_host", "Alice", relayURL)
	joiner := newRelayNode(t, "relay_joiner", "Bob", relayURL)

	var hostChat, joinerChat chatSink
	host.OnChatMessage = hostChat.add
	joiner.OnChatMessage = joinerChat.add

	code := "4242-amber-falcon-river"
	host.HostRoom(code)
	waitFor(t, "host registered on relay", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})

	res := joinAndWait(t, joiner, code, 5*time.Second)
	if !res.ok || res.host != "Alice" {
		t.Fatalf("join failed: %+v", res)
	}

	waitFor(t, "host sees joiner", 3*time.Second, func() bool { return host.GetPeer("relay_joiner") != nil })

	joiner.SendChatMessage("merhaba relay")
	host.SendChatMessage("hos geldin")
	waitFor(t, "chat over relay", 3*time.Second, func() bool {
		return hostChat.has("merhaba relay") && joinerChat.has("hos geldin")
	})

	// Traffic went through the relay (loopback addresses are never used as direct paths in
	// internet mode) and the UDP relay became active.
	waitFor(t, "UDP relay active", 3*time.Second, func() bool {
		return strings.Contains(joiner.Diagnostics().UDPRelay, "active")
	})
	if p := joiner.GetPeersList(); len(p) != 1 || joiner.PeerPath(p[0]) != PathRelayUDP {
		t.Fatalf("expected relay UDP path, got %+v", p)
	}
}

func TestRelayJoinWrongCodeRejected(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "wrong_host", "Alice", relayURL)
	intruder := newRelayNode(t, "wrong_joiner", "Mallory", relayURL)

	host.HostRoom("5151-amber-falcon-river")
	waitFor(t, "host registered", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})

	// Same public room ID, wrong secret words: the relay forwards the handshake, the host
	// cannot be verified and no group key is released.
	res := joinAndWait(t, intruder, "5151-amber-falcon-rain", 5*time.Second)
	if res.ok || !strings.Contains(strings.ToLower(res.reason), "wrong room key") {
		t.Fatalf("expected wrong room key rejection, got %+v", res)
	}
	if host.GetPeer("wrong_joiner") != nil {
		t.Fatal("intruder admitted")
	}
	intruder.mu.RLock()
	defer intruder.mu.RUnlock()
	if intruder.keyring != nil {
		t.Fatal("intruder obtained a group key")
	}
}

func TestRelayPINCheckedByHost(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "pin_host", "Alice", relayURL)
	host.HostRoom("6161-amber-falcon-river")
	host.LockRoom("4829")
	waitFor(t, "host registered", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	time.Sleep(100 * time.Millisecond) // lock_room reaches the relay

	noPin := newRelayNode(t, "pin_nopin", "Bob", relayURL)
	res := joinAndWait(t, noPin, "6161-amber-falcon-river", 5*time.Second)
	if res.ok || !strings.Contains(res.reason, "PIN") {
		t.Fatalf("expected PIN rejection, got %+v", res)
	}

	withPin := newRelayNode(t, "pin_ok", "Carol", relayURL)
	res = joinAndWait(t, withPin, "6161-amber-falcon-river:4829", 5*time.Second)
	if !res.ok {
		t.Fatalf("join with correct PIN failed: %+v", res)
	}
}

func TestLANWrongCodeRejected(t *testing.T) {
	host := NewP2PNode("lan_wrong_host", "Alice", NewAudioEngine())
	host.LanOnly, host.RelayURL = true, ""
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	joiner := NewP2PNode("lan_wrong_joiner", "Mallory", NewAudioEngine())
	joiner.LanOnly, joiner.RelayURL = true, ""
	if err := joiner.Start(); err != nil {
		t.Fatal(err)
	}
	defer joiner.Close()

	host.HostRoom("7171-amber-falcon-river")
	res := joinAndWait(t, joiner, "7171-amber-falcon-rain", 1500*time.Millisecond)
	if res.ok {
		t.Fatal("joined with a wrong room key")
	}
	if host.GetPeer("lan_wrong_joiner") != nil {
		t.Fatal("host admitted a joiner with a wrong room key")
	}
}

func TestGroupKeyRotatesWhenMemberLeaves(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "rk_host", "Alice", relayURL)
	stay := newRelayNode(t, "rk_stay", "Bob", relayURL)
	leave := newRelayNode(t, "rk_leave", "Eve", relayURL)

	var chat chatSink
	stay.OnChatMessage = chat.add

	code := "8181-amber-falcon-river"
	host.HostRoom(code)
	waitFor(t, "host registered", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	for _, n := range []*P2PNode{stay, leave} {
		if res := joinAndWait(t, n, code, 5*time.Second); !res.ok {
			t.Fatalf("%s join failed: %+v", n.LocalID, res)
		}
	}
	waitFor(t, "full mesh", 3*time.Second, func() bool { return len(host.GetPeersList()) == 2 })

	leave.mu.RLock()
	oldRing := leave.keyring
	leave.mu.RUnlock()
	startEpoch, _ := oldRing.Current()

	leave.LeaveRoom()
	waitFor(t, "rekey on host and remaining member", 8*time.Second, func() bool {
		host.mu.RLock()
		he, _ := host.keyring.Current()
		host.mu.RUnlock()
		stay.mu.RLock()
		se, _ := stay.keyring.Current()
		stay.mu.RUnlock()
		return he > startEpoch && se == he
	})

	// New traffic is sealed with a key the departed member never received.
	pkt := P2PPacket{Type: PacketChatMessage, RoomCode: host.roomID, SenderID: host.LocalID, Payload: []byte("secret"), Timestamp: time.Now().UnixMilli()}
	host.mu.RLock()
	sealed, err := sealPacket(&pkt, host.keyring)
	host.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	var out P2PPacket
	if err := openPacket(sealed, &out, oldRing); err == nil {
		t.Fatal("departed member can still decrypt new traffic")
	}

	host.SendChatMessage("after rekey")
	waitFor(t, "chat after rekey", 3*time.Second, func() bool { return chat.has("after rekey") })
}
