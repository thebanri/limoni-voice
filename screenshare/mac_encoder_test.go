package screenshare

import (
	"os/exec"
	"slices"
	"testing"
)

func TestMacEncoderArgs(t *testing.T) {
	hw := buildMacEncoderArgs(macEncoderCandidates[0], "2500k", 60)
	for _, want := range [][]string{{"-c:v", "h264_videotoolbox"}, {"-realtime", "1"}, {"-bf", "0"}, {"-g", "60"}, {"-maxrate", "2500k"}} {
		if i := slices.Index(hw, want[0]); i < 0 || hw[i+1] != want[1] {
			t.Fatalf("hardware args %v lack %v", hw, want)
		}
	}
	if slices.Contains(hw, "libx264") {
		t.Fatal("hardware args still name x264")
	}
	sw := buildMacEncoderArgs(nil, "2500k", 30)
	if sw[1] != "libx264" || !slices.Contains(sw, "30") {
		t.Fatalf("software fallback wrong: %v", sw)
	}
	// Changing the candidate list must not leak into the arguments of a running share.
	hw[1] = "changed"
	if macEncoderCandidates[0][1] != "h264_videotoolbox" {
		t.Fatal("buildMacEncoderArgs aliases the candidate slice")
	}
}

// The probe accepts an encoder FFmpeg has and refuses one it lacks.
func TestProbeFFmpegEncoder(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if !probeFFmpegEncoder(bin, []string{"-c:v", "libx264", "-preset", "ultrafast"}) {
		t.Skip("this ffmpeg has no libx264")
	}
	if probeFFmpegEncoder(bin, []string{"-c:v", "no_such_encoder"}) {
		t.Fatal("probe accepted a missing encoder")
	}
}
