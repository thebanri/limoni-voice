package main

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// What terminals paste when files are dragged onto them is sent as those files; text, folders
// and paths that do not exist stay a paste.
func TestDroppedFiles(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "shot.png")
	spaced := filepath.Join(dir, "ekran görüntüsü 1.png")
	for _, p := range []string{plain, spaced} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fileURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(spaced)}).String()
	cases := []struct {
		name   string
		pasted string
		want   []string
	}{
		{"plain path", plain, []string{plain}},
		{"path with spaces", spaced, []string{spaced}},
		{"quoted (GNOME, Windows Terminal)", "'" + spaced + "' ", []string{spaced}},
		{"file URL (kitty, Konsole)", fileURL + "\r\n", []string{spaced}},
		{"two on lines", plain + "\n" + fileURL, []string{plain, spaced}},
		{"two quoted on a line", `"` + plain + `" "` + spaced + `"`, []string{plain, spaced}},
		{"text", "merhaba dünya", nil},
		{"a path and text", plain + " ve sonra", nil},
		{"a folder", dir, nil},
		{"missing file", filepath.Join(dir, "yok.png"), nil},
	}
	if runtime.GOOS != "windows" {
		escaped := filepath.Join(dir, `ekran\ görüntüsü\ 1.png`)
		cases = append(cases,
			struct {
				name   string
				pasted string
				want   []string
			}{"escaped spaces (Alacritty, macOS)", escaped, []string{spaced}},
			struct {
				name   string
				pasted string
				want   []string
			}{"two escaped on a line", plain + " " + escaped, []string{plain, spaced}})
	}
	for _, c := range cases {
		if got := droppedFiles(c.pasted); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q gave %q, want %q", c.name, c.pasted, got, c.want)
		}
	}
}

// A file dropped onto the room is sent; a pasted sentence is typed into the chat.
func TestDropOntoRoomSendsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &App{room: NewRoomView(), currentScreen: ScreenRoom, appStartTime: time.Now().Add(-time.Minute)}
	var sent []string
	a.room.OnSendFile = func(p string) { sent = append(sent, p) }

	a.handlePaste("'" + path + "'")
	if !slices.Equal(sent, []string{path}) || a.room.ChatInputState.Value() != "" {
		t.Fatalf("sent %q, typed %q", sent, a.room.ChatInputState.Value())
	}
	a.handlePaste("selam")
	if len(sent) != 1 || a.room.ChatInputState.Value() != "selam" {
		t.Fatalf("text paste: sent %q, typed %q", sent, a.room.ChatInputState.Value())
	}
}

// Files copied in a file manager arrive as a text/uri-list; /file sends the ones that exist.
func TestFilesFromURIList(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "rapor 1.pdf")
	b := filepath.Join(dir, "foto.png")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	list := "# copied by Dolphin\r\n" +
		(&url.URL{Scheme: "file", Path: filepath.ToSlash(a)}).String() + "\r\n" +
		(&url.URL{Scheme: "file", Path: filepath.ToSlash(b)}).String() + "\r\n" +
		(&url.URL{Scheme: "file", Path: filepath.ToSlash(dir)}).String() + "\r\n" + // a folder
		"https://example.com/x.png\r\n"
	if got := filesFromURIList(list); !slices.Equal(got, []string{a, b}) {
		t.Fatalf("got %q", got)
	}
	if got := filesFromURIList(""); got != nil {
		t.Fatalf("empty list gave %q", got)
	}
}
