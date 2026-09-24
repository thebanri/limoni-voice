// Room view: the mini HUD, a small layout for short terminals or a window kept beside a
// game or editor.

package main

import (
	"fmt"
	"math"
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

// hudGap is the column gap between items in the mini HUD.
const hudGap = 2

// hudVUWidth is the width of a member's level meter in the mini HUD.
const hudVUWidth = 8

// fitOneRow lays items out on a single row of width columns: full labels if they fit, else
// short ones, dropping items from the end (a pinned item stays) until the rest fit.
func fitOneRow(items []flowItem, width, gap int) flowLayout {
	for _, short := range []bool{false, true} {
		var keep []int
		for i := range items {
			keep = append(keep, i)
		}
		for len(keep) > 0 {
			used := 0
			for n, i := range keep {
				if n > 0 {
					used += gap
				}
				used += cell.StringWidth(items[i].text(short))
			}
			if used <= width {
				break
			}
			if !short {
				keep = nil // try the short labels before dropping anything
				break
			}
			drop := len(keep) - 1
			for drop > 0 && items[keep[drop]].pinRight {
				drop--
			}
			keep = append(keep[:drop], keep[drop+1:]...)
		}
		if len(keep) > 0 {
			return flowLayout{rows: [][]int{keep}, short: short}
		}
	}
	return flowLayout{}
}

// hudMeter draws a member's level meter and returns the column after it.
func hudMeter(buf *buffer.Buffer, x, y uint16, rms float64, speaking, muted bool, theme ThemePalette) uint16 {
	filled := int(math.Round(rms * hudVUWidth * 2))
	if muted {
		filled = 0
	}
	for i := 0; i < hudVUWidth; i++ {
		c := cell.Cell{Content: '▱', Style: cell.Style{Fg: theme.Border, Bg: theme.SurfaceBg}}
		if i < filled {
			c.Content = '▰'
			c.Style.Fg = theme.Accent
			if speaking {
				c.Style = cell.Style{Fg: cell.NewColorRGB(0x00, 0xFF, 0x88), Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
			}
		}
		buf.SetCell(x+uint16(i), y, c)
	}
	return x + hudVUWidth
}

// watchPeer opens the viewer for a peer's screen share.
func (r *RoomView) watchPeer(node *p2p.P2PNode, peer *p2p.PeerInfo) {
	port := peer.VideoPort
	if port <= 0 {
		port = 50100
	}
	fps := peer.VideoFPS
	if fps <= 0 {
		fps = 60
	}
	opts := screenshare.DefaultReceiverOptions(fps)
	opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", peer.Nickname, fps)
	r.SetToast(fmt.Sprintf("Opening %s stream...", peer.Nickname))
	go func() {
		_ = node.StartWatchingScreen(peer.ID, port, opts)
	}()
}

// hudStatusItems is the mini HUD's top line: room, members, role, latency and the way
// back to the full UI.
func (r *RoomView) hudStatusItems(node *p2p.P2PNode, peers []*p2p.PeerInfo) []flowItem {
	theme := CurrentTheme()
	black := cell.NewColorRGB(0x00, 0x00, 0x00)

	items := []flowItem{{
		label: Tf(" ROOM #%s ", node.RoomCode),
		short: fmt.Sprintf(" #%s ", node.RoomCode),
		style: cell.Style{Fg: black, Bg: theme.Warning, Modifier: cell.ModifierBold},
		onClick: func(_ driver.MouseEvent) {
			CopyToClipboard(node.RoomCode)
			r.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))
		},
	}}
	if node.IsLocked {
		lock := flowItem{label: T(" LOCKED "), style: cell.Style{Fg: black, Bg: theme.Danger, Modifier: cell.ModifierBold}}
		if node.IsHost && node.RoomPIN != "" {
			lock.label = Tf(" PIN: %s ", node.RoomPIN)
		}
		items = append(items, lock)
	}
	items = append(items, flowItem{
		label: Tf("%d/4 members", len(peers)+1),
		short: fmt.Sprintf("%d/4", len(peers)+1),
		style: cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold},
	})
	if node.IsHost {
		items = append(items, flowItem{label: T("Host (you)"), short: T("Host"), style: cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg}})
	} else {
		hostNick := node.HostNick
		if hostNick == "" {
			hostNick = T("Host")
		}
		items = append(items, flowItem{label: Tf("Host: %s", hostNick), style: cell.Style{Fg: theme.Secondary, Bg: theme.SurfaceBg}})
	}

	// Average latency over the peers that have a measured ping.
	var total int64
	count := 0
	for _, p := range peers {
		if p.PingMs > 0 {
			total += p.PingMs
			count++
		}
	}
	if count > 0 {
		ping := total / int64(count)
		pingColor := theme.Success
		if ping > 120 {
			pingColor = theme.Danger
		} else if ping > 50 {
			pingColor = theme.Warning
		}
		items = append(items, flowItem{label: fmt.Sprintf("%d ms", ping), style: cell.Style{Fg: pingColor, Bg: theme.SurfaceBg}})
	}

	return append(items, flowItem{
		label:    T(" FULL UI [H] "),
		short:    " [H] ",
		style:    cell.Style{Fg: black, Bg: theme.Accent, Modifier: cell.ModifierBold},
		pinRight: true,
		onClick: func(_ driver.MouseEvent) {
			ToggleCompactHUD()
			r.IsCompactMode = false
		},
	})
}

