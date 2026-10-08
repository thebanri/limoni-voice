package main

import (
	"net/url"
	"strings"
	"time"

	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
)

// inviteScheme is the URL scheme registered by the installers (Linux desktop entry, Windows
// registry, the macOS app's Info.plist): limoni://join/<room code>.
const inviteScheme = "limoni"

// inviteHost serves the join page (website/public/join): chat apps only make http(s) links
// clickable, so the shared link is https://<inviteHost>/join#<room code> and the page opens
// limoni://join/<room code>. The code rides in the fragment, which browsers never send to the
// server, so it stays out of the host's logs and out of link-preview fetches.
const inviteHost = "limoni-voice-website.vercel.app"

// inviteLink returns the invite link for a room code. The code is the room's secret, so the
// link is exactly as sensitive as the code itself.
func inviteLink(code string) string {
	return "https://" + inviteHost + "/join#" + url.PathEscape(code)
}

// parseJoinArg accepts a room code or an invite link (the https join page or limoni://), as
// given on the command line, by the URL handler or pasted into the lobby, and returns the
// room code. Any other http(s) URL is rejected rather than taken as a custom room code.
func parseJoinArg(arg string) (string, bool) {
	arg = strings.TrimSpace(arg)
	if lower := strings.ToLower(arg); strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		u, err := url.Parse(arg)
		if err != nil || !strings.EqualFold(u.Hostname(), inviteHost) || strings.TrimSuffix(u.Path, "/") != "/join" {
			return "", false
		}
		arg = u.Fragment
	} else if strings.HasPrefix(strings.ToLower(arg), inviteScheme+":") {
		rest := arg[len(inviteScheme)+1:]
		rest = strings.TrimPrefix(rest, "//")
		if i := strings.IndexAny(rest, "?#"); i >= 0 {
			rest = rest[:i]
		}
		rest = strings.Trim(rest, "/")
		if lower := strings.ToLower(rest); lower == "join" {
			rest = "" // limoni://join/ without a room key
		} else if strings.HasPrefix(lower, "join/") {
			rest = rest[len("join/"):]
		}
		if unescaped, err := url.PathUnescape(rest); err == nil {
			rest = unescaped
		}
		arg = rest
	}
	code := NormalizeCode(arg)
	return code, code != ""
}

// inviteArmDelay is how long the invite dialog ignores keys after it opens: the terminal
// window can pop up while the user is typing elsewhere, and a stray Enter must not join.
const inviteArmDelay = 700 * time.Millisecond

// openInvite handles a room given on the command line. --join, typed by the user, joins
// right away. A bare argument is what the limoni:// URL handler passes, and any web page
// can open such a link: joining on it would put the user, microphone live, in a room a
// stranger picked. That code is only filled in, and a dialog asks before joining.
func (a *App) openInvite(joinFlag, urlArg string) {
	arg, confirmed := joinFlag, true
	if strings.TrimSpace(arg) == "" {
		arg, confirmed = urlArg, false
	}
	if strings.TrimSpace(arg) == "" {
		return
	}
	code, ok := parseJoinArg(arg)
	if !ok {
		a.lobby.SetToast("Invalid room key or invite link")
		return
	}
	a.lobby.CodeState.SetValue(code)
	a.lobby.ActiveInput = 1
	if confirmed {
		a.joinRoom(code)
		return
	}
	a.pendingInvite, a.inviteShownAt = code, time.Now()
}

// receiveInvite takes an invite link that a process started for it handed over (see
// instance.go): the app was already open, maybe in a room. Like a link the app was started
// with, it only fills in the room and asks.
func (a *App) receiveInvite(link string) {
	code, ok := parseJoinArg(link)
	if !ok {
		a.toast("Invalid room key or invite link")
		return
	}
	a.notifier.NotifyNow(notifyTitle, T("Invite link opened: confirm in Limoni Voice"))
	if a.currentScreen == ScreenRoom && code == a.node.RoomCode {
		a.toast("You are already in this room")
		return
	}
	// The question goes on top of the app's own dialogs. A file offer or a knock stays above
	// it: someone else waits on those.
	if a.showRelayModal {
		a.closeRelayModal()
	}
	if a.showTestModal {
		a.closeTestModal()
	}
	if a.showExitModal {
		a.closeExitModal()
	}
	if a.showDebugModal {
		a.closeDebugModal()
	}
	if a.showLeaveModal {
		a.closeLeaveModal()
	}
	if a.showScreenShareModal {
		a.closeScreenShareModal()
	}
	if a.currentScreen != ScreenRoom {
		a.lobby.CodeState.SetValue(code)
		a.lobby.ActiveInput = 1
	}
	a.pendingInvite, a.inviteShownAt = code, time.Now()
}

