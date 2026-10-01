package main

import (
	"os"
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
