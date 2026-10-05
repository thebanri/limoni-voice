package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/thebanri/limoni-voice/internal/i18n"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
)

func renderLobby(lobby *LobbyView, w, h uint16) *buffer.Buffer {
	buf := buffer.NewBuffer(cell.NewRect(0, 0, w, h))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	lobby.Render(frame, cell.NewRect(0, 0, w, h))
	return buf
}

func lobbyRow(buf *buffer.Buffer, y uint16) string {
	var b strings.Builder
	for x := uint16(0); x < buf.Area.Width; x++ {
		if c := buf.Get(x, y); c != nil && c.Content != 0 {
			b.WriteRune(c.Content)
		} else {
			b.WriteRune(' ')
		}
	}
	return b.String()
}

func isBoxDrawing(r rune) bool { return r >= 0x2500 && r <= 0x257f }

// The cards' text used to run over their right border ("[F3] New Code", the
// shortcut lines): in either language, at any size, the borders stay whole.
func TestLobbyTextStaysInsideItsCards(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.English) })
	for _, lang := range []i18n.Lang{i18n.English, i18n.Turkish} {
		i18n.Set(lang)
		for _, sz := range [][2]uint16{{130, 40}, {100, 30}, {80, 24}} {
			for _, focus := range []int{0, 1, 2} {
				lobby := NewLobbyView()
				lobby.ActiveInput = focus
				lobby.IsPinProtected = focus == 2
				buf := renderLobby(lobby, sz[0], sz[1])
				for y := uint16(1); y < sz[1]-1; y++ {
					for _, x := range []uint16{sz[0] - 1, sz[0] - 2} {
						if r := buf.Get(x, y).Content; !isBoxDrawing(r) {
							t.Fatalf("%s %dx%d focus %d: row %d column %d holds %q, not a border:\n%s",
								lang, sz[0], sz[1], focus, y, x, r, lobbyRow(buf, y))
						}
					}
				}
			}
		}
	}
}

// A card out of focus has a dim border, but its title is drawn as text: in
// the border's colour it could hardly be read.
func TestLobbyTitlesAreReadable(t *testing.T) {
	theme := CurrentTheme()
	lobby := NewLobbyView()
	lobby.ActiveInput = 2
	buf := renderLobby(lobby, 130, 40)
	for _, title := range []string{"YOUR NICKNAME", "JOIN EXISTING ROOM", "INFO & SHORTCUTS"} {
		found := false
		for y := uint16(0); y < 40 && !found; y++ {
			row := lobbyRow(buf, y)
			i := strings.Index(row, title)
			if i < 0 {
				continue
			}
			x := utf8.RuneCountInString(row[:i]) // every cell here is one column
			found = true
			if fg := buf.Get(uint16(x), y).Style.Fg; fg != theme.TextMuted {
				t.Errorf("%q is drawn in %v, want the muted text colour %v", title, fg, theme.TextMuted)
			}
		}
		if !found {
			t.Errorf("%q is not on screen", title)
		}
	}
}

// Each shortcut is said once: the R, T and L buttons are not repeated in the
// lines under them.
func TestLobbyShortcutsAreNotRepeated(t *testing.T) {
	buf := renderLobby(NewLobbyView(), 130, 40)
	var screen strings.Builder
	for y := uint16(0); y < 40; y++ {
		screen.WriteString(lobbyRow(buf, y))
		screen.WriteByte('\n')
	}
	s := screen.String()
	for _, said := range []string{"Relay & Security Settings", "Mic", "Language", "English"} {
		if n := strings.Count(s, said); n > 1 {
			t.Errorf("%q is on screen %d times:\n%s", said, n, s)
		}
	}
	for _, still := range []string{"[Tab]", "[F2]/[C]", "[Esc] Exit"} {
		if !strings.Contains(s, still) {
			t.Errorf("%q is gone from the lobby", still)
		}
	}
}

// Typing with the room card selected went nowhere without a word; now the
// lobby says where typing goes. The card's own keys still work.
func TestTypingOnTheRoomCardSaysWhere(t *testing.T) {
	a := &App{lobby: NewLobbyView()}
	a.lobby.ActiveInput = 2
	a.handleLobbyKey(driver.KeyEvent{Type: driver.KeyRune, Ch: 'b'})
	if a.lobby.ToastMsg != "To type: 1 for your nickname, 3 for a room key" {
		t.Fatalf("typing b on the room card: toast %q", a.lobby.ToastMsg)
	}
	a.lobby.ToastMsg = ""
	a.handleLobbyKey(driver.KeyEvent{Type: driver.KeyRune, Ch: '1'})
	if a.lobby.ActiveInput != 0 || a.lobby.ToastMsg != "" {
		t.Fatalf("1 on the room card: input %d, toast %q", a.lobby.ActiveInput, a.lobby.ToastMsg)
	}
}
