package screenshare

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	winEncoderOnce     sync.Once
	winSelectedEncoder string

	winDDAOnce      sync.Once
	winDDAAvailable bool
)

func probeWindowsEncoder(ffmpegBin string, encoder string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegBin, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=256x256:d=0.01",
		"-c:v", encoder, "-f", "null", "-")
	return cmd.Run() == nil
}

func isWindowsDDAAvailable(ffmpegBin string) bool {
	winDDAOnce.Do(func() {
		if runtime.GOOS != "windows" && !testing.Testing() {
			winDDAAvailable = false
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		cmd := exec.CommandContext(ctx, ffmpegBin, "-y", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "ddagrab=output_idx=0:framerate=30:draw_mouse=0,hwdownload,format=bgra",
			"-vframes", "1", "-f", "null", "-")
		if cmd.Run() == nil {
			winDDAAvailable = true
			logMsg("[SCREENSHARE] Windows DXGI Desktop Duplication (ddagrab) active: 0ms GPU capture")
		} else {
			winDDAAvailable = false
			logMsg("[SCREENSHARE] Windows DXGI Desktop Duplication unavailable, falling back to gdigrab")
		}
	})
	return winDDAAvailable
}

func getBestWindowsEncoder(ffmpegBin string) string {
	winEncoderOnce.Do(func() {
		if runtime.GOOS != "windows" && !testing.Testing() {
			winSelectedEncoder = "libx264"
			return
		}
		// 1. Try NVIDIA NVENC (Fastest, zero CPU overhead)
		if probeWindowsEncoder(ffmpegBin, "h264_nvenc") {
			winSelectedEncoder = "h264_nvenc"
			logMsg("[SCREENSHARE] Windows hardware encoder detected: NVIDIA NVENC (h264_nvenc)")
			return
		}
		// 2. Try AMD AMF
		if probeWindowsEncoder(ffmpegBin, "h264_amf") {
			winSelectedEncoder = "h264_amf"
			logMsg("[SCREENSHARE] Windows hardware encoder detected: AMD AMF (h264_amf)")
			return
		}
		// 3. Try Intel QuickSync (QSV)
		if probeWindowsEncoder(ffmpegBin, "h264_qsv") {
			winSelectedEncoder = "h264_qsv"
			logMsg("[SCREENSHARE] Windows hardware encoder detected: Intel QuickSync (h264_qsv)")
			return
		}
		// 4. Software CPU fallback
		winSelectedEncoder = "libx264"
		logMsg("[SCREENSHARE] Windows encoder active: CPU multi-core (libx264)")
	})
	return winSelectedEncoder
}

func buildWindowsEncoderArgs(encoder string, winBitrate, winMaxRate, winBufSize string, winGop int) []string {
	switch encoder {
	case "h264_nvenc":
		return []string{
			"-c:v", "h264_nvenc",
			"-preset", "p3",
			"-tune", "ll",
			"-rc", "vbr",
			"-cq", "19",
			"-b:v", winBitrate,
			"-maxrate", winMaxRate,
			"-bufsize", winBufSize,
			"-spatial-aq", "1",
			"-temporal-aq", "1",
			"-aq-strength", "8",
			"-qmin", "15",
			"-qmax", "32",
			"-zerolatency", "1",
			"-g", fmt.Sprintf("%d", winGop),
			"-bf", "0",
			"-forced-idr", "1",
			"-delay", "0",
		}
	case "h264_amf":
		return []string{
			"-c:v", "h264_amf",
			"-quality", "speed",
			"-rc", "cbr",
			"-b:v", winBitrate,
			"-maxrate", winMaxRate,
			"-bufsize", winBufSize,
			"-vbaq", "1",
			"-async_depth", "1",
			"-g", fmt.Sprintf("%d", winGop),
			"-bf", "0",
			"-forced_idr", "1",
		}
	case "h264_qsv":
		return []string{
			"-c:v", "h264_qsv",
			"-preset", "veryfast",
			"-scenario", "displayremoting",
			"-b:v", winBitrate,
			"-maxrate", winMaxRate,
			"-bufsize", winBufSize,
			"-max_qp_i", "32",
			"-max_qp_p", "32",
			"-min_qp_i", "15",
			"-min_qp_p", "15",
			"-g", fmt.Sprintf("%d", winGop),
			"-bf", "0",
			"-forced_idr", "1",
		}
	default: // libx264
		return []string{
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-x264-params", fmt.Sprintf("keyint=%d:min-keyint=%d:qpmin=15:qpmax=32:scenecut=0:no-scenecut=1:sync-lookahead=0:rc-lookahead=0:sliced-threads=0:repeat-headers=1:me=hex:subme=2:merange=16:aq-mode=2:aq-strength=1.0", winGop, winGop),
			"-crf", "19",
			"-b:v", winBitrate,
			"-maxrate", winMaxRate,
			"-bufsize", winBufSize,
			"-g", fmt.Sprintf("%d", winGop),
			"-bf", "0",
		}
	}
}

func buildWindowsBroadcastArgs(opt BroadcastOptions, targetURL string, ffmpegBin string, targetHwnd uintptr, winWidth, winHeight int) []string {
	scaleRes := strings.ReplaceAll(opt.Resolution, "x", ":")
	if scaleRes == "" {
		scaleRes = "1920:1080"
	}
	scaleOpt := fmt.Sprintf("scale=%s:flags=bicubic:force_original_aspect_ratio=decrease:in_range=full:out_range=full:in_color_matrix=bt709:out_color_matrix=bt709,pad=ceil(iw/2)*2:ceil(ih/2)*2,format=yuv420p", scaleRes)

	winFps := opt.FPS
	if winFps <= 0 {
		winFps = 60
	}
	winGop := winFps
	if winGop > 120 {
		winGop = 120
	}
	winBitrate := "4.5M"
	winMaxRate := "6.5M"
	winBufSize := "2000k"
	if winFps >= 120 {
		winBitrate = "6.5M"
		winMaxRate = "9.0M"
		winBufSize = "3000k"
	} else if winFps <= 30 {
		winBitrate = "3.0M"
		winMaxRate = "4.5M"
		winBufSize = "1500k"
	}
	if br := opt.bitrateString(""); br != "" {
		winBitrate = br
		winMaxRate = br
	}

	encoder := getBestWindowsEncoder(ffmpegBin)
	encArgs := buildWindowsEncoderArgs(encoder, winBitrate, winMaxRate, winBufSize, winGop)

	colorArgs := []string{
		"-color_range", "2",
		"-colorspace", "bt709",
		"-color_primaries", "bt709",
		"-color_trc", "bt709",
		"-pix_fmt", "yuv420p",
	}

	var args []string
	if targetHwnd != 0 {
		if winWidth <= 0 {
			winWidth = 1920
		}
		if winHeight <= 0 {
			winHeight = 1080
		}
		args = []string{
			"-fflags", "nobuffer+flush_packets",
			"-f", "rawvideo",
			"-pixel_format", "bgra",
			"-video_size", fmt.Sprintf("%dx%d", winWidth, winHeight),
			"-framerate", fmt.Sprintf("%d", winFps),
			"-i", "pipe:0",
			"-vf", scaleOpt,
		}
		args = append(args, encArgs...)
		args = append(args, colorArgs...)
		args = append(args,
			"-bsf:v", "dump_extra",
			"-f", "mpegts",
			"-mpegts_flags", "+latm+pat_pmt_at_frames",
			"-pcr_period", "20",
			targetURL,
		)
		return args
	}

	// Desktop / Monitor capture
	outIdx := 0
	offsetX := "0"
	offsetY := "0"
	sizeW := ""
	sizeH := ""

	if strings.HasPrefix(opt.WindowID, "monitor:") {
		mParts := strings.Split(strings.TrimPrefix(opt.WindowID, "monitor:"), ":")
		if len(mParts) >= 5 {
			// Format: monitor:<idx>:<x>:<y>:<w>:<h>
			if idxVal, err := strconv.Atoi(mParts[0]); err == nil && idxVal >= 0 {
				outIdx = idxVal
			}
			offsetX, offsetY = mParts[1], mParts[2]
			sizeW, sizeH = mParts[3], mParts[4]
		} else if len(mParts) >= 4 {
			// Format: monitor:<x>:<y>:<w>:<h>
			offsetX, offsetY = mParts[0], mParts[1]
			sizeW, sizeH = mParts[2], mParts[3]
		}
	}

	if isWindowsDDAAvailable(ffmpegBin) {
		ddaInput := fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=1,hwdownload,format=bgra", outIdx, winFps)
		args = []string{
			"-fflags", "nobuffer+flush_packets",
			"-f", "lavfi",
			"-i", ddaInput,
			"-vf", scaleOpt,
		}
		args = append(args, encArgs...)
		args = append(args, colorArgs...)
		args = append(args,
			"-bsf:v", "dump_extra",
			"-f", "mpegts",
			"-mpegts_flags", "+latm+pat_pmt_at_frames",
			"-pcr_period", "20",
			targetURL,
		)
		return args
	}

	// Fallback to gdigrab
	inputArgs := []string{
		"-fflags", "nobuffer+flush_packets",
		"-thread_queue_size", "64",
		"-probesize", "32",
		"-analyzeduration", "0",
		"-f", "gdigrab",
		"-framerate", fmt.Sprintf("%d", winFps),
		"-draw_mouse", "1",
	}

	if sizeW != "" && sizeH != "" && (offsetX != "0" || offsetY != "0") {
		inputArgs = append(inputArgs,
			"-offset_x", offsetX,
			"-offset_y", offsetY,
			"-video_size", fmt.Sprintf("%sx%s", sizeW, sizeH),
			"-i", "desktop",
		)
	} else {
		physW, physH := GetPhysicalDesktopSize()
		if physW > 0 && physH > 0 {
			inputArgs = append(inputArgs, "-video_size", fmt.Sprintf("%dx%d", physW, physH), "-offset_x", "0", "-offset_y", "0")
		}
		inputArgs = append(inputArgs, "-i", "desktop")
	}

	args = append(args, inputArgs...)
	args = append(args, "-vf", scaleOpt)
	args = append(args, encArgs...)
	args = append(args, colorArgs...)
	args = append(args,
		"-bsf:v", "dump_extra",
		"-f", "mpegts",
		"-mpegts_flags", "+latm+pat_pmt_at_frames",
		"-pcr_period", "20",
		targetURL,
	)
	return args
}
