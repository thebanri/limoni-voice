package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

var adjectives = []string{
	"crimson", "azure", "neon", "cyber", "golden", "silent", "shadow", "cosmic",
	"hyper", "solar", "lunar", "swift", "brave", "frost", "mystic", "phantom",
	"quantum", "astral", "vivid", "electric", "velvet", "iron", "emerald", "amber",
	"sonic", "turbo", "radiant", "echo", "zenith", "crystal", "apex", "plasma",
}

var nouns = []string{
	"falcon", "wolf", "tiger", "hawk", "dragon", "viper", "eagle", "phoenix",
	"sound", "voice", "wave", "pulse", "echo", "spark", "beacon", "comet",
	"radar", "storm", "stream", "whisper", "signal", "node", "matrix", "nexus",
	"pilot", "orbit", "vortex", "cipher", "prism", "specter", "aurora", "pulsar",
}

// GenerateRoomCode generates a memorable Croc-like room code (e.g. "7492-neon-falcon")
func GenerateRoomCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(9000))
	num := n.Int64() + 1000 // 1000 - 9999

	adjIdx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(adjectives))))
	nounIdx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(nouns))))

	return fmt.Sprintf("%d-%s-%s", num, adjectives[adjIdx.Int64()], nouns[nounIdx.Int64()])
}

// NormalizeCode cleans and standardizes a room code
func NormalizeCode(code string) string {
	return strings.ToLower(strings.TrimSpace(code))
}
