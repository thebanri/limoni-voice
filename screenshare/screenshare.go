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
							Title: "🖥️  " + name,
						})
					}
				} else if parts[0] == "ALL" {
					sub := strings.Split(parts[1], "|")
					if len(sub) >= 5 {
						screenTargets = append(screenTargets, WindowInfo{
							ID:    "desktop",
							Title: "🖥️  " + sub[4],
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
								Title: "🪟  " + title,
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
			Title: "🖥️  Entire Screen (Primary Display)",
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
								Title: "🪟  " + title,
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
		Title: "🎯 Currently Focused Window (Active Application)",
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
						title := fmt.Sprintf("🖥️  Screen %d: %s (%sx%s)%s", idx, monName, w, h, primaryTag)
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
							Title: fmt.Sprintf("🖥️  Screen %d: %s%s", idx, parts[0], res),
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
				Title: "🖥️  Entire Desktop (All Screens Combined)",
			})
		}
	} else {
		targets = append(targets, WindowInfo{
			ID:    "desktop",
			Title: "🖥️  Entire Screen (Primary Display)",
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
							Title: "🪟  " + title,
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
								Title: "🪟  " + displayTitle,
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
							Title: "🪟  " + displayTitle,
						})
					}
					curTitle, curClass = "", ""
				}
			}
		}
	}

	if len(windowTargets) > 0 {
		targets = append(targets, windowTargets...)
	}

	return targets
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
	binPath := filepath.Join(os.TempDir(), "limoni-mac-sckit-v9")
	if info, err := os.Stat(binPath); err == nil && info.Size() > 0 {
		return binPath, nil
	}

	swiftc, err := FindExecutable("swiftc")
	if err != nil {
		return "", errors.New("swiftc not found on macOS")
	}

	srcFile := filepath.Join(os.TempDir(), "limoni_mac_capture.swift")
	if err := os.WriteFile(srcFile, []byte(embeddedMacCaptureSwift), 0644); err != nil {
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

// DefaultBroadcastOptions returns sensible low-latency defaults
func DefaultBroadcastOptions() BroadcastOptions {
	return BroadcastOptions{
		Resolution: "1920x1080",
		FPS:        60,
		Bitrate:    "6M",
		WindowID:   "portal",
		Quality:    "high",
	}
}

// ReceiverOptions defines configuration for the video player
type ReceiverOptions struct {
	WindowTitle    string   // e.g. "Limoni Voice - User Stream"
	KeepAspect     bool     // preserve aspect ratio
	CustomMpvFlags []string // additional mpv flags
}

// DefaultReceiverOptions returns ultra-low-latency receiver defaults
func DefaultReceiverOptions() ReceiverOptions {
	return ReceiverOptions{
		WindowTitle: "Limoni Voice - Live Screen Stream (HD 60 FPS)",
		KeepAspect:  true,
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
			sysDrive+`\ffmpeg\bin`,
			sysDrive+`\ffmpeg`,
			sysDrive+`\mpv`,
			sysDrive+`\tools\ffmpeg\bin`,
			sysDrive+`\tools\mpv`,
			sysDrive+`\ProgramData\chocolatey\bin`,
			sysDrive+`\ProgramData\chocolatey\lib\mpv\tools`,
		)

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
		status.MissingRecommended = "mpv veya ffmpeg (ekran izlemek icin gereklidir)"
	} else if runtime.GOOS == "linux" && !status.HasGPUScreenRecorder && !status.HasFFmpeg && errGst != nil {
		status.MissingRecommended = "gst-launch-1.0, gpu-screen-recorder veya ffmpeg (ekran paylasmak icin gereklidir)"
	} else if runtime.GOOS == "windows" && !status.HasFFmpeg {
		status.MissingRecommended = "ffmpeg (ekran paylasmak icin gereklidir)"
	} else if runtime.GOOS == "darwin" && !status.HasFFmpeg {
		status.MissingRecommended = "ffmpeg (ekran paylasmak icin gereklidir)"
	}

	return status
}

