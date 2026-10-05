package main

import "testing"

// In LAN mode (-lan, or LAN chosen in the relay settings) the relay address is
// empty. The lobby took "" for the default relay, probed the public server and
// showed RELAY: ONLINE; it must not reach for the network at all.
func TestLANModeDoesNotProbeTheRelay(t *testing.T) {
	a := &App{lobby: NewLobbyView()}
	a.probeRelayStatus("", "")
	if a.lobby.RelayStatus != "LAN Mode" || a.lobby.RelayOnline {
		t.Fatalf("LAN mode: status %q, online %v", a.lobby.RelayStatus, a.lobby.RelayOnline)
	}
}
