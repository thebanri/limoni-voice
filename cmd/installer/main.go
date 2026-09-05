package main

import (
	"archive/zip"
	"bufio"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed microphone.obj
var embeddedMicrophoneObj []byte

//go:embed icon.ico
var embeddedIconIco []byte

//go:embed limoni-voice.exe
var embeddedVoiceExe []byte

const (
	FFMPEG_URL = "https://github.com/GyanD/codexffmpeg/releases/download/7.1/ffmpeg-7.1-essentials_build.zip"
	REPO_URL   = "https://github.com/thebanri/limoni-voice"
)

func main() {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
	}

	installDir := filepath.Join(localAppData, "LimoniVoice")
	targetVoiceExe := filepath.Join(installDir, "limoni-voice.exe")
	binDir := filepath.Join(installDir, "bin")

	currExe, _ := os.Executable()
	currExeAbs, _ := filepath.Abs(currExe)
	targetVoiceExeAbs, _ := filepath.Abs(targetVoiceExe)

	// If this installer executable is executing as `limoni-voice.exe` inside the install folder
	// (caused when an older updater bug replaced the app binary with the installer),
	// automatically self-heal by restoring the real application binary and launching it directly.
	if strings.EqualFold(currExeAbs, targetVoiceExeAbs) {
		fmt.Println("==================================================")
		fmt.Println("   🍋  LIMONI VOICE - APPLICATION RESTORE         ")
		fmt.Println("==================================================")
		fmt.Println("[*] Restoring Limoni Voice application executable...")
		if err := selfHealAndLaunch(installDir, targetVoiceExe, currExeAbs); err == nil {
			return
		}
		fmt.Println("[!] Automatic restore failed, running full repair...")
	}

	fmt.Println("==================================================")
	fmt.Println("   🍋  LIMONI VOICE - WINDOWS SETUP INSTALLER     ")
	fmt.Println("==================================================")
	fmt.Println()

	if err := os.MkdirAll(binDir, 0755); err != nil {
		fmt.Printf("[!] Failed to create install directory: %v\n", err)
		pauseAndExit(1)
	}

	fmt.Printf("[*] Target Install Directory: %s\n\n", installDir)

	// 1. Extract embedded microphone.obj (3D visualizer model)
	targetMicObj := filepath.Join(installDir, "microphone.obj")
	if len(embeddedMicrophoneObj) > 0 {
		fmt.Println("[+] Extracting 3D Microphone model (microphone.obj)...")
		_ = os.WriteFile(targetMicObj, embeddedMicrophoneObj, 0644)
	}

	// 2. Extract embedded icon.ico
	targetIconIco := filepath.Join(installDir, "icon.ico")
	if len(embeddedIconIco) > 0 {
		fmt.Println("[+] Extracting application icon (icon.ico)...")
		_ = os.WriteFile(targetIconIco, embeddedIconIco, 0644)
	}

	// 3. Install limoni-voice.exe (Standalone Embedded Binary -> Local Copy -> Online Download)
	if len(embeddedVoiceExe) > 1024 {
		fmt.Println("[+] Extracting Limoni Voice executable (embedded bundle)...")
		if err := writeOrReplaceExecutable(targetVoiceExe, embeddedVoiceExe); err != nil {
			fmt.Printf("[!] Failed to write limoni-voice.exe: %v\n", err)
		}
	} else {
		currDirExe := filepath.Join(".", "limoni-voice.exe")
		currDirExeAbs, _ := filepath.Abs(currDirExe)
		if fileExists(currDirExe) && !strings.EqualFold(currExeAbs, currDirExeAbs) {
			fmt.Println("[+] Copying limoni-voice.exe from local directory...")
			_ = copyFile(currDirExe, targetVoiceExe)
		} else {
			// Download latest release binary
			arch := runtime.GOARCH
			fmt.Println("[*] Downloading limoni-voice.exe from GitHub Releases...")
			downloadURL := fmt.Sprintf("%s/releases/latest/download/limoni-voice_windows_%s.exe", REPO_URL, arch)
			tmpDl := filepath.Join(installDir, "limoni-voice.dl.tmp")
			if err := downloadFileWithProgress(downloadURL, tmpDl, "Limoni Voice"); err == nil {
				data, errRead := os.ReadFile(tmpDl)
				_ = os.Remove(tmpDl)
				if errRead == nil && len(data) > 1024 {
					_ = writeOrReplaceExecutable(targetVoiceExe, data)
				}
			} else {
				fmt.Printf("[-] Failed to download limoni-voice.exe: %v\n", err)
			}
		}
	}

	if !fileExists(targetVoiceExe) {
		fmt.Println("[!] Warning: limoni-voice.exe could not be verified in target path.")
	}

	// 4. Install FFmpeg
	targetFfmpeg := filepath.Join(binDir, "ffmpeg.exe")
	if !fileExists(targetFfmpeg) && !commandExists("ffmpeg.exe") {
		fmt.Println("[*] Downloading and installing FFmpeg (for screen sharing)...")
		tempZip := filepath.Join(os.TempDir(), "ffmpeg_setup.zip")
		if err := downloadFileWithProgress(FFMPEG_URL, tempZip, "FFmpeg"); err == nil {
			fmt.Println("[*] Extracting FFmpeg archive...")
			_ = extractExeFromZip(tempZip, "ffmpeg.exe", targetFfmpeg)
			_ = os.Remove(tempZip)
		}
	} else {
		fmt.Println("[✓] FFmpeg already installed.")
	}

	// 5. Install MPV
	targetMpv := filepath.Join(binDir, "mpv.exe")
	if !fileExists(targetMpv) && !commandExists("mpv.exe") {
		fmt.Println("[*] Checking MPV Player (for stream viewing)...")
		cmd := exec.Command("winget", "install", "-e", "--id", "mpv.mpv", "--accept-source-agreements", "--accept-package-agreements")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// Fallback to shinchiro.mpv or mpv.net
			_ = exec.Command("winget", "install", "-e", "--id", "shinchiro.mpv", "--accept-source-agreements", "--accept-package-agreements").Run()
			_ = exec.Command("winget", "install", "-e", "--id", "mpv.net", "--accept-source-agreements", "--accept-package-agreements").Run()
		}

		// Check if winget placed mpv in packages or programs directory and copy to binDir
		searchDirs := []string{
			filepath.Join(localAppData, "Microsoft", "WinGet", "Packages"),
			filepath.Join(localAppData, "Microsoft", "WinGet", "Links"),
			filepath.Join(localAppData, "Programs", "mpv"),
			filepath.Join(localAppData, "Programs", "mpv.net"),
			filepath.Join(os.Getenv("ProgramFiles"), "mpv"),
		}
		for _, sDir := range searchDirs {
			if _, err := os.Stat(sDir); err == nil {
				_ = filepath.Walk(sDir, func(path string, info os.FileInfo, err error) error {
					if err == nil && !info.IsDir() && strings.EqualFold(info.Name(), "mpv.exe") {
						_ = copyFile(path, targetMpv)
						return io.EOF // stop walking
					}
					return nil
				})
			}
			if fileExists(targetMpv) {
				break
			}
		}
	} else {
		fmt.Println("[✓] MPV Player already installed.")
	}

	// 6. Update PATH
	fmt.Println("[*] Updating system PATH variable...")
	psPathScript := fmt.Sprintf(`
		$binDir = '%s'
		$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
		if ($userPath -notlike "*$binDir*") {
			[Environment]::SetEnvironmentVariable("Path", "$binDir;$userPath", "User")
		}
	`, binDir)
	_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psPathScript).Run()

	// 7. Create Desktop and Start Menu Shortcuts with Icon (Supports OneDrive and International Windows)
	fmt.Println("[*] Creating desktop and start menu shortcuts...")
	psShortcutScript := fmt.Sprintf(`
		$wscript = New-Object -ComObject WScript.Shell
		$desktop = [Environment]::GetFolderPath("Desktop")
		if ($desktop) {
			$shortcutPath = Join-Path $desktop "Limoni Voice.lnk"
			$shortcut = $wscript.CreateShortcut($shortcutPath)
			$shortcut.TargetPath = '%s'
			$shortcut.WorkingDirectory = '%s'
			$shortcut.IconLocation = '%s,0'
			$shortcut.Description = 'Limoni Voice - P2P Encrypted Voice & Screen Sharing'
			$shortcut.Save()
		}
		$programs = [Environment]::GetFolderPath("Programs")
		if ($programs) {
			$smShortcutPath = Join-Path $programs "Limoni Voice.lnk"
			$smShortcut = $wscript.CreateShortcut($smShortcutPath)
			$smShortcut.TargetPath = '%s'
			$smShortcut.WorkingDirectory = '%s'
			$smShortcut.IconLocation = '%s,0'
			$smShortcut.Description = 'Limoni Voice - P2P Encrypted Voice & Screen Sharing'
			$smShortcut.Save()
		}
	`, targetVoiceExe, installDir, targetIconIco, targetVoiceExe, installDir, targetIconIco)
	_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psShortcutScript).Run()

	fmt.Println()
	fmt.Println("==================================================")
	fmt.Println("   🎉  INSTALLATION COMPLETED SUCCESSFULLY!       ")
	fmt.Println("==================================================")
	fmt.Printf("[✓] Limoni Voice: %s\n", targetVoiceExe)
	fmt.Printf("[✓] 3D Model: %s\n", targetMicObj)
	fmt.Printf("[✓] App Icon: %s\n", targetIconIco)
	fmt.Println("[✓] Desktop & Start Menu Shortcuts created!")
	fmt.Println()
	fmt.Println("Press ENTER to launch the application (or close this window)...")

	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadString('\n')

	// Launch detached in a new independent console window
	if fileExists(targetVoiceExe) {
		launchCmd := exec.Command("cmd.exe", "/c", "start", "", targetVoiceExe)
		launchCmd.Dir = installDir
		_ = launchCmd.Start()
	}
}

