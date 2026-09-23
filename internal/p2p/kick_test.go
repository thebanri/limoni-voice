package p2p

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A removed member is told so, leaves, is dropped by everyone and loses the group key; it
// may knock again. A banned member cannot come back.
func TestHostKicksAndBansMembers(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "kick_host", "Alice", relayURL)
	bob := newRelayNode(t, "kick_bob", "Bob", relayURL)
	carol := newRelayNode(t, "kick_carol", "Carol", relayURL)

	var bobKicked, carolBanned atomic.Bool
	bob.OnKicked = func(_ string, ban bool) { bobKicked.Store(!ban) }
	carol.OnKicked = func(_ string, ban bool) { carolBanned.Store(ban) }
	var carolChat chatSink
	carol.OnChatMessage = carolChat.add

	code := "5151-amber-falcon-river"
	host.HostRoom(code)
	waitFor(t, "host registered on relay", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	for _, n := range []*P2PNode{bob, carol} {
		if res := joinAndWait(t, n, code, 5*time.Second); !res.ok {
			t.Fatalf("%s could not join: %+v", n.Nickname, res)
		}
	}
	waitFor(t, "everyone sees everyone", 5*time.Second, func() bool {
		return len(host.GetPeersList()) == 2 && carol.GetPeer("kick_bob") != nil && bob.GetPeer("kick_carol") != nil
	})
	waitFor(t, "the join rekeys settle", 10*time.Second, func() bool {
		e := currentEpoch(host)
		return currentEpoch(bob) == e && currentEpoch(carol) == e
	})

	if err := carol.KickMember("kick_bob", false); err != ErrNotHost {
		t.Fatalf("a member removed someone: %v", err)
	}

	epoch := currentEpoch(host)
	if err := host.KickMember("kick_bob", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bob is told and leaves", 5*time.Second, func() bool {
		bob.mu.RLock()
		defer bob.mu.RUnlock()
		return bobKicked.Load() && !bob.IsConnected
	})
	waitFor(t, "carol drops bob", 5*time.Second, func() bool { return carol.GetPeer("kick_bob") == nil })
	waitFor(t, "the key rotates without bob", 10*time.Second, func() bool {
		return currentEpoch(host) > epoch && currentEpoch(carol) == currentEpoch(host)
	})
	host.SendChatMessage("after bob")
	waitFor(t, "the room keeps working", 5*time.Second, func() bool { return carolChat.has("after bob") })

	// A kick is not a ban: bob may join again.
	if res := joinAndWait(t, bob, code, 5*time.Second); !res.ok {
		t.Fatalf("kicked member could not come back: %+v", res)
	}
	waitFor(t, "bob is back", 5*time.Second, func() bool { return host.GetPeer("kick_bob") != nil })

	if err := host.KickMember("kick_carol", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "carol is banned and leaves", 5*time.Second, func() bool {
		carol.mu.RLock()
		defer carol.mu.RUnlock()
		return carolBanned.Load() && !carol.IsConnected
	})
	res := joinAndWait(t, carol, code, 5*time.Second)
	if res.ok {
		t.Fatal("banned member joined again")
	}
	if !strings.Contains(res.reason, "BANNED") && !strings.Contains(res.reason, "declined") {
		t.Fatalf("unexpected refusal: %+v", res)
	}
}
