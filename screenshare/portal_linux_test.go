//go:build linux

package screenshare

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

// The portal's "streams" result is a(ua{sv}); depending on how it was decoded it reaches
// parsePipewireNodeID in several shapes.
func TestParsePipewireNodeID(t *testing.T) {
	props := map[string]dbus.Variant{"source_type": dbus.MakeVariant(uint32(1))}
	cases := []struct {
		name string
		raw  any
		want uint32
	}{
		{"nil", nil, 0},
		{"bare id", uint32(42), 42},
		{"zero", uint32(0), 0},
		{"godbus structs", []any{[]any{uint32(57), props}}, 57},
		{"fixed pairs", [][2]any{{uint32(58), props}}, 58},
		{"array pairs", []any{[2]any{uint32(59), props}}, 59},
		{"map item", []any{map[string]any{"id": uint32(60)}}, 60},
		{"skips invalid first", []any{[]any{uint32(0)}, []any{uint32(61), props}}, 61},
		{"empty list", []any{}, 0},
		{"unrelated", "hello", 0},
	}
	for _, c := range cases {
		if got := parsePipewireNodeID(c.raw); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
