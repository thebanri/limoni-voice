package p2p

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestChatHistoryRoundTrip(t *testing.T) {
	ts := time.UnixMilli(1_700_000_000_123)
	in := []ChatEntry{
		{SenderID: "a", Nickname: "Ayşe", Text: "merhaba", Timestamp: ts},
		{SenderID: "b", Nickname: "", Text: strings.Repeat("x", 16384), Timestamp: ts.Add(time.Second)},
		{SenderID: "c", Nickname: "Can", Text: "son", Timestamp: ts.Add(2 * time.Second)},
	}
	var out []ChatEntry
	payloads := encodeChatHistory(in)
	if len(payloads) < 2 {
		t.Fatalf("a 16 KB message shares a payload: %d payloads", len(payloads))
	}
	for _, p := range payloads {
		out = append(out, decodeChatHistory(p)...)
	}
	if len(out) != len(in) {
		t.Fatalf("got %d entries, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("entry %d differs: got %q from %q, want %q from %q", i, out[i].Nickname, out[i].SenderID, in[i].Nickname, in[i].SenderID)
		}
	}
	// A truncated payload yields the entries before the damage and nothing invented.
	p := encodeChatHistory(in[:1])[0]
	if got := decodeChatHistory(p[:len(p)-2]); len(got) != 0 {
		t.Fatalf("truncated payload decoded to %+v", got)
	}
}

func TestRecordChatDropsRepeatsAndKeepsTheNewest(t *testing.T) {
	n := &P2PNode{}
	e := ChatEntry{SenderID: "a", Text: "hi", Timestamp: time.UnixMilli(1)}
	if !n.recordChat(e) || n.recordChat(e) {
		t.Fatal("a repeated message was recorded twice")
	}
	for i := range chatHistoryLimit + 5 {
		n.recordChat(ChatEntry{SenderID: "a", Text: "m", Timestamp: time.UnixMilli(int64(i + 10))})
	}
	if len(n.chatLog) != chatHistoryLimit || len(n.chatSeen) != chatHistoryLimit {
		t.Fatalf("history holds %d (%d keys), want %d", len(n.chatLog), len(n.chatSeen), chatHistoryLimit)
	}
	if !n.recordChat(e) {
		t.Fatal("a message trimmed from the history is still treated as known")
	}
}

type historySink struct {
	mu      sync.Mutex
	entries []ChatEntry
}

func (h *historySink) add(es []ChatEntry) {
	h.mu.Lock()
	h.entries = append(h.entries, es...)
	h.mu.Unlock()
}

func (h *historySink) texts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.entries {
		out = append(out, e.Text)
	}
	return out
}

// A member who joins sees what was said before it came in, once.
func TestJoinerReceivesEarlierChat(t *testing.T) {
	saved := chatHistoryDelays
	chatHistoryDelays = []time.Duration{100 * time.Millisecond, 400 * time.Millisecond}
	t.Cleanup(func() { chatHistoryDelays = saved })

	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "hist_host", "Alice", relayURL)
	first := newRelayNode(t, "hist_first", "Bob", relayURL)
	late := newRelayNode(t, "hist_late", "Carol", relayURL)

	var lateHistory historySink
	late.OnChatHistory = lateHistory.add
	var lateChat chatSink
	late.OnChatMessage = lateChat.add

	code := "4242-amber-falcon-lake"
	host.HostRoom(code)
	waitFor(t, "host registered on relay", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	host.SendChatMessage("before anyone") // the host was alone: kept all the same
	if res := joinAndWait(t, first, code, 5*time.Second); !res.ok {
		t.Fatalf("join failed: %+v", res)
	}
	waitFor(t, "host sees Bob", 3*time.Second, func() bool { return host.GetPeer("hist_first") != nil })
	first.SendChatMessage("from bob")
	waitFor(t, "host has Bob's message", 3*time.Second, func() bool {
		host.chatMu.Lock()
		defer host.chatMu.Unlock()
		return len(host.chatLog) == 2
	})

	if res := joinAndWait(t, late, code, 5*time.Second); !res.ok {
		t.Fatalf("late join failed: %+v", res)
	}
	waitFor(t, "Carol gets the earlier chat", 5*time.Second, func() bool { return len(lateHistory.texts()) >= 2 })
	time.Sleep(time.Second) // every member sends twice; none of it may show again

	got := lateHistory.texts() // arrival order depends on which member's copy lands first
	slices.Sort(got)
	if len(got) != 2 || got[0] != "before anyone" || got[1] != "from bob" {
		t.Fatalf("Carol's history = %q", got)
	}
	if lateChat.has("before anyone") || lateChat.has("from bob") {
		t.Fatal("history arrived as new chat messages")
	}
}

