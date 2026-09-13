package screenshare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// pipewireSource is a compositor screencast (portal or Mutter) kept alive across encoder
// restarts, so a bitrate change does not ask the user to pick the screen again.
type pipewireSource struct {
	nodeID  uint32
	reopen  func() (*os.File, error) // portal: new remote fd for the same session
	cleanup func()
}

// own records a freshly acquired source in src (when the caller keeps one) and returns the
// cleanup the caller must run itself (nil when src took ownership).
func (src *pipewireSource) own(nodeID uint32, reopen func() (*os.File, error), cleanup func()) func() {
	if src == nil {
		return cleanup
	}
	src.nodeID, src.reopen, src.cleanup = nodeID, reopen, cleanup
	return nil
}

// buildLinuxBroadcastCommand picks the best Linux capture path for opt. When src already holds
// a compositor stream it is reused. The returned file must be passed to the child as fd 3.
func buildLinuxBroadcastCommand(opt BroadcastOptions, targetURL string, src *pipewireSource, onCancel ...func()) (string, []string, *os.File, func(), error) {
	if src != nil && src.nodeID != 0 {
		var file *os.File
		if src.reopen != nil {
			f, err := src.reopen()
			if err != nil {
				return "", nil, nil, nil, fmt.Errorf("reopen PipeWire remote: %w", err)
			}
			file = f
		}
		bin, args, err := buildGstreamerPipewireCommand(src.nodeID, targetURL, opt, file != nil)
		return bin, args, file, nil, err
	}
	targetID := strings.TrimSpace(opt.WindowID)
	scaleRes := strings.ReplaceAll(opt.Resolution, "x", ":")
	if scaleRes == "" {
		scaleRes = "1920:1080"
	}
	fps := opt.FPS
	if fps <= 0 {
		fps = 60
	}

	wayland := isWayland()
	isWindowTarget := targetID == "portal:window" ||
		targetID == "portal" ||
		strings.HasPrefix(targetID, "app:") ||
		strings.HasPrefix(targetID, "win:") ||
		targetID == "focused"

	// 1. If user selected a Window or App on Wayland -> Route to XDG Desktop Portal Window Cast
	// (GPU Screen Recorder window capture only works in pure X11; Wayland window capture requires XDG Desktop Portal)
	if wayland && isWindowTarget {
		if _, err := FindExecutable("gst-launch-1.0"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
			defer cancel()
			sourceType := uint32(2) // 2 = Window Only
			if targetID == "portal" {
				sourceType = 3 // 3 = Monitor or Window
			}
			nodeID, pwFile, reopen, cleanup, errPortal := requestPortalCast(ctx, sourceType, onCancel...)
			if errPortal == nil && nodeID != 0 {
				bin, args, errGst := buildGstreamerPipewireCommand(nodeID, targetURL, opt, pwFile != nil)
				if errGst == nil {
					return bin, args, pwFile, src.own(nodeID, reopen, cleanup), nil
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

	// 2. Try GPU Screen Recorder if available (Fastest, Hardware accelerated NVENC/VAAPI/AMF/KMS zero-copy)
	// Prioritized for monitor/screen capture on both Wayland and X11, and window capture on pure X11.
	if (!wayland || !isWindowTarget) && targetID != "portal" && targetID != "portal:window" {
		if p, err := FindExecutable("gpu-screen-recorder"); err == nil {
			gsrTarget := ""
			if targetID == "desktop" || targetID == "screen" || targetID == "" {
				gsrTarget = "screen"
			} else if strings.HasPrefix(targetID, "monitor:") {
				parts := strings.Split(strings.TrimPrefix(targetID, "monitor:"), ":")
				if len(parts) > 0 && parts[0] != "" {
					gsrTarget = parts[0]
				} else {
					gsrTarget = "screen"
				}
			} else if !wayland {
				// Pure X11 window capture support in GPU Screen Recorder
				if targetID == "focused" {
					gsrTarget = "focused"
				} else if strings.HasPrefix(targetID, "win:") {
					parts := strings.SplitN(strings.TrimPrefix(targetID, "win:"), ":", 2)
					if len(parts) > 0 && parts[0] != "" {
						gsrTarget = parts[0]
					}
				} else if strings.HasPrefix(targetID, "app:") {
					parts := strings.Split(strings.TrimPrefix(targetID, "app:"), ":")
					if len(parts) > 0 {
						if pid, err := strconv.Atoi(parts[0]); err == nil {
							if winID := findX11WindowByPID(pid); winID != "" {
								gsrTarget = winID
							}
						}
					}
				}
			}

			if gsrTarget != "" {
				bitrateKbps := 4500
				if fps >= 120 {
					bitrateKbps = 6500
				} else if fps <= 30 {
					bitrateKbps = 3000
				}
				if br := opt.bitrateString(""); br != "" {
					clean := strings.TrimSpace(strings.ToLower(br))
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
					"-keyint", "1",
					"-fallback-cpu-encoding", "yes",
					"-restore-portal-session", "no",
					"-c", "mpegts",
					"-o", targetURL,
				}
				return p, args, nil, nil, nil
			}
		}
	}

	// 3. If GNOME Mutter compositor is available (for Screen 1 / Monitors) -> direct popup-less full monitor capture
	if !isWindowTarget && isMutterAvailable() {
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
					return bin, args, nil, src.own(nodeID, nil, cleanup), nil
				}
				if cleanup != nil {
					cleanup()
				}
			}
		}
	} else if wayland {
		// 3b. On KDE Plasma / non-GNOME Wayland compositors -> capture Screen via Desktop Portal
		if _, err := FindExecutable("gst-launch-1.0"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
			defer cancel()
			sourceType := uint32(1) // 1 = Screen only
			if isWindowTarget {
				sourceType = 2
			} else if targetID == "portal" {
				sourceType = 3
			}
			nodeID, pwFile, reopen, cleanup, errPortal := requestPortalCast(ctx, sourceType, onCancel...)
			if errPortal == nil && nodeID != 0 {
				bin, args, errGst := buildGstreamerPipewireCommand(nodeID, targetURL, opt, pwFile != nil)
				if errGst == nil {
					return bin, args, pwFile, src.own(nodeID, reopen, cleanup), nil
				}
				if cleanup != nil {
					cleanup()
				}
			}
		}
	}

	// 4. Try wf-recorder on Wayland / wlroots (Sway, Hyprland, Wayfire)
	if wayland {
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
				"-p", "b="+opt.bitrateString("3000k"),
				"-p", "maxrate="+opt.bitrateString("3000k"),
				"-p", "bufsize="+opt.bitrateString("3000k"),
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
		} else if strings.HasPrefix(targetID, "app:") {
			parts := strings.Split(strings.TrimPrefix(targetID, "app:"), ":")
			winID := ""
			if len(parts) > 0 {
				if pid, err := strconv.Atoi(parts[0]); err == nil {
					winID = findX11WindowByPID(pid)
				}
			}
			if winID != "" {
				inputArgs = append(inputArgs, "-window_id", winID, "-i", display)
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
		bitrate := "4.5M"
		maxRate := "6.5M"
		bufSize := "2000k"
		gopSize := fps
		if gopSize > 120 {
			gopSize = 120
		}
		if fps >= 120 {
			bitrate = "6.5M"
			maxRate = "9.0M"
			bufSize = "3000k"
		} else if fps <= 30 {
			bitrate = "3.0M"
			maxRate = "4.5M"
			bufSize = "1500k"
		}
		if br := opt.bitrateString(""); br != "" {
			bitrate = br
			maxRate = br
		}

		args = append(args,
			"-vf", vf,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-x264-params", fmt.Sprintf("keyint=%d:min-keyint=%d:qpmin=15:qpmax=32:scenecut=0:no-scenecut=1:sync-lookahead=0:rc-lookahead=0:sliced-threads=0:repeat-headers=1:me=hex:subme=2:merange=16:aq-mode=2:aq-strength=1.0", gopSize, gopSize),
			"-crf", "19",
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
