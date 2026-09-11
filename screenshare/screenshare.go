package screenshare

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LogCallback is optional hook to receive internal screenshare logs
var LogCallback func(string)

func logMsg(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	if LogCallback != nil {
		LogCallback(msg)
	}
}

// DependencyStatus contains the availability of required external CLI tools
type DependencyStatus struct {
	HasMPV               bool   `json:"has_mpv"`
	HasGPUScreenRecorder bool   `json:"has_gpu_screen_recorder"`
	HasFFmpeg            bool   `json:"has_ffmpeg"`
	MissingRecommended   string `json:"missing_recommended,omitempty"`
}

// BroadcastOptions defines configuration for the video stream
type BroadcastOptions struct {
	Resolution string // e.g. "1280x720" or "1920x1080"
	FPS        int    // e.g. 60 or 30
	Bitrate    string // e.g. "4M" or "2M"
	WindowID   string // optional window id or "portal" for Wayland/X11 window picker, or "desktop"
	Quality    string // e.g. "medium", "ultra", "fast"
}

// WindowInfo represents a shareable window or screen target
type WindowInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

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
		$idx = 1
		foreach ($s in $screens) {
			$p = if ($s.Primary) {" (Primary)"} else {""}
			"SCREEN|$($s.Bounds.X)|$($s.Bounds.Y)|$($s.Bounds.Width)|$($s.Bounds.Height)|Screen $idx$p ($($s.Bounds.Width)x$($s.Bounds.Height))"
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
					if len(sub) >= 5 {
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
		targets = append(targets, WindowInfo{
			ID:    "desktop",
			Title: "[Desktop] Entire Screen (Primary Display)",
		})
		if binPath, err := getOrBuildMacCaptureBinary(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			cmd := exec.CommandContext(ctx, binPath, "--list")
			out, err := cmd.Output()
			cancel()
			if err == nil {
				lines := strings.Split(string(out), "\n")
				seen := make(map[string]bool)
				for _, line := range lines {
					trimmed := strings.TrimSpace(line)
					if trimmed == "" {
						continue
					}
					parts := strings.SplitN(trimmed, "|", 3)
					if len(parts) == 3 && parts[0] == "WIN" {
						winID := parts[1]
						title := parts[2]
						if !seen[title] && !strings.Contains(title, "Item-0") && !strings.Contains(title, "WindowServer") {
							seen[title] = true
							targets = append(targets, WindowInfo{
								ID:    winID,
								Title: "[Window] " + title,
							})
						}
					}
				}
			}
		}

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

func getMacScreenDevice(binPath string) string {
	return "3:none"
}

const embeddedMacCaptureSwift = `import Foundation
import Darwin
import ScreenCaptureKit
import CoreMedia
import CoreVideo

_ = Darwin.signal(SIGPIPE, SIG_IGN)

let logFilePath = "/tmp/limoni_mac_sckit.log"
func logToFile(_ msg: String) {
    let line = "[\(Date())] \(msg)\n"
    if let data = line.data(using: .utf8) {
        if let handle = FileHandle(forWritingAtPath: logFilePath) {
            handle.seekToEndOfFile()
            handle.write(data)
            handle.closeFile()
        } else {
            try? data.write(to: URL(fileURLWithPath: logFilePath))
        }
    }
    fputs(line, stderr)
}

func writeAll(fd: Int32, buffer: UnsafeRawPointer, count: Int) -> Bool {
    var written = 0
    while written < count {
        let n = write(fd, buffer.advanced(by: written), count - written)
        if n <= 0 {
            if errno == EINTR { continue }
            return false
        }
        written += n
    }
    return true
}

@available(macOS 12.3, *)
class ScreenRecorder: NSObject, SCStreamOutput, SCStreamDelegate {
    var stream: SCStream?
    var isRunning = false
    var frameCount = 0

    func start(fps: Int = 60, width: Int = 1920, height: Int = 1080, targetWindowID: CGWindowID? = nil) async {
        logToFile("[START] Initializing ScreenCaptureKit capture: \(width)x\(height) @ \(fps) FPS, targetWindowID=\(String(describing: targetWindowID))")
        do {
            let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
            guard let display = content.displays.first else {
                logToFile("[ERR] No display found in SCShareableContent")
                exit(1)
            }
            logToFile("[INFO] Found \(content.displays.count) displays and \(content.windows.count) windows")

            var filter: SCContentFilter
            if let winID = targetWindowID, let targetWin = content.windows.first(where: { $0.windowID == winID }) {
                logToFile("[INFO] Target window found: '\(targetWin.title ?? "")' (app: '\(targetWin.owningApplication?.applicationName ?? "")', id: \(winID), frame: \(targetWin.frame))")
                filter = SCContentFilter(display: display, including: [targetWin])
            } else {
                logToFile("[INFO] Using full display capture (Display ID: \(display.displayID), resolution: \(display.width)x\(display.height))")
                filter = SCContentFilter(display: display, excludingApplications: [], exceptingWindows: [])
            }

            let config = SCStreamConfiguration()
            config.width = width
            config.height = height
            config.scalesToFit = true
            config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
            config.pixelFormat = kCVPixelFormatType_32BGRA
            config.showsCursor = true
            config.queueDepth = 8

            let stream = SCStream(filter: filter, configuration: config, delegate: self)
            try stream.addStreamOutput(self, type: .screen, sampleHandlerQueue: DispatchQueue(label: "screen.capture.queue", qos: .userInteractive))

            do {
                try await stream.startCapture()
                self.stream = stream
                self.isRunning = true
                logToFile("[OK] Stream started successfully at \(width)x\(height) @ \(fps) FPS")
            } catch {
                logToFile("[WARN] Filter startCapture failed (\(error)), trying fallback full display filter...")
                let fallbackFilter = SCContentFilter(display: display, excludingApplications: [], exceptingWindows: [])
                let fallbackStream = SCStream(filter: fallbackFilter, configuration: config, delegate: self)
                try fallbackStream.addStreamOutput(self, type: .screen, sampleHandlerQueue: DispatchQueue(label: "screen.capture.queue", qos: .userInteractive))
                try await fallbackStream.startCapture()
                self.stream = fallbackStream
                self.isRunning = true
                logToFile("[OK] Fallback display stream running at \(width)x\(height) @ \(fps) FPS")
            }
        } catch {
            logToFile("[FATAL] Error initializing ScreenCaptureKit: \(error)")
            exit(1)
        }
    }

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of type: SCStreamOutputType) {
        guard sampleBuffer.isValid, type == .screen else { return }
        guard let pixelBuffer = sampleBuffer.imageBuffer else { return }

        CVPixelBufferLockBaseAddress(pixelBuffer, .readOnly)
        defer { CVPixelBufferUnlockBaseAddress(pixelBuffer, .readOnly) }

        guard let baseAddress = CVPixelBufferGetBaseAddress(pixelBuffer) else { return }
        let bytesPerRow = CVPixelBufferGetBytesPerRow(pixelBuffer)
        let width = CVPixelBufferGetWidth(pixelBuffer)
        let height = CVPixelBufferGetHeight(pixelBuffer)
        let rowBytes = width * 4

        self.frameCount += 1
        if self.frameCount == 1 {
            logToFile("[FRAME] First Metal video frame received: \(width)x\(height), bytesPerRow=\(bytesPerRow), expectedRowBytes=\(rowBytes)")
        } else if self.frameCount % 300 == 0 {
            logToFile("[STATS] \(self.frameCount) frames delivered to FFmpeg")
        }

        if bytesPerRow == rowBytes {
            if !writeAll(fd: STDOUT_FILENO, buffer: baseAddress, count: rowBytes * height) {
                logToFile("[PIPE] STDOUT pipe closed by consumer (writeAll failed)")
                exit(0)
            }
        } else {
            for y in 0..<height {
                let rowPtr = baseAddress.advanced(by: y * bytesPerRow)
                if !writeAll(fd: STDOUT_FILENO, buffer: rowPtr, count: rowBytes) {
                    logToFile("[PIPE] STDOUT pipe closed by consumer (row write failed)")
                    exit(0)
                }
            }
        }
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        logToFile("[STOP] Stream stopped with error: \(error)")
        exit(1)
    }
}

var globalRecorder: AnyObject?

if #available(macOS 12.3, *) {
    if CommandLine.arguments.contains("--list") {
        let sem = DispatchSemaphore(value: 0)
        Task {
            do {
                let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
                for (i, d) in content.displays.enumerated() {
                    print("SCREEN|\(d.displayID)|Display \(i+1) (\(d.width)x\(d.height))")
                }
                var seenTitles = Set<String>()
                for w in content.windows {
                    if let title = w.title, !title.isEmpty, w.frame.width > 50, w.frame.height > 50 {
                        let app = w.owningApplication?.applicationName ?? ""
                        let name = app.isEmpty ? title : "\(app) - \(title)"
                        if !seenTitles.contains(name) {
                            seenTitles.insert(name)
                            print("WIN|\(w.windowID)|\(name)")
                        }
                    }
                }
            } catch {
                print("SCREEN|desktop|Primary Display")
            }
            sem.signal()
        }
        _ = sem.wait(timeout: .now() + 2.0)
        exit(0)
    } else {
        var width = 1920
        var height = 1080
        var fps = 60
        var targetWinID: CGWindowID? = nil

        if CommandLine.arguments.count >= 2, let w = Int(CommandLine.arguments[1]) { width = w }
        if CommandLine.arguments.count >= 3, let h = Int(CommandLine.arguments[2]) { height = h }
        if CommandLine.arguments.count >= 4, let f = Int(CommandLine.arguments[3]) { fps = f }
        if CommandLine.arguments.count >= 5, let winStr = CommandLine.arguments[4] as String?, !winStr.isEmpty && winStr != "desktop" && winStr != "portal" {
            if let winNum = UInt32(winStr) {
                targetWinID = CGWindowID(winNum)
            }
        }

        let recorder = ScreenRecorder()
        globalRecorder = recorder
        Task {
            await recorder.start(fps: fps, width: width, height: height, targetWindowID: targetWinID)
        }
        dispatchMain()
    }
} else {
    logToFile("[ERR] ScreenCaptureKit requires macOS 12.3+")
    exit(1)
}
`

func getOrBuildMacCaptureBinary() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = filepath.Join(os.TempDir(), fmt.Sprintf("limoni-%d", os.Getuid()))
	} else {
		cacheDir = filepath.Join(cacheDir, "limoni-voice")
	}
	_ = os.MkdirAll(cacheDir, 0700)

	binPath := filepath.Join(cacheDir, "limoni-mac-sckit-v9")
	if info, err := os.Stat(binPath); err == nil && info.Size() > 0 {
		return binPath, nil
	}

	swiftc, err := FindExecutable("swiftc")
	if err != nil {
		return "", errors.New("swiftc not found on macOS")
	}

	srcFile := filepath.Join(cacheDir, "limoni_mac_capture.swift")
	if err := os.WriteFile(srcFile, []byte(embeddedMacCaptureSwift), 0600); err != nil {
		return "", err
	}
	defer os.Remove(srcFile)

	logMsg("[DARWIN] Compiling native ScreenCaptureKit engine with swiftc...")
	cmd := exec.Command(swiftc, "-O", "-framework", "ScreenCaptureKit", "-framework", "CoreMedia", "-framework", "CoreVideo", srcFile, "-o", binPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("swiftc compilation failed: %w (output: %s)", err, string(out))
	}
	logMsg("[DARWIN] ScreenCaptureKit engine compiled successfully: %s", binPath)
	return binPath, nil
}

