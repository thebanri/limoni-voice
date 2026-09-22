// Opening files and folders with the platform's editor and file manager.

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// OpenInEditor opens the specified file in the system's default or preferred GUI/terminal editor
func OpenInEditor(filePath string) error {
	if testing.Testing() {
		return nil
	}

	absPath, err := filepath.Abs(filePath)
	if err == nil {
		filePath = absPath
	}

	switch runtime.GOOS {
	case "windows":
		// 1. Try VS Code if installed
		if p, err := exec.LookPath("code"); err == nil && p != "" {
			cmd := exec.Command("code", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
		// 2. Try Windows ShellExecute via rundll32 (prevents cmd.exe shell argument injection)
		cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", filePath)
		if err := cmd.Start(); err == nil {
			return nil
		}
		// 3. Fallback to notepad
		cmdNotepad := exec.Command("notepad.exe", filePath)
		return cmdNotepad.Start()

	case "darwin":
		// 1. Try VS Code if installed
		if p, err := exec.LookPath("code"); err == nil && p != "" {
			cmd := exec.Command("code", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
		// 2. Try macOS default text editor
		cmd := exec.Command("open", "-t", filePath)
		if err := cmd.Start(); err == nil {
			return nil
		}
		// 3. Fallback to open
		cmdOpen := exec.Command("open", filePath)
		return cmdOpen.Start()

	default: // Linux, BSD, etc.
		// 1. Check custom GUI editor environment variable
		if customEditor := os.Getenv("VISUAL"); customEditor != "" {
			cmd := exec.Command(customEditor, filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 2. Try VS Code if installed
		if p, err := exec.LookPath("code"); err == nil && p != "" {
			cmd := exec.Command("code", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 3. Try xdg-open (opens default desktop editor e.g. Kate, Gedit, Text Editor)
		if p, err := exec.LookPath("xdg-open"); err == nil && p != "" {
			cmd := exec.Command("xdg-open", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 4. Try gio open
		if p, err := exec.LookPath("gio"); err == nil && p != "" {
			cmd := exec.Command("gio", "open", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 5. Try known common Linux GUI editors
		for _, guiEditor := range []string{"cursor", "vscodium", "code-oss", "zed", "gedit", "kate", "gnome-text-editor", "mousepad", "xed", "pluma", "subl", "sublime-text", "atom", "kwrite"} {
			if p, err := exec.LookPath(guiEditor); err == nil && p != "" {
				cmd := exec.Command(guiEditor, filePath)
				if err := cmd.Start(); err == nil {
					return nil
				}
			}
		}

		// 6. If $TERMINAL and $EDITOR are set, spawn a separate terminal window
		term := os.Getenv("TERMINAL")
		cliEditor := os.Getenv("EDITOR")
		if cliEditor == "" {
			cliEditor = "nano"
		}
		if term != "" {
			cmd := exec.Command(term, "-e", cliEditor, filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 7. Try common terminal emulators to spawn the CLI editor in a new window
		for _, termEmulator := range []string{"x-terminal-emulator", "gnome-terminal", "konsole", "xfce4-terminal", "alacritty", "kitty", "foot", "wezterm", "tilix", "terminator", "xterm"} {
			if p, err := exec.LookPath(termEmulator); err == nil && p != "" {
				var cmd *exec.Cmd
				if termEmulator == "gnome-terminal" {
					cmd = exec.Command(termEmulator, "--", cliEditor, filePath)
				} else {
					cmd = exec.Command(termEmulator, "-e", cliEditor, filePath)
				}
				if err := cmd.Start(); err == nil {
					return nil
				}
			}
		}

		return errors.New("no suitable editor or terminal found to open file")
	}
}

// OpenFolder opens the system file manager at the specified directory
func OpenFolder(dirPath string) error {
	if testing.Testing() {
		return nil
	}

	absPath, err := filepath.Abs(dirPath)
	if err == nil {
		dirPath = absPath
	}

	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", dirPath).Start()
	case "darwin":
		return exec.Command("open", dirPath).Start()
	default:
		return exec.Command("xdg-open", dirPath).Start()
	}
}
