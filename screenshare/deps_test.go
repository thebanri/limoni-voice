package screenshare

import (
	"strings"
	"testing"
)

func TestDistroFamily(t *testing.T) {
	cases := map[string]string{
		"NAME=\"CachyOS Linux\"\nID=cachyos\nID_LIKE=arch\n": "arch",
		"ID=ubuntu\nID_LIKE=debian\n":                        "debian",
		"ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n":          "debian",
		"ID=fedora\n": "fedora",
		"ID=\"opensuse-tumbleweed\"\nID_LIKE=\"opensuse suse\"\n": "suse",
		"ID=nixos\n": "",
		"":           "",
	}
	for osRelease, want := range cases {
		if got := distroFamily(osRelease); got != want {
			t.Errorf("distroFamily(%q) = %q, want %q", osRelease, got, want)
		}
	}
}

func TestInstallCommand(t *testing.T) {
	deps := []linuxDep{depGstPipewire, depGstBad, depGstBad, depGstUgly}
	if got, want := installCommand("debian", deps), "sudo apt install gstreamer1.0-pipewire gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly"; got != want {
		t.Errorf("debian: %q, want %q", got, want)
	}
	if got, want := installCommand("arch", []linuxDep{depMpv}), "sudo pacman -S --needed mpv"; got != want {
		t.Errorf("arch: %q, want %q", got, want)
	}
	if got := installCommand("", deps); got != "" {
		t.Errorf("unknown distro: %q, want empty", got)
	}
	for family, pkgs := range linuxPackages {
		if len(pkgs) != int(depMpv)+1 {
			t.Errorf("%s: %d packages, want one per dependency", family, len(pkgs))
		}
	}
}

func TestMissingDepsErrorMessage(t *testing.T) {
	err := &MissingDepsError{Missing: []string{"GStreamer pipewiresrc", "GStreamer x264enc"}, Install: "sudo apt install gstreamer1.0-pipewire"}
	if got := err.Error(); !strings.HasPrefix(got, "Missing tools to share your screen: GStreamer pipewiresrc, GStreamer x264enc.") ||
		!strings.HasSuffix(got, "Install them with: sudo apt install gstreamer1.0-pipewire") {
		t.Errorf("unexpected message %q", got)
	}
	watch := &MissingDepsError{Watching: true, Missing: []string{"mpv (or ffplay)"}}
	if got := watch.Error(); got != "Missing tools to watch screen shares: mpv (or ffplay). Install them with your package manager." {
		t.Errorf("unexpected message %q", got)
	}
}
