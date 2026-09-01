package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	AppVersion       = "v1.4.5"
	GitHubRepo       = "thebanri/limoni-voice"
	UpdateCheckDelay = 1200 * time.Millisecond
)

type GitHubRelease struct {
	TagName string        `json:"tag_name"`
	Name    string        `json:"name"`
	Assets  []GitHubAsset `json:"assets"`
}

type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// CheckAndUpdateAsync performs a background update check and auto-updates if a newer release exists.
func CheckAndUpdateAsync(notify func(msg string)) {
	time.Sleep(UpdateCheckDelay)

	latestRelease, err := FetchLatestRelease()
	if err != nil || latestRelease == nil {
		return
	}

	if !IsNewerVersion(latestRelease.TagName, AppVersion) {
		return
	}

	rawExecPath, err := os.Executable()
	if err != nil {
		return
	}

	execPath, err := filepath.EvalSymlinks(rawExecPath)
	if err != nil {
		execPath = rawExecPath
	}

	// Do not attempt to self-update if running from a temporary go-build directory (e.g. `go run`)
	if isTemporaryBuild(execPath) {
		notify(fmt.Sprintf("New version %s available at github.com/%s", latestRelease.TagName, GitHubRepo))
		return
	}

	notify(fmt.Sprintf("Updating to %s in background...", latestRelease.TagName))

	err = DownloadAndApplyUpdate(latestRelease, execPath)
	if err != nil {
		notify(fmt.Sprintf("Update failed: %v", err))
		return
	}

	notify(fmt.Sprintf("Updated to %s! (Takes effect on next launch)", latestRelease.TagName))
}

func isTemporaryBuild(path string) bool {
	p := strings.ToLower(path)
	return strings.Contains(p, "/go-build") ||
		strings.Contains(p, "\\go-build") ||
		strings.Contains(p, "b001/exe") ||
		strings.HasPrefix(p, strings.ToLower(os.TempDir()))
}

// FetchLatestRelease queries GitHub for the latest release metadata.
func FetchLatestRelease() (*GitHubRelease, error) {
	client := &http.Client{
		Timeout: 8 * time.Second,
	}

	req, err := http.NewRequestWithContext(context.Background(), "GET", fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", GitHubRepo), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Limoni-Voice-AutoUpdater/"+AppVersion)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api status: %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

// IsNewerVersion parses semver strings like "v1.4.2" and checks if latest > current.
func IsNewerVersion(latest, current string) bool {
	lParts := parseSemver(latest)
	cParts := parseSemver(current)

	if len(lParts) == 0 || len(cParts) == 0 {
		return false
	}

	for i := 0; i < len(lParts) && i < len(cParts); i++ {
		if lParts[i] > cParts[i] {
			return true
		}
		if lParts[i] < cParts[i] {
			return false
		}
	}

	return len(lParts) > len(cParts)
}

func parseSemver(v string) []int {
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if idx := strings.IndexAny(v, "-+"); idx != -1 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			nums = append(nums, n)
		} else {
			nums = append(nums, 0)
		}
	}
	return nums
}

// FindMatchingAsset finds the asset matching current OS and Architecture.
func FindMatchingAsset(release *GitHubRelease, goos, goarch string) *GitHubAsset {
	if release == nil || len(release.Assets) == 0 {
		return nil
	}

	var best *GitHubAsset
	osTarget := strings.ToLower(goos)
	archTarget := strings.ToLower(goarch)

	for _, a := range release.Assets {
		name := strings.ToLower(a.Name)
		if strings.Contains(name, osTarget) && strings.Contains(name, archTarget) {
			if osTarget == "windows" && strings.HasSuffix(name, ".exe") {
				return &a
			}
			if (osTarget == "linux" || osTarget == "darwin") && strings.HasSuffix(name, ".tar.gz") && !strings.Contains(name, ".app.") {
				return &a
			}
			if best == nil {
				best = &a
			}
		}
	}

	return best
}

// DownloadAndApplyUpdate downloads the release asset, extracts the binary and replaces execPath.
func DownloadAndApplyUpdate(release *GitHubRelease, execPath string) error {
	asset := FindMatchingAsset(release, runtime.GOOS, runtime.GOARCH)
	if asset == nil {
		return fmt.Errorf("no compatible release asset found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	req, err := http.NewRequestWithContext(context.Background(), "GET", asset.BrowserDownloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Limoni-Voice-AutoUpdater/"+AppVersion)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	var newBinaryData []byte
	assetName := strings.ToLower(asset.Name)

	if strings.HasSuffix(assetName, ".tar.gz") || strings.HasSuffix(assetName, ".tgz") {
		data, err := extractBinaryFromTarGz(resp.Body)
		if err != nil {
			return fmt.Errorf("extraction error: %w", err)
		}
		newBinaryData = data
	} else {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read error: %w", err)
		}
		newBinaryData = data
	}

	if len(newBinaryData) < 100000 { // Executable should be at least ~100KB
		return fmt.Errorf("downloaded binary too small (%d bytes)", len(newBinaryData))
	}

	return replaceExecutable(execPath, newBinaryData)
}

func extractBinaryFromTarGz(r io.Reader) ([]byte, error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			base := filepath.Base(header.Name)
			if strings.HasPrefix(base, "limoni-voice") {
				return io.ReadAll(tr)
			}
		}
	}

	return nil, fmt.Errorf("executable not found in archive")
}

func replaceExecutable(targetPath string, newBytes []byte) error {
	dir := filepath.Dir(targetPath)
	tmpFile, err := os.CreateTemp(dir, "limoni-voice-update-*.tmp")
	if err != nil {
		// Fallback to os.TempDir if current dir is not writable
		tmpFile, err = os.CreateTemp(os.TempDir(), "limoni-voice-update-*.tmp")
		if err != nil {
			return err
		}
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	if _, err := tmpFile.Write(newBytes); err != nil {
		tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(0755); err != nil {
		tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	// Rename running executable to .old
	oldPath := targetPath + ".old"
	_ = os.Remove(oldPath)

	if err := os.Rename(targetPath, oldPath); err != nil {
		// If rename fails (e.g. permissions), attempt direct overwrite
		if errWrite := os.WriteFile(targetPath, newBytes, 0755); errWrite != nil {
			return fmt.Errorf("failed to replace executable: %w (direct write: %v)", err, errWrite)
		}
		return nil
	}

	if err := os.Rename(tmpName, targetPath); err != nil {
		// Rollback if temp rename failed
		_ = os.Rename(oldPath, targetPath)
		return fmt.Errorf("failed to install new executable: %w", err)
	}

	// Clean up old backup file
	_ = os.Remove(oldPath)
	return nil
}
