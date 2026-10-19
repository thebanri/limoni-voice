// Room view: participant cards, the members sidebar and the screen-share stage.

package main

import (
	"fmt"
	"image"
	"math"
	"strings"
	"time"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/p2p"
	"github.com/thebanri/limoni-voice/screenshare"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
)

// minCardHeight is the least height a member card needs to show more than its border.
const minCardHeight = 4

func (r *RoomView) renderClassicGrid(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine, peers []*p2p.PeerInfo) {
	slots := gridSlots(len(peers)+1, node.RoomLimit())
	rows, cols := gridShape(slots)
	if int(area.Height) < rows*minCardHeight {
		r.renderMemberList(frame, area, node, audio, peers)
		return
	}
	for i := range slots {
		slot := gridCell(area, rows, cols, i)
		switch {
		case i == 0:
			r.renderLocalSlot(frame, slot, node, audio)
		case i <= len(peers):
			r.renderPeerSlot(frame, slot, peers[i-1], node, audio, i+1)
		default:
			r.renderEmptySlot(frame, slot, node.RoomCode, i+1)
		}
	}
}

// gridSlots is how many cards the member grid shows: the members and, while the room has
// room, one empty card to invite into; at least four, as the grid always had.
func gridSlots(members, limit int) int {
	slots := members
	if members < limit {
		slots++
	}
	return max(slots, min(4, limit))
}

// gridShape lays slots out two, three or four to a row.
func gridShape(slots int) (rows, cols int) {
	cols = 2
	if slots > 4 {
		cols = 3
	}
	if slots > 9 {
		cols = 4
	}
	return (slots + cols - 1) / cols, cols
}

// gridCell is the area of card i in a rows×cols grid.
func gridCell(area cell.Rect, rows, cols, i int) cell.Rect {
	r, c := i/cols, i%cols
	w, h := int(area.Width), int(area.Height)
	x0, x1 := w*c/cols, w*(c+1)/cols
	y0, y1 := h*r/rows, h*(r+1)/rows
	return cell.NewRect(area.X+uint16(x0), area.Y+uint16(y0), uint16(x1-x0), uint16(y1-y0))
}

