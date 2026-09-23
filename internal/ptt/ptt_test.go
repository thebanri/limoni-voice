package ptt

import (
	"sync/atomic"
	"testing"
	"time"
)

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

func TestNormalizeKeyAliases(t *testing.T) {
	cases := map[string]string{
		"\t": "Tab", "\n": "Enter", "\r": "Enter", "space": "Space", "TAB": "Tab",
		"return": "Enter", "caps": "CapsLock", "rctrl": "RightCtrl", "Control_R": "RightCtrl",
		"altgr": "RightAlt", "Alt_R": "RightAlt", "xbutton1": "Mouse4", "XButton2": "Mouse5",
		" f11 ": "F11", "z": "Z", "0": "0",
	}
	for in, want := range cases {
		if got, ok := NormalizeKey(in); !ok || got != want {
			t.Errorf("NormalizeKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, k := range Keys {
		if got, ok := NormalizeKey(k); !ok || got != k {
			t.Errorf("canonical key %q does not normalise to itself (%q)", k, got)
		}
	}
}

func TestStartRejectsUnknownKey(t *testing.T) {
	if _, err := Start("F13", func(bool) {}); err == nil {
		t.Fatal("Start accepted an unsupported key")
	}
}

// The poller reports only edges, and a key still held when the watcher closes is
// released, so a closed watcher can never leave the microphone transmitting.
func TestPollerEdgesAndReleaseOnClose(t *testing.T) {
	var state atomic.Bool
	events := make(chan bool, 16)
	cleaned := make(chan struct{})
	p := startPoller("test", state.Load, func(pressed bool) { events <- pressed }, func() { close(cleaned) })
	if p.Backend() != "test" {
		t.Fatalf("backend = %q", p.Backend())
	}
	expect := func(want bool) {
		t.Helper()
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("event %v, want %v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("no %v event", want)
		}
	}
	state.Store(true)
	expect(true)
	time.Sleep(50 * time.Millisecond) // held: several polls, no repeated events
	state.Store(false)
	expect(false)
	state.Store(true)
	expect(true)

	_ = p.Close()
	_ = p.Close() // idempotent
	expect(false)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("cleanup not run on close")
	}
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %v after close", ev)
	default:
	}
}

// Every key the settings offer has a macOS virtual key code (mouse buttons are read apart).
func TestMacKeysCoverEveryKey(t *testing.T) {
	for _, k := range Keys {
		if k == "Mouse4" || k == "Mouse5" {
			continue
		}
		if _, ok := macKeys[k]; !ok {
			t.Errorf("no macOS key code for %q", k)
		}
	}
}
