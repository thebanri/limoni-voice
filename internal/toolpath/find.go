// Package toolpath finds the external programs Limoni Voice runs (FFmpeg, mpv, capture
// tools). The Windows installer uses it too, so what it installs is what the app finds.
package toolpath

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var execCache sync.Map

// Find searches for a binary in PATH, next to current executable, Program Files (all mpv/ffmpeg subfolders), AppData, WinGet, Scoop
func Find(name string) (string, error) {
	if cached, ok := execCache.Load(name); ok {
		if p, ok := cached.(string); ok && p != "" {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}

	// 1. Check system PATH
	if p, err := exec.LookPath(name); err == nil {
		execCache.Store(name, p)
		return p, nil
	}

	exts := []string{""}
	candidateNames := []string{name}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".com", ""}
		if name == "mpv" {
			candidateNames = append(candidateNames, "mpv", "mpvnet", "mpv-player", "mpvcom", "MPV")
		} else if name == "ffmpeg" {
			candidateNames = append(candidateNames, "ffmpeg", "FFmpeg")
		} else if name == "ffplay" {
			candidateNames = append(candidateNames, "ffplay", "FFplay")
		}
	}

	// 2. PATH as saved now: installs made after this process started are not in its own PATH
	searchDirs := registryPathDirs()

	// 3. Current working directory & ./bin & ./tools
	if cwd, err := os.Getwd(); err == nil {
		searchDirs = append(searchDirs, cwd, filepath.Join(cwd, "bin"), filepath.Join(cwd, "tools"))
	}

	// 4. Next to running executable & exe/bin & exe/tools
	if execPath, err := os.Executable(); err == nil {
		execDir := filepath.Dir(execPath)
		searchDirs = append(searchDirs, execDir, filepath.Join(execDir, "bin"), filepath.Join(execDir, "tools"))
	}

	// 5. User home directory, AppData, Scoop, WinGet
	if home, err := os.UserHomeDir(); err == nil {
		searchDirs = append(searchDirs,
			filepath.Join(home, ".limoni-voice", "bin"),
			filepath.Join(home, "AppData", "Local", "limoni-voice", "bin"),
			filepath.Join(home, "AppData", "Local", "LimoniVoice", "bin"),
			filepath.Join(home, "scoop", "shims"),
			filepath.Join(home, "scoop", "apps", "ffmpeg", "current", "bin"),
			filepath.Join(home, "scoop", "apps", "mpv", "current"),
			filepath.Join(home, "AppData", "Local", "Microsoft", "WinGet", "Links"),
			filepath.Join(home, "Downloads"),
			filepath.Join(home, "Desktop"),
		)
	}

	// 6. Windows specific deep folder discovery
	if runtime.GOOS == "windows" {
		sysDrive := os.Getenv("SystemDrive")
		if sysDrive == "" {
			sysDrive = "C:"
		}

		basePfDirs := []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("ProgramW6432"),
			sysDrive + `\Program Files`,
			sysDrive + `\Program Files (x86)`,
		}

		appDataDirs := []string{
			os.Getenv("LOCALAPPDATA"),
			os.Getenv("APPDATA"),
			os.Getenv("ProgramData"),
		}

		// Fixed common install folders and LimoniVoice local bin
		searchDirs = append(searchDirs,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "LimoniVoice", "bin"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "LimoniVoice"),
			filepath.Join(os.Getenv("APPDATA"), "LimoniVoice", "bin"),
			filepath.Join(os.Getenv("APPDATA"), "LimoniVoice"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links"),
			sysDrive+`\ffmpeg\bin`,
			sysDrive+`\ffmpeg`,
			sysDrive+`\mpv`,
			sysDrive+`\tools\ffmpeg\bin`,
			sysDrive+`\tools\mpv`,
			sysDrive+`\ProgramData\chocolatey\bin`,
			sysDrive+`\ProgramData\chocolatey\lib\mpv\tools`,
		)

		// WinGet puts per-user packages under LOCALAPPDATA and machine-wide (elevated) ones
		// under Program Files, each with a Links folder of shims and the extracted Packages.
		wingetRoots := []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet")}
		for _, pf := range []string{os.Getenv("ProgramFiles"), sysDrive + `\Program Files`} {
			if pf != "" {
				wingetRoots = append(wingetRoots, filepath.Join(pf, "WinGet"))
			}
		}
		for _, root := range wingetRoots {
			searchDirs = append(searchDirs, filepath.Join(root, "Links"))
			// Extracted packages: Gyan.FFmpeg_*\ffmpeg-7.1-full_build\bin\ffmpeg.exe, shinchiro.mpv_*\mpv.exe
			wingetPackages := filepath.Join(root, "Packages")
			entries, err := os.ReadDir(wingetPackages)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				pkgDir := filepath.Join(wingetPackages, entry.Name())
				searchDirs = append(searchDirs, pkgDir, filepath.Join(pkgDir, "bin"))
				if subEntries, errSub := os.ReadDir(pkgDir); errSub == nil {
					for _, sub := range subEntries {
						if sub.IsDir() {
							subDir := filepath.Join(pkgDir, sub.Name())
							searchDirs = append(searchDirs, subDir, filepath.Join(subDir, "bin"))
						}
					}
				}
			}
		}

		// Scan Program Files subdirectories matching *mpv*, *MPV*, *ffmpeg*, *FFmpeg*, *player*
		for _, pf := range basePfDirs {
			if pf == "" {
				continue
			}
			searchDirs = append(searchDirs,
				filepath.Join(pf, "mpv"),
				filepath.Join(pf, "MPV"),
				filepath.Join(pf, "MPV Player"),
				filepath.Join(pf, "MPV Player", "bin"),
				filepath.Join(pf, "mpv-net"),
				filepath.Join(pf, "mpv.net"),
				filepath.Join(pf, "mpv-player"),
				filepath.Join(pf, "FFmpeg"),
				filepath.Join(pf, "FFmpeg", "bin"),
				filepath.Join(pf, "ffmpeg"),
				filepath.Join(pf, "ffmpeg", "bin"),
			)

			// Scan all subdirectories in Program Files for any mpv/ffmpeg folder
			if entries, err := os.ReadDir(pf); err == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						lower := strings.ToLower(entry.Name())
						if strings.Contains(lower, "mpv") || strings.Contains(lower, "ffmpeg") || strings.Contains(lower, "player") {
							folderPath := filepath.Join(pf, entry.Name())
							searchDirs = append(searchDirs, folderPath, filepath.Join(folderPath, "bin"))
						}
					}
				}
			}
		}

		// Scan AppData Programs
		for _, ad := range appDataDirs {
			if ad == "" {
				continue
			}
			searchDirs = append(searchDirs,
				filepath.Join(ad, "Programs", "mpv"),
				filepath.Join(ad, "Programs", "mpv.net"),
				filepath.Join(ad, "Programs", "MPV Player"),
				filepath.Join(ad, "Programs", "ffmpeg", "bin"),
			)
			if entries, err := os.ReadDir(filepath.Join(ad, "Programs")); err == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						lower := strings.ToLower(entry.Name())
						if strings.Contains(lower, "mpv") || strings.Contains(lower, "ffmpeg") {
							folderPath := filepath.Join(ad, "Programs", entry.Name())
							searchDirs = append(searchDirs, folderPath, filepath.Join(folderPath, "bin"))
						}
					}
				}
			}
		}
	}

	// Iterate over all discovered directories, candidates and extensions
	for _, dir := range searchDirs {
		for _, cName := range candidateNames {
			for _, ext := range exts {
				candidate := filepath.Join(dir, cName+ext)
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					execCache.Store(name, candidate)
					return candidate, nil
				}
			}
		}
	}

	return "", fmt.Errorf("executable '%s' not found", name)
}
