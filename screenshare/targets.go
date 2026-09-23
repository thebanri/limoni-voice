package screenshare

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ListWindows returns active shareable screen and monitor targets
func ListWindows() []WindowInfo {
	targets := []WindowInfo{
		{ID: "desktop", Title: "🖥️  Screen 1 (Primary - Full View)"},
	}

	switch runtime.GOOS {
	case "windows":
		psScript := `
		Add-Type -AssemblyName System.Windows.Forms
		$screens = [System.Windows.Forms.Screen]::AllScreens
		$idx = 0
		foreach ($s in $screens) {
			$p = if ($s.Primary) {" (Primary)"} else {""}
			"SCREEN|$idx|$($s.Bounds.X)|$($s.Bounds.Y)|$($s.Bounds.Width)|$($s.Bounds.Height)|Screen $($idx+1)$p ($($s.Bounds.Width)x$($s.Bounds.Height))"
			$idx++
		}
		if ($screens.Count -gt 1) {
			"ALL|0|0|0|0|All Screens (Extended Desktop)"
		}
		Get-Process | Where-Object {$_.MainWindowTitle -ne '' -and $_.MainWindowHandle -ne 0} | ForEach-Object {
			"WIN|$($_.MainWindowHandle)|$($_.MainWindowTitle)"
		}
		`
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
		out, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			lines := strings.Split(string(out), "\n")
			var screenTargets []WindowInfo
			var winTargets []WindowInfo
			seen := make(map[string]bool)

			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}
				parts := strings.SplitN(trimmed, "|", 2)
				if len(parts) < 2 {
					continue
				}
				if parts[0] == "SCREEN" {
					sub := strings.Split(parts[1], "|")
					if len(sub) >= 6 {
						sIdx, x, y, w, h, name := sub[0], sub[1], sub[2], sub[3], sub[4], sub[5]
						id := fmt.Sprintf("monitor:%s:%s:%s:%s:%s", sIdx, x, y, w, h)
						if x == "0" && y == "0" && sIdx == "0" {
							id = "desktop"
						}
						screenTargets = append(screenTargets, WindowInfo{
							ID:    id,
							Title: "[Screen] " + name,
						})
					} else if len(sub) >= 5 {
						x, y, w, h, name := sub[0], sub[1], sub[2], sub[3], sub[4]
						id := fmt.Sprintf("monitor:%s:%s:%s:%s", x, y, w, h)
						if x == "0" && y == "0" {
							id = "desktop"
						}
						screenTargets = append(screenTargets, WindowInfo{
							ID:    id,
							Title: "[Screen] " + name,
						})
					}
				} else if parts[0] == "ALL" {
					sub := strings.Split(parts[1], "|")
					if len(sub) >= 5 {
						screenTargets = append(screenTargets, WindowInfo{
							ID:    "desktop",
							Title: "[Desktop] " + sub[4],
						})
					}
				} else if parts[0] == "WIN" {
					winSub := strings.SplitN(parts[1], "|", 2)
					if len(winSub) >= 2 {
						handle := winSub[0]
						title := winSub[1]
						if !seen[title] && !strings.EqualFold(title, "Program Manager") {
							seen[title] = true
							winTargets = append(winTargets, WindowInfo{
								ID:    fmt.Sprintf("hwnd:%s:%s", handle, title),
								Title: "[Window] " + title,
							})
						}
					}
				}
			}

			if len(screenTargets) > 0 {
				targets = screenTargets
			}
			if len(winTargets) > 0 {
				targets = append(targets, winTargets...)
			}
		}

	case "darwin":
		out := ""
		if binPath, err := getOrBuildMacCaptureBinary(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if b, err := exec.CommandContext(ctx, binPath, "--list").Output(); err == nil {
				out = string(b)
			}
			cancel()
		}
		var permissionMissing bool
		targets, permissionMissing = parseMacHelperList(out)
		setMacScreenPermissionMissing(permissionMissing)

	case "linux":
		targets = listLinuxTargets()
	}
	return targets
}

