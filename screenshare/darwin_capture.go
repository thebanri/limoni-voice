package screenshare

import (
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
)

func getMacScreenDevice(binPath string) string {
	return "3:none"
}

// embeddedMacCaptureSwift is the ScreenCaptureKit helper source (video to stdout, system audio
// to fd 3), compiled with swiftc on first use when no prebuilt helper is embedded.
//
//go:embed mac_capture.swift
var embeddedMacCaptureSwift string

func getOrBuildMacCaptureBinary() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = filepath.Join(os.TempDir(), fmt.Sprintf("limoni-%d", os.Getuid()))
	} else {
		cacheDir = filepath.Join(cacheDir, "limoni-voice")
	}
	_ = os.MkdirAll(cacheDir, 0700)

	if prebuilt, err := prebuiltMacHelper(); err == nil && len(prebuilt) > 0 {
		sum := sha256.Sum256(prebuilt)
		binPath := filepath.Join(cacheDir, "limoni-sck-"+hex.EncodeToString(sum[:6]))
		if info, err := os.Stat(binPath); err == nil && info.Size() == int64(len(prebuilt)) {
			return binPath, nil
		}
		tmp := binPath + ".tmp"
		if err := os.WriteFile(tmp, prebuilt, 0700); err == nil && os.Rename(tmp, binPath) == nil {
			logMsg("[DARWIN] Installed prebuilt ScreenCaptureKit helper: %s", binPath)
			return binPath, nil
		}
		_ = os.Remove(tmp)
	}

	sum := sha256.Sum256([]byte(embeddedMacCaptureSwift))
	binPath := filepath.Join(cacheDir, "limoni-mac-sckit-"+hex.EncodeToString(sum[:6]))
	if info, err := os.Stat(binPath); err == nil && info.Size() > 0 {
		return binPath, nil
	}

	swiftc, err := FindExecutable("swiftc")
	if err != nil {
		return "", errors.New("swiftc not found on macOS (install with: xcode-select --install)")
	}

	srcFile := filepath.Join(cacheDir, "limoni_mac_capture.swift")
	if err := os.WriteFile(srcFile, []byte(embeddedMacCaptureSwift), 0600); err != nil {
		return "", err
	}
	defer os.Remove(srcFile)

	logMsg("[DARWIN] Compiling native ScreenCaptureKit engine with swiftc...")
	cmd := exec.Command(swiftc, "-O", "-framework", "ScreenCaptureKit", "-framework", "CoreMedia", "-framework", "CoreVideo", "-framework", "CoreAudio", srcFile, "-o", binPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("swiftc compilation failed: %w (output: %s)", err, string(out))
	}
	logMsg("[DARWIN] ScreenCaptureKit engine compiled successfully: %s", binPath)
	return binPath, nil
}

// readHelperAudio converts the ScreenCaptureKit helper's system audio (48 kHz stereo float32,
// interleaved, little endian) into 20 ms mono int16 frames.
func readHelperAudio(r io.ReadCloser, onFrame func([]int16)) {
	defer r.Close()
	const frameSamples = 960
	buf := make([]byte, 8*frameSamples) // one stereo float32 frame pair = 8 bytes
	frame := make([]int16, 0, frameSamples)
	for {
		n, err := io.ReadFull(r, buf)
		for off := 0; off+8 <= n; off += 8 {
			l := math.Float32frombits(binary.LittleEndian.Uint32(buf[off:]))
			rr := math.Float32frombits(binary.LittleEndian.Uint32(buf[off+4:]))
			v := float64(l+rr) * 0.5 * 32767
			frame = append(frame, int16(math.Max(-32768, math.Min(32767, v))))
			if len(frame) == frameSamples {
				onFrame(frame)
				frame = make([]int16, 0, frameSamples)
			}
		}
		if err != nil {
			return
		}
	}
}