// A long message is cut between characters, never inside one (ş, ğ, emoji are several bytes).
func TestClipChatKeepsCharactersWhole(t *testing.T) {
	text := strings.Repeat("a", MaxChatBytes-1) + "şğü"
	got := ClipChat(text)
	if len(got) > MaxChatBytes || !utf8.ValidString(got) || got != strings.Repeat("a", MaxChatBytes-1) {
		t.Fatalf("clipped to %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if ClipChat("kısa") != "kısa" {
		t.Fatal("a short message changed")
	}
}

func TestChatPartRoundTrip(t *testing.T) {
	e := ChatEntry{SenderID: "a", Nickname: "Ayşe", Timestamp: time.UnixMilli(1_700_000_000_123)}
	text := strings.Repeat("günlük satırı\n", 5000) // 70 KB, split inside ü and ı
	parts := splitChat(text)
	if len(parts) != (len(text)+chatPartSize-1)/chatPartSize {
		t.Fatalf("%d parts for %d bytes", len(parts), len(text))
	}
	var joined []byte
	for i, p := range parts {
		got, history, idx, total, chunk, ok := decodeChatPart(encodeChatPart(e, true, i, len(parts), p))
		if !ok || !history || idx != i || total != len(parts) || got.SenderID != "a" || got.Nickname != "Ayşe" || !got.Timestamp.Equal(e.Timestamp) {
			t.Fatalf("part %d decoded to %+v %v %d/%d ok=%v", i, got, history, idx, total, ok)
		}
		joined = append(joined, chunk...)
	}
	if string(joined) != text {
		t.Fatal("parts do not put the text back together")
	}
	if _, _, _, _, _, ok := decodeChatPart([]byte{0, 1, 'a'}); ok {
		t.Fatal("a truncated part decoded")
	}
}

type fullChatSink struct {
	mu    sync.Mutex
	texts []string
}

func (c *fullChatSink) add(_, _ string, text string, _ time.Time) {
	c.mu.Lock()
	c.texts = append(c.texts, text)
	c.mu.Unlock()
}

func (c *fullChatSink) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.texts...)
}

// A message longer than one packet arrives whole and once, live and in a joiner's history.
func TestLongChatMessageArrivesWhole(t *testing.T) {
	saved := chatHistoryDelays
	chatHistoryDelays = []time.Duration{100 * time.Millisecond, 400 * time.Millisecond}
	t.Cleanup(func() { chatHistoryDelays = saved })

	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "long_host", "Alice", relayURL)
	bob := newRelayNode(t, "long_bob", "Bob", relayURL)
	late := newRelayNode(t, "long_late", "Carol", relayURL)
	var hostChat fullChatSink
	host.OnChatMessage = hostChat.add
	var lateHistory historySink
	late.OnChatHistory = lateHistory.add

	code := "4242-amber-falcon-pond"
	host.HostRoom(code)
	waitFor(t, "host registered on relay", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	if res := joinAndWait(t, bob, code, 5*time.Second); !res.ok {
		t.Fatalf("join failed: %+v", res)
	}
	waitFor(t, "host sees Bob", 3*time.Second, func() bool { return host.GetPeer("long_bob") != nil })

	long := strings.TrimSpace(strings.Repeat("[15:43:24] uzun log satırı çğış\n", 3200)) // ~120 KB
	bob.SendChatMessage(long)
	waitFor(t, "the long message", 5*time.Second, func() bool { return len(hostChat.all()) > 0 })
	time.Sleep(500 * time.Millisecond) // the relay's copy of every part has come too
	if got := hostChat.all(); len(got) != 1 || got[0] != long {
		t.Fatalf("host got %d messages, first %d bytes (want 1 of %d)", len(got), len(got[0]), len(long))
	}

	if res := joinAndWait(t, late, code, 5*time.Second); !res.ok {
		t.Fatalf("late join failed: %+v", res)
	}
	waitFor(t, "Carol gets the long message", 5*time.Second, func() bool { return len(lateHistory.texts()) > 0 })
	time.Sleep(time.Second)
	if got := lateHistory.texts(); len(got) != 1 || got[0] != long {
		t.Fatalf("Carol's history has %d messages (want the long one once)", len(got))
	}
}
