package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

var (
	// AppVersion is dynamically injected during compilation via -ldflags="-X main.AppVersion=vX.Y.Z".
	// Falls back to runtime build info or "dev" when built without flags.
	AppVersion = "dev"
	GitHubRepo = "thebanri/limoni-voice"
	// UpdateSigningPublicKey is an optional base64 ed25519 public key injected at build time
	// (-ldflags="-X main.UpdateSigningPublicKey=..."). When set, checksums.txt must carry a valid signature.
	UpdateSigningPublicKey = ""
	UpdateCheckDelay       = 1200 * time.Millisecond
)

func init() {
	if AppVersion == "dev" || AppVersion == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			if info.Main.Version != "" && info.Main.Version != "(devel)" {
				AppVersion = info.Main.Version
			}
		}
	}
}

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

	osTarget := strings.ToLower(goos)
	archTarget := strings.ToLower(goarch)

	for _, a := range release.Assets {
		name := strings.ToLower(a.Name)

		// Explicitly skip installers, setups, disk images, app bundles, package managers (.deb, .rpm, .msi)
		if strings.Contains(name, "setup") ||
			strings.Contains(name, "installer") ||
			strings.Contains(name, ".dmg") ||
			strings.Contains(name, ".app.") ||
			strings.Contains(name, ".deb") ||
			strings.Contains(name, ".rpm") ||
			strings.Contains(name, ".msi") {
			continue
		}

		if strings.Contains(name, osTarget) && strings.Contains(name, archTarget) {
			if osTarget == "windows" && strings.HasSuffix(name, ".exe") {
				return &a
			}
			if (osTarget == "linux" || osTarget == "darwin") && (strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz")) {
				return &a
			}
		}
	}

	return nil
}

// DownloadAndApplyUpdate downloads the release asset, verifies it against the release's
// checksums.txt (and its ed25519 signature when a signing key is embedded), extracts the
// binary and replaces execPath. Unverifiable updates are refused.
func DownloadAndApplyUpdate(release *GitHubRelease, execPath string) error {
	asset := FindMatchingAsset(release, runtime.GOOS, runtime.GOARCH)
	if asset == nil {
		return fmt.Errorf("no compatible release asset found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	checksumAsset := findAssetByName(release, checksumsAssetName)
	if checksumAsset == nil {
		return errors.New("release has no checksums.txt; refusing unverifiable update")
	}
	checksums, err := downloadAsset(checksumAsset.BrowserDownloadURL, maxChecksumsSize)
	if err != nil {
		return fmt.Errorf("checksums download failed: %w", err)
	}

	if pub := strings.TrimSpace(UpdateSigningPublicKey); pub != "" {
		sigAsset := findAssetByName(release, checksumsAssetName+".sig")
		if sigAsset == nil {
			return errors.New("release is not signed (checksums.txt.sig missing); refusing update")
		}
		sig, err := downloadAsset(sigAsset.BrowserDownloadURL, 4096)
		if err != nil {
			return fmt.Errorf("signature download failed: %w", err)
		}
		if err := VerifyChecksumsSignature(pub, checksums, sig); err != nil {
			return err
		}
	}

	expected, err := ExpectedChecksum(checksums, asset.Name)
	if err != nil {
		return err
	}

	assetData, err := downloadAsset(asset.BrowserDownloadURL, maxUpdateSize)
	if err != nil {
		return err
	}
	if err := VerifyAssetChecksum(assetData, expected); err != nil {
		return fmt.Errorf("%s: %w", asset.Name, err)
	}

	newBinaryData := assetData
	assetName := strings.ToLower(asset.Name)
	if strings.HasSuffix(assetName, ".tar.gz") || strings.HasSuffix(assetName, ".tgz") {
		data, err := extractBinaryFromTarGz(bytes.NewReader(assetData))
		if err != nil {
			return fmt.Errorf("extraction error: %w", err)
		}
		newBinaryData = data
	}

	if len(newBinaryData) < 100000 { // Executable should be at least ~100KB
		return fmt.Errorf("downloaded binary too small (%d bytes)", len(newBinaryData))
	}

	return replaceExecutable(execPath, newBinaryData)
}

const (
	checksumsAssetName = "checksums.txt"
	maxChecksumsSize   = 1 << 20
	maxUpdateSize      = 150 * 1024 * 1024 // 150 MB safety limit against memory exhaustion DoS
)

func findAssetByName(release *GitHubRelease, name string) *GitHubAsset {
	if release == nil {
		return nil
	}
	for i := range release.Assets {
		if release.Assets[i].Name == name {
			return &release.Assets[i]
		}
	}
	return nil
}

func downloadAsset(assetURL string, limit int64) ([]byte, error) {
	client := &http.Client{
		Timeout: 60 * time.Second,
	}
	req, err := http.NewRequestWithContext(context.Background(), "GET", assetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Limoni-Voice-AutoUpdater/"+AppVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return data, nil
}

// ExpectedChecksum finds the SHA-256 hex digest for assetName in sha256sum-formatted content.
func ExpectedChecksum(checksums []byte, assetName string) (string, error) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != assetName {
			continue
		}
		sum := strings.ToLower(fields[0])
		if decoded, err := hex.DecodeString(sum); err != nil || len(decoded) != sha256.Size {
			return "", fmt.Errorf("malformed checksum entry for %s", assetName)
		}
		return sum, nil
	}
	return "", fmt.Errorf("no checksum listed for %s; refusing update", assetName)
}

// VerifyAssetChecksum compares the SHA-256 digest of data with the expected hex digest.
func VerifyAssetChecksum(data []byte, expectedHex string) error {
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(actual), []byte(strings.ToLower(expectedHex))) != 1 {
		return errors.New("checksum mismatch (corrupted or tampered download)")
	}
	return nil
}

// VerifyChecksumsSignature verifies an ed25519 signature (raw 64 bytes or base64) over checksums.txt.
func VerifyChecksumsSignature(publicKeyB64 string, checksums, sig []byte) error {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid embedded update signing key")
	}
	if len(sig) != ed25519.SignatureSize {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
		if err != nil {
			return errors.New("malformed update signature")
		}
		sig = decoded
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(pub), checksums, sig) {
		return errors.New("update signature verification failed; refusing update")
	}
	return nil
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
				return io.ReadAll(io.LimitReader(tr, 150*1024*1024))
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
