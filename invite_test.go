package main

import "testing"

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
		inviteLink(code),
	} {
		got, ok := parseJoinArg(in)
		if !ok || got != NormalizeCode(code) {
			t.Errorf("parseJoinArg(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "limoni://", "limoni://join/", "   "} {
		if got, ok := parseJoinArg(in); ok {
			t.Errorf("parseJoinArg(%q) accepted %q", in, got)
		}
	}
}

// A link from the URL handler only fills in the room; --join joins.
func TestOpenInviteFromLinkNeedsConfirmation(t *testing.T) {
	lobby := NewLobbyView()
	a := &App{lobby: lobby}
	a.openInvite("", "limoni://join/8282-amber-falcon-river")
	if lobby.CodeState.Value() != "8282-amber-falcon-river" || lobby.ActiveInput != 1 {
		t.Fatalf("code not filled in: %q input=%d", lobby.CodeState.Value(), lobby.ActiveInput)
	}
	if lobby.IsConnecting {
		t.Fatal("joined a room from a link without confirmation")
	}

	a.openInvite("", "limoni://join/")
	if lobby.ToastMsg != "Invalid room key or invite link" {
		t.Fatalf("toast = %q", lobby.ToastMsg)
	}
}
