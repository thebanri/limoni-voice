// Package e2ee implements Limoni Voice room cryptography: room codes, the CPace-based
// join handshake that lets a joiner obtain the room group key from the host without
// revealing the room secret to the relay, and the epoch keyring used to seal packets.
package e2ee

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// Words is the 256-entry room code word list (8 bits per word).
var Words = [256]string{
	"acorn", "alpha", "amber", "anchor", "apex", "apple", "arrow", "aspen",
	"astral", "atlas", "aurora", "autumn", "azure", "badger", "bamboo", "banjo",
	"basil", "beacon", "beetle", "berry", "birch", "bison", "blaze", "bloom",
	"bolt", "bonsai", "breeze", "brick", "bronze", "cactus", "camel", "candle",
	"canyon", "carbon", "cedar", "cherry", "cipher", "citrus", "clover", "cobalt",
	"comet", "copper", "coral", "cosmic", "cotton", "coyote", "crane", "crater",
	"crimson", "crystal", "cyber", "daisy", "delta", "desert", "diesel", "dingo",
	"dolphin", "dragon", "drift", "dune", "eagle", "echo", "eclipse", "ember",
	"emerald", "falcon", "fern", "ferret", "fiesta", "fjord", "flame", "flint",
	"forest", "fossil", "fox", "frost", "galaxy", "garnet", "gecko", "geyser",
	"ginger", "glacier", "golden", "granite", "gravel", "harbor", "hawk", "hazel",
	"helix", "heron", "hickory", "honey", "horizon", "husky", "hyper", "igloo",
	"indigo", "iris", "iron", "island", "ivory", "jade", "jaguar", "jasmine",
	"jelly", "jester", "jungle", "kayak", "kernel", "kettle", "kiwi", "koala",
	"lagoon", "lantern", "laser", "lava", "lemon", "lemur", "lilac", "lime",
	"linen", "lion", "lotus", "lunar", "lynx", "magnet", "mango", "maple",
	"marble", "matrix", "meadow", "meteor", "mint", "mirror", "mocha", "monsoon",
	"moose", "mosaic", "mustang", "nebula", "neon", "nexus", "nickel", "nimbus",
	"noble", "nomad", "nova", "oasis", "ocean", "olive", "onyx", "opal",
	"orbit", "orca", "orchid", "otter", "oxygen", "panda", "papaya", "parrot",
	"pebble", "pepper", "phoenix", "pilot", "pine", "pixel", "planet", "plasma",
	"plum", "polar", "pollen", "prism", "puma", "pulsar", "pulse", "quartz",
	"quasar", "quill", "rabbit", "radar", "radish", "rain", "raven", "reef",
	"ripple", "river", "robin", "rocket", "ruby", "saffron", "sage", "salmon",
	"sapphire", "satin", "saturn", "shadow", "sierra", "signal", "silver", "sketch",
	"sky", "slate", "solar", "sonic", "spark", "sphinx", "spice", "spruce",
	"squid", "static", "stone", "storm", "summit", "sunset", "swift", "tango",
	"temple", "thunder", "tiger", "timber", "topaz", "tornado", "toucan", "tulip",
	"tundra", "turbo", "turtle", "umber", "unicorn", "valley", "vapor", "velvet",
	"venus", "violet", "viper", "vivid", "volcano", "vortex", "walnut", "walrus",
	"wander", "wave", "whisper", "willow", "winter", "wizard", "wolf", "yak",
	"yarrow", "yeti", "yodel", "zebra", "zenith", "zephyr", "zinc", "zircon",
}

// SecretWords is the number of secret words in a generated room code (24 bits).
const SecretWords = 3

// GenerateRoomCode returns a memorable code like "7492-amber-falcon-river".
// The 4-digit prefix is the public room identifier shared with the relay; the
// words are the room secret and never leave the client.
func GenerateRoomCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(9000))
	if err != nil {
		panic(err)
	}
	parts := []string{fmt.Sprintf("%d", n.Int64()+1000)}
	var idx [SecretWords]byte
	if _, err := rand.Read(idx[:]); err != nil {
		panic(err)
	}
	for _, b := range idx {
		parts = append(parts, Words[b])
	}
	return strings.Join(parts, "-")
}

// NormalizeCode cleans and standardizes a room code.
func NormalizeCode(code string) string {
	return strings.ToLower(strings.TrimSpace(code))
}

// SplitRoomCode derives the public room identifier and the handshake secret from a room code.
//
// For generated codes ("NNNN-words...") the numeric prefix is the room ID. Custom codes are
// mapped to a hashed ID; their secret strength is only as good as the chosen code.
func SplitRoomCode(code string) (roomID, secret string) {
	clean := NormalizeCode(code)
	if prefix, rest, ok := strings.Cut(clean, "-"); ok && rest != "" && len(prefix) >= 4 && len(prefix) <= 6 && isDigits(prefix) {
		return prefix, clean
	}
	sum := sha256.Sum256([]byte("limoni-room-id-v2:" + clean))
	return "x" + hex.EncodeToString(sum[:6]), clean
}

// IsStrongCode reports whether a code carries at least SecretWords list words after the room ID.
func IsStrongCode(code string) bool {
	clean := NormalizeCode(code)
	prefix, rest, ok := strings.Cut(clean, "-")
	if !ok || !isDigits(prefix) {
		return false
	}
	words := strings.Split(rest, "-")
	if len(words) < SecretWords {
		return false
	}
	for _, w := range words {
		if !isWord(w) {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isWord(w string) bool {
	for _, x := range Words {
		if x == w {
			return true
		}
	}
	return false
}