// acceptInvite joins the room of the invite dialog, leaving the room the app is in, or
// dropping the search for another one.
func (a *App) acceptInvite() {
	code := a.pendingInvite
	a.pendingInvite = ""
	if code == "" {
		return
	}
	if a.currentScreen == ScreenRoom {
		a.leaveRoom()
	} else if a.lobby.IsConnecting {
		a.node.CancelJoin()
		a.lobby.IsConnecting = false
	}
	a.lobby.CodeState.SetValue(code)
	a.lobby.ActiveInput = 1
	a.joinRoom(code)
}

// declineInvite closes the invite dialog. The key stays in the join field.
func (a *App) declineInvite() {
	a.pendingInvite = ""
}

func (a *App) handleInviteKey(e driver.KeyEvent) {
	switch {
	case e.Type == driver.KeyEsc, e.Type == driver.KeyRune && strings.ContainsRune("nNhH", e.Ch):
		a.declineInvite()
	case time.Since(a.inviteShownAt) < inviteArmDelay:
	case e.Type == driver.KeyEnter, e.Type == driver.KeyRune && strings.ContainsRune("yYeE", e.Ch):
		a.acceptInvite()
	}
}

// DrawInviteModal asks whether to join the room an invite link opened the app with; leaving
// says the app is in another room, which joining leaves. It stays until answered, unlike a
// toast, so the key filled in by a link is never a silent surprise.
func DrawInviteModal(frame *terminal.Frame, screenArea cell.Rect, code string, leaving bool, onJoin, onCancel func()) {
	if code == "" {
		return
	}
	modalW, modalH := uint16(58), uint16(8)
	if leaving {
		modalH++
	}
	if screenArea.Width < modalW+2 {
		modalW = screenArea.Width - 2
	}
	if screenArea.Height < modalH+2 {
		return
	}
	area := terminal.CenterRect(screenArea, modalW, modalH)
	theme := CurrentTheme()
	buf := frame.Buffer
	widgets.DrawShadow(buf, area, 2, 1)
	openModal(frame, "invite_dialog", area, onCancel)
	defer frame.EndLayer()
	for y := area.Y; y < area.Y+area.Height; y++ {
		for x := area.X; x < area.X+area.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}
	block := widgets.Block{
		Title:          T(" 🍋 INVITE "),
		TitleAlignment: widgets.AlignCenter,
		Borders:        widgets.BorderAll,
		BorderSymbols:  widgets.SymbolsRounded,
		BorderStyle:    cell.Style{Fg: theme.Warning, Modifier: cell.ModifierBold},
		Style:          cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)
	if inner.Height < modalH-2 || inner.Width < 20 {
		return
	}
	w := int(inner.Width) - 2
	putClipped(buf, inner.X+1, inner.Y+1, w, T("Join this room?"), cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
	putClipped(buf, inner.X+1, inner.Y+2, w, code, cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
	putClipped(buf, inner.X+1, inner.Y+3, w, T("Join only if you know who sent you the link."), cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg})
	if leaving {
		putClipped(buf, inner.X+1, inner.Y+4, w, T("You will leave the room you are in."), cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg})
	}

	join := T(" [Enter] Join ")
	cancel := T(" [Esc] Cancel ")
	joinX := inner.X + 1
	cancelX := joinX + uint16(len([]rune(join))) + 2
	buttonY := inner.Y + inner.Height - 1
	buf.SetString(joinX, buttonY, join, cell.Style{Fg: cell.NewColorRGB(0, 0, 0), Bg: theme.Success, Modifier: cell.ModifierBold})
	buf.SetString(cancelX, buttonY, cancel, cell.Style{Fg: theme.Text, Bg: theme.InputBg, Modifier: cell.ModifierBold})
	clickable(frame, cell.NewRect(joinX, buttonY, uint16(len([]rune(join))), 1), func(_ driver.MouseEvent) { onJoin() })
	clickable(frame, cell.NewRect(cancelX, buttonY, uint16(len([]rune(cancel))), 1), func(_ driver.MouseEvent) { onCancel() })
}
