package screenshare

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

var (
	macEncoderOnce sync.Once
	macEncoder     []string // encoder arguments that passed the probe, without bitrate / GOP
)

// macEncoderCandidates are tried in order: Apple's hardware H.264 encoder with the low
// latency options newer FFmpeg builds know, the same without them, then x264 in software.
var macEncoderCandidates = [][]string{
	{"-c:v", "h264_videotoolbox", "-realtime", "1", "-prio_speed", "1", "-allow_sw", "1"},
	{"-c:v", "h264_videotoolbox", "-realtime", "1", "-allow_sw", "1"},
}

// bestMacEncoder returns the first encoder candidate FFmpeg can run here, or nil for x264.
func bestMacEncoder(ffmpegBin string) []string {
	macEncoderOnce.Do(func() {
		for _, c := range macEncoderCandidates {
			if probeFFmpegEncoder(ffmpegBin, c) {
				macEncoder = c
				logMsg("[SCREENSHARE] macOS hardware encoder active: VideoToolbox (%v)", c)
				return
			}
		}
		logMsg("[SCREENSHARE] VideoToolbox unavailable, encoding with libx264 on the CPU")
	})
	return macEncoder
}

// probeFFmpegEncoder encodes a few blank frames with the given encoder arguments.
func probeFFmpegEncoder(ffmpegBin string, encoderArgs []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=256x256:r=30:d=0.2", "-pix_fmt", "yuv420p"}
	args = append(args, encoderArgs...)
	args = append(args, "-bf", "0", "-f", "null", "-")
	return exec.CommandContext(ctx, ffmpegBin, args...).Run() == nil
}

// buildMacEncoderArgs returns the video encoder arguments for a share: hardware when hw is
// set (from bestMacEncoder), otherwise the low latency x264 setup.
func buildMacEncoderArgs(hw []string, bitrate string, fps int) []string {
	if len(hw) > 0 {
		args := append([]string(nil), hw...)
		return append(args,
			"-b:v", bitrate,
			"-maxrate", bitrate,
			"-bufsize", bitrate,
			"-g", strconv.Itoa(fps),
			"-bf", "0",
		)
	}
	return []string{
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-x264-params", fmt.Sprintf("keyint=%d:min-keyint=%d:qpmin=18:qpmax=38:scenecut=0:no-scenecut=1:sync-lookahead=0:rc-lookahead=0:sliced-threads=0:repeat-headers=1:me=hex:subme=2:merange=16:aq-mode=1", fps, fps),
		"-b:v", bitrate,
		"-maxrate", bitrate,
		"-bufsize", bitrate,
		"-g", strconv.Itoa(fps),
		"-bf", "0",
	}
}
