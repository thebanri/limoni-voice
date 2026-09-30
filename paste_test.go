package main

import (
	"strings"
	"testing"

	"github.com/thebanri/limoni/core/driver"
)

// parseReads feeds reads to Limoni's parser the way its backends' input loops do.
func parseReads(reads [][]byte) []driver.Event {
	var buf []byte
	var events []driver.Event
	for _, r := range reads {
		buf = append(buf, r...)
		for len(buf) > 0 {
			ev, n := driver.ParseBracketedPaste(buf)
			if n == 0 {
				ev, n = driver.ParseEvent(buf)
			}
			if n == 0 {
				break
			}
			events = append(events, ev)
			buf = buf[n:]
		}
	}
	return events
}

// A paste longer than one console read arrives in pieces; until its end marker comes the
// parser must wait instead of turning the text into key presses (which opened the settings
// with "t" and flipped options). Guards the patch in vendor/.../core/driver/parser.go, which
// `go mod vendor` removes.
func TestLongPasteArrivingInPieces(t *testing.T) {
	text := strings.Repeat("Relay: Connected (wss://relay) RTT 117ms\ttest mute deafen\n", 110) // ~6 KB
	stream := []byte("\x1b[200~" + text + "\x1b[201~" + "x")
	var reads [][]byte
	for len(stream) > 0 {
		n := min(512, len(stream))
		reads = append(reads, stream[:n])
		stream = stream[n:]
	}

	events := parseReads(reads)
	if len(events) != 2 {
		t.Fatalf("got %d events, want the paste and the key after it", len(events))
	}
	if events[0].Type != driver.EventPaste || events[0].Paste.Text != text {
		t.Fatalf("first event = %v, want the whole paste (%d bytes)", events[0].Type, len(text))
	}
	if events[1].Type != driver.EventKey || events[1].Key.Ch != 'x' {
		t.Fatalf("key typed after the paste was lost: %+v", events[1])
	}

	// The end marker split across reads.
	events = parseReads([][]byte{[]byte("\x1b[200~ab\x1b[20"), []byte("1~")})
	if len(events) != 1 || events[0].Paste.Text != "ab" {
		t.Fatalf("paste with a split end marker = %+v", events)
	}
}

// Pasting the network diagnostics (F12) into the chat: it arrives in several reads and must
// land in the input as one multi-line text, not be sent line by line.
func TestPasteDiagnosticsIntoChat(t *testing.T) {
	diag := `Local UDP :50000  Public 82.222.238.140:43280  NAT Cone (EIM)
IPv6: none (global address not available)
STUN: 162.159.207.0 → 82.222.238.140:43280 | 46.225.95.169 → 82.222.238.140:43280 | 74.125.250.129 → 82.222.238.140:43280
Relay: Connected (wss://relay.thebanri.dpdns.org/ws) RTT 117ms  UDP relay: unreachable (188.114.96.3:27850) → WebSocket fallback
E2EE group key epoch 17  Opus 0 kbps, FEC loss hint 0%
• TheBanri via P2P  ping 5ms  rx loss 0.0%  jitter 0ms  tx loss 0%  NAT Cone  addr 178.233.157.173:50000  last direct 0s ago
• User_8471 via P2P  ping 12ms  rx loss 0.0%  jitter 2ms  tx loss 0%  NAT Symmetric  addr 95.70.135.12:21299  last direct 0s ago

[03:10:44.949] [NET] 🎯 [STUN] Public endpoint 82.222.238.140:43280 (local :50000, NAT: Unknown)
[03:10:53.599] [ROOM] [CONNECT] Searching room '8968-kiwi-garnet-wolf' and verifying host...
[03:10:54.455] [ROOM] [RELAY] Connected to room 8968-kiwi-garnet-wolf! (Host: TheBanri | Internet E2EE)
[03:10:56.797] [ROOM] [SCREEN] User_8471 stopped screen sharing.`
	stream := []byte("\x1b[200~" + strings.ReplaceAll(diag, "\n", "\r\n") + "\x1b[201~")
	var reads [][]byte
	for len(stream) > 0 {
		n := min(256, len(stream))
		reads = append(reads, stream[:n])
		stream = stream[n:]
	}

	a := &App{room: NewRoomView(), currentScreen: ScreenRoom}
	sent := 0
	a.room.OnSendChat = func(string) { sent++ }
	for _, ev := range parseReads(reads) {
		if ev.Type != driver.EventPaste {
			t.Fatalf("paste split into a %v event", ev.Type)
		}
		a.handlePaste(ev.Paste.Text)
	}
	if sent != 0 || len(a.room.Messages) != 0 {
		t.Fatalf("pasting sent %d messages / added %d lines", sent, len(a.room.Messages))
	}
	if got := a.room.ChatInputState.Value(); got != diag {
		t.Fatalf("chat input holds %d bytes, want the %d pasted", len(got), len(diag))
	}
}