// GetPresetOptions returns tailored broadcasting options for 30, 60, or 120 FPS
func GetPresetOptions(fps int, targetID string) BroadcastOptions {
	if fps <= 0 {
		fps = 60
	}
	if targetID == "" {
		targetID = "portal"
	}

	bitrate := "1.8M"
	quality := "high"
	switch fps {
	case 120:
		bitrate = "2.4M"
		quality = "ultra"
	case 30:
		bitrate = "1M"
		quality = "fast"
	case 60:
		bitrate = "1.8M"
		quality = "high"
	default:
		fps = 60
		bitrate = "1.8M"
		quality = "high"
	}

	return BroadcastOptions{
		Resolution: "1920x1080",
		FPS:        fps,
		Bitrate:    bitrate,
		WindowID:   targetID,
		Quality:    quality,
	}
}

// DefaultBroadcastOptions returns sensible low-latency defaults (60 FPS balanced)
func DefaultBroadcastOptions() BroadcastOptions {
	return GetPresetOptions(60, "portal")
}

// ReceiverOptions defines configuration for the video player
type ReceiverOptions struct {
	WindowTitle     string   // e.g. "Limoni Voice - User Stream"
	KeepAspect      bool     // preserve aspect ratio
	FPS             int      // Stream framerate (30, 60, 120)
	PreferredPlayer string   // "ffplay", "mpv", or "" (auto)
	CustomMpvFlags  []string // additional mpv flags
}

// DefaultReceiverOptions returns ultra-low-latency receiver defaults
func DefaultReceiverOptions(fps ...int) ReceiverOptions {
	streamFPS := 60
	if len(fps) > 0 && fps[0] > 0 {
		streamFPS = fps[0]
	}
	return ReceiverOptions{
		WindowTitle: fmt.Sprintf("Limoni Voice - Live Screen Stream (%d FPS)", streamFPS),
		KeepAspect:  true,
		FPS:         streamFPS,
	}
}

