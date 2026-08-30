package screenshare

import (
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

	if runtime.GOOS == "windows" {
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
	} else if runtime.GOOS == "darwin" {
		var screenTargets []WindowInfo
		var winTargets []WindowInfo

		// 1. Discover all connected displays via FFmpeg / avfoundation device list
		if ffmpegPath, err := FindExecutable("ffmpeg"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			out, _ := exec.CommandContext(ctx, ffmpegPath, "-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()
			cancel()

			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				lower := strings.ToLower(line)
				if strings.Contains(lower, "capture screen") {
					// Example line: [AVFoundation indev @ 0x...] [1] Capture screen 0
					parts := strings.Split(line, "]")
					var devNum string
					for _, part := range parts {
						if idx1 := strings.LastIndex(part, "["); idx1 != -1 {
							devNum = strings.TrimSpace(part[idx1+1:])
							break
						}
					}
					scrNum := len(screenTargets)
					if idxScreen := strings.Index(lower, "capture screen"); idxScreen != -1 {
						after := strings.TrimSpace(line[idxScreen+len("capture screen"):])
						fields := strings.Fields(after)
						if len(fields) > 0 {
							if n, err := strconv.Atoi(fields[0]); err == nil {
								scrNum = n
							}
						}
					}
					scrTitle := fmt.Sprintf("Screen %d", scrNum+1)
					if scrNum == 0 {
						scrTitle += " (Primary - Built-in)"
					} else {
						scrTitle += " (Secondary / External Display)"
					}
					id := fmt.Sprintf("mac_screen:%d", scrNum)
					if devNum != "" {
						id = fmt.Sprintf("mac_dev:%s", devNum)
					}
					screenTargets = append(screenTargets, WindowInfo{
						ID:    id,
						Title: "🖥️  " + scrTitle,
					})
				}
			}
		}

		// 2. Discover displays via system_profiler SPDisplaysDataType if not found by ffmpeg
		if len(screenTargets) == 0 {
			ctxSP, cancelSP := context.WithTimeout(context.Background(), 2*time.Second)
			outSP, errSP := exec.CommandContext(ctxSP, "system_profiler", "SPDisplaysDataType").Output()
			cancelSP()

			if errSP == nil && len(outSP) > 0 {
				lines := strings.Split(string(outSP), "\n")
				dispCount := 0
				for _, line := range lines {
					trimmed := strings.TrimSpace(line)
					if strings.HasSuffix(trimmed, ":") && (strings.Contains(trimmed, "Display") || strings.Contains(trimmed, "LCD") || strings.Contains(trimmed, "Monitor") || strings.Contains(trimmed, "Color")) {
						dispName := strings.TrimSuffix(trimmed, ":")
						dispCount++
						title := fmt.Sprintf("Screen %d (%s)", dispCount, dispName)
						if dispCount == 1 {
							title += " (Primary)"
						} else {
							title += " (External)"
						}
						screenTargets = append(screenTargets, WindowInfo{
							ID:    fmt.Sprintf("mac_screen:%d", dispCount-1),
							Title: "🖥️  " + title,
						})
					}
				}
			}
		}

		// Always ensure at least Screen 1 and Screen 2 exist for multiple display setups
		if len(screenTargets) == 0 {
			screenTargets = append(screenTargets,
				WindowInfo{
					ID:    "mac_screen:0",
					Title: "🖥️  Screen 1 (Primary Display)",
				},
				WindowInfo{
					ID:    "mac_screen:1",
					Title: "🖥️  Screen 2 (External / Secondary Display)",
				},
			)
		} else if len(screenTargets) == 1 {
			screenTargets = append(screenTargets, WindowInfo{
				ID:    "mac_screen:1",
				Title: "🖥️  Screen 2 (External / Secondary Display)",
			})
		}

		// 3. Discover open application windows on macOS via AppleScript
		asScript := `
		tell application "System Events"
			set outText to ""
			set pList to every process whose visible is true and background only is false
			repeat with p in pList
				try
					set pName to name of p
					tell p
						set wList to name of every window
						set hasWin to false
						repeat with w in wList
							if w is not "" and w is not missing value then
								set outText to outText & pName & "|" & w & "\n"
								set hasWin to true
							end if
						end repeat
						if not hasWin then
							set outText to outText & pName & "|" & pName & "\n"
						end if
					end tell
				end try
			end repeat
			return outText
		end tell
		`
		ctxAS, cancelAS := context.WithTimeout(context.Background(), 2*time.Second)
		outAS, errAS := exec.CommandContext(ctxAS, "osascript", "-e", asScript).Output()
		cancelAS()

		if errAS == nil && len(outAS) > 0 {
			lines := strings.Split(string(outAS), "\n")
			seen := make(map[string]bool)
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}
				parts := strings.SplitN(trimmed, "|", 2)
				if len(parts) >= 2 {
					appName, winTitle := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
					if appName == "" || appName == "Dock" || appName == "Finder" || appName == "loginwindow" || appName == "ControlCenter" || appName == "NotificationCenter" || appName == "Spotlight" || appName == "Limoni Voice" {
						continue
					}
					key := appName + ":" + winTitle
					if !seen[key] {
						seen[key] = true
						displayTitle := appName
						if winTitle != "" && winTitle != appName {
							displayTitle = fmt.Sprintf("%s - %s", appName, winTitle)
						}
						winTargets = append(winTargets, WindowInfo{
							ID:    "mac_win:" + appName,
							Title: "🪟  " + displayTitle,
						})
					}
				}
			}
		}

		targets = screenTargets
		if len(winTargets) > 0 {
			targets = append(targets, winTargets...)
		}
	}
	return targets
}

