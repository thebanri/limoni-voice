package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
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
		TagName: "v1.4.2",
		Assets: []GitHubAsset{
			{Name: "limoni-voice_v1.4.2_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux_amd64.tar.gz"},
			{Name: "limoni-voice_v1.4.2_linux_arm64.tar.gz", BrowserDownloadURL: "https://example.com/linux_arm64.tar.gz"},
			{Name: "limoni-voice_v1.4.2_windows_amd64.exe", BrowserDownloadURL: "https://example.com/windows_amd64.exe"},
			{Name: "limoni-voice_v1.4.2_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin_arm64.tar.gz"},
		},
	}

	linuxAmd64 := FindMatchingAsset(rel, "linux", "amd64")
	if linuxAmd64 == nil || linuxAmd64.Name != "limoni-voice_v1.4.2_linux_amd64.tar.gz" {
		t.Fatalf("Expected linux amd64 asset, got %v", linuxAmd64)
	}

	winAmd64 := FindMatchingAsset(rel, "windows", "amd64")
	if winAmd64 == nil || winAmd64.Name != "limoni-voice_v1.4.2_windows_amd64.exe" {
		t.Fatalf("Expected windows amd64 asset, got %v", winAmd64)
	}

	darwinArm64 := FindMatchingAsset(rel, "darwin", "arm64")
	if darwinArm64 == nil || darwinArm64.Name != "limoni-voice_v1.4.2_darwin_arm64.tar.gz" {
		t.Fatalf("Expected darwin arm64 asset, got %v", darwinArm64)
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
