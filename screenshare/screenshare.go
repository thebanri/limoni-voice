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
	"strings"
	"sync"
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
		{ID: "desktop", Title: "🖥️  Ekran 1 (Ana Ekran - Spotify, Oyunlar ve Tum Uygulamalar)"},
	}

	if runtime.GOOS == "windows" {
		psScript := `
		Add-Type -AssemblyName System.Windows.Forms
		$screens = [System.Windows.Forms.Screen]::AllScreens
		$idx = 1
		foreach ($s in $screens) {
			$p = if ($s.Primary) {" (Ana Ekran)"} else {""}
			"SCREEN|$($s.Bounds.X)|$($s.Bounds.Y)|$($s.Bounds.Width)|$($s.Bounds.Height)|Ekran $idx$p ($($s.Bounds.Width)x$($s.Bounds.Height))"
			$idx++
		}
		if ($screens.Count -gt 1) {
			"ALL|0|0|0|0|Tum Ekranlar (Genisletilmis Masaustu)"
		}
		`
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
		out, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			lines := strings.Split(string(out), "\n")
			var screenTargets []WindowInfo

			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}
				parts := strings.Split(trimmed, "|")
				if len(parts) >= 6 {
					tag, x, y, w, h, name := parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]
					if tag == "SCREEN" {
						id := fmt.Sprintf("monitor:%s:%s:%s:%s", x, y, w, h)
						if x == "0" && y == "0" {
							id = "desktop"
						}
						screenTargets = append(screenTargets, WindowInfo{
							ID:    id,
							Title: "🖥️  " + name,
						})
					} else if tag == "ALL" {
						screenTargets = append(screenTargets, WindowInfo{
							ID:    "desktop",
							Title: "🖥️  " + name,
						})
					}
				}
			}

			if len(screenTargets) > 0 {
				targets = screenTargets
			}
		}
	}
	return targets
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
		WindowTitle: "Limoni Voice - Canli Ekran Yayini (HD 60 FPS)",
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

		// Fixed common install folders
		searchDirs = append(searchDirs,
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
		targetURL = fmt.Sprintf("udp://%s:%d?pkt_size=1316", targetIP, port)
	}

	var binPath string
	var args []string

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
				"-q", opt.Quality,
				"-tune", "performance",
				"-keyint", "15",
				"-c", "mpegts",
				"-o", targetURL,
			}
		} else if p, err := FindExecutable("ffmpeg"); err == nil {
			// Fallback to ffmpeg x11grab on Linux
			binPath = p
			args = []string{
				"-f", "x11grab",
				"-framerate", fmt.Sprintf("%d", opt.FPS),
				"-i", ":0.0",
				"-vf", fmt.Sprintf("scale=%s:flags=bicubic", opt.Resolution),
				"-c:v", "libx264",
				"-preset", "ultrafast",
				"-tune", "zerolatency",
				"-x264-params", "repeat-headers=1:keyint=15:min-keyint=15:scenecut=0",
				"-crf", "20",
				"-b:v", "6M",
				"-maxrate", "8M",
				"-bufsize", "2M",
				"-pix_fmt", "yuv420p",
				"-g", "15",
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
		// Windows desktop capture via FFmpeg gdigrab (100% reliable across all GPUs, laptops, and multi-monitor setups)
		p, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil, errors.New("'ffmpeg.exe' bulunamadi. Lutfen 'ffmpeg.exe' dosyasini uygulamanin yanina koyun veya PowerShell'de 'winget install Gyan.FFmpeg' calistirin.")
		}
		binPath = p
		scaleOpt := "scale=min(1920\\,trunc(iw/2)*2):-2,format=yuv420p"

		inputArgs := []string{
			"-fflags", "nobuffer+flush_packets",
			"-thread_queue_size", "2",
			"-probesize", "32",
			"-analyzeduration", "0",
			"-f", "gdigrab",
			"-framerate", fmt.Sprintf("%d", opt.FPS),
			"-draw_mouse", "1",
		}

		if strings.HasPrefix(opt.WindowID, "title=") {
			// Capture exact application window (e.g. Spotify, Discord, Chrome, VSCode)
			inputArgs = append(inputArgs, "-i", opt.WindowID)
		} else if strings.HasPrefix(opt.WindowID, "monitor:") {
			// Format: monitor:X:Y:WIDTH:HEIGHT
			mParts := strings.Split(opt.WindowID, ":")
			if len(mParts) >= 5 && (mParts[1] != "0" || mParts[2] != "0") {
				inputArgs = append(inputArgs,
					"-offset_x", mParts[1],
					"-offset_y", mParts[2],
					"-video_size", fmt.Sprintf("%sx%s", mParts[3], mParts[4]),
					"-i", "desktop",
				)
			} else {
				inputArgs = append(inputArgs, "-i", "desktop")
			}
		} else if opt.WindowID != "" && opt.WindowID != "portal" && opt.WindowID != "desktop" {
			inputArgs = append(inputArgs, "-i", fmt.Sprintf("title=%s", opt.WindowID))
		} else {
			inputArgs = append(inputArgs, "-i", "desktop")
		}

		args = append(inputArgs,
			"-vf", scaleOpt,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-x264-params", "repeat-headers=1:keyint=15:min-keyint=15:scenecut=0:sync-lookahead=0:rc-lookahead=0:sliced-threads=1",
			"-crf", "20",
			"-b:v", "6M",
			"-maxrate", "8M",
			"-bufsize", "2M",
			"-pix_fmt", "yuv420p",
			"-g", "15",
			"-bf", "0",
			"-bsf:v", "dump_extra",
			"-f", "mpegts",
			"-mpegts_flags", "+latm+pat_pmt_at_frames",
			targetURL,
		)

	case "darwin":
		p, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil, errors.New("'ffmpeg' is required on macOS for screen sharing (avfoundation)")
		}
		binPath = p
		args = []string{
			"-fflags", "nobuffer+flush_packets",
			"-f", "avfoundation",
			"-capture_cursor", "1",
			"-framerate", fmt.Sprintf("%d", opt.FPS),
			"-i", "1:none",
			"-vf", fmt.Sprintf("scale=%s", opt.Resolution),
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-x264-params", "repeat-headers=1:keyint=15:min-keyint=15:scenecut=0:sync-lookahead=0:rc-lookahead=0:sliced-threads=1",
			"-g", "15",
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
		windowTitle = "Limoni Voice - Canli Ekran Yayini (HD 60 FPS)"
	}

	streamURL := fmt.Sprintf("tcp://127.0.0.1:%d", port)

	var binPath string
	var args []string

	if p, err := FindExecutable("mpv"); err == nil {
		binPath = p
		args = []string{
			streamURL,
			"--really-quiet",
			"--no-audio",
			"--profile=low-latency",
			"--untimed=yes",
			"--cache=no",
			"--no-cache",
			"--hwdec=auto-safe",
			"--video-sync=desync",
			"--framedrop=decoder+vo",
			"--demuxer-readahead-secs=0",
			"--demuxer-max-bytes=100K",
			"--demuxer-max-back-bytes=0",
			"--demuxer-lavf-format=mpegts",
			"--demuxer-lavf-o=fflags=nobuffer+flush_packets",
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
			"-fflags", "nobuffer+fastseek+flush_packets",
			"-analyzeduration", "0",
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
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(s.stderrBuf.String()))
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