func (r *RoomView) renderSidebarMembers(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine, peers []*p2p.PeerInfo) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         T(" VOICE CHANNEL MEMBERS "),
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.BorderFocused},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	// Keep rows laid out for more height inside the border.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer
		_ = buf
		// Calculate height per slot
		totalSlots := len(peers) + 1
		slotHeight := int(inner.Height) / totalSlots
		if slotHeight < 3 {
			slotHeight = 3
		}

		currY := inner.Y

		// 1. Self Slot
		selfCard := cell.Rect{X: inner.X, Y: currY, Width: inner.Width, Height: uint16(slotHeight)}
		r.renderMemberMiniCard(frame, selfCard, node.Nickname+T(" (YOU)"), audio.LocalRMS, audio.IsSpeaking, audio.Muted, audio.Deafened, node.IsSharingScreen, false, 0, false, true)
		currY += uint16(slotHeight)

		// 2. Peers Slots
		for _, peer := range peers {
			if currY+uint16(slotHeight) > inner.Y+inner.Height {
				break
			}
			peerCard := cell.Rect{X: inner.X, Y: currY, Width: inner.Width, Height: uint16(slotHeight)}
			isReconnecting := time.Since(peer.LastSeen) > 8000*time.Millisecond
			isBeingWatched := node.IsWatchingScreen && node.WatchingPeerID == peer.ID
			trans := peerTransport(node, peer)
			r.renderMemberMiniCard(frame, peerCard, peer.Nickname, peer.RMS, peer.Speaking, peer.IsMuted, peer.IsDeafened, peer.IsSharingScreen, isBeingWatched, peer.PingMs, isReconnecting, false, trans)

			if peer.IsSharingScreen {
				targetPeer := peer
				clickable(frame, peerCard, func(_ driver.MouseEvent) {
					port := targetPeer.VideoPort
					if port <= 0 {
						port = 50100
					}
					fps := targetPeer.VideoFPS
					if fps <= 0 {
						fps = 60
					}
					opts := screenshare.DefaultReceiverOptions(fps)
					opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", targetPeer.Nickname, fps)
					r.SetToast(fmt.Sprintf("Starting %s stream (%d FPS)...", targetPeer.Nickname, fps))
					go func() {
						err := node.StartWatchingScreen(targetPeer.ID, port, opts)
						if err != nil {
							r.SetToast(fmt.Sprintf("Error: %v", err))
						} else {
							r.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", targetPeer.Nickname, fps))
						}
					}()
				})
			}
			// Volume below the level meter, or at the end of the status line on a 3-row card.
			// Drawn after the card's watch click so the buttons take the click.
			volY, volX := peerCard.Y+3, peerCard.X+2
			if peerCard.Height < 4 {
				volY = peerCard.Y + 1
				volX = peerCard.X + peerCard.Width - uint16(min(int(peerCard.Width), peerVolumeWidth(peer, audio)+1))
			}
			r.drawPeerVolume(frame, volX, volY, peerCard.X+peerCard.Width, peer, audio)
			currY += uint16(slotHeight)
		}
	})
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalSec := int(d.Seconds())
	hours := totalSec / 3600
	minutes := (totalSec % 3600) / 60
	seconds := totalSec % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func (r *RoomView) renderMemberMiniCard(frame *terminal.Frame, area cell.Rect, name string, rms float64, isSpeaking, isMuted, isDeafened, isSharing, isBeingWatched bool, pingMs int64, isReconnecting bool, isSelf bool, trans ...string) {
	theme := CurrentTheme()
	buf := frame.Buffer

	// Icon & Color
	var icon string
	nameStyle := cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}

	if isSelf {
		nameStyle.Fg = theme.Accent
	}

	if isReconnecting {
		icon = "[!]"
		nameStyle.Fg = theme.Warning
	} else if isBeingWatched {
		icon = "[W]"
		nameStyle.Fg = theme.Secondary
	} else if isSharing {
		icon = "[*]"
		nameStyle.Fg = theme.Accent
	} else if isSpeaking {
		icon = "●"
		nameStyle.Fg = theme.Success
	} else if isDeafened {
		icon = "[D]"
		nameStyle.Fg = theme.Warning
	} else if isMuted {
		icon = "[M]"
		nameStyle.Fg = theme.Danger
	} else {
		icon = "○"
	}

	titleText := fmt.Sprintf("%s %s", icon, name)
	if isBeingWatched {
		titleText += T(" [WATCHING]")
	} else if isSharing {
		titleText += T(" [LIVE]")
	}
	buf.SetString(area.X+1, area.Y, clipToWidth(titleText, int(area.Width)-1), nameStyle)

	// Status Line / Ping
	statusStr := ""
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
	if isReconnecting {
		statusStr = T("[Reconnecting...]")
		statusStyle.Fg = theme.Warning
	} else if isDeafened {
		statusStr = T("[Deafened]")
		statusStyle.Fg = theme.Warning
	} else if isMuted {
		statusStr = T("[Muted]")
		statusStyle.Fg = theme.Danger
	} else if isSpeaking {
		statusStr = T("[Speaking]")
		statusStyle.Fg = theme.Success
	} else {
		statusStr = T("[Connected]")
	}

	if pingMs > 0 {
		tTag := ""
		if len(trans) > 0 && trans[0] != "" {
			tTag = fmt.Sprintf(" (%s)", trans[0])
		}
		statusStr += fmt.Sprintf(" • %dms%s", pingMs, tTag)
	}

	if area.Height >= 2 {
		buf.SetString(area.X+2, area.Y+1, clipToWidth(statusStr, int(area.Width)-2), statusStyle)
	}

	// Audio Level bar at bottom of mini-card
	if area.Height >= 3 && area.Width > 6 {
		meterRect := cell.Rect{X: area.X + 2, Y: area.Y + 2, Width: area.Width - 4, Height: 1}
		DrawHorizontalLevelMeter(buf, meterRect, rms, isSpeaking, isMuted)
	}
}

// drawOwnShareControls draws, from row y, a line on your own broadcast with its stop and
// preview buttons, for when you share while watching someone else. It returns the row after.
func (r *RoomView) drawOwnShareControls(frame *terminal.Frame, inner cell.Rect, y uint16, node *p2p.P2PNode) uint16 {
	theme := CurrentTheme()
	buf := frame.Buffer
	maxW := int(inner.Width) - 4
	fps := node.ActiveScreenShareFPS
	if fps <= 0 {
		fps = 60
	}
	stats := node.ScreenStats()
	status := Tf("YOUR SCREEN IS LIVE (%d FPS)", fps)
	if stats.Watchers > 0 {
		status += " · " + Tf("%d viewer(s) · %s · %.1f Mbps (adapts to their connection)", stats.Watchers, stats.Preset, float64(stats.Kbps)/1000)
	}
	buf.SetString(inner.X+3, y, clipToWidth(status, maxW), cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})

	stopText := T("   [V] STOP BROADCAST (Click)   ")
	previewText := T("   [O] PREVIEW MY SCREEN (Click)   ")
	if node.IsPreviewingScreen() {
		previewText = T("   [O] CLOSE PREVIEW (Click)   ")
	}
	stopX, rowY := inner.X+3, y+1
	buf.SetString(stopX, rowY, clipToWidth(stopText, maxW), cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Danger, Modifier: cell.ModifierBold})
	clickable(frame, cell.NewRect(stopX, rowY, uint16(len([]rune(stopText))), 1), func(_ driver.MouseEvent) {
		_ = node.StopScreenShare()
		r.SetToast("Screen share stopped")
	})
	// The preview button beside the stop button, or under it when they do not fit.
	previewX, previewY := stopX+uint16(cell.StringWidth(stopText))+2, rowY
	if int(previewX)+cell.StringWidth(previewText) > int(inner.X+inner.Width) {
		previewX, previewY = stopX, rowY+1
	}
	buf.SetString(previewX, previewY, clipToWidth(previewText, int(inner.X+inner.Width)-int(previewX)-1), cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold})
	clickable(frame, cell.NewRect(previewX, previewY, uint16(len([]rune(previewText))), 1), func(_ driver.MouseEvent) {
		go toggleScreenPreview(node, r)
	})
	return previewY + 2
}

