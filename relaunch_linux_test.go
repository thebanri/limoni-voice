package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The app menu's Alacritty on Wayland is reopened on X11 so dropped files reach the app; a
// shell's, a terminal that is not Alacritty, or a session without XWayland are left alone.
func TestNeedsX11Relaunch(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}
	getenv := func(k string) string { return env[k] }
	menu := []string{"alacritty", "-e", self, "limoni://join/x"}

	if !needsX11Relaunch(getenv, "/usr/bin/alacritty", menu) {
		t.Fatal("the menu's Alacritty on Wayland was not reopened")
	}
	if needsX11Relaunch(getenv, "/usr/bin/fish", []string{"fish"}) {
		t.Fatal("started from a shell, the window was reopened")
	}
	if needsX11Relaunch(getenv, "/usr/bin/konsole", []string{"konsole", "-e", self}) {
		t.Fatal("Konsole, which passes drops on, was reopened")
	}
	if needsX11Relaunch(getenv, "/usr/bin/alacritty", []string{"alacritty", "-e", "/usr/bin/htop"}) {
		t.Fatal("an Alacritty running another program was reopened")
	}
	env["LIMONI_NO_X11"] = "1"
	if needsX11Relaunch(getenv, "/usr/bin/alacritty", menu) {
		t.Fatal("LIMONI_NO_X11 did not turn it off")
	}
	delete(env, "LIMONI_NO_X11")
	delete(env, "DISPLAY")
	if needsX11Relaunch(getenv, "/usr/bin/alacritty", menu) {
		t.Fatal("reopened without XWayland to move to")
	}

	got := withoutEnv([]string{"A=1", "WAYLAND_DISPLAY=wayland-0", "WAYLAND_DISPLAY_X=2"}, "WAYLAND_DISPLAY")
	if !slices.Equal(got, []string{"A=1", "WAYLAND_DISPLAY_X=2"}) {
		t.Fatalf("withoutEnv: %q", got)
	}
}

// The invite link handler opens the app in the user's terminal: $TERMINAL, then Plasma's.
func TestUserTerminalChoice(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kdeglobals"), []byte("[General]\nTerminalApplication=alacritty\nTerminalService=Alacritty.desktop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got := kdeTerminal(); got != "alacritty" {
		t.Fatalf("kdeTerminal() = %q", got)
	}
	env := func(k string) string { return map[string]string{"TERMINAL": "foot"}[k] }
	if got := preferredTerminals(env, "alacritty"); got[0] != "foot" || got[1] != "alacritty" {
		t.Fatalf("order: %q", got[:3])
	}
	if got := preferredTerminals(func(string) string { return "" }, ""); got[0] != "xdg-terminal-exec" {
		t.Fatalf("without a choice: %q", got[:3])
	}
	if a := terminalExecArgs("gnome-terminal"); len(a) != 1 || a[0] != "--" {
		t.Fatalf("gnome-terminal args %q", a)
	}
}
