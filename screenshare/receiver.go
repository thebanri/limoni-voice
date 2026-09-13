package screenshare

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// StartReceiving launches a high-performance native video window (mpv or ffplay fallback) with
// zero-latency flags. The MPEG-TS stream is written to Session.Stdin(): a private pipe, so no
// other local process can connect to the player or read the decrypted stream.
func StartReceiving(ctx context.Context, opts ...ReceiverOptions) (*Session, error) {
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

	streamURL := "-" // stdin

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
				"--vd-lavc-threads=0",
				"--vd-lavc-fast=yes",
				"--cache=no",
				"--demuxer-readahead-secs=0",
				"--stream-buffer-size=128k",
				"--framedrop=vo",
				"--hwdec=auto-safe",
				"--vd-lavc-show-all=no",
				"--video-sync=audio",
				fmt.Sprintf("--fps=%d", opt.FPS),
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
			"pipe:0",
		}
	} else if binPath == "" {
		if runtime.GOOS == "windows" {
			return nil, errors.New("'mpv.exe' or 'ffplay.exe' not found to watch stream. Please place 'mpv.exe' next to the application or run 'winget install mpv.mpv' in PowerShell.")
		}
		return nil, errors.New("'ffplay' or 'mpv' not found on system to watch stream. Please install ffmpeg or mpv (e.g., sudo pacman -S ffmpeg / sudo apt install ffmpeg).")
	}

	logMsg("[RECEIVER] Starting command: %s %s", binPath, strings.Join(args, " "))

	s := newSession(ctx, false, "pipe:")
	genCtx, genCancel := context.WithCancel(s.ctx)
	g := &generation{cancel: genCancel, exited: make(chan struct{})}
	g.cmd = exec.CommandContext(genCtx, binPath, args...)
	g.cmd.Stdout = nil
	setupProcessGroup(g.cmd)
	g.stderr = captureStderr(g.cmd, "MPV-LIVE")
	stdin, err := g.cmd.StdinPipe()
	if err != nil {
		genCancel()
		s.cancel()
		return nil, fmt.Errorf("failed to open player pipe: %w", err)
	}
	if err := g.cmd.Start(); err != nil {
		genCancel()
		s.cancel()
		return nil, fmt.Errorf("failed to start screen receiver (%s): %w", binPath, err)
	}
	s.stdin = stdin
	s.attach(g)
	return s, nil
}