func (r *RoomView) renderStreamStage(frame *terminal.Frame, area cell.Rect, streamingPeers []*p2p.PeerInfo, node *p2p.P2PNode, audio *engine.AudioEngine) {
	theme := CurrentTheme()
	stageTitle := T(" LIVE STREAM STAGE ")
	borderCol := theme.Accent
	if !node.IsWatchingScreen && !node.IsSharingScreen {
		borderCol = theme.BorderFocused
	}

	block := widgets.Block{
		Title:         stageTitle,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: borderCol, Modifier: cell.ModifierBold},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)
	buf := frame.Buffer

	r.LastStageArea = inner

	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	// Every live share is a tile on the stage, our own first, under the controls of what we
	// watch or share.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer // drawClipped's own while it runs
		maxW := int(inner.Width) - 4
		nextY := inner.Y + 1
		if node.IsWatchingScreen {
			watchedNick := node.WatchingPeerNick
			if watchedNick == "" {
				if p := node.GetPeer(node.WatchingPeerID); p != nil {
					watchedNick = p.Nickname
				} else {
					watchedNick = T("Stream")
				}
			}
			streamFPS := 60
			if p := node.GetPeer(node.WatchingPeerID); p != nil && p.VideoFPS > 0 {
				streamFPS = p.VideoFPS
			}
			topBarText := Tf(" %s'S LIVE STREAM ACTIVE (%d FPS) ", strings.ToUpper(watchedNick), streamFPS)
			buf.SetString(inner.X+3, nextY, clipToWidth(topBarText, maxW), cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})

			btnText := T("   [W] STOP WATCHING (Click)   ")
			btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Danger, Modifier: cell.ModifierBold}
			buf.SetString(inner.X+3, nextY+1, clipToWidth(btnText, maxW), btnStyle)
			clickable(frame, cell.NewRect(inner.X+3, nextY+1, uint16(len([]rune(btnText))), 1), func(_ driver.MouseEvent) {
				_ = node.StopWatchingScreen()
				r.SetToast("Screen viewer closed")
			})
			// The stream's own sound, next to the stop button, or below it when narrow.
			volX, volY := inner.X+3+uint16(cell.StringWidth(btnText))+2, nextY+1
			if int(volX)+streamVolumeWidth() > int(inner.X+inner.Width) {
				volX, volY = inner.X+3, nextY+2
			}
			r.drawStreamVolume(frame, volX, volY, inner.X+inner.Width, audio, node.WatchingPeerID, watchedNick)
			nextY = volY + 2
		}
		if node.IsSharingScreen {
			nextY = r.drawOwnShareControls(frame, inner, nextY, node)
		}
		if !node.IsWatchingScreen && !node.IsSharingScreen {
			msg := Tf("%d member(s) live · click a stream to watch", len(streamingPeers))
			buf.SetString(inner.X+3, nextY, clipToWidth(msg, maxW), cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
			nextY += 2
		}

		var tiles []streamTile
		if node.IsSharingScreen {
			tiles = append(tiles, streamTile{id: node.LocalID, nick: T("You"), fps: node.ActiveScreenShareFPS, preview: node.OwnScreenPreview(), self: true})
		}
		for _, p := range streamingPeers {
			tiles = append(tiles, streamTile{id: p.ID, nick: p.Nickname, fps: p.VideoFPS, preview: p.ScreenPreview, peer: p,
				watching: node.IsWatchingScreen && node.WatchingPeerID == p.ID})
		}
		if bottom := inner.Y + inner.Height; nextY < bottom {
			r.drawStreamTiles(frame, cell.NewRect(inner.X+1, nextY, inner.Width-2, bottom-nextY), tiles, node)
		}
	})
}

// streamTile is one live share on the stage.
type streamTile struct {
	id, nick string
	fps      int
	preview  image.Image
	self     bool
	watching bool // open in our viewer
	peer     *p2p.PeerInfo
}

