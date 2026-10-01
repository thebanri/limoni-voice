package main

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/thebanri/limoni-voice/internal/clipboard"
)

var (
	clipboardMu   sync.RWMutex
	mockClipboard string
)

// SetMockClipboard sets the in-memory clipboard text used in automated tests.
func SetMockClipboard(text string) {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	mockClipboard = text
}

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

	// Never touch the host machine's physical clipboard when running automated tests!
	if testing.Testing() {
		clipboardMu.Lock()
		mockClipboard = text
		clipboardMu.Unlock()
		return true
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

	// Linux: own the clipboard natively (Wayland data-control, then X11), no tools needed
	if runtime.GOOS == "linux" && clipboard.Write(text) == nil {
		return true
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
	if testing.Testing() {
		clipboardMu.RLock()
		defer clipboardMu.RUnlock()
		return mockClipboard
	}

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

	// Linux: read it natively (Wayland data-control, then X11); the tools below are only
	// a fallback for when that can't reach a display.
	native := false
	if runtime.GOOS == "linux" {
		if text, err := clipboard.Read(); err == nil {
			raw, native = text, true
		}
	}

	// 3. Try wl-paste (Wayland)
	if !native && raw == "" && os.Getenv("WAYLAND_DISPLAY") != "" {
		if path, err := exec.LookPath("wl-paste"); err == nil {
			cmd := exec.Command(path, "--no-newline")
			out, err := cmd.Output()
			if err == nil && len(out) > 0 {
				raw = string(out)
			}
		}
	}

	// 4. Try xclip (X11)
	if !native && raw == "" && os.Getenv("DISPLAY") != "" {
		if path, err := exec.LookPath("xclip"); err == nil {
			cmd := exec.Command(path, "-selection", "clipboard", "-o")
			out, err := cmd.Output()
			if err == nil && len(out) > 0 {
				raw = string(out)
			}
		}
	}

	// 5. Try xsel (X11)
	if !native && raw == "" && os.Getenv("DISPLAY") != "" {
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

// mockClipboardImage is the clipboard image automated tests see.
var mockClipboardImage []byte

// imageExts names the file extension of each clipboard image type.
var imageExts = map[string]string{
	"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "image/gif": "gif", "image/bmp": "bmp",
}

// imageMimeOrder is the order image types are picked in when the clipboard offers several.
var imageMimeOrder = []string{"image/png", "image/jpeg", "image/webp", "image/gif", "image/bmp"}

// GetClipboardImage returns the image on the clipboard (a screenshot, a copied picture) and
// its file extension, or nil when the clipboard holds no image.
func GetClipboardImage() ([]byte, string, error) {
	if testing.Testing() {
		clipboardMu.RLock()
		defer clipboardMu.RUnlock()
		return mockClipboardImage, "png", nil
	}
	switch runtime.GOOS {
	case "windows", "darwin":
		return clipboardImageViaScript()
	}
	// Linux: natively (Wayland data-control, then X11); wl-paste and xclip only when that
	// can't reach a display.
	data, mime, err := clipboard.ReadImage()
	if err == nil {
		return data, imageExts[mime], nil
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if data, ext := clipboardImageViaTool("wl-paste", []string{"--list-types"}, func(mime string) []string {
			return []string{"--no-newline", "--type", mime}
		}); data != nil {
			return data, ext, nil
		}
	}
	if os.Getenv("DISPLAY") != "" {
		if data, ext := clipboardImageViaTool("xclip", []string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, func(mime string) []string {
			return []string{"-selection", "clipboard", "-t", mime, "-o"}
		}); data != nil {
			return data, ext, nil
		}
	}
	return nil, "", err
}

// clipboardImageViaTool asks a clipboard tool which types the clipboard holds and reads the
// first image type among them.
func clipboardImageViaTool(tool string, listArgs []string, readArgs func(mime string) []string) ([]byte, string) {
	path, err := exec.LookPath(tool)
	if err != nil {
		return nil, ""
	}
	types, err := exec.Command(path, listArgs...).Output()
	if err != nil {
		return nil, ""
	}
	offered := strings.Fields(string(types))
	for _, mime := range imageMimeOrder {
		for _, have := range offered {
			if have == mime {
				if data, err := exec.Command(path, readArgs(mime)...).Output(); err == nil && len(data) > 0 {
					return data, imageExts[mime]
				}
			}
		}
	}
	return nil, ""
}

// clipboardImageViaScript has PowerShell (Windows) or AppleScript (macOS) save the clipboard's
// image as a PNG file. The file's path goes in an environment variable, so nothing in it is
// read as script.
func clipboardImageViaScript() ([]byte, string, error) {
	f, err := os.CreateTemp("", "limoni-clipboard-*.png")
	if err != nil {
		return nil, "", err
	}
	out := f.Name()
	f.Close()
	defer os.Remove(out)

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-STA", "-Command",
			"Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; "+
				"$i = [System.Windows.Forms.Clipboard]::GetImage(); if ($i -eq $null) { exit 3 }; "+
				"$i.Save($env:LIMONI_CLIP_OUT, [System.Drawing.Imaging.ImageFormat]::Png)")
	} else {
		cmd = exec.Command("osascript",
			"-e", "try",
			"-e", "set d to the clipboard as «class PNGf»",
			"-e", "on error",
			"-e", "return \"none\"",
			"-e", "end try",
			"-e", "set f to open for access (POSIX file (system attribute \"LIMONI_CLIP_OUT\")) with write permission",
			"-e", "set eof f to 0",
			"-e", "write d to f",
			"-e", "close access f")
	}
	cmd.Env = append(os.Environ(), "LIMONI_CLIP_OUT="+out)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 3 {
			return nil, "", nil // no image
		}
		return nil, "", err
	}
	data, err := os.ReadFile(out)
	if err != nil || len(data) == 0 {
		return nil, "", err
	}
	return data, "png", nil
}

