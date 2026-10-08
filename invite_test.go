package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/p2p"
	"github.com/thebanri/limoni/animation"
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

// The invite dialog shows the room and its buttons answer clicks; in a room it says that
// joining leaves it.
func TestInviteModalButtons(t *testing.T) {
	for _, leaving := range []bool{false, true} {
		testInviteModalButtons(t, leaving)
	}
}

func testInviteModalButtons(t *testing.T, leaving bool) {
	term, err := terminal.New(driver.NewPortableBackend(driver.NewMemoryTerminalIO(nil, 100, 30)))
	if err != nil {
		t.Fatal(err)
	}
	var joined, cancelled int
	draw := func() *buffer.Buffer {
		var buf *buffer.Buffer
		_ = term.Draw(func(f *terminal.Frame) {
			DrawInviteModal(f, f.Area(), "8282-amber-falcon-river", leaving, func() { joined++ }, func() { cancelled++ })
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
	if leaving {
		find(buf, "You will leave the room you are in.")
	}
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

// An invite link opened while the app is in a room asks there, over the room, and joining
// leaves the room; the room's own link only says so.
func TestReceiveInviteInRoom(t *testing.T) {
	term, err := terminal.New(driver.NewPortableBackend(driver.NewMemoryTerminalIO(nil, 120, 30)))
	if err != nil {
		t.Fatal(err)
	}
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("invite_in_room", "Alice", audio)
	defer node.Close()
	node.HostRoom("5759-other-room-words")
	a := &App{term: term, room: NewRoomView(), lobby: NewLobbyView(), node: node, audio: audio,
		notifier: newDesktopNotifier(), currentScreen: ScreenRoom, leaveDialogAnim: animation.NewFloat(0)}

	a.receiveInvite(inviteLink(node.RoomCode))
	if a.pendingInvite != "" || a.room.ToastMsg != "You are already in this room" {
		t.Fatalf("own room's link: pending %q, toast %q", a.pendingInvite, a.room.ToastMsg)
	}

	a.openLeaveModal()
	const code = "8282-amber-falcon-river"
	a.receiveInvite(inviteLink(code))
	if a.pendingInvite != code || a.showLeaveModal {
		t.Fatalf("pending %q, leave dialog still open: %v", a.pendingInvite, a.showLeaveModal)
	}
	if a.lobby.CodeState.Value() == code {
		t.Fatal("the lobby's key changed before the user answered")
	}

	// Keys go to the question, not to the room underneath.
	a.inviteShownAt = time.Now().Add(-time.Second)
	a.handleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: 'm'})
	if a.pendingInvite != code || audio.Muted {
		t.Fatal("a key reached the room while the invite dialog was open")
	}
	a.handleKey(driver.KeyEvent{Type: driver.KeyEsc})
	if a.pendingInvite != "" || a.currentScreen != ScreenRoom {
		t.Fatal("declining did not keep the app in its room")
	}
}

// A process started for an invite link hands it to the open app, which then owns the
// socket alone; with the app gone, the next one takes over the leftover socket file.
func TestInviteForwardedToOpenApp(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "invite.sock")
	if forwardInvite(path, "limoni://join/1") {
		t.Fatal("an invite was taken with no app open")
	}

	got := make(chan string, 1)
	ln := listenForInvites(path, func(link string) { got <- link })
	if ln == nil {
		t.Skip("no unix sockets here")
	}
	if second := listenForInvites(path, func(string) {}); second != nil {
		second.Close()
		t.Fatal("a second copy took the socket of the open one")
	}

	const link = "https://" + inviteHost + "/join#8282-amber-falcon-river"
	if !forwardInvite(path, link+"\n") {
		t.Fatal("the open app did not take the invite")
	}
	select {
	case l := <-got:
		if l != link {
			t.Fatalf("delivered %q, want %q", l, link)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the invite never reached the app")
	}
	ln.Close()
	if forwardInvite(path, link) {
		t.Fatal("an invite was taken after the app closed")
	}

	// A copy that crashed leaves its socket file behind.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln = listenForInvites(path, func(string) {})
	if ln == nil {
		t.Fatal("a leftover socket file kept the app from listening")
	}
	ln.Close()
}

// shortTempDir is a temporary directory with a path short enough for a unix socket (macOS
// allows 104 bytes, and t.TempDir can be longer).
func shortTempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "lv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
