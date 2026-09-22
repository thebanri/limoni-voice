package main

import (
	"strings"
	"testing"
	"time"
)

// knockHost opens a room with knock-to-join and answers each knock with decide.
func knockHost(t *testing.T, host *P2PNode, code string, decide bool) chan string {
	t.Helper()
	knocked := make(chan string, 4)
	host.KnockToJoin = true
	host.OnKnock = func(id, nickname string) {
		knocked <- nickname
		time.Sleep(300 * time.Millisecond) // the host takes a moment
		host.ApproveJoin(id, decide)
	}
	host.HostRoom(code)
	return knocked
}

func TestKnockLetInViaRelay(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "kn_host", "Alice", relayURL)
	joiner := newRelayNode(t, "kn_join", "Bob", relayURL)
	var chat chatSink
	joiner.OnChatMessage = chat.add
	waiting := make(chan struct{}, 1)
	joiner.OnJoinWaiting = func() { waiting <- struct{}{} }

	code := "9191-amber-falcon-river"
	knocked := knockHost(t, host, code, true)
	waitFor(t, "host registered", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	if res := joinAndWait(t, joiner, code, 5*time.Second); !res.ok {
		t.Fatalf("join failed: %+v", res)
	}
	if nick := <-knocked; nick != "Bob" {
		t.Fatalf("knock from %q", nick)
	}
	select {
	case <-waiting:
	default:
		t.Fatal("joiner was not told it is waiting for the host")
	}
	waitFor(t, "joiner registered as peer", 3*time.Second, func() bool { return host.GetPeer("kn_join") != nil })
	host.SendChatMessage("welcome in")
	waitFor(t, "chat after approval", 3*time.Second, func() bool { return chat.has("welcome in") })
}

func TestKnockTurnedAwayViaRelay(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "kd_host", "Alice", relayURL)
	joiner := newRelayNode(t, "kd_join", "Eve", relayURL)

	code := "9292-amber-falcon-river"
	knockHost(t, host, code, false)
	waitFor(t, "host registered", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	res := joinAndWait(t, joiner, code, 5*time.Second)
	if res.ok {
		t.Fatal("joined although the host turned it away")
	}
	if !strings.Contains(res.reason, "declined") {
		t.Fatalf("reason = %q", res.reason)
	}
	joiner.mu.RLock()
	hasKey := joiner.keyring != nil
	joiner.mu.RUnlock()
	if hasKey || host.GetPeer("kd_join") != nil {
		t.Fatal("turned-away joiner got the room key or a peer slot")
	}
}

func TestKnockLetInOnLAN(t *testing.T) {
	host := NewP2PNode("knl_host", "Alice", NewAudioEngine())
	host.LanOnly, host.RelayURL = true, ""
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	joiner := NewP2PNode("knl_join", "Bob", NewAudioEngine())
	joiner.LanOnly, joiner.RelayURL = true, ""
	if err := joiner.Start(); err != nil {
		t.Fatal(err)
	}
	defer joiner.Close()

	knockHost(t, host, "9393-amber-falcon-river", true)
	if res := joinAndWait(t, joiner, "9393-amber-falcon-river", 5*time.Second); !res.ok {
		t.Fatalf("LAN join after approval failed: %+v", res)
	}
}

// Requests the host leaves unanswered are handed back once their window has passed.
func TestKnockQueueExpiry(t *testing.T) {
	var q knockQueue
	now := time.Now()
	q.add("a", "Ann", now.Add(-knockWindow-time.Second))
	q.add("b", "Bob", now)
	if q.add("b", "Bob", now) {
		t.Fatal("duplicate knock queued")
	}
	active, expired := q.front(now)
	if active == nil || active.id != "b" || len(expired) != 1 || expired[0].id != "a" {
		t.Fatalf("active=%v expired=%v", active, expired)
	}
	if r, ok := q.pop(); !ok || r.id != "b" {
		t.Fatal("pop did not return the waiting request")
	}
	if _, ok := q.pop(); ok {
		t.Fatal("queue not empty")
	}
}
