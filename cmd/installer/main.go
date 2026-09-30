package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thebanri/limoni-voice/assets"
)

const (
	REPO_URL = "https://github.com/thebanri/limoni-voice"
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

	// 1. Extract embedded icon.ico (the 3D microphone model is embedded in the app itself)
	targetIconIco := filepath.Join(installDir, "icon.ico")
	fmt.Println("[+] Extracting application icon (icon.ico)...")
	_ = os.WriteFile(targetIconIco, assets.IconICO, 0644)

	// 2. Install limoni-voice.exe (Standalone Embedded Binary -> Local Copy -> Online Download)
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

	// 3. Shortcuts, invite links and PATH come before the FFmpeg and MPV downloads: winget can
	// take minutes, and a window closed during it must not leave the app without a shortcut.
	if fileExists(targetVoiceExe) {
		createShortcuts(targetVoiceExe, installDir, targetIconIco)
	} else {
		fmt.Println("[!] Shortcuts skipped: limoni-voice.exe is missing (an antivirus may have removed it).")
	}

	if err := registerInviteScheme(targetVoiceExe, targetIconIco); err != nil {
		fmt.Printf("[-] Could not register limoni:// invite links: %v\n", err)
	} else {
		fmt.Println("[+] limoni:// invite links now open Limoni Voice")
	}

	fmt.Println("[*] Updating user PATH variable...")
	psPathScript := fmt.Sprintf(`
		$binDir = '%s'
		$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
		if ($userPath -notlike "*$binDir*") {
			[Environment]::SetEnvironmentVariable("Path", "$binDir;$userPath", "User")
		}
	`, strings.ReplaceAll(binDir, "'", "''"))
	if out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psPathScript).CombinedOutput(); err != nil {
		fmt.Printf("[-] Could not update PATH: %v %s\n", err, strings.TrimSpace(string(out)))
	}

	// 4. Screen share tools: FFmpeg and mpv
	for _, t := range tools {
		ensureTool(t, binDir)
	}

	fmt.Println()
	fmt.Println("==================================================")
	fmt.Println("   🎉  INSTALLATION COMPLETED SUCCESSFULLY!       ")
	fmt.Println("==================================================")
	fmt.Printf("[✓] Limoni Voice: %s\n", targetVoiceExe)
	fmt.Printf("[✓] App Icon: %s\n", targetIconIco)
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

// createShortcuts puts a Limoni Voice shortcut on the desktop and in the Start Menu, and
// says which one failed and why.
func createShortcuts(exe, dir, icon string) {
	fmt.Println("[*] Creating desktop and Start Menu shortcuts...")
	folders := shortcutFolders()
	if len(folders) == 0 {
		fmt.Println("[-] Could not find the desktop or Start Menu folder.")
		return
	}
	for _, name := range []string{"desktop", "Start Menu"} {
		folder, ok := folders[name]
		if !ok {
			fmt.Printf("[-] Could not find the %s folder.\n", name)
			continue
		}
		lnk := filepath.Join(folder, "Limoni Voice.lnk")
		if err := createShortcut(lnk, exe, dir, icon, "Limoni Voice - P2P Encrypted Voice & Screen Sharing"); err != nil {
			fmt.Printf("[-] Could not create the %s shortcut: %v\n", name, err)
			continue
		}
		fmt.Printf("[+] %s shortcut: %s\n", name, lnk)
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

	// Ensure the icon exists
	targetIconIco := filepath.Join(installDir, "icon.ico")
	if !fileExists(targetIconIco) {
		_ = os.WriteFile(targetIconIco, assets.IconICO, 0644)
	}

	_ = os.Remove(oldPath)

	fmt.Println("[✓] Limoni Voice restored successfully! Launching...")
	time.Sleep(300 * time.Millisecond)

	launchCmd := exec.Command("cmd.exe", "/c", "start", "", targetVoiceExe)
	launchCmd.Dir = installDir
	return launchCmd.Start()
}

// registerInviteScheme registers the limoni:// URL scheme under HKCU, so no administrator
// rights are needed; removing HKCU\Software\Classes\limoni undoes it.
func registerInviteScheme(exe, icon string) error {
	const key = `HKCU\Software\Classes\limoni`
	for _, args := range [][]string{
		{"add", key, "/ve", "/d", "URL:Limoni Voice invite", "/f"},
		{"add", key, "/v", "URL Protocol", "/d", "", "/f"},
		{"add", key + `\DefaultIcon`, "/ve", "/d", icon, "/f"},
		{"add", key + `\shell\open\command`, "/ve", "/d", `"` + exe + `" "%1"`, "/f"},
	} {
		if out, err := exec.Command("reg", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
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

// downloadStallTimeout aborts a download that has received nothing for this long, so a stuck
// connection fails (and is retried) instead of leaving the installer waiting forever.
const downloadStallTimeout = 45 * time.Second

func downloadFileWithProgress(url, targetPath, label string) (err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stall := time.AfterFunc(downloadStallTimeout, cancel)
	defer stall.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("no response for %v", downloadStallTimeout)
		}
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
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(targetPath)
		}
	}()

	total := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 32*1024)
	lastPrint := time.Now()

	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			stall.Reset(downloadStallTimeout)
			if _, werr := out.Write(buf[:n]); werr != nil {
				fmt.Println()
				return werr
			}
			downloaded += int64(n)
			if time.Since(lastPrint) > 200*time.Millisecond || rerr == io.EOF {
				lastPrint = time.Now()
				if total > 0 {
					pct := float64(downloaded) / float64(total) * 100
					fmt.Printf("\r[%s] Downloading: %.1f MB / %.1f MB (%%%.1f)", label, float64(downloaded)/1024/1024, float64(total)/1024/1024, pct)
				} else {
					fmt.Printf("\r[%s] Downloading: %.1f MB", label, float64(downloaded)/1024/1024)
				}
			}
		}
		if rerr != nil {
			fmt.Println()
			if rerr == io.EOF {
				break
			}
			if ctx.Err() != nil {
				return fmt.Errorf("download stalled: nothing received for %v", downloadStallTimeout)
			}
			return rerr
		}
	}
	if total > 0 && downloaded != total {
		return fmt.Errorf("download incomplete: %d of %d bytes", downloaded, total)
	}
	return nil
}

func pauseAndExit(code int) {
	fmt.Println("\nPress ENTER to exit...")
	var dummy string
	_, _ = fmt.Scanln(&dummy)
	os.Exit(code)
}
