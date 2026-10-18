// Pictures and sounds sent in the chat: shown as a /image or /sound message, opened in a
// window that shows the picture or plays the sound, and saved only when asked.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thebanri/limoni-voice/internal/audioio"
	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/p2p"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
)

var (
	imageFileExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true}
	soundFileExts = map[string]bool{".mp3": true, ".wav": true, ".ogg": true, ".oga": true, ".opus": true, ".flac": true, ".m4a": true, ".aac": true}
)

const (
	maxKeptMedia      = 20                // pictures and sounds kept in memory to be opened
	maxKeptMediaBytes = 200 * 1024 * 1024 // and their size together; the oldest go first
	maxImagePixels    = 50_000_000        // a bigger picture would take gigabytes to decode
	maxSoundSeconds   = 600               // decoded PCM is 96 KB a second
)

// mediaLabel is how a picture or sound file shows in the chat, or "" for any other file.
func mediaLabel(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case imageFileExts[ext]:
		return "/image " + name
	case soundFileExts[ext]:
		return "/sound " + name
	}
	return ""
}

// addMedia keeps a picture or sound file and shows it in the chat; it reports false for
// other files, which go through the accept dialog.
func (a *App) addMedia(m *p2p.FileOffer, isSelf bool) bool {
	label := mediaLabel(m.FileName)
	if label == "" || m.IsCode {
		return false
	}
	a.fileOfferMu.Lock()
	if a.media == nil {
		a.media = map[string]*p2p.FileOffer{}
	}
	a.media[m.TransferID] = m
	a.mediaOrder = append(a.mediaOrder, m.TransferID)
	for len(a.mediaOrder) > 1 && (len(a.mediaOrder) > maxKeptMedia || a.keptMediaBytes() > maxKeptMediaBytes) {
		delete(a.media, a.mediaOrder[0])
		a.mediaOrder = a.mediaOrder[1:]
	}
	a.fileOfferMu.Unlock()
	a.room.AddMediaMessage(m.SenderNick, m.SenderID, label, m.TransferID, isSelf)
	return true
}

func (a *App) keptMediaBytes() int {
	n := 0
	for _, m := range a.media {
		n += len(m.Data)
	}
	return n
}

// addSentMedia shows a picture or sound we sent in our own chat.
func (a *App) addSentMedia(name string, data []byte) {
	a.addMedia(&p2p.FileOffer{
		TransferID: "self-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		FileName:   name,
		FileSize:   int64(len(data)),
		Data:       data,
	}, true)
}

func (a *App) activeMedia() *p2p.FileOffer {
	a.fileOfferMu.Lock()
	defer a.fileOfferMu.Unlock()
	return a.mediaView
}

// openMedia opens the window of a picture or sound from the chat; it is decoded in the
// background, a sound starts playing when it is.
func (a *App) openMedia(id string) {
	a.fileOfferMu.Lock()
	m := a.media[id]
	a.fileOfferMu.Unlock()
	if m == nil {
		a.toast(T("This file is no longer kept; ask for it to be sent again"))
		return
	}
	a.closeMedia()
	a.fileOfferMu.Lock()
	a.mediaView, a.mediaImage, a.mediaNote = m, nil, T("Loading…")
	a.fileOfferMu.Unlock()
	go func() {
		img, pcm, note := loadMedia(m)
		a.fileOfferMu.Lock()
		defer a.fileOfferMu.Unlock()
		if a.mediaView != m {
			return // closed meanwhile
		}
		a.mediaNote = note
		if img != nil {
			a.mediaImage = &widgets.Image{Img: img}
		}
		if pcm != nil {
			a.audio.PlayClip(pcm)
		}
	}()
}

// loadMedia decodes a picture or sound. What cannot be shown or played here (a WebP
// picture, a sound without ffmpeg) is opened in the system's own program instead.
func loadMedia(m *p2p.FileOffer) (image.Image, []byte, string) {
	if imageFileExts[strings.ToLower(filepath.Ext(m.FileName))] {
		img, err := decodeImage(m.Data)
		if err == nil {
			return img, nil, ""
		}
		return nil, nil, openMediaExternally(m, Tf("Could not show it here (%v); opened in the system's viewer", err))
	}
	pcm, err := decodeSound(m.Data)
	if err == nil {
		return nil, pcm, T("▶ Playing")
	}
	return nil, nil, openMediaExternally(m, Tf("Could not play it here (%v); opened in the system's player", err))
}

