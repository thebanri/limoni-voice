package main

import "github.com/thebanri/limoni-voice/internal/e2ee"

// GenerateRoomCode generates a memorable room code (e.g. "7492-amber-falcon-river").
// The numeric prefix identifies the room on the relay; the words are the E2EE secret.
func GenerateRoomCode() string {
	return e2ee.GenerateRoomCode()
}

// NormalizeCode cleans and standardizes a room code
func NormalizeCode(code string) string {
	return e2ee.NormalizeCode(code)
}