// streamAspect is the shape a tile is laid out for: a 16:9 picture in cells half as wide as
// they are tall.
const streamAspect = 16.0 / 9 * 2

// drawStreamTiles lays the tiles out in area, one to three to a row, each as large as fits.
func (r *RoomView) drawStreamTiles(frame *terminal.Frame, area cell.Rect, tiles []streamTile, node *p2p.P2PNode) {
	if len(tiles) == 0 || area.Width < 12 || area.Height < 5 {
		return
	}
	cols := min(len(tiles), 2)
	if len(tiles) > 4 {
		cols = 3
	}
	rows := (len(tiles) + cols - 1) / cols
	for i, t := range tiles {
		slot := gridCell(area, rows, cols, i)
		// The picture's shape, plus its border and its label row, centred in the slot.
		w, h := int(slot.Width), int(slot.Height)
		if fitW := int(float64(h-3)*streamAspect) + 2; fitW < w {
			w = max(fitW, 12)
		} else {
			h = min(h, int(float64(w-2)/streamAspect)+3)
		}
		tile := cell.NewRect(slot.X+uint16((int(slot.Width)-w)/2), slot.Y+uint16((int(slot.Height)-h)/2), uint16(w), uint16(h))
		r.drawStreamTile(frame, tile, t, node)
	}
}

// drawStreamTile draws a share in a live border: its preview, or a note while none came, and
// a label saying what a click does — watch it, stop watching, or preview our own.
func (r *RoomView) drawStreamTile(frame *terminal.Frame, area cell.Rect, t streamTile, node *p2p.P2PNode) {
	theme := CurrentTheme()
	if r.stageTiles == nil {
		r.stageTiles = map[string]*stageTile{}
	}
	st := r.stageTiles[t.id]
	if st == nil {
		st = &stageTile{}
		r.stageTiles[t.id] = st
	}
	px, py := r.pointerAt()
	hovered := area.Contains(px, py)
	var shown image.Image
	if t.preview != nil {
		shown, _ = st.fx.step(t.preview, hovered)
	} else {
		st.fx.step(nil, hovered)
	}
	borderCol := mixColor(theme.Danger, theme.Accent, st.fx.hover)
	if t.watching {
		borderCol = theme.Accent
	}
	block := widgets.Block{
		Title:         clipToWidth(Tf(" 🔴 LIVE · %s ", t.nick), int(area.Width)-4),
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: borderCol, Modifier: cell.ModifierBold},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)
	if inner.Width < 4 || inner.Height < 2 {
		return
	}
	buf := frame.Buffer
	pic := cell.NewRect(inner.X, inner.Y, inner.Width, inner.Height-1)
	if shown != nil {
		b := shown.Bounds()
		c, rr := fitImageCells(b.Dx(), b.Dy(), int(pic.Width), int(pic.Height))
		w := &st.img
		w.Img, w.Background, w.OpaqueBackground = shown, theme.SurfaceBg, true
		frame.RenderWidget(w, cell.NewRect(pic.X+uint16((int(pic.Width)-c)/2), pic.Y+uint16((int(pic.Height)-rr)/2), uint16(c), uint16(rr)))
	} else {
		note := T("Preview coming…")
		buf.SetString(pic.X+uint16(max(0, (int(pic.Width)-cell.StringWidth(note))/2)), pic.Y+pic.Height/2, clipToWidth(note, int(pic.Width)), cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg, Modifier: cell.ModifierItalic})
	}

	fps := t.fps
	if fps <= 0 {
		fps = 60
	}
	label := Tf("► Click to watch · %d FPS", fps)
	switch {
	case t.self:
		label = T("Your stream · click to preview")
	case t.watching:
		label = T("■ Watching · click to stop")
	}
	buf.SetString(inner.X+1, inner.Y+inner.Height-1, clipToWidth(label, int(inner.Width)-2), cell.Style{Fg: borderCol, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})

	clickable(frame, area, func(_ driver.MouseEvent) {
		switch {
		case t.self:
			go toggleScreenPreview(node, r)
		case t.watching:
			go func() {
				_ = node.StopWatchingScreen()
				r.SetToast("Screen viewer closed")
			}()
		default:
			port := t.peer.VideoPort
			if port <= 0 {
				port = 50100
			}
			opts := screenshare.DefaultReceiverOptions(fps)
			opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", t.nick, fps)
			r.SetToast(fmt.Sprintf("Starting %s stream...", t.nick))
			go func() {
				if err := node.StartWatchingScreen(t.id, port, opts); err != nil {
					r.SetToast(fmt.Sprintf("Error: %v", err))
				} else {
					r.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", t.nick, fps))
				}
			}()
		}
	})
}

