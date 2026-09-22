package main

import (
	"net/url"
	"strings"
)

// inviteScheme is the URL scheme registered by the installers (Linux desktop entry, Windows
// registry): limoni://join/<room code>.
const inviteScheme = "limoni"

// inviteLink returns the invite link for a room code. The code is the room's secret, so the
// link is exactly as sensitive as the code itself.
func inviteLink(code string) string {
	return inviteScheme + "://join/" + code
}

// parseJoinArg accepts a room code or an invite link as given on the command line (a URL
// handler passes the link as the first argument) and returns the room code.
func parseJoinArg(arg string) (string, bool) {
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(strings.ToLower(arg), inviteScheme+":") {
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

// openInvite handles a room given on the command line. --join, typed by the user, joins
// right away. A bare argument is what the limoni:// URL handler passes, and any web page
// can open such a link: joining on it would put the user, microphone live, in a room a
// stranger picked. That code is only filled in; pressing Enter joins.
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
	a.lobby.SetToast("Invite to room " + code + ": press Enter to join")
}