func parseXrandrGeometry(geom string) (w, h, x, y string) {
	w, h, x, y = "1920", "1080", "0", "0"
	xIdx := strings.Index(geom, "x")
	if xIdx == -1 {
		return
	}
	rawW := geom[:xIdx]
	if slashIdx := strings.Index(rawW, "/"); slashIdx != -1 {
		w = rawW[:slashIdx]
	} else {
		w = rawW
	}

	rest := geom[xIdx+1:]
	plusIdx := strings.Index(rest, "+")
	if plusIdx != -1 {
		rawH := rest[:plusIdx]
		if slashIdx := strings.Index(rawH, "/"); slashIdx != -1 {
			h = rawH[:slashIdx]
		} else {
			h = rawH
		}

		offsets := strings.Split(rest[plusIdx+1:], "+")
		if len(offsets) >= 2 {
			x = offsets[0]
			y = offsets[1]
		} else if len(offsets) == 1 {
			x = offsets[0]
		}
	} else {
		if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
			h = rest[:slashIdx]
		} else {
			h = rest
		}
	}
	return
}

func getActiveWindowIDX11() string {
	if xpropBin, err := FindExecutable("xprop"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		cmd := exec.CommandContext(ctx, xpropBin, "-root", "_NET_ACTIVE_WINDOW")
		out, err := cmd.Output()
		if err == nil {
			str := string(out)
			if idx := strings.Index(str, "#"); idx != -1 {
				winID := strings.TrimSpace(str[idx+1:])
				if winID != "" && winID != "0x0" {
					return winID
				}
			}
		}
	}
	return ""
}

func watchX11WindowLiveness(ctx context.Context, winID string, cancel context.CancelFunc) {
	if winID == "" || winID == "0x0" {
		return
	}
	xpropBin, err := FindExecutable("xprop")
	if err != nil {
		return
	}

	ticker := time.NewTicker(600 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkCtx, checkCancel := context.WithTimeout(ctx, 400*time.Millisecond)
			cmd := exec.CommandContext(checkCtx, xpropBin, "-id", winID, "WM_NAME")
			err := cmd.Run()
			checkCancel()
			if err != nil {
				logMsg("[X11-WATCH] Target window %s was closed. Automatically ending screen stream.", winID)
				cancel()
				return
			}
		}
	}
}

func watchPIDLiveness(ctx context.Context, pid int, cancel context.CancelFunc) {
	if pid <= 1 {
		return
	}
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
				logMsg("[APP-WATCH] Target process (PID %d) was closed. Ending screen stream.", pid)
				cancel()
				return
			}
		}
	}
}

// TargetProcess returns the process behind an application or window target (Linux app:/win:,
// Windows hwnd:), so screen share audio can be limited to that program. ok is false for
// screens, the focused-window and portal targets, and wherever the process cannot be found;
// those share the whole system output. macOS windows are handled by the capture helper.
func TargetProcess(targetID string) (pid int, name string, ok bool) {
	switch {
	case strings.HasPrefix(targetID, "app:"):
		parts := strings.SplitN(strings.TrimPrefix(targetID, "app:"), ":", 2)
		pid, _ = strconv.Atoi(parts[0])
		if len(parts) == 2 {
			name = parts[1]
		}
	case strings.HasPrefix(targetID, "win:") && runtime.GOOS == "linux":
		winID, _, _ := strings.Cut(strings.TrimPrefix(targetID, "win:"), ":")
		pid = x11WindowPID(winID)
	case strings.HasPrefix(targetID, "hwnd:"):
		handle, _, _ := strings.Cut(strings.TrimPrefix(targetID, "hwnd:"), ":")
		if h, err := strconv.ParseUint(handle, 10, 64); err == nil && h != 0 {
			pid = windowProcessID(uintptr(h))
		}
	}
	if pid <= 1 {
		return 0, "", false
	}
	if name == "" && runtime.GOOS == "linux" {
		if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
			name = strings.TrimSpace(string(comm))
		}
	}
	return pid, name, true
}