func DrawHorizontalLevelMeter(buf *buffer.Buffer, area cell.Rect, rms float64, isSpeaking, isMuted bool) {
	if area.Width == 0 || area.Height == 0 {
		return
	}

	theme := CurrentTheme()
	filled := int(rms * float64(area.Width))
	if filled > int(area.Width) {
		filled = int(area.Width)
	}

	meterStyle := cell.Style{Fg: theme.WaveColor, Bg: theme.SurfaceBg}
	if isMuted {
		meterStyle.Fg = theme.Border
	} else if isSpeaking {
		meterStyle.Fg = theme.Success
	}

	for x := 0; x < int(area.Width); x++ {
		ch := ' '
		st := meterStyle
		if x < filled {
			ch = '━'
			st.Modifier = cell.ModifierBold
		}
		buf.SetCell(area.X+uint16(x), area.Y, cell.Cell{Content: ch, Style: st})
	}
}

func (r *RoomView) renderLocalSlot(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine) {
	theme := CurrentTheme()
	borderStyle := cell.Style{Fg: theme.BorderFocused}
	statusText := T("[LISTENING]")
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.CardBg}

	if audio.Deafened {
		borderStyle = cell.Style{Fg: theme.Warning}
		statusText = T("[DEAFENED]")
		statusStyle = cell.Style{Fg: theme.Warning, Bg: theme.CardBg}
	} else if audio.Muted {
		borderStyle = cell.Style{Fg: theme.Danger}
		statusText = T("[MIC OFF]")
		statusStyle = cell.Style{Fg: theme.Danger, Bg: theme.CardBg}
	} else if audio.InputMode == engine.InputModePushToTalk {
		if audio.IsTransmitting() {
			borderStyle = cell.Style{
				Fg:       theme.Success,
				Modifier: cell.ModifierBold,
			}
			statusText = T("[PTT TALKING...]")
			statusStyle = cell.Style{
				Fg:       theme.Success,
				Bg:       theme.CardBg,
				Modifier: cell.ModifierBold,
			}
		} else {
			borderStyle = cell.Style{Fg: theme.BorderFocused}
			statusText = T("[PTT IDLE (SPACE)]")
			statusStyle = cell.Style{
				Fg: theme.Warning,
				Bg: theme.CardBg,
			}
		}
	} else if audio.IsSpeaking {
		borderStyle = cell.Style{
			Fg:       theme.Success,
			Modifier: cell.ModifierBold,
		}
		statusText = T("[SPEAKING...]")
		statusStyle = cell.Style{
			Fg:       theme.Success,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}
	}

	title := Tf(" [1] %s (YOU) ", node.Nickname)
	block := widgets.Block{
		Title:         title,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   borderStyle,
		Style:         cell.Style{Bg: theme.CardBg},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.CardBg}})
		}
	}

	// Keep rows laid out for more height inside the border.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer
		_ = buf
		statusLabel := T("Status: ")
		buf.SetString(inner.X+1, inner.Y, statusLabel, cell.Style{Fg: theme.Text, Bg: theme.CardBg})
		buf.SetString(inner.X+1+uint16(len([]rune(statusLabel))), inner.Y, statusText, statusStyle)

		gainStr := Tf("Vol: %.0f%%", audio.Gain*100)
		statusEnd := 1 + len([]rune(statusLabel)) + len([]rune(statusText)) + 1
		if int(inner.Width) >= statusEnd+len([]rune(gainStr))+1 {
			buf.SetString(inner.X+inner.Width-uint16(len([]rune(gainStr)))-1, inner.Y, gainStr, cell.Style{Fg: theme.Secondary, Bg: theme.CardBg})
		}

		// If self is sharing screen, show live broadcasting banner inside the card
		if node.IsSharingScreen && inner.Height >= 5 {
			meterHeight := inner.Height - 4
			if meterHeight < 1 {
				meterHeight = 1
			}
			meterRect := cell.Rect{
				X:      inner.X + 1,
				Y:      inner.Y + 1,
				Width:  inner.Width - 2,
				Height: meterHeight,
			}
			DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, T("AUDIO LEVEL"))

			// Broadcast Banner
			bannerY := inner.Y + meterHeight + 1
			bannerW := inner.Width - 2
			bannerStyle := cell.Style{
				Fg:       theme.Danger,
				Bg:       theme.SurfaceBg,
				Modifier: cell.ModifierBold,
			}
			for bx := uint16(0); bx < bannerW; bx++ {
				buf.SetCell(inner.X+1+bx, bannerY, cell.Cell{Content: ' ', Style: bannerStyle})
				buf.SetCell(inner.X+1+bx, bannerY+1, cell.Cell{Content: ' ', Style: bannerStyle})
			}

			localFPS := node.ActiveScreenShareFPS
			if localFPS <= 0 {
				localFPS = 60
			}
			bTitle := Tf(" LIVE: Sharing Your Screen (%d FPS) ", localFPS)
			if uint16(len([]rune(bTitle))) > bannerW {
				bTitle = T(" LIVE STREAMING ")
			}
			buf.SetString(inner.X+2, bannerY, bTitle, bannerStyle)

			bAction := T("   [V] Stop Broadcast (Click)   ")
			bActionStyle := cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Danger,
				Modifier: cell.ModifierBold,
			}
			buf.SetString(inner.X+2, bannerY+1, bAction, bActionStyle)

			clickable(frame, cell.NewRect(inner.X+1, bannerY, bannerW, 2), func(_ driver.MouseEvent) {
				_ = node.StopScreenShare()
				r.SetToast("Screen share stopped")
			})

		} else if inner.Height >= 2 && inner.Width >= 4 {
			meterRect := cell.Rect{
				X:      inner.X + 1,
				Y:      inner.Y + 1,
				Width:  inner.Width - 2,
				Height: inner.Height - 1,
			}
			DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, T("AUDIO LEVEL"))
		}
	})
}

