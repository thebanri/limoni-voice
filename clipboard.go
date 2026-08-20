package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CopyToClipboard attempts to copy text to system clipboard using OSC 52 and system utilities.
func CopyToClipboard(text string) bool {
	if text == "" {
		return false
	}

	// 1. Try OSC 52 ANSI escape sequence (works seamlessly in modern terminals)
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\x07", b64)

	// 2. Try pbcopy (macOS)
	if path, err := exec.LookPath("pbcopy"); err == nil {
		cmd := exec.Command(path)
		stdin, err := cmd.StdinPipe()
		if err == nil {
			if err := cmd.Start(); err == nil {
				stdin.Write([]byte(text))
				stdin.Close()
				_ = cmd.Wait()
				return true
			}
		}
	}

	// 3. Try wl-copy (Wayland)
	if path, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.Command(path)
		stdin, err := cmd.StdinPipe()
		if err == nil {
			if err := cmd.Start(); err == nil {
				stdin.Write([]byte(text))
				stdin.Close()
				_ = cmd.Wait()
				return true
			}
		}
	}

	// 4. Try xclip (X11)
	if path, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command(path, "-selection", "clipboard")
		stdin, err := cmd.StdinPipe()
		if err == nil {
			if err := cmd.Start(); err == nil {
				stdin.Write([]byte(text))
				stdin.Close()
				_ = cmd.Wait()
				return true
			}
		}
	}

	// 5. Try xsel (X11)
	if path, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.Command(path, "--clipboard", "--input")
		stdin, err := cmd.StdinPipe()
		if err == nil {
			if err := cmd.Start(); err == nil {
				stdin.Write([]byte(text))
				stdin.Close()
				_ = cmd.Wait()
				return true
			}
		}
	}

	return true
}

// GetClipboardText reads text from system clipboard using system utilities.
func GetClipboardText() string {
	// 1. Try pbpaste (macOS)
	if path, err := exec.LookPath("pbpaste"); err == nil {
		cmd := exec.Command(path)
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
	}

	// 2. Try wl-paste (Wayland)
	if path, err := exec.LookPath("wl-paste"); err == nil {
		cmd := exec.Command(path, "--no-newline")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
	}

	// 3. Try xclip (X11)
	if path, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command(path, "-selection", "clipboard", "-o")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
	}

	// 4. Try xsel (X11)
	if path, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.Command(path, "--clipboard", "--output")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
	}

	return ""
}