// hudControlItems are the mini HUD's buttons, most important first.
func (r *RoomView) hudControlItems(node *p2p.P2PNode, audio *engine.AudioEngine, peers []*p2p.PeerInfo) []flowItem {
	theme := CurrentTheme()
	black := cell.NewColorRGB(0x00, 0x00, 0x00)
	white := cell.NewColorRGB(0xFF, 0xFF, 0xFF)
	pill := func(fg, bg cell.Color) cell.Style {
		return cell.Style{Fg: fg, Bg: bg, Modifier: cell.ModifierBold}
	}

	mic := flowItem{label: T(" MIC ON [M] "), short: T(" MIC [M] "), style: pill(black, theme.Success)}
	switch {
	case audio.Muted:
		mic = flowItem{label: T(" MUTED [M] "), style: pill(white, theme.Danger)}
	case audio.IsSpeaking:
		mic = flowItem{label: T(" SPEAKING [M] "), short: T(" TALK [M] "), style: pill(black, cell.NewColorRGB(0x00, 0xFF, 0x88))}
	case audio.InputMode == engine.InputModePushToTalk:
		mic = flowItem{label: T(" PTT [M] "), style: pill(black, theme.Warning)}
	}
	mic.onClick = func(_ driver.MouseEvent) {
		if isMuted := audio.ToggleMute(); isMuted {
			node.SendMuteState(true)
			r.SetToast("Microphone Off (Muted)")
		} else {
			node.SendMuteState(false)
			r.SetToast("Microphone Active")
		}
	}

	deafen := flowItem{label: T(" AUDIO ON [D] "), short: T(" SPK [D] "), style: pill(black, theme.Secondary)}
	if audio.Deafened {
		deafen = flowItem{label: T(" DEAFENED [D] "), short: T(" DEAF [D] "), style: pill(black, theme.Warning)}
	}
	deafen.onClick = func(_ driver.MouseEvent) {
		isDeaf := audio.ToggleDeafen()
		node.SendDeafenState(isDeaf)
		node.SendMuteState(audio.Muted)
		if isDeaf {
			r.SetToast("Audio Deafened (All Sounds Muted)")
		} else {
			r.SetToast("Audio Restored")
		}
	}

	var streaming *p2p.PeerInfo
	for _, p := range peers {
		if p.IsSharingScreen {
			streaming = p
			break
		}
	}
	var share flowItem
	switch {
	case node.IsSharingScreen:
		share = flowItem{label: T(" SHARING [V] "), style: pill(black, theme.Accent), onClick: func(_ driver.MouseEvent) {
			_ = node.StopScreenShare()
			r.SetToast("Screen share stopped")
		}}
	case node.IsWatchingScreen:
		share = flowItem{label: T(" WATCHING [W] "), style: pill(black, theme.Secondary), onClick: func(_ driver.MouseEvent) {
			_ = node.StopWatchingScreen()
			r.SetToast("Stream viewer closed")
		}}
	case streaming != nil:
		share = flowItem{label: Tf(" WATCH %s [W] ", streaming.Nickname), short: T(" WATCH [W] "), style: pill(white, theme.Danger), onClick: func(_ driver.MouseEvent) {
			r.watchPeer(node, streaming)
		}}
	default:
		share = flowItem{label: T(" SHARE [V] "), style: cell.Style{Fg: theme.Text, Bg: theme.CardBg, Modifier: cell.ModifierBold}, onClick: func(_ driver.MouseEvent) {
			if r.OnOpenScreenShareModal != nil {
				r.OnOpenScreenShareModal()
			}
		}}
	}

	noise := flowItem{
		label: Tf(" NOISE: %s [N] ", tr(audio.SuppressionModeString())),
		style: cell.Style{Fg: theme.Accent, Bg: theme.CardBg},
		onClick: func(_ driver.MouseEvent) {
			audio.CycleSuppressionMode()
			r.SetToast(fmt.Sprintf("Noise Filter: %s", audio.SuppressionModeString()))
		},
	}
	sfx := flowItem{label: T(" SFX [F] "), style: cell.Style{Fg: theme.Secondary, Bg: theme.CardBg}, onClick: func(_ driver.MouseEvent) {
		if r.OnTriggerSFX != nil {
			r.OnTriggerSFX()
		}
	}}
	leave := flowItem{label: T(" LEAVE [Esc] "), style: cell.Style{Fg: theme.Danger, Bg: theme.CardBg, Modifier: cell.ModifierBold}, onClick: func(_ driver.MouseEvent) {
		if r.OnLeave != nil {
			r.OnLeave()
		}
	}}

	return []flowItem{mic, deafen, share, noise, sfx, leave}
}