// x11WindowPID reads _NET_WM_PID of an X11 (or XWayland) window.
func x11WindowPID(winID string) int {
	xpropBin, err := FindExecutable("xprop")
	if err != nil || winID == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, xpropBin, "-id", winID, "_NET_WM_PID").Output()
	if err != nil {
		return 0
	}
	_, val, found := strings.Cut(string(out), "=")
	if !found {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(val))
	return pid
}

func findX11WindowByPID(pid int) string {
	if pid <= 1 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if xdotoolBin, err := FindExecutable("xdotool"); err == nil {
		out, err := exec.CommandContext(ctx, xdotoolBin, "search", "--pid", strconv.Itoa(pid)).Output()
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			for i := len(lines) - 1; i >= 0; i-- {
				w := strings.TrimSpace(lines[i])
				if w != "" && w != "0" {
					return w
				}
			}
		}
	}

	if xpropBin, err := FindExecutable("xprop"); err == nil {
		out, err := exec.CommandContext(ctx, xpropBin, "-root", "_NET_CLIENT_LIST").Output()
		if err == nil {
			outStr := string(out)
			if idx := strings.Index(outStr, "#"); idx != -1 {
				winIDs := strings.Split(outStr[idx+1:], ",")
				for _, rawID := range winIDs {
					winID := strings.TrimSpace(rawID)
					if winID == "" || winID == "0x0" {
						continue
					}
					pidOut, err := exec.CommandContext(ctx, xpropBin, "-id", winID, "_NET_WM_PID").Output()
					if err == nil && strings.Contains(string(pidOut), fmt.Sprintf("= %d", pid)) {
						return winID
					}
				}
			}
		}
	}
	return ""
}

func getLinuxDisplay() string {
	if disp := os.Getenv("DISPLAY"); disp != "" {
		return disp
	}
	return ":0.0"
}

func isWayland() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland")
}

