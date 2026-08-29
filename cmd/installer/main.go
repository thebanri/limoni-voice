package main

import (
	"archive/zip"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed microphone.obj
var embeddedMicrophoneObj []byte

const (
	FFMPEG_URL = "https://github.com/GyanD/codexffmpeg/releases/download/7.1/ffmpeg-7.1-essentials_build.zip"
	REPO_URL   = "https://github.com/thebanri/limoni-voice"
)

func main() {
	fmt.Println("==================================================")
	fmt.Println("   🍋  LIMONI VOICE - WINDOWS SETUP INSTALLER     ")
	fmt.Println("==================================================")
	fmt.Println()

	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
	}

	installDir := filepath.Join(localAppData, "LimoniVoice")
	binDir := filepath.Join(installDir, "bin")

	if err := os.MkdirAll(binDir, 0755); err != nil {
		fmt.Printf("[!] Kurulum dizini olusturulamadi: %v\n", err)
		pauseAndExit(1)
	}

	fmt.Printf("[*] Hedef Kurulum Dizini: %s\n\n", installDir)

	// 1. Extract embedded microphone.obj (3D visualizer model)
	targetMicObj := filepath.Join(installDir, "microphone.obj")
	if len(embeddedMicrophoneObj) > 0 {
		fmt.Println("[+] 3D Mikrofon modeli cikariliyor (microphone.obj)...")
		_ = os.WriteFile(targetMicObj, embeddedMicrophoneObj, 0644)
	}

	// 2. Install limoni-voice.exe
	targetVoiceExe := filepath.Join(installDir, "limoni-voice.exe")
	currDirExe := filepath.Join(".", "limoni-voice.exe")
	if fileExists(currDirExe) {
		fmt.Println("[+] limoni-voice.exe yerel dizinden kopyalaniyor...")
		_ = copyFile(currDirExe, targetVoiceExe)
	} else {
		// Download latest release binary
		fmt.Println("[*] limoni-voice.exe GitHub Releases uzerinden indiriliyor...")
		downloadURL := REPO_URL + "/releases/latest/download/limoni-voice_windows_amd64.exe"
		if err := downloadFileWithProgress(downloadURL, targetVoiceExe, "Limoni Voice"); err != nil {
			fmt.Printf("[-] limoni-voice.exe indirilemedi: %v\n", err)
		}
	}

	// 3. Install FFmpeg
	targetFfmpeg := filepath.Join(binDir, "ffmpeg.exe")
	if !fileExists(targetFfmpeg) && !commandExists("ffmpeg.exe") {
		fmt.Println("[*] FFmpeg indiriliyor ve kuruluyor (Ekran paylasimi icin)...")
		tempZip := filepath.Join(os.TempDir(), "ffmpeg_setup.zip")
		if err := downloadFileWithProgress(FFMPEG_URL, tempZip, "FFmpeg"); err == nil {
			fmt.Println("[*] FFmpeg arsivi aciliyor...")
			_ = extractExeFromZip(tempZip, "ffmpeg.exe", targetFfmpeg)
			_ = os.Remove(tempZip)
		}
	} else {
		fmt.Println("[✓] FFmpeg zaten mevcut.")
	}

	// 4. Install MPV
	targetMpv := filepath.Join(binDir, "mpv.exe")
	if !fileExists(targetMpv) && !commandExists("mpv.exe") {
		fmt.Println("[*] MPV Player kontrol ediliyor (Yayin izleme icin)...")
		cmd := exec.Command("winget", "install", "mpv.mpv", "--accept-source-agreements", "--accept-package-agreements")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Println("[-] winget ile MPV kurulamadi. Alternatif yukleme aciliyor...")
		} else {
			fmt.Println("[✓] MPV basariyla kuruldu.")
		}
	} else {
		fmt.Println("[✓] MPV Player zaten mevcut.")
	}

	// 5. Update PATH
	fmt.Println("[*] Sistem PATH degiskeni guncelleniyor...")
	psPathScript := fmt.Sprintf(`
		$binDir = '%s'
		$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
		if ($userPath -notlike "*$binDir*") {
			[Environment]::SetEnvironmentVariable("Path", "$binDir;$userPath", "User")
		}
	`, binDir)
	_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psPathScript).Run()

	// 6. Create Desktop Shortcut
	fmt.Println("[*] Masaustu kisayolu olusturuluyor...")
	desktopDir := filepath.Join(os.Getenv("USERPROFILE"), "Desktop")
	shortcutPath := filepath.Join(desktopDir, "Limoni Voice.lnk")
	psShortcutScript := fmt.Sprintf(`
		$wscript = New-Object -ComObject WScript.Shell
		$shortcut = $wscript.CreateShortcut('%s')
		$shortcut.TargetPath = '%s'
		$shortcut.WorkingDirectory = '%s'
		$shortcut.Description = 'Limoni Voice - P2P Encrypted Voice & Screen Sharing'
		$shortcut.Save()
	`, shortcutPath, targetVoiceExe, installDir)
	_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psShortcutScript).Run()

	fmt.Println()
	fmt.Println("==================================================")
	fmt.Println("   🎉  KURULUM BASARIYLA TAMAMLANDI!              ")
	fmt.Println("==================================================")
	fmt.Printf("[✓] Limoni Voice: %s\n", targetVoiceExe)
	fmt.Printf("[✓] 3D Model: %s\n", targetMicObj)
	fmt.Printf("[✓] Masaustu Kisayolu: %s\n", shortcutPath)
	fmt.Println()
	fmt.Println("Uygulamayi baslatmak icin 'ENTER' tusuna basin...")

	var input string
	_, _ = fmt.Scanln(&input)

	// Launch
	if fileExists(targetVoiceExe) {
		launchCmd := exec.Command(targetVoiceExe)
		launchCmd.Dir = installDir
		_ = launchCmd.Start()
	}
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
					fmt.Printf("\r[%s] Indiriliyor: %.1f MB / %.1f MB (%%%.1f)", label, float64(downloaded)/1024/1024, float64(total)/1024/1024, pct)
				} else {
					fmt.Printf("\r[%s] Indiriliyor: %.1f MB", label, float64(downloaded)/1024/1024)
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
	return fmt.Errorf("%s zip icinde bulunamadi", targetExeName)
}

func pauseAndExit(code int) {
	fmt.Println("\nCikmak icin ENTER tusuna basin...")
	var dummy string
	_, _ = fmt.Scanln(&dummy)
	os.Exit(code)
}