func (r *RoomView) renderPeerSlot(frame *terminal.Frame, area cell.Rect, peer *p2p.PeerInfo, node *p2p.P2PNode, audio *engine.AudioEngine, slotNum int) {
	theme := CurrentTheme()
	borderStyle := cell.Style{Fg: theme.Border}
	statusText := T("[LISTENING]")
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.CardBg}

	if peer.IsSharingScreen {
		borderStyle = cell.Style{
			Fg:       theme.Accent,
			Modifier: cell.ModifierBold,
		}
	}

	if time.Since(peer.LastSeen) > 3500*time.Millisecond {
		borderStyle = cell.Style{Fg: theme.Warning}
		statusText = T("[RECONNECTING...]")
		statusStyle = cell.Style{
			Fg:       theme.Warning,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}
	} else if peer.IsDeafened {
		borderStyle = cell.Style{Fg: theme.Warning}
		statusText = T("[DEAFENED]")
		statusStyle = cell.Style{Fg: theme.Warning, Bg: theme.CardBg}
	} else if peer.IsMuted {
		borderStyle = cell.Style{Fg: theme.Danger}
		statusText = T("[MIC OFF]")
		statusStyle = cell.Style{Fg: theme.Danger, Bg: theme.CardBg}
	} else if peer.Speaking {
		borderStyle = cell.Style{
			Fg:       theme.Success,
			Modifier: cell.ModifierBold,
		}
		statusText = T("[SPEAKING...]")
		statusStyle = cell.Style{
			Fg:       theme.Success,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}
	}

	title := fmt.Sprintf(" [%d] %s ", slotNum, peer.Nickname)
	if peer.IsSharingScreen {
		title = Tf(" [%d] %s 🔴 [LIVE STREAMING] ", slotNum, peer.Nickname)
	}

	block := widgets.Block{
		Title:         title,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   borderStyle,
		Style:         cell.Style{Bg: theme.CardBg},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.CardBg}})
		}
	}

	// Keep rows laid out for more height inside the border.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer
		_ = buf
		statusLabel := T("Status: ")
		buf.SetString(inner.X+1, inner.Y, statusLabel, cell.Style{Fg: theme.Text, Bg: theme.CardBg})
		buf.SetString(inner.X+1+uint16(len([]rune(statusLabel))), inner.Y, statusText, statusStyle)

		pingStr := T("PING: --")
		if peer.PingMs > 0 {
			pingStr = Tf("PING: %dms (%s)", peer.PingMs, peerTransport(node, peer))
			if peer.LossPct >= 1 {
				pingStr = Tf("PING: %dms %d%% loss (%s)", peer.PingMs, int(peer.LossPct), peerTransport(node, peer))
			}
		}
		volVal := 1.0
		if audio != nil {
			volVal = audio.GetPeerVolume(peer.ID)
		}
		volPct := int(math.Round(volVal * 100))
		volStr := Tf("[VOL: %d%%]", volPct)
		volLen := uint16(len([]rune(volStr)))
		pingLen := uint16(len([]rune(pingStr)))
		// Right-aligned pills never cover the status text: a narrow card drops the volume
		// pill first, then the ping.
		statusEnd := inner.X + 1 + uint16(len([]rune(statusLabel))) + uint16(len([]rune(statusText))) + 1
		pingX := int(inner.X) + int(inner.Width) - int(pingLen) - 1
		volX := pingX - int(volLen) - 1

		if volX >= int(statusEnd) {
			volX := uint16(volX)
			volStyle := cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Secondary,
				Modifier: cell.ModifierBold,
			}
			if volPct == 0 {
				volStyle = cell.Style{
					Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
					Bg:       theme.Danger,
					Modifier: cell.ModifierBold,
				}
			} else if volPct > 100 {
				volStyle = cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Accent,
					Modifier: cell.ModifierBold,
				}
			}
			buf.SetString(volX, inner.Y, volStr, volStyle)
			clickable(frame, cell.NewRect(volX, inner.Y, volLen, 1), func(_ driver.MouseEvent) {
				if audio != nil {
					curV := int(math.Round(audio.GetPeerVolume(peer.ID) * 100))
					var nextV float64
					switch {
					case curV >= 200:
						nextV = 0.0
					case curV == 0:
						nextV = 0.50
					case curV < 100:
						nextV = float64(curV+25) / 100.0
					default:
						nextV = float64(curV+25) / 100.0
						if nextV > 2.0 {
							nextV = 2.0
						}
					}
					audio.SetPeerVolume(peer.ID, nextV)
					r.SetToast(fmt.Sprintf("Volume for %s set to %d%%", peer.Nickname, int(math.Round(nextV*100))))
				}
			})

			buf.SetString(uint16(pingX), inner.Y, pingStr, cell.Style{Fg: theme.Success, Bg: theme.CardBg})
		} else if pingX >= int(statusEnd) {
			buf.SetString(uint16(pingX), inner.Y, pingStr, cell.Style{Fg: theme.Success, Bg: theme.CardBg})
		}

		// Discord-style Stream Preview Banner if peer is sharing screen
		if peer.IsSharingScreen && inner.Height >= 5 {
			meterHeight := inner.Height - 4
			if meterHeight < 1 {
				meterHeight = 1
			}
			meterRect := cell.Rect{
				X:      inner.X + 1,
				Y:      inner.Y + 1,
				Width:  inner.Width - 2,
				Height: meterHeight,
			}
			DrawVerticalLevelMeter(buf, meterRect, peer.RMS, peer.Speaking, peer.IsMuted, T("AUDIO LEVEL"))

			// Stream Preview Card Box
			bannerY := inner.Y + meterHeight + 1
			bannerW := inner.Width - 2

			bannerBg := cell.Style{
				Fg:       theme.Accent,
				Bg:       theme.SurfaceBg,
				Modifier: cell.ModifierBold,
			}

			for bx := uint16(0); bx < bannerW; bx++ {
				buf.SetCell(inner.X+1+bx, bannerY, cell.Cell{Content: ' ', Style: bannerBg})
				buf.SetCell(inner.X+1+bx, bannerY+1, cell.Cell{Content: ' ', Style: bannerBg})
			}

			peerFPS := peer.VideoFPS
			if peerFPS <= 0 {
				peerFPS = 60
			}
			bTitle := Tf(" %s Sharing Screen (%d FPS)", peer.Nickname, peerFPS)
			if uint16(len([]rune(bTitle))) > bannerW {
				bTitle = Tf(" %s LIVE STREAM", peer.Nickname)
			}
			buf.SetString(inner.X+2, bannerY, bTitle, bannerBg)

			if node.IsWatchingScreen && node.WatchingPeerID == peer.ID {
				bBtnText := T("   [W] Stop Watching (Click)   ")
				bBtnStyle := cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Danger,
					Modifier: cell.ModifierBold,
				}
				buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
			} else if node.IsWatchingScreen {
				bBtnText := T("   ► Switch to Stream (Click)   ")
				bBtnStyle := cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Secondary,
					Modifier: cell.ModifierBold,
				}
				buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
			} else {
				bBtnText := T("   ► [W] WATCH STREAM (Click)   ")
				bBtnStyle := cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Accent,
					Modifier: cell.ModifierBold,
				}
				buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
			}

			// Click on preview banner to watch / stop watching
			clickable(frame, cell.NewRect(inner.X+1, bannerY, bannerW, 2), func(_ driver.MouseEvent) {
				if node.IsWatchingScreen && node.WatchingPeerID == peer.ID {
					go func() {
						_ = node.StopWatchingScreen()
						r.SetToast("Screen viewer closed")
					}()
				} else {
					port := peer.VideoPort
					if port <= 0 {
						port = 50100
					}
					opts := screenshare.DefaultReceiverOptions(peerFPS)
					opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", peer.Nickname, peerFPS)
					r.SetToast("🎬 Starting stream viewer...")
					go func() {
						err := node.StartWatchingScreen(peer.ID, port, opts)
						if err != nil {
							r.SetToast(fmt.Sprintf("Error: %v", err))
						} else {
							r.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", peer.Nickname, peerFPS))
						}
					}()
				}
			})

		} else if inner.Height >= 2 && inner.Width >= 4 {
			meterRect := cell.Rect{
				X:      inner.X + 1,
				Y:      inner.Y + 1,
				Width:  inner.Width - 2,
				Height: inner.Height - 1,
			}
			DrawVerticalLevelMeter(buf, meterRect, peer.RMS, peer.Speaking, peer.IsMuted, T("AUDIO LEVEL"))
		}
	})
}

