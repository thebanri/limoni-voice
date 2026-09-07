package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[NOPXYZ_]|\x1b`)

// SanitizeClipboardText removes ANSI escape sequences (bracketed paste markers, OSC codes, colors)
// and unprintable ASCII control characters to ensure compatibility with web browsers and text editors.
func SanitizeClipboardText(s string) string {
	s = reANSI.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || r >= 32 {
			if r != 127 && r != '\uFEFF' && r != '\u200B' && r != '\u200C' && r != '\u200D' {
				b.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// CopyToClipboard attempts to copy text to system clipboard using OSC 52 and system utilities.
func CopyToClipboard(text string) bool {
	text = SanitizeClipboardText(text)
	if text == "" {
		return false
	}

	// 1. Try OSC 52 ANSI escape sequence (works seamlessly in modern terminals)
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\x07", b64)
	if os.Getenv("TMUX") != "" {
		fmt.Fprintf(os.Stdout, "\x1bPtmux;\x1b\x1b]52;c;%s\x07\x1b\\", b64)
	}
	_ = os.Stdout.Sync()

	// 2. Windows: clip or PowerShell
	if runtime.GOOS == "windows" {
		if path, err := exec.LookPath("clip"); err == nil {
			cmd := exec.Command(path)
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				return true
			}
		}
		if path, err := exec.LookPath("powershell"); err == nil {
			cmd := exec.Command(path, "-NoProfile", "-Command", "Set-Clipboard -Value $input")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				return true
			}
		}
	}

	// 3. macOS: pbcopy
	if runtime.GOOS == "darwin" {
		if path, err := exec.LookPath("pbcopy"); err == nil {
			cmd := exec.Command(path)
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				return true
			}
		}
	}

	// 4. Linux: Wayland wl-copy (with UTF-8 text MIME type for browser compatibility)
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if path, err := exec.LookPath("wl-copy"); err == nil {
			cmd := exec.Command(path, "--type", "text/plain;charset=utf-8")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				return true
			}
			cmd2 := exec.Command(path)
			cmd2.Stdin = strings.NewReader(text)
			if err := cmd2.Run(); err == nil {
				return true
			}
		}
	}

	// 5. Linux: X11 xclip
	if os.Getenv("DISPLAY") != "" {
		if path, err := exec.LookPath("xclip"); err == nil {
			cmd := exec.Command(path, "-selection", "clipboard", "-in")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				return true
			}
		}
	}

	// 6. Linux: X11 xsel
	if os.Getenv("DISPLAY") != "" {
		if path, err := exec.LookPath("xsel"); err == nil {
			cmd := exec.Command(path, "--clipboard", "--input")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				return true
			}
		}
	}

	// 7. WSL fallback (clip.exe)
	if path, err := exec.LookPath("clip.exe"); err == nil {
		cmd := exec.Command(path)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return true
		}
	}

	// 8. General fallback to wl-copy
	if path, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.Command(path)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return true
		}
	}

	return true
}

// GetClipboardText reads text from system clipboard using system utilities.
func GetClipboardText() string {
	var raw string

	// 1. Try Windows PowerShell Get-Clipboard
	if runtime.GOOS == "windows" {
		if path, err := exec.LookPath("powershell"); err == nil {
			cmd := exec.Command(path, "-NoProfile", "-Command", "Get-Clipboard")
			out, err := cmd.Output()
			if err == nil && len(out) > 0 {
				raw = string(out)
			}
		}
	}

	// 2. Try pbpaste (macOS)
	if raw == "" && runtime.GOOS == "darwin" {
		if path, err := exec.LookPath("pbpaste"); err == nil {
			cmd := exec.Command(path)
			out, err := cmd.Output()
			if err == nil && len(out) > 0 {
				raw = string(out)
			}
		}
	}

	// 3. Try wl-paste (Wayland)
	if raw == "" && os.Getenv("WAYLAND_DISPLAY") != "" {
		if path, err := exec.LookPath("wl-paste"); err == nil {
			cmd := exec.Command(path, "--no-newline")
			out, err := cmd.Output()
			if err == nil && len(out) > 0 {
				raw = string(out)
			}
		}
	}

	// 4. Try xclip (X11)
	if raw == "" && os.Getenv("DISPLAY") != "" {
		if path, err := exec.LookPath("xclip"); err == nil {
			cmd := exec.Command(path, "-selection", "clipboard", "-o")
			out, err := cmd.Output()
			if err == nil && len(out) > 0 {
				raw = string(out)
			}
		}
	}

	// 5. Try xsel (X11)
	if raw == "" && os.Getenv("DISPLAY") != "" {
		if path, err := exec.LookPath("xsel"); err == nil {
			cmd := exec.Command(path, "--clipboard", "--output")
			out, err := cmd.Output()
			if err == nil && len(out) > 0 {
				raw = string(out)
			}
		}
	}

	return SanitizeClipboardText(raw)
}

// OpenBrowserURL opens a web URL in the user's default browser.
func OpenBrowserURL(urlStr string) error {
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return nil
	}
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		urlStr = "https://" + urlStr
	}

	switch runtime.GOOS {
	case "windows":
		// Option 1: rundll32 url.dll,FileProtocolHandler <url>
		cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", urlStr)
		if err := cmd.Start(); err == nil {
			return nil
		}
		// Option 2: cmd.exe /c start "" <url>
		cmd = exec.Command("cmd", "/c", "start", "", urlStr)
		if err := cmd.Start(); err == nil {
			return nil
		}
		// Option 3: PowerShell Start-Process
		return exec.Command("powershell", "-NoProfile", "-Command", fmt.Sprintf("Start-Process '%s'", strings.ReplaceAll(urlStr, "'", "''"))).Start()
	case "darwin":
		cmd := exec.Command("open", urlStr)
		return cmd.Start()
	default:
		// Try xdg-open on Linux/Unix, fallback to sensible browser or open
		if path, err := exec.LookPath("xdg-open"); err == nil {
			cmd := exec.Command(path, urlStr)
			return cmd.Start()
		} else if path, err := exec.LookPath("open"); err == nil {
			cmd := exec.Command(path, urlStr)
			return cmd.Start()
		} else if path, err := exec.LookPath("sensible-browser"); err == nil {
			cmd := exec.Command(path, urlStr)
			return cmd.Start()
		}
		cmd := exec.Command("xdg-open", urlStr)
		return cmd.Start()
	}
}

