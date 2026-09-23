package p2p

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseRelayList(t *testing.T) {
	saved := DefaultRelayFallbacks
	DefaultRelayFallbacks = []string{"wss://backup.example/ws"}
	t.Cleanup(func() { DefaultRelayFallbacks = saved })

	cases := map[string][]string{
		"":                                  {DefaultRelayURL, "wss://backup.example/ws"},
		"default":                           {DefaultRelayURL, "wss://backup.example/ws"},
		"relay.one.com":                     {"wss://relay.one.com/ws"},
		"relay.one.com, 192.168.1.3:27850":  {"wss://relay.one.com/ws", "ws://192.168.1.3:27850/ws"},
		"relay.one.com relay.one.com,a.com": {"wss://relay.one.com/ws", "wss://a.com/ws"},
		"lan":                               nil,
		"relay.one.com, off":                nil,
	}
	for in, want := range cases {
		if got := ParseRelayList(in); !slices.Equal(got, want) {
			t.Errorf("ParseRelayList(%q) = %v, want %v", in, got, want)
		}
	}
	if got := ParseRelayList(FormatRelayList([]string{"wss://a.com/ws", "ws://b:1/ws"})); !slices.Equal(got, []string{"wss://a.com/ws", "ws://b:1/ws"}) {
		t.Errorf("round trip lost relays: %v", got)
	}
}

// deadRelayURL returns the address of a relay that is no longer listening.
func deadRelayURL(t *testing.T) string {
	hs := httptest.NewServer(nil)
	u := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	hs.Close()
	return u
}

// A host whose primary relay is down opens the room on its backup; a joiner whose primary
// relay is up but does not have the room asks the backup and finds it.
func TestRelayFallback(t *testing.T) {
	relayA, _ := startTestRelay(t)
	relayB, _ := startTestRelay(t)
	dead := deadRelayURL(t)

	host := newRelayNode(t, "fb_host", "Alice", relayB)
	host.SetRelays([]string{dead, relayB})
	var hostChat chatSink
	host.OnChatMessage = hostChat.add
	joiner := newRelayNode(t, "fb_joiner", "Bob", relayA)
	joiner.SetRelays([]string{relayA, relayB})

	code := "6262-amber-falcon-river"
	host.HostRoom(code)
	waitFor(t, "host registered on its backup relay", 5*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	if got := host.ActiveRelayURL(); got != relayB {
		t.Fatalf("host is on %s, want the backup %s", got, relayB)
	}

	if res := joinAndWait(t, joiner, code, 8*time.Second); !res.ok {
		t.Fatalf("join failed: %+v", res)
	}
	if got := joiner.ActiveRelayURL(); got != relayB {
		t.Fatalf("joiner is on %s, want %s", got, relayB)
	}

	joiner.SendChatMessage("found you")
	waitFor(t, "chat over the backup relay", 5*time.Second, func() bool { return hostChat.has("found you") })
}

// With every relay asked and none having the room, the join fails as before.
func TestRelayFallbackGivesUp(t *testing.T) {
	relayA, _ := startTestRelay(t)
	relayB, _ := startTestRelay(t)
	joiner := newRelayNode(t, "fb_lonely", "Bob", relayA)
	joiner.SetRelays([]string{relayA, relayB})
	res := joinAndWait(t, joiner, "7373-amber-falcon-river", 6*time.Second)
	if res.ok || res.reason == "" {
		t.Fatalf("join of a room nobody hosts: %+v", res)
	}
}