// Session manages the lifecycle of a broadcaster or receiver subprocess
type Session struct {
	cmd         *exec.Cmd
	extraCmd    *exec.Cmd
	cleanupFunc func()
	ctx         context.Context
	cancel      context.CancelFunc
	errCh       chan error
	doneCh      chan struct{}
	stopped     bool
	isBroad     bool
	targetURL   string
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	stderrBuf   *bytes.Buffer
	mu          sync.Mutex
}

func (s *Session) Stdin() io.WriteCloser {
	return s.stdin
}

func (s *Session) Stdout() io.ReadCloser {
	return s.stdout
}

func (s *Session) BinPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return s.cmd.Path
	}
	return ""
}

var execCache sync.Map

// FindExecutable searches for a binary in PATH, next to current executable, Program Files (all mpv/ffmpeg subfolders), AppData, WinGet, Scoop
func FindExecutable(name string) (string, error) {
	if cached, ok := execCache.Load(name); ok {
		if p, ok := cached.(string); ok && p != "" {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}

	// 1. Check system PATH
	if p, err := exec.LookPath(name); err == nil {
		execCache.Store(name, p)
		return p, nil
	}

	exts := []string{""}
	candidateNames := []string{name}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".com", ""}
		if name == "mpv" {
			candidateNames = append(candidateNames, "mpv", "mpvnet", "mpv-player", "mpvcom", "MPV")
		} else if name == "ffmpeg" {
			candidateNames = append(candidateNames, "ffmpeg", "FFmpeg")
		} else if name == "ffplay" {
			candidateNames = append(candidateNames, "ffplay", "FFplay")
		}
	}

	searchDirs := []string{}

	// 2. Current working directory & ./bin & ./tools
	if cwd, err := os.Getwd(); err == nil {
		searchDirs = append(searchDirs, cwd, filepath.Join(cwd, "bin"), filepath.Join(cwd, "tools"))
	}

	// 3. Next to running executable & exe/bin & exe/tools
	if execPath, err := os.Executable(); err == nil {
		execDir := filepath.Dir(execPath)
		searchDirs = append(searchDirs, execDir, filepath.Join(execDir, "bin"), filepath.Join(execDir, "tools"))
	}

	// 4. User home directory, AppData, Scoop, WinGet
	if home, err := os.UserHomeDir(); err == nil {
		searchDirs = append(searchDirs,
			filepath.Join(home, ".limoni-voice", "bin"),
			filepath.Join(home, "AppData", "Local", "limoni-voice", "bin"),
			filepath.Join(home, "AppData", "Local", "LimoniVoice", "bin"),
			filepath.Join(home, "scoop", "shims"),
			filepath.Join(home, "scoop", "apps", "ffmpeg", "current", "bin"),
			filepath.Join(home, "scoop", "apps", "mpv", "current"),
			filepath.Join(home, "AppData", "Local", "Microsoft", "WinGet", "Links"),
			filepath.Join(home, "Downloads"),
			filepath.Join(home, "Desktop"),
		)
	}

	// 5. Windows specific deep folder discovery
	if runtime.GOOS == "windows" {
		sysDrive := os.Getenv("SystemDrive")
		if sysDrive == "" {
			sysDrive = "C:"
		}

		basePfDirs := []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("ProgramW6432"),
			sysDrive + `\Program Files`,
			sysDrive + `\Program Files (x86)`,
		}

		appDataDirs := []string{
			os.Getenv("LOCALAPPDATA"),
			os.Getenv("APPDATA"),
			os.Getenv("ProgramData"),
		}

		// Fixed common install folders and LimoniVoice local bin
		searchDirs = append(searchDirs,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "LimoniVoice", "bin"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "LimoniVoice"),
			filepath.Join(os.Getenv("APPDATA"), "LimoniVoice", "bin"),
			filepath.Join(os.Getenv("APPDATA"), "LimoniVoice"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links"),
			sysDrive+`\ffmpeg\bin`,
			sysDrive+`\ffmpeg`,
			sysDrive+`\mpv`,
			sysDrive+`\tools\ffmpeg\bin`,
			sysDrive+`\tools\mpv`,
			sysDrive+`\ProgramData\chocolatey\bin`,
			sysDrive+`\ProgramData\chocolatey\lib\mpv\tools`,
		)

		// Scan WinGet Packages recursively for extracted packages
		wingetPackages := filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Packages")
		if entries, err := os.ReadDir(wingetPackages); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					pkgDir := filepath.Join(wingetPackages, entry.Name())
					searchDirs = append(searchDirs, pkgDir)
					// Scan 1 level down
					if subEntries, errSub := os.ReadDir(pkgDir); errSub == nil {
						for _, sub := range subEntries {
							if sub.IsDir() {
								searchDirs = append(searchDirs, filepath.Join(pkgDir, sub.Name()))
							}
						}
					}
				}
			}
		}

		// Scan Program Files subdirectories matching *mpv*, *MPV*, *ffmpeg*, *FFmpeg*, *player*
		for _, pf := range basePfDirs {
			if pf == "" {
				continue
			}
			searchDirs = append(searchDirs,
				filepath.Join(pf, "mpv"),
				filepath.Join(pf, "MPV"),
				filepath.Join(pf, "MPV Player"),
				filepath.Join(pf, "MPV Player", "bin"),
				filepath.Join(pf, "mpv-net"),
				filepath.Join(pf, "mpv.net"),
				filepath.Join(pf, "mpv-player"),
				filepath.Join(pf, "FFmpeg"),
				filepath.Join(pf, "FFmpeg", "bin"),
				filepath.Join(pf, "ffmpeg"),
				filepath.Join(pf, "ffmpeg", "bin"),
			)

			// Scan all subdirectories in Program Files for any mpv/ffmpeg folder
			if entries, err := os.ReadDir(pf); err == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						lower := strings.ToLower(entry.Name())
						if strings.Contains(lower, "mpv") || strings.Contains(lower, "ffmpeg") || strings.Contains(lower, "player") {
							folderPath := filepath.Join(pf, entry.Name())
							searchDirs = append(searchDirs, folderPath, filepath.Join(folderPath, "bin"))
						}
					}
				}
			}
		}

		// Scan AppData Programs
		for _, ad := range appDataDirs {
			if ad == "" {
				continue
			}
			searchDirs = append(searchDirs,
				filepath.Join(ad, "Programs", "mpv"),
				filepath.Join(ad, "Programs", "mpv.net"),
				filepath.Join(ad, "Programs", "MPV Player"),
				filepath.Join(ad, "Programs", "ffmpeg", "bin"),
			)
			if entries, err := os.ReadDir(filepath.Join(ad, "Programs")); err == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						lower := strings.ToLower(entry.Name())
						if strings.Contains(lower, "mpv") || strings.Contains(lower, "ffmpeg") {
							folderPath := filepath.Join(ad, "Programs", entry.Name())
							searchDirs = append(searchDirs, folderPath, filepath.Join(folderPath, "bin"))
						}
					}
				}
			}
		}
	}

	// Iterate over all discovered directories, candidates and extensions
	for _, dir := range searchDirs {
		for _, cName := range candidateNames {
			for _, ext := range exts {
				candidate := filepath.Join(dir, cName+ext)
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					execCache.Store(name, candidate)
					return candidate, nil
				}
			}
		}
	}

	return "", fmt.Errorf("executable '%s' not found", name)
}

