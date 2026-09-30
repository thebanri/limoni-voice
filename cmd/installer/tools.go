package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thebanri/limoni-voice/internal/toolpath"
)

// The screen share tools are FFmpeg (sharing; its ffplay is the fallback player) and mpv
// (watching). Each comes from a pinned release archive checked against its SHA-256 and is
// unpacked into the install's bin folder, where Limoni Voice looks first. Afterwards the
// installer looks the tool up the way the app does and runs it once, so a tool it reports
// as installed is one the app finds and can start.

// toolArchive is a zip holding a tool; files are the base names copied out of it into bin.
type toolArchive struct {
	url    string
	sha256 string
	files  []string
}

type tool struct {
	label    string
	exe      string   // the name toolpath.Find looks up
	check    []string // arguments that print the version and exit
	purpose  string
	archive  func(arch string) (toolArchive, bool)
	wingetID string
}

var tools = []tool{
	{
		label: "FFmpeg", exe: "ffmpeg", check: []string{"-version"}, purpose: "for screen sharing",
		archive: func(arch string) (toolArchive, bool) {
			// An x64 build: Windows on ARM runs it emulated.
			return toolArchive{
				url:    "https://github.com/GyanD/codexffmpeg/releases/download/7.1/ffmpeg-7.1-essentials_build.zip",
				sha256: "fa7d4d7e795db0e2503f49f105f46ed5852386f0cfdd819899be3b65ebde24fc",
				files:  []string{"ffmpeg.exe", "ffplay.exe"},
			}, arch == "amd64" || arch == "arm64"
		},
		wingetID: "Gyan.FFmpeg",
	},
	{
		label: "mpv", exe: "mpv", check: []string{"--version"}, purpose: "for watching screen shares",
		archive: func(arch string) (toolArchive, bool) {
			// The official builds: mpv.exe links everything but the Vulkan loader, which the
			// archive carries (the .pdb debug symbols next to them are left out).
			switch arch {
			case "amd64":
				return toolArchive{
					url:    "https://github.com/mpv-player/mpv/releases/download/v0.41.0/mpv-v0.41.0-x86_64-pc-windows-msvc.zip",
					sha256: "4e197f729f5071c6772f35fffd96e0f36e3e8a044bd9479b136bb09b7c6a80ff",
					files:  []string{"mpv.exe", "vulkan-1.dll"},
				}, true
			case "arm64":
				return toolArchive{
					url:    "https://github.com/mpv-player/mpv/releases/download/v0.41.0/mpv-v0.41.0-aarch64-pc-windows-msvc.zip",
					sha256: "a822abeffd0ac88951f4084f3425f949842aa17d616f880637ebe9041e482e97",
					files:  []string{"mpv.exe", "vulkan-1.dll"},
				}, true
			}
			return toolArchive{}, false
		},
		// Needs administrator rights and installs to Program Files: the last resort only.
		wingetID: "shinchiro.mpv",
	},
}

// windowsArch is the machine's architecture, also when a 32-bit installer runs on it.
func windowsArch() string {
	for _, v := range []string{os.Getenv("PROCESSOR_ARCHITEW6432"), os.Getenv("PROCESSOR_ARCHITECTURE")} {
		switch strings.ToUpper(v) {
		case "AMD64":
			return "amd64"
		case "ARM64":
			return "arm64"
		}
	}
	return runtime.GOARCH
}

// ensureTool makes sure Limoni Voice finds a working t: from the archive, else from winget.
// It says what it did, and why it failed when it did.
func ensureTool(t tool, binDir string) {
	if p, err := findWorkingTool(t); err == nil {
		fmt.Printf("[✓] %s already installed: %s\n", t.label, p)
		return
	}
	fmt.Printf("[*] Installing %s (%s)...\n", t.label, t.purpose)

	var failures []string
	if a, ok := t.archive(windowsArch()); ok {
		if err := installArchive(t.label, a, binDir); err != nil {
			failures = append(failures, "download: "+err.Error())
		}
		p, err := findWorkingTool(t)
		if err == nil {
			fmt.Printf("[✓] %s installed: %s\n", t.label, p)
			return
		}
		if len(failures) == 0 {
			failures = append(failures, err.Error())
		}
	}

	if t.wingetID != "" {
		fmt.Printf("[*] Trying winget (%s); Windows may ask for permission...\n", t.wingetID)
		cmd := exec.Command("winget", "install", "-e", "--id", t.wingetID, "--accept-source-agreements", "--accept-package-agreements")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			failures = append(failures, "winget: "+err.Error())
		}
		if p, err := findWorkingTool(t); err == nil {
			fmt.Printf("[✓] %s installed: %s\n", t.label, p)
			return
		} else if len(failures) == 0 {
			failures = append(failures, err.Error())
		}
	}

	fmt.Printf("[!] %s could not be installed (%s).\n", t.label, strings.Join(failures, "; "))
	fmt.Printf("    Limoni Voice works without it, except %s. Run this installer again,\n", strings.TrimPrefix(t.purpose, "for "))
	fmt.Printf("    or in PowerShell: winget install -e --id %s\n", t.wingetID)
}

// findWorkingTool finds t as Limoni Voice does and checks that it starts.
func findWorkingTool(t tool) (string, error) {
	p, err := toolpath.Find(t.exe)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, p, t.check...).CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			err = fmt.Errorf("%w: %s", err, firstLine(msg))
		}
		return "", fmt.Errorf("%s does not start (%v)", p, err)
	}
	return p, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// installArchive downloads a (retrying up to three times), checks its SHA-256 and unpacks
// its files into dir.
func installArchive(label string, a toolArchive, dir string) error {
	tmp := filepath.Join(os.TempDir(), "limoni_"+strings.ToLower(label)+"_setup.zip")
	defer os.Remove(tmp)

	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			fmt.Printf("[-] %v\n[*] Retrying %s download (%d/3)...\n", err, label, attempt)
			time.Sleep(2 * time.Second)
		}
		if err = downloadFileWithProgress(a.url, tmp, label); err != nil {
			continue
		}
		if err = checkSHA256(tmp, a.sha256); err != nil {
			continue
		}
		fmt.Printf("[*] Extracting %s...\n", label)
		return extractFromZip(tmp, dir, a.files)
	}
	return err
}

func checkSHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return errors.New("download corrupted (SHA-256 mismatch)")
	}
	return nil
}

// extractFromZip copies each of names (matched by base name, anywhere in the archive) into
// dir. Every file goes to a temporary name first, so a failed extraction never leaves a
// broken program behind that would later count as installed.
func extractFromZip(zipPath, dir string, names []string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, name := range names {
		var entry *zip.File
		for _, f := range r.File {
			if strings.EqualFold(filepath.Base(f.Name), name) {
				entry = f
				break
			}
		}
		if entry == nil {
			return fmt.Errorf("%s not found in the archive", name)
		}
		if err := extractFile(entry, filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func extractFile(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, rc)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// A running copy (an open Limoni Voice watching a stream) cannot be replaced;
		// moving it aside works even then.
		if fileExists(dest) {
			old := dest + ".old"
			_ = os.Remove(old)
			_ = os.Rename(dest, old)
		}
		err = os.Rename(tmp, dest)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