func (r *RoomView) renderEmptySlot(frame *terminal.Frame, area cell.Rect, roomCode string, slotNum int) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         Tf(" [%d] EMPTY SLOT (WAITING) ", slotNum),
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.Border},
		Style:         cell.Style{Bg: theme.CardBg},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.CardBg}})
		}
	}

	// Keep rows laid out for more height inside the border.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer
		_ = buf
		txt1 := T("Invite Your Friend:")
		txt2 := Tf("Room Code: %s", roomCode)
		txt3 := T("Press [C] to copy the code")

		clickable(frame, area, func(_ driver.MouseEvent) {
			CopyToClipboard(roomCode)
			r.SetToast(fmt.Sprintf("Room Code Copied: %s", roomCode))
		})

		yCenter := inner.Y + inner.Height/2
		if yCenter > inner.Y+1 {
			yCenter--
		}

		buf.SetString(inner.X+2, yCenter, txt1, cell.Style{Fg: theme.TextMuted, Bg: theme.CardBg})
		buf.SetString(inner.X+2, yCenter+1, txt2, cell.Style{
			Fg:       theme.Warning,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		})
		buf.SetString(inner.X+2, yCenter+2, txt3, cell.Style{Fg: theme.Secondary, Bg: theme.CardBg})
	})
}