// OpenBrowserURL opens a web URL in the user's default browser safely.
func OpenBrowserURL(urlStr string) error {
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return nil
	}

	// Prevent shell/argument injection: reject control chars, quotes, and shell operators
	if strings.ContainsAny(urlStr, " \t\r\n\x00\"'`;&|$><^%") {
		return fmt.Errorf("URL contains disallowed characters: %s", urlStr)
	}

	// Reject disallowed schemes (e.g. file://, javascript:, data:)
	if strings.Contains(urlStr, "://") {
		if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
			return fmt.Errorf("unsupported or dangerous URL scheme: %s", urlStr)
		}
	} else {
		if strings.Contains(urlStr, ":") {
			return fmt.Errorf("disallowed URL format: %s", urlStr)
		}
		urlStr = "https://" + urlStr
	}

	// Validate URL structure: only permit valid http and https URLs
	parsed, err := url.Parse(urlStr)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("invalid or disallowed URL: %s", urlStr)
	}

	cleanURL := parsed.String()

	if testing.Testing() {
		return nil
	}

	switch runtime.GOOS {
	case "windows":
		// Option 1: rundll32.exe url.dll,FileProtocolHandler <url> (invokes Windows ShellExecute safely)
		cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", cleanURL)
		if err := startDetached(cmd); err == nil {
			return nil
		}
		// Option 2: Parameterized PowerShell Start-Process (no string interpolation)
		return startDetached(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Start-Process -FilePath $args[0]", cleanURL))
	case "darwin":
		cmd := exec.Command("open", cleanURL)
		return startDetached(cmd)
	default:
		// Try xdg-open on Linux/Unix, fallback to sensible browser
		if path, err := exec.LookPath("xdg-open"); err == nil {
			cmd := exec.Command(path, cleanURL)
			return startDetached(cmd)
		} else if path, err := exec.LookPath("sensible-browser"); err == nil {
			cmd := exec.Command(path, cleanURL)
			return startDetached(cmd)
		}
		cmd := exec.Command("xdg-open", cleanURL)
		return startDetached(cmd)
	}
}