func getMacScreenDevice(binPath string) string {
	return getMacScreenDeviceIndex(binPath, 0)
}

func getMacScreenDeviceIndex(binPath string, screenNum int) string {
	if binPath == "" {
		binPath = "ffmpeg"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "-f", "avfoundation", "-list_devices", "true", "-i", "")
	out, _ := cmd.CombinedOutput()

	outStr := string(out)
	lines := strings.Split(outStr, "\n")
	targetLabel := fmt.Sprintf("capture screen %d", screenNum)

	// 1. Look for specific screen index
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, targetLabel) {
			parts := strings.Split(line, "]")
			for _, part := range parts {
				if idx1 := strings.LastIndex(part, "["); idx1 != -1 {
					numStr := strings.TrimSpace(part[idx1+1:])
					if _, err := strconv.Atoi(numStr); err == nil {
						return numStr + ":none"
					}
				}
			}
		}
	}

	// 2. Fallback to any capture screen
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "capture screen") {
			parts := strings.Split(line, "]")
			for _, part := range parts {
				if idx1 := strings.LastIndex(part, "["); idx1 != -1 {
					numStr := strings.TrimSpace(part[idx1+1:])
					if _, err := strconv.Atoi(numStr); err == nil {
						return numStr + ":none"
					}
				}
			}
		}
	}

	// 3. Fallback to 1:none
	return "1:none"
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
		targetURL = fmt.Sprintf("udp://%s:%d?pkt_size=940&buffer_size=4194304", targetIP, port)
	}

	var binPath string
	var args []string
	var targetHwnd uintptr

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
			return nil, errors.New("'ffmpeg' is required on macOS for screen sharing (avfoundation)")
		}
		binPath = p

		screenDev := getMacScreenDevice(binPath)
		scaleRes := strings.ReplaceAll(opt.Resolution, "x", ":")
		if scaleRes == "" {
			scaleRes = "1920:1080"
		}
		scaleFilter := fmt.Sprintf("scale=%s:flags=bicubic,format=yuv420p", scaleRes)

		if strings.HasPrefix(opt.WindowID, "mac_dev:") {
			devNum := strings.TrimPrefix(opt.WindowID, "mac_dev:")
			screenDev = devNum + ":none"
		} else if strings.HasPrefix(opt.WindowID, "mac_screen:") {
			scrIdxStr := strings.TrimPrefix(opt.WindowID, "mac_screen:")
			scrIdx, _ := strconv.Atoi(scrIdxStr)
			screenDev = getMacScreenDeviceIndex(binPath, scrIdx)
		} else if strings.HasPrefix(opt.WindowID, "mac_win:") {
			appName := strings.TrimPrefix(opt.WindowID, "mac_win:")
			screenDev = getMacScreenDeviceIndex(binPath, 0)
			// Get window position and size on macOS
			boundsScript := fmt.Sprintf(`tell application "System Events" to tell process "%s" to get {position, size} of window 1`, appName)
			ctxB, cancelB := context.WithTimeout(context.Background(), 1*time.Second)
			outB, errB := exec.CommandContext(ctxB, "osascript", "-e", boundsScript).Output()
			cancelB()

			if errB == nil && len(outB) > 0 {
				clean := strings.ReplaceAll(strings.ReplaceAll(string(outB), "{", ""), "}", "")
				parts := strings.Split(clean, ",")
				if len(parts) >= 4 {
					x, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
					y, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
					w, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
					h, _ := strconv.Atoi(strings.TrimSpace(parts[3]))
					if w > 100 && h > 100 && x >= 0 && y >= 0 {
						w = (w / 2) * 2
						h = (h / 2) * 2
						x = (x / 2) * 2
						y = (y / 2) * 2
						scaleFilter = fmt.Sprintf("crop=%d:%d:%d:%d,scale=%s:flags=bicubic,format=yuv420p", w, h, x, y, scaleRes)
					}
				}
			}
		}

		fps := opt.FPS
		if fps <= 0 {
			fps = 30
		}

		args = []string{
			"-fflags", "nobuffer+flush_packets",
			"-f", "avfoundation",
			"-capture_cursor", "1",
			"-framerate", fmt.Sprintf("%d", fps),
			"-i", screenDev,
			"-vf", scaleFilter,
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

	default:
		return nil, fmt.Errorf("unsupported platform for screen broadcasting: %s", runtime.GOOS)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(sessionCtx, binPath, args...)
	setupProcessGroup(cmd)

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
	cmd.Stderr = stderrBuf

	s := &Session{
		cmd:       cmd,
		ctx:       sessionCtx,
		cancel:    cancel,
		errCh:     make(chan error, 1),
		doneCh:    make(chan struct{}),
		isBroad:   true,
		targetURL: targetURL,
		stdout:    stdoutPipe,
		stderrBuf: stderrBuf,
	}

	if err := cmd.Start(); err != nil {
		cancel()
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
			"--force-window=yes",
			"--idle=yes",
			"--keep-open=yes",
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

	sessionCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(sessionCtx, binPath, args...)
	cmd.Stdout = nil
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf
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

	if err != nil && s.ctx.Err() == nil {
		if s.stderrBuf != nil && s.stderrBuf.Len() > 0 {
			lines := strings.Split(strings.TrimSpace(s.stderrBuf.String()), "\n")
			var errLines []string
			for i := len(lines) - 1; i >= 0 && len(errLines) < 3; i-- {
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