func selfHealAndLaunch(installDir, targetVoiceExe, currExeAbs string) error {
	var appBytes []byte

	if len(embeddedVoiceExe) > 1024 {
		appBytes = embeddedVoiceExe
	} else {
		arch := runtime.GOARCH
		downloadURL := fmt.Sprintf("%s/releases/latest/download/limoni-voice_windows_%s.exe", REPO_URL, arch)
		tmpDl := filepath.Join(installDir, "limoni-voice.dl.tmp")
		if err := downloadFileWithProgress(downloadURL, tmpDl, "Limoni Voice"); err == nil {
			data, errRead := os.ReadFile(tmpDl)
			_ = os.Remove(tmpDl)
			if errRead == nil && len(data) > 1024 {
				appBytes = data
			}
		}
	}

	if len(appBytes) < 1024 {
		return fmt.Errorf("no application payload available")
	}

	// Rename running installer executable so we can write real limoni-voice.exe
	oldPath := filepath.Join(installDir, "limoni-voice.installer.old")
	_ = os.Remove(oldPath)
	if err := os.Rename(currExeAbs, oldPath); err != nil {
		return fmt.Errorf("failed to rename running installer: %w", err)
	}

	if err := os.WriteFile(targetVoiceExe, appBytes, 0755); err != nil {
		_ = os.Rename(oldPath, currExeAbs)
		return fmt.Errorf("failed to write application binary: %w", err)
	}

	// Ensure 3D model & icon exist
	targetMicObj := filepath.Join(installDir, "microphone.obj")
	if !fileExists(targetMicObj) && len(embeddedMicrophoneObj) > 0 {
		_ = os.WriteFile(targetMicObj, embeddedMicrophoneObj, 0644)
	}
	targetIconIco := filepath.Join(installDir, "icon.ico")
	if !fileExists(targetIconIco) && len(embeddedIconIco) > 0 {
		_ = os.WriteFile(targetIconIco, embeddedIconIco, 0644)
	}

	_ = os.Remove(oldPath)

	fmt.Println("[✓] Limoni Voice restored successfully! Launching...")
	time.Sleep(300 * time.Millisecond)

	launchCmd := exec.Command("cmd.exe", "/c", "start", "", targetVoiceExe)
	launchCmd.Dir = installDir
	return launchCmd.Start()
}