func (r *RoomView) renderCompactHUD(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine) {
	r.mu.Lock()
	r.LastLogArea = cell.Rect{} // no chat panel in the HUD
	toast := tr(r.ToastMsg)
	var lastMsg RoomMessage
	if n := len(r.Messages); n > 0 {
		lastMsg = r.Messages[n-1]
	}
	r.mu.Unlock()

	if area.Height < 1 || area.Width < 8 {
		return
	}
	theme := CurrentTheme()
	buf := frame.Buffer
	peers := node.GetPeersList()

	for y := area.Y; y < area.Y+area.Height; y++ {
		for x := area.X; x < area.X+area.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}
	inner := area
	if area.Height >= 4 && area.Width >= 32 {
		title := Tf(" 🍋 LIMONI · MINI HUD · %s ", formatDuration(time.Since(r.StartTime)))
		if area.Width < 50 {
			title = T(" 🍋 MINI HUD ")
		}
		block := widgets.Block{
			Title:         title,
			Borders:       widgets.BorderAll,
			BorderSymbols: widgets.SymbolsRounded,
			BorderStyle:   cell.Style{Fg: theme.BorderFocused},
			Style:         cell.Style{Bg: theme.SurfaceBg},
		}
		frame.RenderWidget(block, area)
		inner = block.Inner(area)
		// A column of padding inside the border.
		if inner.Width > 4 {
			inner = cell.NewRect(inner.X+1, inner.Y, inner.Width-2, inner.Height)
		}
	}
	if inner.Height < 1 || inner.Width < 6 {
		return
	}

	status := r.hudStatusItems(node, peers)
	controls := r.hudControlItems(node, audio, peers)
	width := int(inner.Width)
	row := func(y uint16) cell.Rect { return cell.NewRect(inner.X, y, inner.Width, 1) }

	// One line: the room code, the three main buttons and the way back.
	if inner.Height == 1 {
		items := []flowItem{status[0], controls[0], controls[1], controls[2], status[len(status)-1]}
		drawFlow(frame, row(inner.Y), items, fitOneRow(items, width, 1), 1)
		return
	}

	y := inner.Y
	bottom := inner.Y + inner.Height
	drawFlow(frame, row(y), status, fitOneRow(status, width, hudGap), hudGap)
	y++

	// Buttons keep their full labels, wrapping, while that leaves room for every member;
	// otherwise they go short to save rows.
	members := 1 + len(peers)
	fl := layoutFlow(controls, width, hudGap, 2)
	if len(fl.rows) > 1 && int(bottom-y)-len(fl.rows) < members {
		fl = layoutFlow(controls, width, hudGap, 1)
	}
	if int(bottom-y) == 1 {
		fl = fitOneRow(controls, width, hudGap)
	}
	ctrlRows := min(len(fl.rows), int(bottom-y))
	drawFlow(frame, cell.NewRect(inner.X, y, inner.Width, uint16(ctrlRows)), controls, fl, hudGap)
	y += uint16(ctrlRows)

	// Members, one aligned row each, under a divider when there is room for it.
	rest := int(bottom - y)
	footer := toast != "" || lastMsg.Text != ""
	if rest >= members+1+btoi(footer) {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: '─', Style: cell.Style{Fg: theme.Border, Bg: theme.SurfaceBg}})
		}
		y++
		rest--
	}
	shown := min(members, rest)
	if footer && rest > members {
		shown = members
	} else if footer && rest <= members && rest > 1 && members > 1 {
		shown = rest // members come before the chat line
	}
	r.drawHUDMembers(frame, inner, y, shown, node, audio, peers)
	y += uint16(shown)

	// Last line: a toast, or the latest chat message.
	if y < bottom && footer {
		text := toast
		style := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Warning, Modifier: cell.ModifierBold}
		if text == "" {
			sender := lastMsg.Sender
			if lastMsg.IsSelf {
				sender = T("You")
			}
			text = lastMsg.Text
			if sender != "" {
				text = sender + ": " + text
			}
			style = cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
		}
		buf.SetString(inner.X, bottom-1, clipToWidth(" "+text+" ", width), style)
	}
}

