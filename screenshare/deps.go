package screenshare

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// MissingDepsError reports the programs or GStreamer plugins a screen share needs but the
// system lacks, together with the command that installs them on this Linux distribution.
type MissingDepsError struct {
	Watching bool     // false: sharing a screen, true: watching one
	Missing  []string // what is missing, e.g. "gst-launch-1.0" or "GStreamer pipewiresrc"
	Install  string   // install command for this distribution, "" when unknown
}

func (e *MissingDepsError) Error() string {
	purpose := "share your screen"
	if e.Watching {
		purpose = "watch screen shares"
	}
	msg := fmt.Sprintf("Missing tools to %s: %s.", purpose, strings.Join(e.Missing, ", "))
	if e.Install != "" {
		msg += " Install them with: " + e.Install
	} else {
		msg += " Install them with your package manager."
	}
	return msg
}

// linuxDep is one installable piece of the Linux screen share tool chain.
type linuxDep int

const (
	depGstTools    linuxDep = iota // gst-launch-1.0, gst-inspect-1.0 and the core elements
	depGstPipewire                 // pipewiresrc
	depGstBase                     // videoconvert, videoscale, videorate
	depGstGood                     // udpsink
	depGstBad                      // h264parse, mpegtsmux
	depGstUgly                     // x264enc
	depFFmpeg                      // ffmpeg, ffplay
	depMpv                         // mpv
)

// linuxPackages names the package that provides each dependency, per distribution family.
var linuxPackages = map[string][]string{
	//        tools                 pipewire                  base                          good                          bad                               ugly                          ffmpeg    mpv
	"debian": {"gstreamer1.0-tools", "gstreamer1.0-pipewire", "gstreamer1.0-plugins-base", "gstreamer1.0-plugins-good", "gstreamer1.0-plugins-bad", "gstreamer1.0-plugins-ugly", "ffmpeg", "mpv"},
	"arch":   {"gstreamer", "gst-plugin-pipewire", "gst-plugins-base", "gst-plugins-good", "gst-plugins-bad", "gst-plugins-ugly", "ffmpeg", "mpv"},
	"fedora": {"gstreamer1", "pipewire-gstreamer", "gstreamer1-plugins-base", "gstreamer1-plugins-good", "gstreamer1-plugins-bad-free", "gstreamer1-plugins-ugly", "ffmpeg", "mpv"},
	"suse":   {"gstreamer", "gstreamer-plugin-pipewire", "gstreamer-plugins-base", "gstreamer-plugins-good", "gstreamer-plugins-bad", "gstreamer-plugins-ugly", "ffmpeg", "mpv"},
}

var installPrefix = map[string]string{
	"debian": "sudo apt install ",
	"arch":   "sudo pacman -S --needed ",
	"fedora": "sudo dnf install ",
	"suse":   "sudo zypper install ",
}

// distroFamily maps the contents of /etc/os-release to a key of linuxPackages ("" if unknown).
func distroFamily(osRelease string) string {
	var ids []string
	for line := range strings.SplitSeq(osRelease, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || (key != "ID" && key != "ID_LIKE") {
			continue
		}
		ids = append(ids, strings.Fields(strings.ToLower(strings.Trim(val, `"'`)))...)
	}
	for _, id := range ids {
		switch id {
		case "debian", "ubuntu", "linuxmint", "pop", "raspbian":
			return "debian"
		case "arch", "archlinux", "manjaro", "endeavouros", "cachyos":
			return "arch"
		case "fedora", "rhel", "centos", "nobara":
			return "fedora"
		case "suse", "opensuse", "opensuse-leap", "opensuse-tumbleweed", "sles":
			return "suse"
		}
	}
	return ""
}

// installCommand returns the command installing deps on the distribution family ("" if unknown).
func installCommand(family string, deps []linuxDep) string {
	pkgs, ok := linuxPackages[family]
	if !ok || len(deps) == 0 {
		return ""
	}
	var names []string
	for _, d := range deps {
		if !slices.Contains(names, pkgs[d]) {
			names = append(names, pkgs[d])
		}
	}
	return installPrefix[family] + strings.Join(names, " ")
}

// newMissingDepsError builds the error for the missing items and the packages providing them.
func newMissingDepsError(watching bool, missing []string, deps []linuxDep) *MissingDepsError {
	osRelease, _ := os.ReadFile("/etc/os-release")
	return &MissingDepsError{
		Watching: watching,
		Missing:  missing,
		Install:  installCommand(distroFamily(string(osRelease)), deps),
	}
}
