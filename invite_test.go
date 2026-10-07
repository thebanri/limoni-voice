package main

import (
	"strings"
	"testing"

	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
)

func TestParseJoinArg(t *testing.T) {
	const code = "8282-amber-falcon-river"
	for _, in := range []string{
		code,
		" " + code + " ",
		"limoni://join/" + code,
		"LIMONI://join/" + code + "/",
		"limoni:join/" + code,
		"limoni://" + code,
		"limoni://join/" + code + "?utm=x",
		"limoni://join/8282%2Damber%2Dfalcon%2Driver",
		"https://limoni-voice-website.vercel.app/join#" + code,
		"HTTPS://Limoni-Voice-Website.vercel.app/join/#" + code,
		"https://limoni-voice-website.vercel.app/join?ref=x#8282%2Damber%2Dfalcon%2Driver",
		inviteLink(code),
	} {
		got, ok := parseJoinArg(in)
		if !ok || got != NormalizeCode(code) {
			t.Errorf("parseJoinArg(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{
		"", "limoni://", "limoni://join/", "   ",
		"https://limoni-voice-website.vercel.app/join",
		"https://limoni-voice-website.vercel.app/join#",
		"https://limoni-voice-website.vercel.app/#" + code,
		"https://evil.example/join#" + code,
		"https://limoni-voice-website.vercel.app.evil.example/join#" + code,
	} {
		if got, ok := parseJoinArg(in); ok {
			t.Errorf("parseJoinArg(%q) accepted %q", in, got)
		}
	}
}

// The key rides in the fragment, escaped, so a custom key with spaces or a '#' survives.
func TestInviteLinkRoundTrip(t *testing.T) {
	for _, code := range []string{"8282-amber-falcon-river", "our room #2", "kod/ü?x"} {
		link := inviteLink(code)
		if !strings.HasPrefix(link, "https://"+inviteHost+"/join#") {
			t.Fatalf("inviteLink(%q) = %q", code, link)
		}
		if got, ok := parseJoinArg(link); !ok || got != NormalizeCode(code) {
			t.Errorf("parseJoinArg(inviteLink(%q)) = %q, %v", code, got, ok)
		}
	}
}

// A link from the URL handler only fills in the room and asks; --join joins.
func TestOpenInviteFromLinkNeedsConfirmation(t *testing.T) {
	const code = "8282-amber-falcon-river"
	lobby := NewLobbyView()
	a := &App{lobby: lobby}
	a.openInvite("", "limoni://join/"+code)
	if lobby.CodeState.Value() != code || lobby.ActiveInput != 1 {
		t.Fatalf("code not filled in: %q input=%d", lobby.CodeState.Value(), lobby.ActiveInput)
	}
	if a.pendingInvite != code {
		t.Fatalf("invite dialog not open: %q", a.pendingInvite)
	}

	// The window may pop up while the user is typing elsewhere: an Enter right away is dropped.
	a.handleInviteKey(driver.KeyEvent{Type: driver.KeyEnter})
	if a.pendingInvite != code || lobby.IsConnecting {
		t.Fatal("an Enter as the dialog opened joined the room")
	}

	a.handleInviteKey(driver.KeyEvent{Type: driver.KeyEsc})
	if a.pendingInvite != "" || lobby.IsConnecting {
		t.Fatalf("Esc did not close the dialog: %q", a.pendingInvite)
	}
	if lobby.CodeState.Value() != code {
		t.Fatalf("declining cleared the key: %q", lobby.CodeState.Value())
	}

	a.openInvite("", "limoni://join/")
	if lobby.ToastMsg != "Invalid room key or invite link" || a.pendingInvite != "" {
		t.Fatalf("toast = %q, pending = %q", lobby.ToastMsg, a.pendingInvite)
	}
}

// The invite dialog shows the room and its buttons answer clicks.
func TestInviteModalButtons(t *testing.T) {
	term, err := terminal.New(driver.NewPortableBackend(driver.NewMemoryTerminalIO(nil, 100, 30)))
	if err != nil {
		t.Fatal(err)
	}
	var joined, cancelled int
	draw := func() *buffer.Buffer {
		var buf *buffer.Buffer
		_ = term.Draw(func(f *terminal.Frame) {
			DrawInviteModal(f, f.Area(), "8282-amber-falcon-river", func() { joined++ }, func() { cancelled++ })
			buf = f.Buffer
		})
		return buf
	}
	find := func(buf *buffer.Buffer, text string) (uint16, uint16) {
		for y := uint16(0); y < buf.Area.Height; y++ {
			if x := strings.Index(screenText(buf, y), text); x >= 0 {
				return uint16(len([]rune(screenText(buf, y)[:x]))), y
			}
		}
		t.Fatalf("%q not on screen", text)
		return 0, 0
	}

	buf := draw()
	find(buf, "Join this room?")
	find(buf, "8282-amber-falcon-river")
	x, y := find(buf, "[Esc] Cancel")
	term.RouteMouseEvent(driver.MouseEvent{X: x + 1, Y: y, Button: driver.MouseLeft})
	if cancelled != 1 || joined != 0 {
		t.Fatalf("cancel click: joined=%d cancelled=%d", joined, cancelled)
	}

	x, y = find(draw(), "[Enter] Join")
	term.RouteMouseEvent(driver.MouseEvent{X: x + 1, Y: y, Button: driver.MouseLeft})
	if joined != 1 {
		t.Fatalf("join click: joined=%d cancelled=%d", joined, cancelled)
	}
}
