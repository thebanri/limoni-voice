package p2p

import (
	"testing"
	"time"
)

// A network change (Wi-Fi to mobile data, a VPN coming up) drops the relay connection. The
// member reconnects with its member token and stays in the room: same group key, and chat
// flows both ways again.
func TestRelayReconnectKeepsMemberInRoom(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "rc_host", "Alice", relayURL)
	member := newRelayNode(t, "rc_member", "Bob", relayURL)
	var hostChat, memberChat chatSink
	host.OnChatMessage = hostChat.add
	member.OnChatMessage = memberChat.add

	code := "9494-amber-falcon-river"
	host.HostRoom(code)
	waitFor(t, "host registered", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	if res := joinAndWait(t, member, code, 5*time.Second); !res.ok {
		t.Fatalf("join failed: %+v", res)
	}
	member.SendChatMessage("before")
	waitFor(t, "chat before the drop", 3*time.Second, func() bool { return hostChat.has("before") })

	member.mu.Lock()
	conn := member.wsConn
	member.mu.Unlock()
	if conn == nil {
		t.Fatal("member has no relay connection")
	}
	_ = conn.Close() // the network went away under the socket

	waitFor(t, "relay reconnected", 10*time.Second, func() bool {
		member.mu.RLock()
		defer member.mu.RUnlock()
		return member.isRelayConnected && member.wsConn != nil && member.wsConn != conn
	})
	member.mu.RLock()
	stillIn := member.IsConnected
	member.mu.RUnlock()
	if !stillIn {
		t.Fatal("member left the room after reconnecting")
	}
	member.SendChatMessage("after")
	waitFor(t, "member → host after reconnect", 5*time.Second, func() bool { return hostChat.has("after") })
	host.SendChatMessage("welcome back")
	waitFor(t, "host → member after reconnect", 5*time.Second, func() bool { return memberChat.has("welcome back") })
	if host.GetPeer("rc_member") == nil {
		t.Fatal("host lost the member")
	}
}

// The host's relay connection dropping must not end the room either: it reclaims the room
// with its host token and keeps admitting and talking.
func TestRelayReconnectKeepsHostsRoom(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "rch_host", "Alice", relayURL)
	member := newRelayNode(t, "rch_member", "Bob", relayURL)
	late := newRelayNode(t, "rch_late", "Carol", relayURL)
	var memberChat chatSink
	member.OnChatMessage = memberChat.add

	code := "9595-amber-falcon-river"
	host.HostRoom(code)
	waitFor(t, "host registered", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	if res := joinAndWait(t, member, code, 5*time.Second); !res.ok {
		t.Fatalf("join failed: %+v", res)
	}

	host.mu.Lock()
	conn := host.wsConn
	host.mu.Unlock()
	_ = conn.Close()
	waitFor(t, "host reconnected", 10*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.isRelayConnected && host.wsConn != nil && host.wsConn != conn && host.IsHost
	})

	host.SendChatMessage("still here")
	waitFor(t, "host → member after reconnect", 5*time.Second, func() bool { return memberChat.has("still here") })
	if res := joinAndWait(t, late, code, 5*time.Second); !res.ok {
		t.Fatalf("host no longer admits after reconnecting: %+v", res)
	}
}
