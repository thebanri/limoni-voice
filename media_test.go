package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/p2p"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
)

func TestMediaLabel(t *testing.T) {
	for name, want := range map[string]string{
		"foto.PNG":  "/image foto.PNG",
		"ses.mp3":   "/sound ses.mp3",
		"notes.txt": "",
	} {
		if got := mediaLabel(name); got != want {
			t.Errorf("mediaLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFitImageCells(t *testing.T) {
	if c, r := fitImageCells(200, 100, 100, 40); c != 100 || r != 25 {
		t.Errorf("wide picture: %dx%d", c, r)
	}
	if c, r := fitImageCells(100, 200, 100, 40); c != 40 || r != 40 {
		t.Errorf("tall picture: %dx%d", c, r)
	}
}

// A picture that comes in shows as /image in the chat, without the accept dialog; a click
// on it opens its window, which shows it and saves it on Download.
func TestIncomingPictureOpensFromTheChat(t *testing.T) {
	var pic bytes.Buffer
	if err := png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 4, 2))); err != nil {
		t.Fatal(err)
	}
	a := &App{room: NewRoomView(), currentScreen: ScreenRoom, notifier: newDesktopNotifier()}
	a.room.OnOpenMedia = a.openMedia
	offer := &p2p.FileOffer{TransferID: "t1", SenderNick: "Ayşe", FileName: "foto.png", FileSize: int64(pic.Len()), Data: pic.Bytes()}
	a.enqueueFileOffer(offer)
	if a.activeFileOffer() != nil {
		t.Fatal("a picture asked to be accepted")
	}

	audio := engine.NewAudioEngine()
	buf := buffer.NewBuffer(cell.NewRect(0, 0, 100, 40))
	a.room.Render(terminal.NewFrame(buf, terminal.NewFocusManager()), cell.NewRect(0, 0, 100, 40), p2p.NewP2PNode("me", "You", audio), audio)
	a.room.mu.Lock()
	rows := a.room.renderedLines
	a.room.mu.Unlock()
	clicked := false
	for _, rl := range rows {
		if rl.ClickURL == mediaLinkPrefix+"t1" {
			clicked = a.room.HandleChatClick(rl.StartX+20, rl.RowY)
			break
		}
	}
	if !clicked || a.activeMedia() != offer {
		t.Fatal("a click on /image did not open the picture")
	}
	if img, _, _ := loadMedia(offer); img == nil || img.Bounds().Dx() != 4 {
		t.Fatal("the picture was not decoded")
	}

	saved := filepath.Join(p2p.GetLimoniTransfersDir(), "foto.png")
	os.Remove(saved)
	defer os.Remove(saved)
	a.downloadMedia()
	if got, err := os.ReadFile(saved); err != nil || !bytes.Equal(got, pic.Bytes()) {
		t.Fatalf("download did not save the picture: %v", err)
	}
}

// A picture shows a preview under its message once decoded, in rows that open it on a
// click; one scrolled half out of view shows the rows in view.
func TestPicturePreviewShowsUnderItsMessage(t *testing.T) {
	var pic bytes.Buffer
	if err := png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 400, 200))); err != nil {
		t.Fatal(err)
	}
	a := &App{room: NewRoomView(), currentScreen: ScreenRoom, notifier: newDesktopNotifier()}
	a.enqueueFileOffer(&p2p.FileOffer{TransferID: "t2", SenderNick: "Ayşe", FileName: "foto.png", FileSize: int64(pic.Len()), Data: pic.Bytes()})
	var preview image.Image
	for deadline := time.Now().Add(5 * time.Second); preview == nil && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		a.room.mu.Lock()
		preview = a.room.Messages[len(a.room.Messages)-1].Preview
		a.room.mu.Unlock()
	}
	if preview == nil {
		t.Fatal("no preview came")
	}

	lines := a.room.buildDisplayLines(a.room.Messages, 80)
	var rows []roomDisplayLine
	for _, l := range lines {
		if l.Preview != nil {
			rows = append(rows, l)
		}
	}
	if len(rows) != 8 || rows[0].Preview.cols != 32 || rows[7].PreviewRow != 7 {
		t.Fatalf("%d preview rows, want 8 of 32 columns", len(rows))
	}
	if s := rows[0].Preview.slice(3, 8); s.Bounds().Dy() != preview.Bounds().Dy()*5/8 {
		t.Fatalf("rows 3 to 8 are %v of a picture %v", s.Bounds(), preview.Bounds())
	}
	if rows[0].Preview.slice(3, 8) != rows[0].Preview.slice(3, 8) {
		t.Fatal("the same part is a new picture each time")
	}

	audio := engine.NewAudioEngine()
	buf := buffer.NewBuffer(cell.NewRect(0, 0, 100, 40))
	a.room.Render(terminal.NewFrame(buf, terminal.NewFocusManager()), cell.NewRect(0, 0, 100, 40), p2p.NewP2PNode("me", "You", audio), audio)
	a.room.mu.Lock()
	n := 0
	for _, rl := range a.room.renderedLines {
		if rl.ClickURL == mediaLinkPrefix+"t2" {
			n++
		}
	}
	a.room.mu.Unlock()
	if n != 9 {
		t.Fatalf("%d rows open the picture, want its line and 8 of preview", n)
	}
}
