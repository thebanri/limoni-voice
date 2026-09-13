package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		latest   string
		current  string
		expected bool
	}{
		{"v1.4.2", "v1.4.1", true},
		{"v1.5.0", "v1.4.1", true},
		{"v2.0.0", "v1.99.99", true},
		{"v1.4.1", "v1.4.1", false},
		{"v1.4.0", "v1.4.1", false},
		{"v1.3.9", "v1.4.1", false},
		{"1.4.2", "v1.4.1", true},
		{"v1.4.2-beta.1", "v1.4.1", true},
		{"v1.4.1", "v1.4.2", false},
	}

	for _, tt := range tests {
		got := IsNewerVersion(tt.latest, tt.current)
		if got != tt.expected {
			t.Errorf("IsNewerVersion(%q, %q) = %v; expected %v", tt.latest, tt.current, got, tt.expected)
		}
	}
}

func TestFindMatchingAsset(t *testing.T) {
	rel := &GitHubRelease{
		TagName: "v1.0.0",
		Assets: []GitHubAsset{
			{Name: "Limoni-Voice-Setup_windows_amd64.exe", BrowserDownloadURL: "https://example.com/Limoni-Voice-Setup_windows_amd64.exe"},
			{Name: "Limoni-Voice-Setup.exe", BrowserDownloadURL: "https://example.com/Limoni-Voice-Setup.exe"},
			{Name: "Limoni-Voice-Setup_windows_arm64.exe", BrowserDownloadURL: "https://example.com/Limoni-Voice-Setup_windows_arm64.exe"},
			{Name: "limoni-voice_v1.0.0_windows_amd64.exe", BrowserDownloadURL: "https://example.com/limoni-voice_v1.0.0_windows_amd64.exe"},
			{Name: "limoni-voice_v1.0.0_windows_arm64.exe", BrowserDownloadURL: "https://example.com/limoni-voice_v1.0.0_windows_arm64.exe"},
			{Name: "limoni-voice_v1.0.0_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux_amd64.tar.gz"},
			{Name: "limoni-voice_v1.0.0_linux_arm64.tar.gz", BrowserDownloadURL: "https://example.com/linux_arm64.tar.gz"},
			{Name: "limoni-voice_1.0.0_amd64.deb", BrowserDownloadURL: "https://example.com/linux_amd64.deb"},
			{Name: "Limoni-Voice_v1.0.0_macOS_arm64.dmg", BrowserDownloadURL: "https://example.com/Limoni-Voice_v1.0.0_macOS_arm64.dmg"},
			{Name: "Limoni-Voice_v1.0.0_macOS_arm64.app.zip", BrowserDownloadURL: "https://example.com/Limoni-Voice_v1.0.0_macOS_arm64.app.zip"},
			{Name: "limoni-voice_v1.0.0_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin_arm64.tar.gz"},
		},
	}

	winAmd64 := FindMatchingAsset(rel, "windows", "amd64")
	if winAmd64 == nil || winAmd64.Name != "limoni-voice_v1.0.0_windows_amd64.exe" {
		t.Fatalf("Expected windows amd64 standalone binary, got %v", winAmd64)
	}

	winArm64 := FindMatchingAsset(rel, "windows", "arm64")
	if winArm64 == nil || winArm64.Name != "limoni-voice_v1.0.0_windows_arm64.exe" {
		t.Fatalf("Expected windows arm64 standalone binary, got %v", winArm64)
	}

	linuxAmd64 := FindMatchingAsset(rel, "linux", "amd64")
	if linuxAmd64 == nil || linuxAmd64.Name != "limoni-voice_v1.0.0_linux_amd64.tar.gz" {
		t.Fatalf("Expected linux amd64 tarball, got %v", linuxAmd64)
	}

	darwinArm64 := FindMatchingAsset(rel, "darwin", "arm64")
	if darwinArm64 == nil || darwinArm64.Name != "limoni-voice_v1.0.0_darwin_arm64.tar.gz" {
		t.Fatalf("Expected darwin arm64 tarball, got %v", darwinArm64)
	}
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	payload := []byte("FAKE_LIMONI_VOICE_BINARY_DATA")
	header := &tar.Header{
		Name: "limoni-voice",
		Mode: 0755,
		Size: int64(len(payload)),
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("WriteHeader failed: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	tw.Close()
	gzw.Close()

	extracted, err := extractBinaryFromTarGz(&buf)
	if err != nil {
		t.Fatalf("extractBinaryFromTarGz failed: %v", err)
	}

	if string(extracted) != string(payload) {
		t.Fatalf("Expected payload %q, got %q", payload, extracted)
	}
}

func TestReplaceExecutable(t *testing.T) {
	tmpDir := t.TempDir()
	origPath := filepath.Join(tmpDir, "limoni-voice-test-bin")

	origContent := []byte("ORIGINAL_BINARY_BYTES")
	if err := os.WriteFile(origPath, origContent, 0755); err != nil {
		t.Fatalf("Failed to create mock executable: %v", err)
	}

	newContent := []byte("NEW_UPDATED_BINARY_BYTES")
	if err := replaceExecutable(origPath, newContent); err != nil {
		t.Fatalf("replaceExecutable failed: %v", err)
	}

	readBack, err := os.ReadFile(origPath)
	if err != nil {
		t.Fatalf("Failed to read replaced executable: %v", err)
	}

	if string(readBack) != string(newContent) {
		t.Fatalf("Replaced content mismatch: expected %q, got %q", newContent, readBack)
	}
}

func TestUpdateChecksumVerification(t *testing.T) {
	asset := []byte("limoni-voice release payload")
	sum := sha256.Sum256(asset)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  limoni-voice_v9.9.9_linux_amd64.tar.gz\n" +
		strings.Repeat("0", 64) + "  checksums-other.bin\n")

	expected, err := ExpectedChecksum(checksums, "limoni-voice_v9.9.9_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("ExpectedChecksum failed: %v", err)
	}
	if err := VerifyAssetChecksum(asset, expected); err != nil {
		t.Fatalf("valid asset rejected: %v", err)
	}
	tampered := append([]byte{}, asset...)
	tampered[0] ^= 0xff
	if err := VerifyAssetChecksum(tampered, expected); err == nil {
		t.Fatal("tampered asset accepted")
	}
	if _, err := ExpectedChecksum(checksums, "missing.tar.gz"); err == nil {
		t.Fatal("missing checksum entry must refuse the update")
	}
}

func TestUpdateSignatureVerification(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	checksums := []byte("abc  file\n")
	sig := ed25519.Sign(priv, checksums)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	if err := VerifyChecksumsSignature(pubB64, checksums, sig); err != nil {
		t.Fatalf("raw signature rejected: %v", err)
	}
	if err := VerifyChecksumsSignature(pubB64, checksums, []byte(base64.StdEncoding.EncodeToString(sig))); err != nil {
		t.Fatalf("base64 signature rejected: %v", err)
	}
	if err := VerifyChecksumsSignature(pubB64, []byte("abc  evil\n"), sig); err == nil {
		t.Fatal("signature over different content accepted")
	}
}
