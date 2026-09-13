package screenshare

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// ChunkDatagramSize is the MPEG-TS payload per UDP datagram requested from encoders
// (6 × 188 bytes). It keeps encrypted video packets well below relay and MTU limits.
const ChunkDatagramSize = 1128

// broadcastPlan describes how to launch one encoder generation.
type broadcastPlan struct {
	bin        string
	args       []string
	extraFiles []*os.File // passed to the child from fd 3; closed in the parent after start
	cleanup    func()     // releases a capture source the session must keep until it ends

	hwnd       uintptr // Windows window capture fed through stdin
	winW, winH int

	macHelper          string // macOS ScreenCaptureKit helper feeding ffmpeg's stdin
	macW, macH, macFPS int
}

func parseResolution(res string, defW, defH int) (int, int) {
	w, h := defW, defH
	if parts := strings.Split(res, "x"); len(parts) == 2 {
		if v, err := strconv.Atoi(parts[0]); err == nil && v > 0 {
			w = v
		}
		if v, err := strconv.Atoi(parts[1]); err == nil && v > 0 {
			h = v
		}
	}
	return w / 2 * 2, h / 2 * 2
}

// planBuilder is replaceable in tests.
var planBuilder = buildBroadcastPlan

func buildBroadcastPlan(opt BroadcastOptions, targetURL string, src *pipewireSource, onSourceClosed func()) (*broadcastPlan, error) {
	plan := &broadcastPlan{}
	switch runtime.GOOS {
	case "linux":
		bin, args, pwFile, cleanup, err := buildLinuxBroadcastCommand(opt, targetURL, src, onSourceClosed)
		if err != nil {
			return nil, err
		}
		plan.bin, plan.args, plan.cleanup = bin, args, cleanup
		if pwFile != nil {
			plan.extraFiles = append(plan.extraFiles, pwFile)
		}

	case "windows":
		// Windows desktop & window capture via FFmpeg (DXGI Desktop Duplication & NVENC/AMF/QSV hardware acceleration)
		p, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil, errors.New("'ffmpeg.exe' not found. Please place 'ffmpeg.exe' next to the application or run 'winget install Gyan.FFmpeg' in PowerShell.")
		}
		plan.bin = p
		if strings.HasPrefix(opt.WindowID, "hwnd:") {
			parts := strings.SplitN(strings.TrimPrefix(opt.WindowID, "hwnd:"), ":", 2)
			if len(parts) > 0 {
				parsed, _ := strconv.ParseUint(parts[0], 10, 64)
				plan.hwnd = uintptr(parsed)
			}
		}
		if plan.hwnd != 0 {
			plan.winW, plan.winH = parseResolution(opt.Resolution, 1920, 1080)
		}
		plan.args = buildWindowsBroadcastArgs(opt, targetURL, p, plan.hwnd, plan.winW, plan.winH)

	case "darwin":
		p, err := FindExecutable("ffmpeg")
		if err != nil {
			return nil, errors.New("'ffmpeg' is required on macOS for screen sharing (brew install ffmpeg)")
		}
		plan.bin = p
		fps := opt.FPS
		if fps <= 0 {
			fps = 60
		}
		width, height := parseResolution(opt.Resolution, 1920, 1080)
		bitrate := opt.bitrateString("2500k")
		encoder := []string{
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-x264-params", fmt.Sprintf("keyint=%d:min-keyint=%d:qpmin=18:qpmax=38:scenecut=0:no-scenecut=1:sync-lookahead=0:rc-lookahead=0:sliced-threads=0:repeat-headers=1:me=hex:subme=2:merange=16:aq-mode=1", fps, fps),
			"-b:v", bitrate,
			"-maxrate", bitrate,
			"-bufsize", bitrate,
			"-pix_fmt", "yuv420p",
			"-g", strconv.Itoa(fps),
			"-bf", "0",
			"-f", "mpegts",
			"-mpegts_flags", "+pat_pmt_at_frames",
			"-pcr_period", "20",
			"-flush_packets", "1",
			targetURL,
		}
		if helper, sckitErr := getOrBuildMacCaptureBinary(); sckitErr == nil {
			logMsg("[DARWIN] Using native Apple ScreenCaptureKit -> FFmpeg rawvideo pipe")
			plan.args = append([]string{
				"-f", "rawvideo",
				"-pixel_format", "bgra",
				"-video_size", fmt.Sprintf("%dx%d", width, height),
				"-framerate", strconv.Itoa(fps),
				"-i", "-",
				"-vf", "format=yuv420p",
			}, encoder...)
			plan.macHelper, plan.macW, plan.macH, plan.macFPS = helper, width, height, fps
		} else {
			logMsg("[DARWIN] ScreenCaptureKit unavailable (%v), falling back to AVFoundation (no system audio)", sckitErr)
			plan.args = append([]string{
				"-f", "avfoundation",
				"-capture_cursor", "1",
				"-pixel_format", "uyvy422",
				"-i", getMacScreenDevice(p),
				"-vf", fmt.Sprintf("scale=%d:%d:flags=bicubic,format=yuv420p", width, height),
				"-r", strconv.Itoa(fps),
			}, encoder...)
		}

	default:
		return nil, fmt.Errorf("unsupported platform for screen broadcasting: %s", runtime.GOOS)
	}
	return plan, nil
}

