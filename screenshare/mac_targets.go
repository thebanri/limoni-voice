package screenshare

import (
	"strings"
	"sync/atomic"
)

// MacScreenPermissionHint is shown when macOS blocks screen recording. The permission belongs
// to the terminal Limoni Voice runs in, and macOS only applies it after that app restarts.
const MacScreenPermissionHint = "macOS is blocking screen recording: System Settings → Privacy & Security → Screen & System Audio Recording, turn on your terminal app, then restart it"

var macScreenPermissionMissing atomic.Bool

func setMacScreenPermissionMissing(v bool) { macScreenPermissionMissing.Store(v) }

// PermissionHint returns what the user has to allow before screen sharing can work, or "".
func PermissionHint() string {
	if macScreenPermissionMissing.Load() {
		return MacScreenPermissionHint
	}
	return ""
}

// parseMacHelperList turns the ScreenCaptureKit helper's --list output into share targets:
// the main display first, the other displays when there are several, then the windows. It
// also reports whether the helper found the Screen Recording permission missing.
func parseMacHelperList(out string) (targets []WindowInfo, permissionMissing bool) {
	targets = []WindowInfo{{ID: "desktop", Title: "[Desktop] Entire Screen (Primary Display)"}}
	var screens, windows []WindowInfo
	seen := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		switch {
		case len(parts) >= 2 && parts[0] == "PERMISSION" && parts[1] == "screen":
			permissionMissing = true
		case len(parts) == 3 && parts[0] == "SCREEN" && parts[1] != "desktop":
			screens = append(screens, WindowInfo{ID: "display:" + parts[1], Title: "[Screen] " + parts[2]})
		case len(parts) == 3 && parts[0] == "WIN":
			title := parts[2]
			if seen[title] || strings.Contains(title, "Item-0") || strings.Contains(title, "WindowServer") {
				continue
			}
			seen[title] = true
			windows = append(windows, WindowInfo{ID: parts[1], Title: "[Window] " + title})
		}
	}
	// With one display the "Entire Screen" entry already is that display.
	if len(screens) > 1 {
		targets = append(targets, screens...)
	}
	return append(targets, windows...), permissionMissing
}
