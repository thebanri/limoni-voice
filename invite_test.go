package main

import (
	"strings"
	"testing"
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
