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
			cmd := exec.Command(binPath, "--list")
			if out, err := cmd.Output(); err == nil {
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
		targets = append(targets, WindowInfo{
			ID:    "desktop",
			Title: "🖥️  Entire Screen (Primary Display)",
		})
		// Try wmctrl -l
		if p, err := FindExecutable("wmctrl"); err == nil {
			cmd := exec.Command(p, "-l")
			if out, err := cmd.Output(); err == nil {
				lines := strings.Split(string(out), "\n")
				seen := make(map[string]bool)
				for _, line := range lines {
					trimmed := strings.TrimSpace(line)
					if trimmed == "" {
						continue
					}
					fields := strings.Fields(trimmed)
					if len(fields) >= 4 {
						winID := fields[0]
						title := strings.Join(fields[3:], " ")
						if !seen[title] && !strings.EqualFold(title, "Desktop") {
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
	}
	return targets
}

func getMacScreenDevice(binPath string) string {
	return "3:none"
}

const embeddedMacCaptureSwift = `import Foundation
import ScreenCaptureKit
import CoreMedia
import CoreVideo

@available(macOS 12.3, *)
class ScreenRecorder: NSObject, SCStreamOutput, SCStreamDelegate {
    var stream: SCStream?
    let stdoutHandle = FileHandle.standardOutput

    func start(fps: Int = 60, width: Int = 1920, height: Int = 1080, targetWindowID: CGWindowID? = nil) async {
        do {
            let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
            var filter: SCContentFilter?

            if let winID = targetWindowID, let targetWin = content.windows.first(where: { $0.windowID == winID }) {
                filter = SCContentFilter(desktopIndependentWindow: targetWin)
            } else if let display = content.displays.first {
                filter = SCContentFilter(display: display, excludingApplications: [], exceptingWindows: [])
            }

            guard let activeFilter = filter else {
                fputs("Error: No display or window found\n", stderr)
                exit(1)
            }

            let config = SCStreamConfiguration()
            config.width = width
            config.height = height
            config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
            config.pixelFormat = kCVPixelFormatType_32BGRA
            config.showsCursor = true
            config.queueDepth = 5

            let stream = SCStream(filter: activeFilter, configuration: config, delegate: self)
            try stream.addStreamOutput(self, type: .screen, sampleHandlerQueue: DispatchQueue(label: "screen.capture.queue", qos: .userInteractive))
            try await stream.startCapture()
            self.stream = stream
            fputs("[SCKIT] ScreenCaptureKit stream running at \(width)x\(height) @ \(fps) FPS\n", stderr)
        } catch {
            fputs("Error starting ScreenCaptureKit: \(error)\n", stderr)
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
        let height = CVPixelBufferGetHeight(pixelBuffer)
        let totalBytes = bytesPerRow * height

        let data = Data(bytes: baseAddress, count: totalBytes)
        stdoutHandle.write(data)
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        fputs("Stream stopped with error: \(error)\n", stderr)
        exit(1)
    }
}

if #available(macOS 12.3, *) {
    if CommandLine.arguments.contains("--list") {
        Task {
            do {
                let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
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
            exit(0)
        }
        dispatchMain()
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
        Task {
            await recorder.start(fps: fps, width: width, height: height, targetWindowID: targetWinID)
        }
        dispatchMain()
    }
} else {
    fputs("ScreenCaptureKit requires macOS 12.3+\n", stderr)
    exit(1)
}
`

func getOrBuildMacCaptureBinary() (string, error) {
	binPath := filepath.Join(os.TempDir(), "limoni-mac-sckit")
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
	cmd       *exec.Cmd
	extraCmd  *exec.Cmd
	ctx       context.Context
	cancel    context.CancelFunc
	errCh     chan error
	doneCh    chan struct{}
	stopped   bool
	isBroad   bool
	targetURL string
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderrBuf *bytes.Buffer
	mu        sync.Mutex
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

	hasReceiver := errMpv == nil || errFFplay == nil

	status := DependencyStatus{
		HasMPV:               hasReceiver,
		HasFFmpeg:            errFFmpeg == nil,
		HasGPUScreenRecorder: errGSR == nil,
	}

	if !hasReceiver {
		status.MissingRecommended = "mpv veya ffmpeg (ekran izlemek icin gereklidir)"
	} else if runtime.GOOS == "linux" && !status.HasGPUScreenRecorder && !status.HasFFmpeg {
		status.MissingRecommended = "gpu-screen-recorder veya ffmpeg (ekran paylasmak icin gereklidir)"
	} else if runtime.GOOS == "windows" && !status.HasFFmpeg {
		status.MissingRecommended = "ffmpeg (ekran paylasmak icin gereklidir)"
	} else if runtime.GOOS == "darwin" && !status.HasFFmpeg {
		status.MissingRecommended = "ffmpeg (ekran paylasmak icin gereklidir)"
	}

	return status
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
	var targetHwnd uintptr
	var macSckitBin string
	var macWidth, macHeight, macFps int

	switch runtime.GOOS {
	case "linux":
		// Check if gpu-screen-recorder is available
		if p, err := FindExecutable("gpu-screen-recorder"); err == nil {
			binPath = p
			windowTarget := opt.WindowID
			if windowTarget == "" {
				windowTarget = "portal"
			}
			args = []string{
				"-w", windowTarget,
				"-s", opt.Resolution,
				"-f", fmt.Sprintf("%d", opt.FPS),
				"-k", "h264",
				"-q", "high",
				"-tune", "performance",
				"-keyint", "15",
				"-c", "mpegts",
				"-o", targetURL,
			}
		} else if p, err := FindExecutable("ffmpeg"); err == nil {
			// Fallback to ffmpeg x11grab on Linux
			binPath = p
			scaleRes := strings.ReplaceAll(opt.Resolution, "x", ":")
			if scaleRes == "" {
				scaleRes = "1920:1080"
			}
			args = []string{
				"-f", "x11grab",
				"-framerate", fmt.Sprintf("%d", opt.FPS),
				"-i", ":0.0",
				"-vf", fmt.Sprintf("scale=%s:flags=bicubic", scaleRes),
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
			return nil, errors.New("neither 'gpu-screen-recorder' nor 'ffmpeg' was found on this Linux system")
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
					logMsg("[FFMPEG-LIVE] %s", trimmed)
				}
			}
		}()
	} else {
		cmd.Stderr = stderrBuf
	}

	s := &Session{
		cmd:       cmd,
		extraCmd:  sckitCmd,
		ctx:       sessionCtx,
		cancel:    cancel,
		errCh:     make(chan error, 1),
		doneCh:    make(chan struct{}),
		isBroad:   true,
		targetURL: targetURL,
		stdout:    stdoutPipe,
		stderrBuf: stderrBuf,
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
			"-loglevel", "quiet",
			"-flags", "low_delay",
			"-fflags", "nobuffer+flush_packets",
			"-probesize", "1024",
			"-analyzeduration", "0",
			"-f", "mpegts",
			"-window_title", windowTitle,
			"-autoexit",
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
	s.mu.Unlock()

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
	s.mu.Unlock()

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