func listLinuxTargets() []WindowInfo {
	var targets []WindowInfo
	seenMonitors := make(map[string]bool)
	var monitorTargets []WindowInfo
	var windowTargets []WindowInfo
	seenWins := make(map[string]bool)

	// 1. Focused Window option (Always captures whichever window is active)
	targets = append(targets, WindowInfo{
		ID:    "focused",
		Title: "[Active Window] Currently Focused Application",
	})

	// 2. Discover Monitors via xrandr
	if xrandrBin, err := FindExecutable("xrandr"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		cmd := exec.CommandContext(ctx, xrandrBin, "--listmonitors")
		out, err := cmd.Output()
		cancel()
		if err == nil {
			lines := strings.Split(string(out), "\n")
			idx := 1
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "Monitors:") || strings.HasPrefix(trimmed, "WARNING:") {
					continue
				}
				fields := strings.Fields(trimmed)
				if len(fields) >= 3 {
					isPrimary := strings.Contains(line, "*")
					primaryTag := ""
					if isPrimary {
						primaryTag = " (Primary)"
					}
					monName := fields[len(fields)-1]
					geomField := fields[len(fields)-2]
					w, h, x, y := parseXrandrGeometry(geomField)
					if monName != "" && !seenMonitors[monName] {
						seenMonitors[monName] = true
						id := fmt.Sprintf("monitor:%s:%s:%s:%s:%s", monName, w, h, x, y)
						title := fmt.Sprintf("[Screen %d] %s (%sx%s)%s", idx, monName, w, h, primaryTag)
						monitorTargets = append(monitorTargets, WindowInfo{
							ID:    id,
							Title: title,
						})
						idx++
					}
				}
			}
		}
	}

	// Fallback: discover monitors via gpu-screen-recorder --list-monitors
	if len(monitorTargets) == 0 {
		if gsrBin, err := FindExecutable("gpu-screen-recorder"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			cmd := exec.CommandContext(ctx, gsrBin, "--list-monitors")
			out, err := cmd.Output()
			cancel()
			if err == nil {
				lines := strings.Split(string(out), "\n")
				idx := 1
				for _, line := range lines {
					trimmed := strings.TrimSpace(line)
					if trimmed == "" || trimmed == "portal" || trimmed == "region" || strings.HasPrefix(trimmed, "/dev/") {
						continue
					}
					parts := strings.Split(trimmed, "|")
					if len(parts) >= 1 && parts[0] != "" && !seenMonitors[parts[0]] {
						seenMonitors[parts[0]] = true
						res := ""
						if len(parts) >= 2 {
							res = fmt.Sprintf(" (%s)", parts[1])
						}
						monitorTargets = append(monitorTargets, WindowInfo{
							ID:    "monitor:" + parts[0],
							Title: fmt.Sprintf("[Screen %d] %s%s", idx, parts[0], res),
						})
						idx++
					}
				}
			}
		}
	}

	if len(monitorTargets) > 0 {
		targets = append(targets, monitorTargets...)
		if len(monitorTargets) > 1 {
			targets = append(targets, WindowInfo{
				ID:    "desktop",
				Title: "[Desktop] All Screens Combined",
			})
		}
	} else {
		targets = append(targets, WindowInfo{
			ID:    "desktop",
			Title: "[Desktop] Entire Screen (Primary Display)",
		})
	}

	// 3. Discover Windows via wmctrl
	if p, err := FindExecutable("wmctrl"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		cmd := exec.CommandContext(ctx, p, "-l")
		out, err := cmd.Output()
		cancel()
		if err == nil {
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}
				fields := strings.Fields(trimmed)
				if len(fields) >= 4 {
					winID := fields[0]
					title := strings.Join(fields[3:], " ")
					if !seenWins[title] && !strings.EqualFold(title, "Desktop") {
						seenWins[title] = true
						windowTargets = append(windowTargets, WindowInfo{
							ID:    fmt.Sprintf("win:%s:%s", winID, title),
							Title: "[Window] " + title,
						})
					}
				}
			}
		}
	}

	// 4. Discover Windows via xprop (_NET_CLIENT_LIST_STACKING / _NET_CLIENT_LIST)
	if xpropBin, err := FindExecutable("xprop"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		cmd := exec.CommandContext(ctx, xpropBin, "-root", "_NET_CLIENT_LIST_STACKING")
		out, err := cmd.Output()
		cancel()
		if err != nil || len(out) == 0 {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
			cmd2 := exec.CommandContext(ctx2, xpropBin, "-root", "_NET_CLIENT_LIST")
			out, _ = cmd2.Output()
			cancel2()
		}
		if len(out) > 0 {
			outStr := string(out)
			if idx := strings.Index(outStr, "#"); idx != -1 {
				winIDs := strings.Split(outStr[idx+1:], ",")
				for _, rawID := range winIDs {
					winID := strings.TrimSpace(rawID)
					if winID == "" || winID == "0x0" {
						continue
					}
					nameCtx, nameCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
					nameCmd := exec.CommandContext(nameCtx, xpropBin, "-id", winID, "_NET_WM_NAME", "WM_NAME", "WM_CLASS")
					nameOut, err := nameCmd.Output()
					nameCancel()
					if err == nil {
						lines := strings.Split(string(nameOut), "\n")
						var rawTitle, rawClass string
						for _, nl := range lines {
							if strings.HasPrefix(nl, "_NET_WM_NAME") || strings.HasPrefix(nl, "WM_NAME") {
								if eqIdx := strings.Index(nl, "="); eqIdx != -1 {
									t := strings.Trim(strings.TrimSpace(nl[eqIdx+1:]), "\"")
									if t != "" && rawTitle == "" {
										rawTitle = t
									}
								}
							} else if strings.HasPrefix(nl, "WM_CLASS") {
								if eqIdx := strings.Index(nl, "="); eqIdx != -1 {
									c := strings.Trim(strings.TrimSpace(nl[eqIdx+1:]), "\"")
									if c != "" && rawClass == "" {
										rawClass = c
									}
								}
							}
						}
						displayTitle := rawTitle
						if displayTitle == "" {
							displayTitle = rawClass
						}
						if displayTitle != "" && !seenWins[displayTitle] && !strings.EqualFold(displayTitle, "Desktop") {
							seenWins[displayTitle] = true
							windowTargets = append(windowTargets, WindowInfo{
								ID:    fmt.Sprintf("win:%s:%s", winID, displayTitle),
								Title: "[Window] " + displayTitle,
							})
						}
					}
				}
			}
		}
	}

	// 5. Discover Windows on Hyprland
	if hyprBin, err := FindExecutable("hyprctl"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		cmd := exec.CommandContext(ctx, hyprBin, "clients")
		out, err := cmd.Output()
		cancel()
		if err == nil {
			lines := strings.Split(string(out), "\n")
			var curTitle, curClass string
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "title:") {
					curTitle = strings.TrimSpace(strings.TrimPrefix(trimmed, "title:"))
				} else if strings.HasPrefix(trimmed, "class:") {
					curClass = strings.TrimSpace(strings.TrimPrefix(trimmed, "class:"))
				} else if trimmed == "" {
					if curTitle != "" && !seenWins[curTitle] {
						seenWins[curTitle] = true
						displayTitle := curTitle
						if curClass != "" {
							displayTitle = fmt.Sprintf("%s - %s", curClass, curTitle)
						}
						windowTargets = append(windowTargets, WindowInfo{
							ID:    "focused",
							Title: "[Window] " + displayTitle,
						})
					}
					curTitle, curClass = "", ""
				}
			}
		}
	}

	// 6. Discover Running Graphical Applications (Wayland & X11 native apps: Zen, VS Code, Discord, etc.)
	guiApps := listRunningLinuxGUIApps()
	for _, app := range guiApps {
		if !seenWins[app.Title] && !seenWins[app.Name] {
			seenWins[app.Title] = true
			seenWins[app.Name] = true
			windowTargets = append(windowTargets, WindowInfo{
				ID:    fmt.Sprintf("app:%d:%s", app.PID, app.Name),
				Title: app.Title,
			})
		}
	}

	if len(windowTargets) > 0 {
		targets = append(targets, windowTargets...)
	}

	return targets
}

