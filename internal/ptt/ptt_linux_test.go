//go:build linux

package ptt

import (
	"errors"
	"testing"
)

func TestLinuxKeyMapping(t *testing.T) {
	for _, key := range Keys {
		if key == "Mouse4" || key == "Mouse5" {
			continue
		}
		if _, ok := x11Keysym(key); !ok {
			t.Errorf("no X11 keysym for %s", key)
		}
		if portalTrigger(key) == "" {
			t.Errorf("no portal trigger for %s", key)
		}
	}
	cases := map[string]string{"F9": "F9", "A": "a", "7": "7", "Space": "space", "RightAlt": "Alt_R", "Enter": "Return"}
	for key, want := range cases {
		if got := portalTrigger(key); got != want {
			t.Errorf("portalTrigger(%s) = %q, want %q", key, got, want)
		}
	}
	if sym, _ := x11Keysym("Q"); sym != 'q' {
		t.Errorf("X11 keysym for Q = %#x, want 'q'", sym)
	}
}

// Side mouse buttons are offered in the key list but Linux cannot watch them; the error
// says so instead of asking the portal to bind a key named "mouse4".
func TestLinuxRejectsMouseButtons(t *testing.T) {
	for _, key := range []string{"Mouse4", "Mouse5"} {
		if _, err := Start(key, func(bool) {}); !errors.Is(err, errMouseUnsupported) {
			t.Errorf("Start(%s) = %v, want errMouseUnsupported", key, err)
		}
	}
}