// CheckDependencies checks for required tools based on current OS and roles
func CheckDependencies() DependencyStatus {
	_, errMpv := FindExecutable("mpv")
	_, errFFplay := FindExecutable("ffplay")
	_, errFFmpeg := FindExecutable("ffmpeg")
	_, errGSR := FindExecutable("gpu-screen-recorder")
	_, errGst := FindExecutable("gst-launch-1.0")

	hasReceiver := errMpv == nil || errFFplay == nil

	status := DependencyStatus{
		HasMPV:               hasReceiver,
		HasFFmpeg:            errFFmpeg == nil,
		HasGPUScreenRecorder: errGSR == nil,
	}

	if !hasReceiver {
		status.MissingRecommended = "mpv or ffmpeg (required for screen watching)"
	} else if runtime.GOOS == "linux" && !status.HasGPUScreenRecorder && !status.HasFFmpeg && errGst != nil {
		status.MissingRecommended = "gst-launch-1.0, gpu-screen-recorder or ffmpeg (required for screen sharing)"
	} else if runtime.GOOS == "windows" && !status.HasFFmpeg {
		status.MissingRecommended = "ffmpeg (required for screen sharing)"
	} else if runtime.GOOS == "darwin" && !status.HasFFmpeg {
		status.MissingRecommended = "ffmpeg (required for screen sharing)"
	}

	return status
}

