package screenshare

import "testing"

func TestParseResolution(t *testing.T) {
	cases := []struct {
		in           string
		wantW, wantH int
	}{
		{"1920x1080", 1920, 1080},
		{"1281x721", 1280, 720}, // encoders need even dimensions
		{"", 1920, 1080},
		{"garbage", 1920, 1080},
		{"0x-5", 1920, 1080},
		{"800x", 800, 1080},
	}
	for _, c := range cases {
		if w, h := parseResolution(c.in, 1920, 1080); w != c.wantW || h != c.wantH {
			t.Errorf("parseResolution(%q) = %dx%d, want %dx%d", c.in, w, h, c.wantW, c.wantH)
		}
	}
}

func TestInstallHint(t *testing.T) {
	only := func(bin string) func(string) bool { return func(b string) bool { return b == bin } }
	cases := []struct {
		goos string
		has  func(string) bool
		pkgs []string
		want string
	}{
		{"windows", nil, []string{"mpv", "ffmpeg"}, "winget install -e --id shinchiro.mpv && winget install -e --id Gyan.FFmpeg"},
		{"darwin", nil, []string{"ffmpeg"}, "brew install ffmpeg"},
		{"linux", only("pacman"), []string{"mpv", "gstreamer"}, "sudo pacman -S --needed mpv gstreamer gst-plugin-pipewire gst-plugins-good gst-plugins-bad gst-plugins-ugly"},
		{"linux", only("apt-get"), []string{"ffmpeg"}, "sudo apt install ffmpeg"},
		{"linux", only("dnf"), []string{"mpv"}, "sudo dnf install mpv"},
		{"linux", only("zypper"), []string{"mpv"}, ""},
	}
	for _, c := range cases {
		has := c.has
		if has == nil {
			has = func(string) bool { return false }
		}
		if got := installHintFor(c.goos, has, c.pkgs); got != c.want {
			t.Errorf("installHintFor(%s, %v) = %q, want %q", c.goos, c.pkgs, got, c.want)
		}
	}
}

func TestTargetProcess(t *testing.T) {
	if pid, name, ok := TargetProcess("app:4242:chrome"); !ok || pid != 4242 || name != "chrome" {
		t.Fatalf("app target: %d %q %v", pid, name, ok)
	}
	for _, id := range []string{"desktop", "focused", "portal", "monitor:0:0:0:1920:1080", "app:1:init", "app:x:y"} {
		if _, _, ok := TargetProcess(id); ok {
			t.Fatalf("%s resolved to a process; it must share the whole output", id)
		}
	}
}

func TestMacWindowTarget(t *testing.T) {
	for id, want := range map[string]bool{"4821": true, "desktop": false, "portal": false, "": false, "monitor:1": false} {
		if got := MacWindowTarget(id); got != want {
			t.Fatalf("MacWindowTarget(%q) = %v", id, got)
		}
	}
}
