package screenshare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	WindowID   string // optional window id or "portal" for Wayland/X11 window picker
	Quality    string // e.g. "medium", "ultra", "fast"
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

// ReceiverOptions defines configuration for the video stream receiver
type ReceiverOptions struct {
	WindowTitle    string // Title for the playback window if applicable
	KeepAspect     bool
	CustomMpvFlags []string
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
	mu        sync.Mutex
}

// FindExecutable searches for a binary in PATH, next to current executable, in ~/.limoni-voice/bin/, WinGet, Scoop, and common Windows paths
func FindExecutable(name string) (string, error) {
	// 1. Check system PATH
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ""}
	}

	searchDirs := []string{}

	// 2. Current working directory & ./bin
	if cwd, err := os.Getwd(); err == nil {
		searchDirs = append(searchDirs, cwd, filepath.Join(cwd, "bin"), filepath.Join(cwd, "tools"))
	}

	// 3. Next to running executable & exe/bin
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

	// 5. Common Windows installer directories
	if runtime.GOOS == "windows" {
		searchDirs = append(searchDirs,
			`C:\ffmpeg\bin`,
			`C:\ffmpeg`,
			`C:\mpv`,
			`C:\Program Files\mpv`,
			`C:\Program Files (x86)\mpv`,
			`C:\Program Files\FFmpeg\bin`,
			`C:\Program Files (x86)\FFmpeg\bin`,
			`C:\ProgramData\chocolatey\bin`,
			`C:\ProgramData\chocolatey\lib\mpv\tools`,
			`C:\tools\ffmpeg\bin`,
			`C:\tools\mpv`,
		)
	}

	for _, dir := range searchDirs {
		for _, ext := range exts {
			candidate := filepath.Join(dir, name+ext)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
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

// StartBroadcasting starts hardware-accelerated screen capture and streams over UDP
func StartBroadcasting(ctx context.Context, targetIP string, port int, opts ...BroadcastOptions) (*Session, error) {
	if targetIP == "" {
		return nil, errors.New("target IP cannot be empty")
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port: %d", port)
	}

	opt := DefaultBroadcastOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	targetURL := fmt.Sprintf("udp://%s:%d?pkt_size=1316", targetIP, port)

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
				"-keyint", "5",
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
				"-crf", "20",
				"-b:v", "4M",
				"-maxrate", "6M",
				"-bufsize", "1M",
				"-pix_fmt", "yuv420p",
				"-g", "15",
				"-bf", "0",
				"-bsf:v", "dump_extra",
				"-f", "mpegts",
				targetURL,
			}
		} else {
			return nil, errors.New("neither 'gpu-screen-recorder' nor 'ffmpeg' was found on this Linux system")
		}

	case "windows":
		// Windows DXGI Desktop Duplication API via FFmpeg ddagrab
		p, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil, errors.New("'ffmpeg.exe' bulunamadi. Lutfen 'ffmpeg.exe' dosyasini uygulamanin yanina koyun veya PowerShell'de 'winget install Gyan.FFmpeg' calistirin.")
		}
		binPath = p
		scaleOpt := fmt.Sprintf("scale=%s:flags=bicubic", opt.Resolution)
		args = []string{
			"-f", "lavfi",
			"-i", fmt.Sprintf("ddagrab=framerate=%d:draw_mouse=1", opt.FPS),
			"-vf", scaleOpt,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-crf", "20",
			"-b:v", "4M",
			"-maxrate", "6M",
			"-bufsize", "1M",
			"-pix_fmt", "yuv420p",
			"-g", "15",
			"-bf", "0",
			"-bsf:v", "dump_extra",
			"-f", "mpegts",
			targetURL,
		}

	case "darwin":
		p, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil, errors.New("'ffmpeg' is required on macOS for screen sharing (avfoundation)")
		}
		binPath = p
		args = []string{
			"-f", "avfoundation",
			"-capture_cursor", "1",
			"-framerate", fmt.Sprintf("%d", opt.FPS),
			"-i", "1:none",
			"-vf", fmt.Sprintf("scale=%s", opt.Resolution),
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-g", "15",
			"-f", "mpegts",
			targetURL,
		}

	default:
		return nil, fmt.Errorf("unsupported platform for screen broadcasting: %s", runtime.GOOS)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(sessionCtx, binPath, args...)
	setupProcessGroup(cmd)

	s := &Session{
		cmd:       cmd,
		ctx:       sessionCtx,
		cancel:    cancel,
		errCh:     make(chan error, 1),
		doneCh:    make(chan struct{}),
		isBroad:   true,
		targetURL: targetURL,
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

	streamURL := fmt.Sprintf("udp://0.0.0.0:%d?reuse=1&pkt_size=1316&buffer_size=1048576", port)

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
			"--video-sync=desync",
			"--vd-lavc-threads=1",
			"--framedrop=decoder+vo",
			"--demuxer-lavf-format=mpegts",
			"--demuxer-lavf-analyzeduration=0",
			"--demuxer-lavf-probesize=32",
			"--demuxer-lavf-o=fflags=nobuffer+flush_packets",
			"--title=" + windowTitle,
			"--autofit=65%x65%",
			"--keep-open=no",
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
			"-probesize", "32",
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
	cmd.Stderr = nil
	setupProcessGroup(cmd)

	s := &Session{
		cmd:       cmd,
		ctx:       sessionCtx,
		cancel:    cancel,
		errCh:     make(chan error, 1),
		doneCh:    make(chan struct{}),
		isBroad:   false,
		targetURL: streamURL,
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