func buildLinuxBroadcastCommand(opt BroadcastOptions, targetURL string, onCancel ...func()) (string, []string, *os.File, func(), error) {
	targetID := strings.TrimSpace(opt.WindowID)
	scaleRes := strings.ReplaceAll(opt.Resolution, "x", ":")
	if scaleRes == "" {
		scaleRes = "1920:1080"
	}
	fps := opt.FPS
	if fps <= 0 {
		fps = 60
	}

	// 1. If user selected a specific Window / App on Wayland -> use Desktop Portal Window Cast
	if targetID == "portal:window" || targetID == "portal" || strings.HasPrefix(targetID, "app:") || strings.HasPrefix(targetID, "win:") || targetID == "focused" {
		if isWayland() {
			if _, err := FindExecutable("gst-launch-1.0"); err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
				defer cancel()
				nodeID, pwFile, cleanup, errPortal := RequestPortalScreenCast(ctx, 2, onCancel...) // 2 = Window Only
				if errPortal == nil && nodeID != 0 {
					bin, args, errGst := buildGstreamerPipewireCommand(nodeID, targetURL, opt, pwFile != nil)
					if errGst == nil {
						return bin, args, pwFile, cleanup, nil
					}
					if cleanup != nil {
						cleanup()
					}
				} else if errPortal != nil {
					logMsg("[PORTAL] Window selection failed or cancelled: %v", errPortal)
					return "", nil, nil, nil, errPortal
				}
			}
		}
	}

	// 2. If GNOME Mutter compositor is available (for Screen 1 / Monitors) -> direct popup-less full monitor capture
	if isMutterAvailable() {
		if _, err := FindExecutable("gst-launch-1.0"); err == nil {
			connector := ""
			if strings.HasPrefix(targetID, "monitor:") {
				parts := strings.Split(strings.TrimPrefix(targetID, "monitor:"), ":")
				if len(parts) > 0 && parts[0] != "" {
					connector = parts[0]
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			nodeID, cleanup, errMutter := RequestMutterScreenCast(ctx, connector)
			if errMutter == nil && nodeID != 0 {
				bin, args, errGst := buildGstreamerPipewireCommand(nodeID, targetURL, opt, false)
				if errGst == nil {
					return bin, args, nil, cleanup, nil
				}
				if cleanup != nil {
					cleanup()
				}
			}
		}
	} else if isWayland() {
		// 2b. On KDE Plasma / non-GNOME Wayland compositors -> capture Screen via Desktop Portal
		if _, err := FindExecutable("gst-launch-1.0"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
			defer cancel()
			nodeID, pwFile, cleanup, errPortal := RequestPortalScreenCast(ctx, 3, onCancel...) // 3 = Screen or Window
			if errPortal == nil && nodeID != 0 {
				bin, args, errGst := buildGstreamerPipewireCommand(nodeID, targetURL, opt, pwFile != nil)
				if errGst == nil {
					return bin, args, pwFile, cleanup, nil
				}
				if cleanup != nil {
					cleanup()
				}
			}
		}
	}

	// 3. Try GPU Screen Recorder if available (Fastest, Hardware accelerated NVENC/VAAPI/AMF/KMS)
	if p, err := FindExecutable("gpu-screen-recorder"); err == nil {
		gsrTarget := "screen"
		if targetID == "focused" {
			gsrTarget = "focused"
		} else if strings.HasPrefix(targetID, "monitor:") {
			parts := strings.Split(strings.TrimPrefix(targetID, "monitor:"), ":")
			if len(parts) > 0 && parts[0] != "" {
				gsrTarget = parts[0]
			}
		} else if strings.HasPrefix(targetID, "win:") {
			parts := strings.SplitN(strings.TrimPrefix(targetID, "win:"), ":", 2)
			if len(parts) > 0 && parts[0] != "" {
				gsrTarget = parts[0]
			}
		} else if targetID == "desktop" || targetID == "screen" || targetID == "" {
			gsrTarget = "screen"
		} else {
			gsrTarget = targetID
		}

		bitrateKbps := 1800
		if fps >= 120 {
			bitrateKbps = 2500
		} else if fps <= 30 {
			bitrateKbps = 1200
		}
		if opt.Bitrate != "" {
			clean := strings.TrimSpace(strings.ToLower(opt.Bitrate))
			if strings.HasSuffix(clean, "m") {
				val, _ := strconv.ParseFloat(strings.TrimSuffix(clean, "m"), 64)
				if val > 0 {
					bitrateKbps = int(val * 1000)
				}
			} else if strings.HasSuffix(clean, "k") {
				val, _ := strconv.Atoi(strings.TrimSuffix(clean, "k"))
				if val > 0 {
					bitrateKbps = val
				}
			}
		}

		args := []string{
			"-w", gsrTarget,
			"-s", opt.Resolution,
			"-f", fmt.Sprintf("%d", fps),
			"-k", "h264",
			"-bm", "cbr",
			"-q", fmt.Sprintf("%d", bitrateKbps),
			"-tune", "performance",
			"-keyint", "2",
			"-fallback-cpu-encoding", "yes",
			"-restore-portal-session", "no",
			"-c", "mpegts",
			"-o", targetURL,
		}
		return p, args, nil, nil, nil
	}

	// 4. Try wf-recorder on Wayland / wlroots (Sway, Hyprland, Wayfire)
	if isWayland() {
		if p, err := FindExecutable("wf-recorder"); err == nil {
			var wfArgs []string
			if strings.HasPrefix(targetID, "monitor:") {
				parts := strings.Split(strings.TrimPrefix(targetID, "monitor:"), ":")
				if len(parts) > 0 && parts[0] != "" {
					wfArgs = append(wfArgs, "-o", parts[0])
				}
			}
			wfArgs = append(wfArgs,
				"-m", "mpegts",
				"-c", "libx264",
				"-p", "preset=ultrafast",
				"-p", "tune=zerolatency",
				"-p", "keyint=30",
				"-p", "crf=23",
				"-r", fmt.Sprintf("%d", fps),
				"-f", targetURL,
			)
			return p, wfArgs, nil, nil, nil
		}
	}

	// 5. Universal direct FFmpeg capture across all X11 Linux distributions (GNOME, KDE, XFCE, Cinnamon, MATE, i3, etc.)
	if p, err := FindExecutable("ffmpeg"); err == nil {
		display := getLinuxDisplay()
		vf := fmt.Sprintf("scale=%s:flags=bicubic,format=yuv420p", scaleRes)

		var inputArgs []string
		if strings.HasPrefix(targetID, "monitor:") {
			parts := strings.Split(strings.TrimPrefix(targetID, "monitor:"), ":")
			if len(parts) >= 5 && parts[1] != "" && parts[2] != "" {
				w, h, x, y := parts[1], parts[2], parts[3], parts[4]
				inputArgs = append(inputArgs,
					"-video_size", fmt.Sprintf("%sx%s", w, h),
					"-i", fmt.Sprintf("%s+%s,%s", display, x, y),
				)
			} else {
				inputArgs = append(inputArgs, "-i", display)
			}
		} else if targetID == "focused" {
			winID := getActiveWindowIDX11()
			if winID != "" {
				inputArgs = append(inputArgs, "-window_id", winID, "-i", display)
			} else {
				inputArgs = append(inputArgs, "-i", display)
			}
		} else if strings.HasPrefix(targetID, "win:") {
			parts := strings.SplitN(strings.TrimPrefix(targetID, "win:"), ":", 2)
			if len(parts) > 0 && parts[0] != "" {
				inputArgs = append(inputArgs,
					"-window_id", parts[0],
					"-i", display,
				)
			} else {
				inputArgs = append(inputArgs, "-i", display)
			}
		} else {
			inputArgs = append(inputArgs, "-i", display)
		}

		args := []string{
			"-fflags", "nobuffer+flush_packets",
			"-thread_queue_size", "64",
			"-probesize", "32",
			"-analyzeduration", "0",
			"-f", "x11grab",
			"-framerate", fmt.Sprintf("%d", fps),
			"-draw_mouse", "1",
		}
		args = append(args, inputArgs...)
		bitrate := "1.8M"
		maxRate := "2.4M"
		bufSize := "600k"
		gopSize := fps
		if gopSize > 120 {
			gopSize = 120
		}
		if fps >= 120 {
			bitrate = "2.4M"
			maxRate = "3.2M"
			bufSize = "800k"
		} else if fps <= 30 {
			bitrate = "1M"
			maxRate = "1.5M"
			bufSize = "400k"
		}
		if opt.Bitrate != "" {
			bitrate = opt.Bitrate
			maxRate = opt.Bitrate
		}

		args = append(args,
			"-vf", vf,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-x264-params", fmt.Sprintf("keyint=%d:min-keyint=%d:qpmin=18:qpmax=38:scenecut=0:no-scenecut=1:sync-lookahead=0:rc-lookahead=0:sliced-threads=0:repeat-headers=1:me=hex:subme=2:merange=16:aq-mode=1", gopSize, gopSize),
			"-crf", "22",
			"-b:v", bitrate,
			"-maxrate", maxRate,
			"-bufsize", bufSize,
			"-pix_fmt", "yuv420p",
			"-g", fmt.Sprintf("%d", gopSize),
			"-bf", "0",
			"-bsf:v", "dump_extra",
			"-f", "mpegts",
			"-mpegts_flags", "+latm+pat_pmt_at_frames",
			targetURL,
		)
		return p, args, nil, nil, nil
	}

	return "", nil, nil, nil, errors.New("required screen capture tools ('gpu-screen-recorder', 'gst-launch-1.0' or 'ffmpeg') not found on system")
}

// StartBroadcasting starts hardware-accelerated screen capture and streams over pipe or UDP
func StartBroadcasting(ctx context.Context, targetIP string, port int, opts ...BroadcastOptions) (*Session, error) {
	if targetIP == "" {
		return nil, errors.New("target IP cannot be empty (use '-' for pipe)")
	}
	usePipe := (targetIP == "-")
	if !usePipe && (port <= 0 || port > 65535) {
		return nil, fmt.Errorf("invalid port: %d", port)
	}

	opt := DefaultBroadcastOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	targetURL := "-"
	if !usePipe {
		targetURL = fmt.Sprintf("udp://%s:%d?pkt_size=1316", targetIP, port)
	}

	var binPath string
	var args []string
	var extraFiles []*os.File
	var cleanupFunc func()
	var targetHwnd uintptr
	var winWidth, winHeight int
	var macSckitBin string
	var macWidth, macHeight, macFps int

	sessionCtx, cancel := context.WithCancel(ctx)

	switch runtime.GOOS {
	case "linux":
		var err error
		var pwFile *os.File
		binPath, args, pwFile, cleanupFunc, err = buildLinuxBroadcastCommand(opt, targetURL, cancel)
		if err != nil {
			cancel()
			return nil, err
		}
		if pwFile != nil {
			extraFiles = append(extraFiles, pwFile)
		}

	case "windows":
		// Windows desktop & window capture via FFmpeg
		p, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil, errors.New("'ffmpeg.exe' not found. Please place 'ffmpeg.exe' next to the application or run 'winget install Gyan.FFmpeg' in PowerShell.")
		}
		binPath = p

		scaleRes := strings.ReplaceAll(opt.Resolution, "x", ":")
		if scaleRes == "" {
			scaleRes = "1920:1080"
		}
		scaleOpt := fmt.Sprintf("scale=%s:flags=bicubic:force_original_aspect_ratio=decrease,pad=ceil(iw/2)*2:ceil(ih/2)*2,format=yuv420p", scaleRes)

		if strings.HasPrefix(opt.WindowID, "hwnd:") {
			parts := strings.SplitN(strings.TrimPrefix(opt.WindowID, "hwnd:"), ":", 2)
			if len(parts) > 0 {
				parsed, _ := strconv.ParseUint(parts[0], 10, 64)
				targetHwnd = uintptr(parsed)
			}
		}

		winFps := opt.FPS
		if winFps <= 0 {
			winFps = 60
		}
		winGop := winFps
		if winGop > 120 {
			winGop = 120
		}
		winBitrate := "1.8M"
		winMaxRate := "2.4M"
		winBufSize := "600k"
		if winFps >= 120 {
			winBitrate = "2.4M"
			winMaxRate = "3.2M"
			winBufSize = "800k"
		} else if winFps <= 30 {
			winBitrate = "1M"
			winMaxRate = "1.5M"
			winBufSize = "400k"
		}
		if opt.Bitrate != "" {
			winBitrate = opt.Bitrate
			winMaxRate = opt.Bitrate
		}

		if targetHwnd != 0 {
			winWidth = 1920
			winHeight = 1080
			if opt.Resolution != "" && strings.Contains(opt.Resolution, "x") {
				parts := strings.Split(opt.Resolution, "x")
				if len(parts) == 2 {
					if w, err := strconv.Atoi(parts[0]); err == nil && w > 0 {
						winWidth = w
					}
					if h, err := strconv.Atoi(parts[1]); err == nil && h > 0 {
						winHeight = h
					}
				}
			}
			winWidth = (winWidth / 2) * 2
			winHeight = (winHeight / 2) * 2

			args = []string{
				"-fflags", "nobuffer+flush_packets",
				"-f", "rawvideo",
				"-pixel_format", "bgra",
				"-video_size", fmt.Sprintf("%dx%d", winWidth, winHeight),
				"-framerate", fmt.Sprintf("%d", winFps),
				"-i", "pipe:0",
				"-vf", scaleOpt,
				"-c:v", "libx264",
				"-preset", "ultrafast",
				"-tune", "zerolatency",
				"-x264-params", fmt.Sprintf("keyint=%d:min-keyint=%d:qpmin=18:qpmax=38:scenecut=0:no-scenecut=1:sync-lookahead=0:rc-lookahead=0:sliced-threads=0:repeat-headers=1:me=hex:subme=2:merange=16:aq-mode=1", winGop, winGop),
				"-crf", "23",
				"-b:v", winBitrate,
				"-maxrate", winMaxRate,
				"-bufsize", winBufSize,
				"-pix_fmt", "yuv420p",
				"-g", fmt.Sprintf("%d", winGop),
				"-bf", "0",
				"-bsf:v", "dump_extra",
				"-f", "mpegts",
				"-mpegts_flags", "+latm+pat_pmt_at_frames",
				"-pcr_period", "20",
				targetURL,
			}
		} else {
			inputArgs := []string{
				"-fflags", "nobuffer+flush_packets",
				"-thread_queue_size", "64",
				"-probesize", "32",
				"-analyzeduration", "0",
				"-f", "gdigrab",
				"-framerate", fmt.Sprintf("%d", winFps),
				"-draw_mouse", "1",
			}

			if strings.HasPrefix(opt.WindowID, "monitor:") {
				mParts := strings.Split(opt.WindowID, ":")
				if len(mParts) >= 5 && (mParts[1] != "0" || mParts[2] != "0") {
					inputArgs = append(inputArgs,
						"-offset_x", mParts[1],
						"-offset_y", mParts[2],
						"-video_size", fmt.Sprintf("%sx%s", mParts[3], mParts[4]),
						"-i", "desktop",
					)
				} else {
					physW, physH := GetPhysicalDesktopSize()
					if physW > 0 && physH > 0 {
						inputArgs = append(inputArgs, "-video_size", fmt.Sprintf("%dx%d", physW, physH), "-offset_x", "0", "-offset_y", "0")
					}
					inputArgs = append(inputArgs, "-i", "desktop")
				}
			} else {
				physW, physH := GetPhysicalDesktopSize()
				if physW > 0 && physH > 0 {
					inputArgs = append(inputArgs, "-video_size", fmt.Sprintf("%dx%d", physW, physH), "-offset_x", "0", "-offset_y", "0")
				}
				inputArgs = append(inputArgs, "-i", "desktop")
			}

			args = append(inputArgs,
				"-vf", scaleOpt,
				"-c:v", "libx264",
				"-preset", "ultrafast",
				"-tune", "zerolatency",
				"-x264-params", fmt.Sprintf("keyint=%d:min-keyint=%d:qpmin=18:qpmax=38:scenecut=0:no-scenecut=1:sync-lookahead=0:rc-lookahead=0:sliced-threads=0:repeat-headers=1:me=hex:subme=2:merange=16:aq-mode=1", winGop, winGop),
				"-crf", "23",
				"-b:v", winBitrate,
				"-maxrate", winMaxRate,
				"-bufsize", winBufSize,
				"-pix_fmt", "yuv420p",
				"-g", fmt.Sprintf("%d", winGop),
				"-bf", "0",
				"-bsf:v", "dump_extra",
				"-f", "mpegts",
				"-mpegts_flags", "+latm+pat_pmt_at_frames",
				"-pcr_period", "20",
				targetURL,
			)
		}

	case "darwin":
		p, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil, errors.New("'ffmpeg' is required on macOS for screen sharing (brew install ffmpeg)")
		}
		binPath = p

		fps := opt.FPS
		if fps <= 0 {
			fps = 60
		}
		width := 1920
		height := 1080
		if opt.Resolution != "" && strings.Contains(opt.Resolution, "x") {
			parts := strings.Split(opt.Resolution, "x")
			if len(parts) == 2 {
				if w, err := strconv.Atoi(parts[0]); err == nil && w > 0 {
					width = w
				}
				if h, err := strconv.Atoi(parts[1]); err == nil && h > 0 {
					height = h
				}
			}
		}

		macHelper, sckitErr := getOrBuildMacCaptureBinary()
		if sckitErr == nil {
			logMsg("[DARWIN] Using native Apple ScreenCaptureKit -> FFmpeg rawvideo pipe")
			args = []string{
				"-f", "rawvideo",
				"-pixel_format", "bgra",
				"-video_size", fmt.Sprintf("%dx%d", width, height),
				"-framerate", fmt.Sprintf("%d", fps),
				"-i", "-",
				"-vf", "format=yuv420p",
				"-c:v", "libx264",
				"-preset", "ultrafast",
				"-tune", "zerolatency",
				"-x264-params", "keyint=60:min-keyint=60:qpmin=18:qpmax=38:scenecut=0:no-scenecut=1:sync-lookahead=0:rc-lookahead=0:sliced-threads=0:repeat-headers=1:me=hex:subme=2:merange=16:aq-mode=1",
				"-crf", "23",
				"-b:v", "2M",
				"-maxrate", "2.5M",
				"-bufsize", "2.5M",
				"-pix_fmt", "yuv420p",
				"-g", "60",
				"-bf", "0",
				"-f", "mpegts",
				"-mpegts_flags", "+pat_pmt_at_frames",
				"-pcr_period", "20",
				"-flush_packets", "1",
				targetURL,
			}
			macSckitBin = macHelper
			macWidth = width
			macHeight = height
			macFps = fps
		} else {
			logMsg("[DARWIN] ScreenCaptureKit unavailable (%v), falling back to AVFoundation", sckitErr)
			screenDev := getMacScreenDevice(binPath)
			scaleRes := fmt.Sprintf("%d:%d", width, height)
			args = []string{
				"-f", "avfoundation",
				"-capture_cursor", "1",
				"-pixel_format", "uyvy422",
				"-i", screenDev,
				"-vf", fmt.Sprintf("scale=%s:flags=bicubic,format=yuv420p", scaleRes),
				"-r", fmt.Sprintf("%d", fps),
				"-c:v", "libx264",
				"-preset", "ultrafast",
				"-tune", "zerolatency",
				"-x264-params", "keyint=60:min-keyint=60:qpmin=18:qpmax=38:scenecut=0:no-scenecut=1:sync-lookahead=0:rc-lookahead=0:sliced-threads=0:repeat-headers=1:me=hex:subme=2:merange=16:aq-mode=1",
				"-crf", "23",
				"-b:v", "2M",
				"-maxrate", "2.5M",
				"-bufsize", "2.5M",
				"-pix_fmt", "yuv420p",
				"-g", "60",
				"-bf", "0",
				"-f", "mpegts",
				"-mpegts_flags", "+pat_pmt_at_frames",
				"-pcr_period", "20",
				"-flush_packets", "1",
				targetURL,
			}
		}

	default:
		return nil, fmt.Errorf("unsupported platform for screen broadcasting: %s", runtime.GOOS)
	}

	logMsg("[BROADCAST] Starting command: %s %s", binPath, strings.Join(args, " "))

	cmd := exec.CommandContext(sessionCtx, binPath, args...)
	if len(extraFiles) > 0 {
		cmd.ExtraFiles = extraFiles
	}
	setupProcessGroup(cmd)

	var sckitCmd *exec.Cmd
	if macSckitBin != "" {
		sckitCmd = exec.CommandContext(sessionCtx, macSckitBin, fmt.Sprintf("%d", macWidth), fmt.Sprintf("%d", macHeight), fmt.Sprintf("%d", macFps), opt.WindowID)
		setupProcessGroup(sckitCmd)
		sckitOut, err := sckitCmd.StdoutPipe()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to open ScreenCaptureKit pipe: %w", err)
		}
		cmd.Stdin = sckitOut

		if sckitErrPipe, errP := sckitCmd.StderrPipe(); errP == nil {
			go func() {
				scanner := bufio.NewScanner(sckitErrPipe)
				for scanner.Scan() {
					trimmed := strings.TrimSpace(scanner.Text())
					if trimmed != "" {
						logMsg("[SCKIT-LIVE] %s", trimmed)
					}
				}
			}()
		}
	}

	var stdinPipe io.WriteCloser
	if targetHwnd != 0 {
		var err error
		stdinPipe, err = cmd.StdinPipe()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to open stdin pipe for window capture: %w", err)
		}
	}

	var stdoutPipe io.ReadCloser
	if usePipe {
		var err error
		stdoutPipe, err = cmd.StdoutPipe()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to open stdout pipe: %w", err)
		}
	} else {
		cmd.Stdout = nil
	}
	stderrBuf := &bytes.Buffer{}
	stderrPipe, errP := cmd.StderrPipe()
	if errP == nil {
		go func() {
			scanner := bufio.NewScanner(stderrPipe)
			for scanner.Scan() {
				text := scanner.Text()
				stderrBuf.WriteString(text + "\n")
				trimmed := strings.TrimSpace(text)
				if trimmed != "" {
					logMsg("[BROADCAST-LIVE] %s", trimmed)
				}
			}
		}()
	} else {
		cmd.Stderr = stderrBuf
	}

	s := &Session{
		cmd:         cmd,
		extraCmd:    sckitCmd,
		cleanupFunc: cleanupFunc,
		ctx:         sessionCtx,
		cancel:      cancel,
		errCh:       make(chan error, 1),
		doneCh:      make(chan struct{}),
		isBroad:     true,
		targetURL:   targetURL,
		stdout:      stdoutPipe,
		stderrBuf:   stderrBuf,
	}

	if sckitCmd != nil {
		if err := sckitCmd.Start(); err != nil {
			cancel()
			return nil, fmt.Errorf("failed to start ScreenCaptureKit engine: %w", err)
		}
	}

	if err := cmd.Start(); err != nil {
		cancel()
		if sckitCmd != nil && sckitCmd.Process != nil {
			_ = sckitCmd.Process.Kill()
		}
		return nil, fmt.Errorf("failed to start screen broadcaster (%s): %w", binPath, err)
	}

	if targetHwnd != 0 && stdinPipe != nil {
		go StreamWindowFrames(sessionCtx, targetHwnd, opt.FPS, winWidth, winHeight, stdinPipe)
	}

	if runtime.GOOS == "linux" {
		if strings.HasPrefix(opt.WindowID, "app:") {
			parts := strings.Split(strings.TrimPrefix(opt.WindowID, "app:"), ":")
			if len(parts) > 0 {
				if pid, err := strconv.Atoi(parts[0]); err == nil && pid > 1 {
					go watchPIDLiveness(sessionCtx, pid, cancel)
				}
			}
		} else if strings.HasPrefix(opt.WindowID, "win:") || opt.WindowID == "focused" {
			targetWinID := strings.TrimPrefix(opt.WindowID, "win:")
			if opt.WindowID == "focused" {
				targetWinID = getActiveWindowIDX11()
			} else if strings.Contains(targetWinID, ":") {
				targetWinID = strings.Split(targetWinID, ":")[0]
			}
			if targetWinID != "" {
				go watchX11WindowLiveness(sessionCtx, targetWinID, cancel)
			}
		}
	}

	go s.monitor()
	return s, nil
}