// renderMemberList is the member grid for a window too short for cards: one line a member.
func (r *RoomView) renderMemberList(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine, peers []*p2p.PeerInfo) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         Tf(" VOICE CHANNEL MEMBERS (%d/%d) ", len(peers)+1, node.RoomLimit()),
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.BorderFocused},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)
	if inner.Width < 6 || inner.Height == 0 {
		return
	}
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			frame.Buffer.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}
	inner = cell.NewRect(inner.X+1, inner.Y, inner.Width-2, inner.Height)
	r.drawHUDMembers(frame, inner, inner.Y, int(inner.Height), node, audio, peers)
}

// streamVolumeLabel is the stream volume control's text before its buttons.
func streamVolumeLabel() string { return T("Stream sound [,/.]:") }

func streamVolumeWidth() int {
	return cell.StringWidth(streamVolumeLabel()) + cell.StringWidth(" [-] MUTED [+]")
}

// drawStreamVolume draws "Stream sound: [-] 100% [+]" at x, y; the buttons change how loud the
// sound of peerID's stream plays, in 10% steps from 0 to 200%.
func (r *RoomView) drawStreamVolume(frame *terminal.Frame, x, y, right uint16, audio *engine.AudioEngine, peerID, nick string) {
	if audio == nil || int(x)+streamVolumeWidth() > int(right) {
		return
	}
	theme := CurrentTheme()
	buf := frame.Buffer
	label := streamVolumeLabel()
	buf.SetString(x, y, label, cell.Style{Fg: theme.Secondary, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
	x += uint16(cell.StringWidth(label)) + 1

	buf.SetString(x, y, "[-]", cell.Style{Fg: theme.Secondary, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
	clickable(frame, cell.NewRect(x, y, 3, 1), func(_ driver.MouseEvent) { r.AdjustStreamVolume(audio, peerID, nick, -streamVolumeStep) })
	x += 4
	vol := audio.ScreenAudioVolumeFor(peerID)
	text, style := fmt.Sprintf("%3d%%", int(math.Round(vol*100))), cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg}
	if vol == 0 {
		text, style = T("MUTED"), cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
	}
	buf.SetString(x, y, text, style)
	x += uint16(cell.StringWidth(text)) + 1
	buf.SetString(x, y, "[+]", cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
	clickable(frame, cell.NewRect(x, y, 3, 1), func(_ driver.MouseEvent) { r.AdjustStreamVolume(audio, peerID, nick, streamVolumeStep) })
}

// streamVolumeStep is one click or key press on the stream volume.
const streamVolumeStep = 0.1

// AdjustStreamVolume changes the sound of peerID's stream by delta and says the new level.
func (r *RoomView) AdjustStreamVolume(audio *engine.AudioEngine, peerID, nick string, delta float64) {
	if peerID == "" {
		return
	}
	v := audio.SetScreenAudioVolumeFor(peerID, math.Round((audio.ScreenAudioVolumeFor(peerID)+delta)*10)/10)
	if v == 0 {
		r.SetToast(fmt.Sprintf("%s's stream sound muted", nick))
		return
	}
	r.SetToast(fmt.Sprintf("%s's stream sound: %d%%", nick, int(math.Round(v*100))))
}