func decodeImage(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxImagePixels {
		return nil, errors.New(T("picture too large"))
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// decodeSound turns any sound file into the engine's mono 16-bit PCM with ffmpeg.
func decodeSound(data []byte) ([]byte, error) {
	ffmpeg := audioio.FindTool("ffmpeg")
	if ffmpeg == "" {
		return nil, errors.New(T("ffmpeg not found"))
	}
	cmd := exec.Command(ffmpeg, "-v", "error", "-i", "pipe:0", "-t", strconv.Itoa(maxSoundSeconds),
		"-f", "s16le", "-ac", "1", "-ar", strconv.Itoa(engine.AudioSampleRate), "pipe:1")
	cmd.Stdin = bytes.NewReader(data)
	pcm, err := cmd.Output()
	if err == nil && len(pcm) == 0 {
		err = errors.New(T("no sound in the file"))
	}
	return pcm, err
}

// openMediaExternally writes the file to the temp folder and opens it there, returning the
// note the window shows.
// shortcut: the temp copy stays behind until the system clears its temp folder.
func openMediaExternally(m *p2p.FileOffer, note string) string {
	f, err := os.CreateTemp("", "limoni-*"+strings.ToLower(filepath.Ext(m.FileName)))
	if err != nil {
		return Tf("Could not open it: %v", err)
	}
	_, err = f.Write(m.Data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = OpenPath(f.Name())
	}
	if err != nil {
		return Tf("Could not open it: %v", err)
	}
	return note
}

func (a *App) closeMedia() {
	a.fileOfferMu.Lock()
	wasOpen := a.mediaView != nil
	a.mediaView, a.mediaImage, a.mediaNote = nil, nil, ""
	a.fileOfferMu.Unlock()
	a.audio.StopClip()
	if wasOpen {
		a.term.ForceFullRedraw() // takes the picture off the screen
	}
}

func (a *App) downloadMedia() {
	m := a.activeMedia()
	if m == nil {
		return
	}
	path, err := p2p.SaveAcceptedFile(m)
	if err != nil {
		a.toast(fmt.Sprintf("Save error: %v", err))
		return
	}
	a.room.AddLog(Tf("[FILE] Saved '%s' to %s", m.FileName, path))
	a.toast(Tf("✓ Saved: %s", path))
}

func (a *App) handleMediaKey(e driver.KeyEvent) {
	switch {
	case e.Type == driver.KeyEsc || e.Type == driver.KeyRune && (e.Ch == 'q' || e.Ch == 'Q'):
		a.closeMedia()
	case e.Type == driver.KeyEnter || e.Type == driver.KeyRune && (e.Ch == 'd' || e.Ch == 'D'):
		a.downloadMedia()
	}
}

func (a *App) drawMedia(f *terminal.Frame) {
	a.fileOfferMu.Lock()
	m, img, note := a.mediaView, a.mediaImage, a.mediaNote
	a.fileOfferMu.Unlock()
	if m != nil {
		DrawMediaModal(f, f.Area(), m, img, note, a.downloadMedia, a.closeMedia)
	}
}

// fitImageCells is the size in cells a w×h picture takes in at most cols×rows, keeping its
// shape: a cell is about twice as tall as it is wide.
func fitImageCells(w, h, cols, rows int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	c, r := cols, cols*h/(w*2)
	if r > rows {
		c, r = rows*w*2/h, rows
	}
	return max(c, 1), max(r, 1)
}

// DrawMediaModal draws the window of a picture or sound from the chat, with its Download and
// Close buttons.
func DrawMediaModal(frame *terminal.Frame, screen cell.Rect, m *p2p.FileOffer, img *widgets.Image, note string, onDownload, onClose func()) {
	isImage := imageFileExts[strings.ToLower(filepath.Ext(m.FileName))]
	w, h := 64, 8
	if isImage && img != nil {
		w, h = 120, 40
	}
	w, h = min(w, int(screen.Width)-2), min(h, int(screen.Height)-2)
	if w < 20 || h < 5 {
		return
	}
	area := terminal.CenterRect(screen, uint16(w), uint16(h))
	widgets.DrawShadow(frame.Buffer, area, 2, 1)
	openModal(frame, "media_dialog", area, onClose)
	defer frame.EndLayer()

	theme := CurrentTheme()
	bg := theme.SurfaceBg
	buf := frame.Buffer
	for y := area.Y; y < area.Y+area.Height; y++ {
		for x := area.X; x < area.X+area.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: bg}})
		}
	}
	title := T(" 🔊 SOUND ")
	if isImage {
		title = T(" 🖼 IMAGE ")
	}
	block := widgets.Block{
		Title:          title,
		TitleAlignment: widgets.AlignCenter,
		Borders:        widgets.BorderAll,
		BorderSymbols:  widgets.SymbolsRounded,
		BorderStyle:    cell.Style{Fg: theme.BorderFocused, Modifier: cell.ModifierBold},
		Style:          cell.Style{Bg: bg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)
	textW := int(inner.Width) - 2

	info := fmt.Sprintf("%s  (%s)", m.FileName, formatBytes(m.FileSize))
	if m.SenderNick != "" {
		info = Tf("From: %s", m.SenderNick) + "  ·  " + info
	}
	buf.SetString(inner.X+1, inner.Y, clipToWidth(info, textW), cell.Style{Fg: theme.Text, Bg: bg, Modifier: cell.ModifierBold})

	btnRow := inner.Y + inner.Height - 1
	if img != nil {
		b := img.Img.Bounds()
		boxW, boxH := textW, int(btnRow-inner.Y)-2
		c, r := fitImageCells(b.Dx(), b.Dy(), boxW, boxH)
		x := inner.X + 1 + uint16((boxW-c)/2)
		y := inner.Y + 1 + uint16((boxH-r)/2)
		frame.RenderWidget(img, cell.NewRect(x, y, uint16(c), uint16(r)))
	} else if note != "" {
		buf.SetString(inner.X+1, inner.Y+2, clipToWidth(note, textW), cell.Style{Fg: theme.Secondary, Bg: bg})
	}

	btnX := inner.X + 1
	for _, btn := range []struct {
		label string
		color cell.Color
		do    func()
	}{{T(" [D] Download "), theme.Success, onDownload}, {T(" [Esc] Close "), theme.Danger, onClose}} {
		bw := uint16(cell.StringWidth(btn.label))
		if btnX+bw > inner.X+inner.Width {
			break
		}
		buf.SetString(btnX, btnRow, btn.label, cell.Style{Fg: cell.NewColorRGB(0, 0, 0), Bg: btn.color, Modifier: cell.ModifierBold})
		do := btn.do
		clickable(frame, cell.NewRect(btnX, btnRow, bw, 1), func(_ driver.MouseEvent) { do() })
		btnX += bw + 2
	}
}