func buildLinuxBroadcastCommand(opt BroadcastOptions, targetURL string) (string, []string, func(), error) {
	targetID := strings.TrimSpace(opt.WindowID)
	scaleRes := strings.ReplaceAll(opt.Resolution, "x", ":")
	if scaleRes == "" {
		scaleRes = "1920:1080"
	}
	fps := opt.FPS
	if fps <= 0 {
		fps = 60
	}

	// 1. If GNOME Mutter compositor is available (GNOME Wayland/X11):
	// Direct, popup-less, 100% full desktop capture with embedded cursor via PipeWire + GStreamer
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
				bin, args, errGst := buildGstreamerPipewireCommand(nodeID, targetURL, fps)
				if errGst == nil {
					return bin, args, cleanup, nil
				}
				if cleanup != nil {
					cleanup()
				}
			}
		}
	}

	// 2. Try GPU Screen Recorder if available (Fastest, Hardware accelerated NVENC/VAAPI/AMF/KMS)
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

		args := []string{
			"-w", gsrTarget,
			"-s", opt.Resolution,
			"-f", fmt.Sprintf("%d", fps),
			"-k", "h264",
			"-q", "high",
			"-tune", "performance",
			"-keyint", "15",
			"-restore-portal-session", "no",
			"-c", "mpegts",
			"-o", targetURL,
		}
		return p, args, nil, nil
	}

	// 3. Try wf-recorder on Wayland / wlroots (Sway, Hyprland, Wayfire)
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
			return p, wfArgs, nil, nil
		}
	}

	// 4. Universal direct FFmpeg capture across all X11 Linux distributions (GNOME, KDE, XFCE, Cinnamon, MATE, i3, etc.)
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
			"-thread_queue_size", "2",
			"-probesize", "32",
			"-analyzeduration", "0",
			"-f", "x11grab",
			"-framerate", fmt.Sprintf("%d", fps),
			"-draw_mouse", "1",
		}
		args = append(args, inputArgs...)
		args = append(args,
			"-vf", vf,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-x264-params", "repeat-headers=1:keyint=30:min-keyint=30:scenecut=0:sync-lookahead=0:rc-lookahead=0:sliced-threads=1",
			"-crf", "23",
			"-maxrate", "8M",
			"-bufsize", "16M",
			"-pix_fmt", "yuv420p",
			"-g", "30",
			"-bf", "0",
			"-bsf:v", "dump_extra",
			"-f", "mpegts",
			"-mpegts_flags", "+latm+pat_pmt_at_frames",
			targetURL,
		)
		return p, args, nil, nil
	}

	return "", nil, nil, errors.New("sistemde ekran paylasimi icin gerekli araclar ('gpu-screen-recorder', 'gst-launch-1.0' veya 'ffmpeg') bulunamadi")
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
		targetURL = fmt.Sprintf("udp://%s:%d?pkt_size=940", targetIP, port)
	}

	var binPath string
	var args []string
	var extraFiles []*os.File
	var cleanupFunc func()
	var targetHwnd uintptr
	var macSckitBin string
	var macWidth, macHeight, macFps int

	switch runtime.GOOS {
	case "linux":
		var err error
		binPath, args, cleanupFunc, err = buildLinuxBroadcastCommand(opt, targetURL)
		if err != nil {
			return nil, err
		}

	case "windows":
		// Windows desktop & window capture via FFmpeg
		p, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil, errors.New("'ffmpeg.exe' bulunamadi. Lutfen 'ffmpeg.exe' dosyasini uygulamanin yanina koyun veya PowerShell'de 'winget install Gyan.FFmpeg' calistirin.")
		}
		binPath = p
		scaleOpt := "pad=ceil(iw/2)*2:ceil(ih/2)*2,format=yuv420p"

		if strings.HasPrefix(opt.WindowID, "hwnd:") {
			parts := strings.SplitN(strings.TrimPrefix(opt.WindowID, "hwnd:"), ":", 2)
			if len(parts) > 0 {
				parsed, _ := strconv.ParseUint(parts[0], 10, 64)
				targetHwnd = uintptr(parsed)
			}
		}

		if targetHwnd != 0 {
			w, h := GetWindowDimensions(targetHwnd)
			args = []string{
				"-fflags", "nobuffer+flush_packets",
				"-f", "rawvideo",
				"-pixel_format", "bgra",
				"-video_size", fmt.Sprintf("%dx%d", w, h),
				"-framerate", fmt.Sprintf("%d", opt.FPS),
				"-i", "pipe:0",
				"-vf", scaleOpt,
				"-c:v", "libx264",
				"-preset", "ultrafast",
				"-tune", "zerolatency",
				"-x264-params", "repeat-headers=1:keyint=30:min-keyint=30:scenecut=0:sync-lookahead=0:rc-lookahead=0:sliced-threads=1",
				"-crf", "23",
				"-maxrate", "8M",
				"-bufsize", "16M",
				"-pix_fmt", "yuv420p",
				"-g", "30",
				"-bf", "0",
				"-bsf:v", "dump_extra",
				"-f", "mpegts",
				"-mpegts_flags", "+latm+pat_pmt_at_frames",
				targetURL,
			}
		} else {
			inputArgs := []string{
				"-fflags", "nobuffer+flush_packets",
				"-thread_queue_size", "2",
				"-probesize", "32",
				"-analyzeduration", "0",
				"-f", "gdigrab",
				"-framerate", fmt.Sprintf("%d", opt.FPS),
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
				"-x264-params", "repeat-headers=1:keyint=30:min-keyint=30:scenecut=0:sync-lookahead=0:rc-lookahead=0:sliced-threads=1",
				"-crf", "23",
				"-maxrate", "8M",
				"-bufsize", "16M",
				"-pix_fmt", "yuv420p",
				"-g", "30",
				"-bf", "0",
				"-bsf:v", "dump_extra",
				"-f", "mpegts",
				"-mpegts_flags", "+latm+pat_pmt_at_frames",
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
				"-x264-params", "repeat-headers=1:keyint=30:min-keyint=30:scenecut=0:sync-lookahead=0:rc-lookahead=0:sliced-threads=1",
				"-crf", "23",
				"-maxrate", "8M",
				"-bufsize", "16M",
				"-pix_fmt", "yuv420p",
				"-g", "30",
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
				"-x264-params", "repeat-headers=1:keyint=30:min-keyint=30:scenecut=0:sync-lookahead=0:rc-lookahead=0:sliced-threads=1",
				"-crf", "23",
				"-maxrate", "8M",
				"-bufsize", "16M",
				"-pix_fmt", "yuv420p",
				"-g", "30",
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

	sessionCtx, cancel := context.WithCancel(ctx)
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
		go StreamWindowFrames(sessionCtx, targetHwnd, opt.FPS, stdinPipe)
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
		windowTitle = "Limoni Voice - Live Screen Stream (HD 60 FPS)"
	}

	streamURL := fmt.Sprintf("tcp://127.0.0.1:%d", port)

	var binPath string
	var args []string

	if p, err := FindExecutable("mpv"); err == nil {
		binPath = p
		args = []string{
			streamURL,
			"--no-config",
			"--ytdl=no",
			"--load-scripts=no",
			"--really-quiet",
			"--no-audio",
			"--profile=low-latency",
			"--cache=no",
			"--no-cache",
			"--hwdec=auto-safe",
			"--video-sync=desync",
			"--framedrop=vo",
			"--untimed=yes",
			"--no-osc",
			"--osc=no",
			"--no-osd-bar",
			"--osd-bar=no",
			"--osd-level=0",
			"--cursor-autohide=1000",
			"--demuxer-readahead-secs=0",
			"--demuxer-max-bytes=100K",
			"--demuxer-max-back-bytes=0",
			"--demuxer-lavf-format=mpegts",
			"--demuxer-lavf-analyzeduration=0",
			"--demuxer-lavf-probesize=1024",
			"--demuxer-lavf-o=fflags=+nobuffer+flush_packets",
			"--title=" + windowTitle,
			"--autofit=65%x65%",
		}
		if len(opt.CustomMpvFlags) > 0 {
			args = append(args, opt.CustomMpvFlags...)
		}
	} else if p, err := FindExecutable("ffplay"); err == nil {
		binPath = p
		args = []string{
			"-loglevel", "warning",
			"-flags", "low_delay",
			"-fflags", "nobuffer+flush_packets+discardcorrupt",
			"-framedrop",
			"-sync", "ext",
			"-probesize", "32",
			"-analyzeduration", "0",
			"-f", "mpegts",
			"-window_title", windowTitle,
			"-i", streamURL,
		}
	} else {
		if runtime.GOOS == "windows" {
			return nil, errors.New("ekran izlemek icin 'mpv.exe' veya 'ffplay.exe' bulunamadi. Lutfen 'mpv.exe'yi uygulamanin yanina koyun veya PowerShell'de 'winget install mpv.mpv' calistirin.")
		}
		return nil, errors.New("ekrani izlemek icin sistemde 'mpv' veya 'ffplay' (ffmpeg) bulunamadi. Lutfen 'mpv' yukleyin (ornek: sudo apt install mpv / brew install mpv).")
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