// StartBroadcasting starts hardware-accelerated screen capture and streams MPEG-TS to
// udp://targetIP:port in ChunkDatagramSize datagrams.
func StartBroadcasting(ctx context.Context, targetIP string, port int, opts ...BroadcastOptions) (*Session, error) {
	if targetIP == "" || targetIP == "-" {
		return nil, errors.New("target IP cannot be empty")
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port: %d", port)
	}
	opt := DefaultBroadcastOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}
	targetURL := fmt.Sprintf("udp://%s:%d?pkt_size=%d", targetIP, port, ChunkDatagramSize)

	s := newSession(ctx, true, targetURL)
	s.pwSrc = &pipewireSource{}
	if err := s.launchBroadcast(opt); err != nil {
		s.cancel()
		if s.pwSrc.cleanup != nil {
			s.pwSrc.cleanup()
		}
		return nil, err
	}

	if runtime.GOOS == "linux" {
		if strings.HasPrefix(opt.WindowID, "app:") {
			parts := strings.Split(strings.TrimPrefix(opt.WindowID, "app:"), ":")
			if len(parts) > 0 {
				if pid, err := strconv.Atoi(parts[0]); err == nil && pid > 1 {
					go watchPIDLiveness(s.ctx, pid, s.cancel)
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
				go watchX11WindowLiveness(s.ctx, targetWinID, s.cancel)
			}
		}
	}
	return s, nil
}

// CommandBuilder returns the encoder command for a custom broadcast.
type CommandBuilder func(opt BroadcastOptions, targetURL string) (bin string, args []string)

// StartCustomBroadcast runs build's command as the encoder (writing MPEG-TS to targetURL) with
// the same session semantics as StartBroadcasting, including Restart with new options.
func StartCustomBroadcast(ctx context.Context, targetIP string, port int, opt BroadcastOptions, build CommandBuilder) (*Session, error) {
	targetURL := fmt.Sprintf("udp://%s:%d?pkt_size=%d", targetIP, port, ChunkDatagramSize)
	s := newSession(ctx, true, targetURL)
	s.custom = build
	if err := s.launchBroadcast(opt); err != nil {
		s.cancel()
		return nil, err
	}
	return s, nil
}

// launchBroadcast builds and starts one encoder generation for opt.
func (s *Session) launchBroadcast(opt BroadcastOptions) error {
	var plan *broadcastPlan
	if s.custom != nil {
		bin, args := s.custom(opt, s.targetURL)
		plan = &broadcastPlan{bin: bin, args: args}
	} else {
		var err error
		if plan, err = planBuilder(opt, s.targetURL, s.pwSrc, s.cancel); err != nil {
			return err
		}
	}
	closeExtra := func() {
		for _, f := range plan.extraFiles {
			_ = f.Close()
		}
	}
	if plan.cleanup != nil {
		s.mu.Lock()
		s.cleanupFunc = plan.cleanup
		s.mu.Unlock()
	}

	logMsg("[BROADCAST] Starting command: %s %s", plan.bin, strings.Join(plan.args, " "))
	genCtx, genCancel := context.WithCancel(s.ctx)
	g := &generation{cancel: genCancel, exited: make(chan struct{})}
	g.cmd = exec.CommandContext(genCtx, plan.bin, plan.args...)
	g.cmd.ExtraFiles = plan.extraFiles
	setupProcessGroup(g.cmd)
	g.stderr = captureStderr(g.cmd, "BROADCAST-LIVE")

	var audioRead *os.File
	if plan.macHelper != "" {
		helperArgs := []string{strconv.Itoa(plan.macW), strconv.Itoa(plan.macH), strconv.Itoa(plan.macFPS), opt.WindowID}
		var audioWrite *os.File
		if opt.OnAudio != nil {
			if r, w, err := os.Pipe(); err == nil {
				audioRead, audioWrite = r, w
				helperArgs = append(helperArgs, "audio")
			}
		}
		g.extra = exec.CommandContext(genCtx, plan.macHelper, helperArgs...)
		setupProcessGroup(g.extra)
		if audioWrite != nil {
			g.extra.ExtraFiles = []*os.File{audioWrite} // fd 3 in the helper
		}
		helperOut, err := g.extra.StdoutPipe()
		if err != nil {
			genCancel()
			closeExtra()
			return fmt.Errorf("failed to open ScreenCaptureKit pipe: %w", err)
		}
		g.cmd.Stdin = helperOut
		captureStderr(g.extra, "SCKIT-LIVE")
		if err := g.extra.Start(); err != nil {
			genCancel()
			closeExtra()
			if audioRead != nil {
				audioRead.Close()
				audioWrite.Close()
			}
			return fmt.Errorf("failed to start ScreenCaptureKit engine: %w", err)
		}
		if audioWrite != nil {
			audioWrite.Close()
		}
	}

	var framePipe io.WriteCloser
	if plan.hwnd != 0 {
		var err error
		framePipe, err = g.cmd.StdinPipe()
		if err != nil {
			genCancel()
			closeExtra()
			return fmt.Errorf("failed to open stdin pipe for window capture: %w", err)
		}
	}

	if err := g.cmd.Start(); err != nil {
		genCancel()
		closeExtra()
		if g.extra != nil && g.extra.Process != nil {
			_ = g.extra.Process.Kill()
		}
		if audioRead != nil {
			audioRead.Close()
		}
		return fmt.Errorf("failed to start screen broadcaster (%s): %w", plan.bin, err)
	}
	closeExtra()

	if plan.hwnd != 0 && framePipe != nil {
		go StreamWindowFrames(genCtx, plan.hwnd, opt.FPS, plan.winW, plan.winH, framePipe)
	}
	if audioRead != nil {
		go readHelperAudio(audioRead, opt.OnAudio)
	}

	s.mu.Lock()
	s.opt = opt
	s.mu.Unlock()
	s.attach(g)
	return nil
}