// StartReceiving launches a high-performance native video window (mpv or ffplay fallback) with zero-latency flags
func StartReceiving(ctx context.Context, port int, opts ...ReceiverOptions) (*Session, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid receiver port: %d", port)
	}

	opt := DefaultReceiverOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	windowTitle := opt.WindowTitle
	if windowTitle == "" {
		if opt.FPS > 0 {
			windowTitle = fmt.Sprintf("Limoni Voice - Live Screen Stream (%d FPS)", opt.FPS)
		} else {
			windowTitle = "Limoni Voice - Live Screen Stream"
		}
	}

	streamURL := fmt.Sprintf("tcp://127.0.0.1:%d", port)

	var binPath string
	var args []string

	useFfplay := strings.EqualFold(opt.PreferredPlayer, "ffplay")

	if !useFfplay {
		if p, err := FindExecutable("mpv"); err == nil {
			binPath = p
			args = []string{
				streamURL,
				"--no-config",
				"--ytdl=no",
				"--really-quiet",
				"--no-audio",
				"--profile=low-latency",
				"--untimed",
				"--vd-lavc-threads=0",
				"--cache=no",
				"--demuxer-readahead-secs=0",
				"--stream-buffer-size=4k",
				"--framedrop=vo",
				"--hwdec=auto-safe",
				"--vd-lavc-show-all=no",
				"--video-sync=desync",
				"--force-window=yes",
				"--ontop=yes",
				"--keep-open=yes",
				"--idle=yes",
				"--no-osc",
				"--no-osd-bar",
				"--osd-level=1",
				fmt.Sprintf("--osd-playing-msg=Limoni Voice Stream (%d FPS) - Press 'Shift+I' for live stats", opt.FPS),
				"--cursor-autohide=1000",
				"--demuxer-lavf-format=mpegts",
				"--demuxer-lavf-analyzeduration=0.1",
				"--demuxer-lavf-probesize=32768",
				"--title=" + windowTitle,
				"--autofit=65%x65%",
			}
			if runtime.GOOS == "windows" {
				args = append(args, "--d3d11-sync-interval=0", "--swapchain-depth=1")
			} else {
				// On Linux, always specify --vo=gpu explicitly so mpv doesn't fall through to x11
				// which triggers 'Assertion !vo->x11 failed' on Wayland/NVIDIA systems.
				args = append(args, "--vo=gpu")
				if isWayland() {
					args = append(args, "--gpu-context=wayland")
				}
			}
			if len(opt.CustomMpvFlags) > 0 {
				args = append(args, opt.CustomMpvFlags...)
			}
		} else if p, err := FindExecutable("ffplay"); err == nil {
			useFfplay = true
			binPath = p
		}
	}

	if useFfplay {
		if binPath == "" {
			if p, err := FindExecutable("ffplay"); err == nil {
				binPath = p
			} else {
				return nil, errors.New("'ffplay' executable not found")
			}
		}
		threads := runtime.NumCPU()
		if threads > 8 {
			threads = 8
		} else if threads < 2 {
			threads = 2
		}
		args = []string{
			"-an",
			"-sn",
			"-loglevel", "warning",
			"-flags", "low_delay",
			"-fflags", "nobuffer+flush_packets",
			"-threads", fmt.Sprintf("%d", threads),
			"-probesize", "65536",
			"-analyzeduration", "100000",
			"-f", "mpegts",
			"-alwaysontop",
			"-window_title", windowTitle,
			"-x", "1280",
			"-y", "720",
			streamURL,
		}
	} else if binPath == "" {
		if runtime.GOOS == "windows" {
			return nil, errors.New("'mpv.exe' or 'ffplay.exe' not found to watch stream. Please place 'mpv.exe' next to the application or run 'winget install mpv.mpv' in PowerShell.")
		}
		return nil, errors.New("'ffplay' or 'mpv' not found on system to watch stream. Please install ffmpeg or mpv (e.g., sudo pacman -S ffmpeg / sudo apt install ffmpeg).")
	}

	logMsg("[RECEIVER] Starting command: %s %s", binPath, strings.Join(args, " "))

	sessionCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(sessionCtx, binPath, args...)
	cmd.Stdout = nil
	stderrBuf := &bytes.Buffer{}
	stderrPipe, errP := cmd.StderrPipe()
	if errP == nil {
		go func() {
			scanner := bufio.NewScanner(stderrPipe)
			for scanner.Scan() {
				text := scanner.Text()
				stderrBuf.WriteString(text + "\n")
				trimmed := strings.TrimSpace(text)
				if trimmed != "" {
					logMsg("[MPV-LIVE] %s", trimmed)
				}
			}
		}()
	} else {
		cmd.Stderr = stderrBuf
	}
	setupProcessGroup(cmd)

	s := &Session{
		cmd:       cmd,
		ctx:       sessionCtx,
		cancel:    cancel,
		errCh:     make(chan error, 1),
		doneCh:    make(chan struct{}),
		isBroad:   false,
		targetURL: streamURL,
		stderrBuf: stderrBuf,
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start screen receiver (%s): %w", binPath, err)
	}

	go s.monitor()
	return s, nil
}

