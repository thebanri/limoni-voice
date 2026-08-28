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
		Resolution: "1280x720",
		FPS:        60,
		Bitrate:    "3M",
		WindowID:   "portal",
		Quality:    "medium",
	}
}

// ReceiverOptions defines configuration for the Kitty mpv receiver
type ReceiverOptions struct {
	VO             string // e.g. "kitty" (default) or "gpu" / "tct"
	WindowTitle    string // Title for the playback window if applicable
	KeepAspect     bool
	Left           int    // 1-based character column offset inside terminal
	Top            int    // 1-based character row offset inside terminal
	Cols           int    // Width in terminal character columns
	Rows           int    // Height in terminal character rows
	Geometry       string // e.g. "65%x80%+33%+12%"
	CustomMpvFlags []string
}

// DefaultReceiverOptions returns ultra-low-latency Kitty receiver defaults
func DefaultReceiverOptions() ReceiverOptions {
	return ReceiverOptions{
		VO:         "kitty",
		KeepAspect: true,
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

// FindExecutable searches for a binary in PATH, next to current executable, in ~/.limoni-voice/bin/, and common Windows paths
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

	// 2. Next to running executable
	if execPath, err := os.Executable(); err == nil {
		searchDirs = append(searchDirs, filepath.Dir(execPath))
	}

	// 3. User app cache directory (~/.limoni-voice/bin)
	if home, err := os.UserHomeDir(); err == nil {
		searchDirs = append(searchDirs, filepath.Join(home, ".limoni-voice", "bin"))
		searchDirs = append(searchDirs, filepath.Join(home, "AppData", "Local", "limoni-voice", "bin"))
	}

	// 4. Common Windows installer directories
	if runtime.GOOS == "windows" {
		searchDirs = append(searchDirs,
			`C:\ffmpeg\bin`,
			`C:\ProgramData\chocolatey\bin`,
			`C:\Program Files\mpv`,
			`C:\Program Files (x86)\mpv`,
			`C:\tools\ffmpeg\bin`,
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
	_, errGSR := FindExecutable("gpu-screen-recorder")
	_, errFFmpeg := FindExecutable("ffmpeg")

	status := DependencyStatus{
		HasMPV:               errMpv == nil,
		HasGPUScreenRecorder: errGSR == nil,
		HasFFmpeg:            errFFmpeg == nil,
	}

	if !status.HasMPV {
		status.MissingRecommended = "mpv (ekran izlemek icin gereklidir)"
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
				"-vf", fmt.Sprintf("scale=%s", opt.Resolution),
				"-c:v", "libx264",
				"-preset", "ultrafast",
				"-tune", "zerolatency",
				"-g", "5",
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
			return nil, errors.New("'ffmpeg.exe' bulunamadi. Lutfen 'ffmpeg.exe' dosyasini uygulamanin yanina koyun veya sistem PATH'ine ekleyin.")
		}
		binPath = p
		scaleOpt := fmt.Sprintf("scale=%s", opt.Resolution)
		args = []string{
			"-f", "lavfi",
			"-i", fmt.Sprintf("ddagrab=framerate=%d:draw_mouse=1", opt.FPS),
			"-vf", scaleOpt,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-g", "5",
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

// StartReceiving launches mpv configured with Kitty graphics protocol and zero-latency flags
func StartReceiving(ctx context.Context, port int, opts ...ReceiverOptions) (*Session, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid receiver port: %d", port)
	}

	mpvPath, err := FindExecutable("mpv")
	if err != nil {
		return nil, errors.New("'mpv' bulunamadi. Lutfen 'mpv' uygulamasini yukleyin veya 'mpv.exe'yi uygulamanin yanina koyun.")
	}

	opt := DefaultReceiverOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	streamURL := fmt.Sprintf("udp://0.0.0.0:%d?reuse=1&pkt_size=1316&buffer_size=65536", port)

	args := []string{
		streamURL,
		"--really-quiet",
		"--no-audio",
		"--vo=kitty,gpu,x11",
		"--vo-kitty-use-shm=yes",
		"--demuxer-lavf-format=mpegts",
		"--demuxer-lavf-analyzeduration=0",
		"--demuxer-lavf-probesize=32",
		"--demuxer-lavf-o=fflags=nobuffer+flush_packets",
		"--profile=low-latency",
		"--untimed=yes",
		"--cache=no",
		"--no-cache",
		"--video-sync=desync",
		"--vd-lavc-threads=1",
		"--framedrop=decoder+vo",
		"--keepaspect=yes",
		"--idle=yes",
	}

	if opt.Left > 0 {
		args = append(args, fmt.Sprintf("--vo-kitty-left=%d", opt.Left))
	}
	if opt.Top > 0 {
		args = append(args, fmt.Sprintf("--vo-kitty-top=%d", opt.Top))
	}
	if opt.Cols > 0 {
		args = append(args, fmt.Sprintf("--vo-kitty-cols=%d", opt.Cols))
	}
	if opt.Rows > 0 {
		args = append(args, fmt.Sprintf("--vo-kitty-rows=%d", opt.Rows))
	}

	if len(opt.CustomMpvFlags) > 0 {
		args = append(args, opt.CustomMpvFlags...)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(sessionCtx, mpvPath, args...)
	cmd.Stdout = os.Stdout // Kitty escape codes flow directly to terminal block
	cmd.Stderr = nil       // Suppress ffmpeg decoding noise from corrupting TUI
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
		return nil, fmt.Errorf("failed to start mpv screen receiver: %w", err)
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

// Stop terminates the subprocess and cleans up terminal graphics
func (s *Session) Stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	s.mu.Unlock()

	s.cancel()

	// Clean up Kitty terminal graphics immediately
	_, _ = os.Stdout.WriteString("\x1b_Ga=d,d=A\x1b\\")

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
