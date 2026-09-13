package ptt

import "testing"

func TestNormalizeKey(t *testing.T) {
	cases := map[string]string{" ": "Space", "f9": "F9", "capslock": "CapsLock", "a": "A", "RALT": "RightAlt", "mouse5": "Mouse5"}
	for in, want := range cases {
		if got, ok := NormalizeKey(in); !ok || got != want {
			t.Fatalf("NormalizeKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := NormalizeKey("F13"); ok {
		t.Fatal("unsupported key accepted")
	}
	if !IsTypingKey("Space") || IsTypingKey("F9") {
		t.Fatal("typing key classification wrong")
	}
}
