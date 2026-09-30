package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallArchive(t *testing.T) {
	archive := testZip(t, map[string]string{
		"ffmpeg-7.1-essentials_build/bin/ffmpeg.exe":  "FFMPEG",
		"ffmpeg-7.1-essentials_build/bin/ffplay.exe":  "FFPLAY",
		"ffmpeg-7.1-essentials_build/bin/ffprobe.exe": "FFPROBE",
	})
	sum := sha256.Sum256(archive)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(archive) }))
	defer srv.Close()

	dir := t.TempDir()
	a := toolArchive{url: srv.URL, sha256: hex.EncodeToString(sum[:]), files: []string{"ffmpeg.exe", "ffplay.exe"}}
	if err := installArchive("FFmpeg", a, dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"ffmpeg.exe": "FFMPEG", "ffplay.exe": "FFPLAY"} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ffprobe.exe")); err == nil {
		t.Error("ffprobe.exe extracted although not asked for")
	}

	// A download that does not match the pinned hash installs nothing.
	bad := t.TempDir()
	a.sha256 = "00" + a.sha256[2:]
	if err := installArchive("FFmpeg", a, bad); err == nil {
		t.Fatal("corrupted download accepted")
	}
	if entries, _ := os.ReadDir(bad); len(entries) != 0 {
		t.Errorf("files left after a failed install: %v", entries)
	}

	// A file missing from the archive is an error, not a silent skip.
	if err := extractFromZip(filepath.Join(t.TempDir(), "none.zip"), dir, []string{"mpv.exe"}); err == nil {
		t.Error("missing archive accepted")
	}
}

func TestToolArchivesPerArch(t *testing.T) {
	for _, tl := range tools {
		for _, arch := range []string{"amd64", "arm64"} {
			a, ok := tl.archive(arch)
			if !ok || a.url == "" || len(a.sha256) != 64 || len(a.files) == 0 {
				t.Errorf("%s has no usable archive for %s: %+v", tl.label, arch, a)
			}
		}
		if _, ok := tl.archive("386"); ok {
			t.Errorf("%s offers a 64-bit archive to 32-bit Windows", tl.label)
		}
	}
}