type linuxGUIAppInfo struct {
	Name  string
	PID   int
	Title string
}

func listRunningLinuxGUIApps() []linuxGUIAppInfo {
	var apps []linuxGUIAppInfo
	seen := make(map[string]bool)

	knownApps := map[string]string{
		"zen-bin":          "[Window] Zen Browser",
		"zen":              "[Window] Zen Browser",
		"firefox":          "[Window] Mozilla Firefox",
		"firefox-bin":      "[Window] Mozilla Firefox",
		"chrome":           "[Window] Google Chrome",
		"google-chrome":    "[Window] Google Chrome",
		"chromium":         "[Window] Chromium",
		"brave":            "[Window] Brave Browser",
		"opera":            "[Window] Opera Browser",
		"antigravity-ide":  "[Window] Antigravity IDE",
		"code":             "[Window] Visual Studio Code",
		"codium":           "[Window] VSCodium",
		"discord":          "[Window] Discord",
		"vesktop":          "[Window] Vesktop (Discord)",
		"webcord":          "[Window] WebCord (Discord)",
		"telegram-desktop": "[Window] Telegram",
		"slack":            "[Window] Slack",
		"spotify":          "[Window] Spotify",
		"steam":            "[Window] Steam",
		"nautilus":         "[Window] Files (Nautilus)",
		"thunar":           "[Window] Files (Thunar)",
		"dolphin":          "[Window] Files (Dolphin)",
		"gnome-terminal":   "[Window] GNOME Terminal",
		"alacritty":        "[Window] Alacritty Terminal",
		"kitty":            "[Window] Kitty Terminal",
		"wezterm-gui":      "[Window] WezTerm",
		"foot":             "[Window] Foot Terminal",
		"obs":              "[Window] OBS Studio",
		"mpv":              "[Window] MPV Player",
		"vlc":              "[Window] VLC Media Player",
	}

	files, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	for _, f := range files {
		if !f.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(f.Name())
		if err != nil || pid <= 1 {
			continue
		}

		commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(commBytes))

		if title, ok := knownApps[strings.ToLower(comm)]; ok {
			if !seen[title] {
				seen[title] = true
				apps = append(apps, linuxGUIAppInfo{
					Name:  comm,
					PID:   pid,
					Title: title,
				})
			}
		}
	}

	return apps
}