// drawHUDMembers draws up to n member rows from y: you first, then each peer, with the name,
// a level meter, state or volume controls, and latency or a watch button.
func (r *RoomView) drawHUDMembers(frame *terminal.Frame, inner cell.Rect, y uint16, n int, node *p2p.P2PNode, audio *engine.AudioEngine, peers []*p2p.PeerInfo) {
	if n <= 0 {
		return
	}
	theme := CurrentTheme()
	buf := frame.Buffer
	right := inner.X + inner.Width
	black := cell.NewColorRGB(0x00, 0x00, 0x00)
	white := cell.NewColorRGB(0xFF, 0xFF, 0xFF)

	selfName := T("You")
	nameW := cell.StringWidth(selfName)
	for _, p := range peers {
		nameW = max(nameW, cell.StringWidth(p.Nickname))
	}
	nameW = min(nameW, max(6, int(inner.Width)/4))

	// put draws label at x if it fits and returns the column after it plus a gap.
	put := func(x, y uint16, label string, style cell.Style, onClick func()) uint16 {
		w := uint16(cell.StringWidth(label))
		if x+w > right {
			return right
		}
		buf.SetString(x, y, label, style)
		if onClick != nil {
			clickable(frame, cell.NewRect(x, y, w, 1), func(_ driver.MouseEvent) { onClick() })
		}
		return x + w + hudGap
	}
	head := func(y uint16, name string, speaking, muted bool, rms float64) uint16 {
		dot := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
		nameStyle := cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
		switch {
		case muted:
			dot.Fg = theme.Danger
		case speaking:
			dot.Fg = cell.NewColorRGB(0x00, 0xFF, 0x88)
			nameStyle.Fg = dot.Fg
		}
		x := inner.X
		buf.SetString(x, y, "●", dot)
		x += 2
		if x+uint16(nameW) >= right {
			return right
		}
		buf.SetString(x, y, clipToWidth(name, nameW), nameStyle)
		x += uint16(nameW) + hudGap
		if x+hudVUWidth > right {
			return right
		}
		return hudMeter(buf, x, y, rms, speaking, muted, theme) + hudGap
	}

	// You.
	selfSpeaking := audio.IsSpeaking && !audio.Muted
	x := head(y, selfName, selfSpeaking, audio.Muted, audio.LocalRMS)
	switch {
	case audio.Muted:
		x = put(x, y, T("MUTED"), cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}, nil)
	case audio.Deafened:
		x = put(x, y, T("DEAFENED"), cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}, nil)
	case selfSpeaking:
		x = put(x, y, T("SPEAKING"), cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}, nil)
	default:
		x = put(x, y, T("mic on"), cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}, nil)
	}
	if node.IsSharingScreen {
		put(x, y, T(" SHARING SCREEN "), cell.Style{Fg: black, Bg: theme.Accent, Modifier: cell.ModifierBold}, nil)
	}

	for i, p := range peers {
		if i+1 >= n {
			return
		}
		peer := p
		py := y + uint16(i+1)
		x := head(py, peer.Nickname, peer.Speaking && !peer.IsMuted, peer.IsMuted, peer.RMS)

		setVol := func(v float64) {
			v = max(0, min(2, v))
			audio.SetPeerVolume(peer.ID, v)
			r.SetToast(fmt.Sprintf("Volume for %s: %d%%", peer.Nickname, int(math.Round(v*100))))
		}
		vol := int(math.Round(audio.GetPeerVolume(peer.ID) * 100))
		volText := fmt.Sprintf("%3d%%", vol)
		volStyle := cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg}
		if peer.IsMuted || vol == 0 {
			volText, volStyle = T("MUTED"), cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
		}
		if x+uint16(cell.StringWidth("[-] "+volText+" [+]")) <= right {
			x = put(x, py, "[-]", cell.Style{Fg: theme.Secondary, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}, func() {
				setVol(audio.GetPeerVolume(peer.ID) - 0.25)
			}) - hudGap + 1
			x = put(x, py, volText, volStyle, nil) - hudGap + 1
			x = put(x, py, "[+]", cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}, func() {
				setVol(audio.GetPeerVolume(peer.ID) + 0.25)
			})
		}

		switch {
		case peer.IsSharingScreen && node.IsWatchingScreen && node.WatchingPeerID == peer.ID:
			put(x, py, T(" WATCHING [W] "), cell.Style{Fg: black, Bg: theme.Secondary, Modifier: cell.ModifierBold}, func() {
				_ = node.StopWatchingScreen()
				r.SetToast("Stream viewer closed")
			})
		case peer.IsSharingScreen:
			put(x, py, T(" WATCH LIVE [W] "), cell.Style{Fg: white, Bg: theme.Danger, Modifier: cell.ModifierBold}, func() {
				r.watchPeer(node, peer)
			})
		case peer.PingMs > 0:
			put(x, py, fmt.Sprintf("%d ms %s", peer.PingMs, peerTransport(node, peer)), cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}, nil)
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// peerTransport labels the path audio to a peer takes: LAN, P2P, P2P-v6, Relay-UDP or Relay.
func peerTransport(node *p2p.P2PNode, peer *p2p.PeerInfo) string {
	if node == nil {
		return peer.Path(false)
	}
	return node.PeerPath(peer)
}