// monitor waits for process exit and cleans up
func (s *Session) monitor() {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.stopped = true
	cleanup := s.cleanupFunc
	s.cleanupFunc = nil
	s.mu.Unlock()

	if cleanup != nil {
		cleanup()
	}

	stderrStr := ""
	if s.stderrBuf != nil {
		stderrStr = strings.TrimSpace(s.stderrBuf.String())
	}

	if s.extraCmd != nil && s.extraCmd.Process != nil {
		go killProcessGroup(s.extraCmd)
	}

	if err != nil && s.ctx.Err() == nil {
		logMsg("[SESSION] Process exited with error: %v\n[STDERR]: %s", err, stderrStr)
		if stderrStr != "" {
			lines := strings.Split(stderrStr, "\n")
			var errLines []string
			for i := len(lines) - 1; i >= 0 && len(errLines) < 5; i-- {
				trimmed := strings.TrimSpace(lines[i])
				if trimmed != "" && !strings.HasPrefix(trimmed, "frame=") && !strings.HasPrefix(trimmed, "size=") {
					errLines = append([]string{trimmed}, errLines...)
				}
			}
			if len(errLines) > 0 {
				err = fmt.Errorf("%w: %s", err, strings.Join(errLines, " | "))
			}
		}
		s.errCh <- err
	} else {
		logMsg("[SESSION] Process terminated cleanly.")
	}
	close(s.doneCh)
}

// Stop terminates the subprocess
func (s *Session) Stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	cleanup := s.cleanupFunc
	s.cleanupFunc = nil
	s.mu.Unlock()

	if cleanup != nil {
		cleanup()
	}

	s.cancel()

	// Terminate process group instantly in background
	if s.cmd != nil && s.cmd.Process != nil {
		go killProcessGroup(s.cmd)
	}
	if s.extraCmd != nil && s.extraCmd.Process != nil {
		go killProcessGroup(s.extraCmd)
	}

	return nil
}

// Done returns a channel that closes when the session terminates
func (s *Session) Done() <-chan struct{} {
	return s.doneCh
}

// Err returns any error encountered during execution
func (s *Session) Err() <-chan error {
	return s.errCh
}

// IsBroadcasting returns true if this session is sending video
func (s *Session) IsBroadcasting() bool {
	return s.isBroad
}

// TargetURL returns the UDP stream URL
func (s *Session) TargetURL() string {
	return s.targetURL
}
