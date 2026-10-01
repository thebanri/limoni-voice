package main

import (
	"os"
	"runtime"
	"strings"
)

// dropsBlockedHint is shown on entering a room in a terminal that passes no dropped files on.
const dropsBlockedHint = "[INFO] This terminal (Alacritty on Wayland) does not pass dragged files on. Copy the file in your file manager (Ctrl+C) and press Ctrl+V here."

// terminalDropsBlocked is set at start: the app runs in an Alacritty on Wayland.
var terminalDropsBlocked bool

// alacrittyOnWayland reports, from the environment the app started with, whether it runs in
// an Alacritty on Wayland, which passes no dropped files on (its winit has no Wayland drag and
// drop). Alacritty picks Wayland when WAYLAND_DISPLAY is set, and the app inherits that; one
// reopened on X11 (see relaunchForDragAndDrop) starts the app without it.
func alacrittyOnWayland(getenv func(string) string) bool {
	inAlacritty := getenv("ALACRITTY_WINDOW_ID") != "" || getenv("ALACRITTY_SOCKET") != "" || getenv("TERM") == "alacritty"
	return inAlacritty && getenv("WAYLAND_DISPLAY") != ""
}

// maxDroppedFiles bounds how many files one drop sends.
const maxDroppedFiles = 20

// droppedFiles returns the files a paste names when it is nothing but paths to existing files,
// which is what a terminal pastes when files are dragged onto it: plain, quoted, with escaped
// spaces, or as file:// URLs; several separated by spaces or lines. Anything else (text, a
// folder, a path that does not exist) returns nil and stays a paste.
func droppedFiles(pasted string) []string {
	text := strings.TrimSpace(pasted)
	if text == "" || len(text) > 64*1024 {
		return nil
	}
	if p := existingFile(text); p != "" {
		return []string{p} // one path, spaces and all
	}
	var parts []string
	if strings.ContainsAny(text, "\r\n") {
		parts = strings.FieldsFunc(text, func(r rune) bool { return r == '\r' || r == '\n' })
	} else {
		parts = splitDroppedPaths(text)
	}
	if len(parts) == 0 || len(parts) > maxDroppedFiles {
		return nil
	}
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		p := existingFile(part)
		if p == "" {
			return nil
		}
		files = append(files, p)
	}
	return files
}

// existingFile returns the regular file a pasted path names, or "".
func existingFile(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	candidates := []string{cleanFilePath(raw)}
	if runtime.GOOS != "windows" && strings.Contains(raw, `\`) {
		candidates = append(candidates, cleanFilePath(unescapeShell(raw))) // a\ b.png
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// splitDroppedPaths splits a line of dropped paths at spaces outside quotes and escapes.
func splitDroppedPaths(s string) []string {
	var parts []string
	var cur strings.Builder
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'' && runtime.GOOS != "windows":
			cur.WriteRune(r) // kept: existingFile unescapes it
			escaped = true
		case quote != 0:
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			cur.WriteRune(r)
			quote = r
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// unescapeShell drops the backslashes a shell-style escape puts before spaces and the like.
func unescapeShell(s string) string {
	var b strings.Builder
	escaped := false
	for _, r := range s {
		if r == '\\' && !escaped {
			escaped = true
			continue
		}
		b.WriteRune(r)
		escaped = false
	}
	return b.String()
}
