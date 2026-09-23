package main

import "github.com/thebanri/limoni-voice/internal/p2p"
import "testing"

func TestMatchPeer(t *testing.T) {
	peers := []*p2p.PeerInfo{{ID: "p1", Nickname: "Alice"}, {ID: "p2", Nickname: "Alina"}, {ID: "p3", Nickname: "Bob"}}
	for target, want := range map[string]string{"alice": "p1", "@Bob": "p3", "b": "p3", "p2": "p2"} {
		if p, why := matchPeer(peers, target); p == nil || p.ID != want {
			t.Fatalf("%q: got %v (%s), want %s", target, p, why, want)
		}
	}
	for _, target := range []string{"ali", "carol", ""} {
		if p, why := matchPeer(peers, target); p != nil || why == "" {
			t.Fatalf("%q matched %v", target, p)
		}
	}
}