func writeOrReplaceExecutable(targetPath string, data []byte) error {
	if fileExists(targetPath) {
		oldPath := targetPath + ".old"
		_ = os.Remove(oldPath)
		if err := os.Rename(targetPath, oldPath); err == nil {
			if errWrite := os.WriteFile(targetPath, data, 0755); errWrite != nil {
				_ = os.Rename(oldPath, targetPath)
				return errWrite
			}
			_ = os.Remove(oldPath)
			return nil
		}
	}
	return os.WriteFile(targetPath, data, 0755)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func downloadFileWithProgress(url, targetPath, label string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	out, err := os.Create(targetPath)
	if err != nil {
		return err
	}
	defer out.Close()

	total := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 32*1024)
	lastPrint := time.Now()

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, _ = out.Write(buf[:n])
			downloaded += int64(n)
			if time.Since(lastPrint) > 200*time.Millisecond || err == io.EOF {
				lastPrint = time.Now()
				if total > 0 {
					pct := float64(downloaded) / float64(total) * 100
					fmt.Printf("\r[%s] Downloading: %.1f MB / %.1f MB (%%%.1f)", label, float64(downloaded)/1024/1024, float64(total)/1024/1024, pct)
				} else {
					fmt.Printf("\r[%s] Downloading: %.1f MB", label, float64(downloaded)/1024/1024)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	fmt.Println()
	return nil
}

func extractExeFromZip(zipPath, targetExeName, destExePath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if strings.EqualFold(filepath.Base(f.Name), targetExeName) {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()

			out, err := os.Create(destExePath)
			if err != nil {
				return err
			}
			defer out.Close()

			_, err = io.Copy(out, rc)
			return err
		}
	}
	return fmt.Errorf("%s not found in zip archive", targetExeName)
}

func pauseAndExit(code int) {
	fmt.Println("\nPress ENTER to exit...")
	var dummy string
	_, _ = fmt.Scanln(&dummy)
	os.Exit(code)
}
