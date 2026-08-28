package main

import (
	"fmt"
	"time"

	"github.com/thebanri/limoni/core/backend"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/layout"
	"github.com/thebanri/limoni/widgets"
	"github.com/thebanri/limoni-voice/screenshare"
)

type RoomView struct {
	StartTime       time.Time
	ToastMsg        string
	ToastTimer      int
	Logs            []string
	OnLeave         func()
	OnOpenTestModal func()
	LastStageArea   cell.Rect
}

func NewRoomView() *RoomView {
	return &RoomView{
		StartTime: time.Now(),
		Logs:      make([]string, 0),
	}
}

func (r *RoomView) AddLog(msg string) {
	// Format log message with timestamp
	ts := time.Now().Format("15:04:05")
	r.Logs = append(r.Logs, fmt.Sprintf("[%s] %s", ts, msg))
	if len(r.Logs) > 200 {
		r.Logs = r.Logs[len(r.Logs)-100:]
	}
}

func (r *RoomView) SetToast(msg string) {
	r.ToastMsg = msg
	r.ToastTimer = 90 // ~3 seconds at 30 FPS
}

func (r *RoomView) Update() {
	if r.ToastTimer > 0 {
		r.ToastTimer--
		if r.ToastTimer == 0 {
			r.ToastMsg = ""
		}
	}
}

func (r *RoomView) Render(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	footerHeight := uint16(7)
	if node.IsWatchingScreen {
		footerHeight = 4 // Compact footer when watching stream so Stage gets maximum height!
	}

	fl := layout.NewFlexLayout(layout.Vertical, 0,
		layout.Fixed(3),            // Header
		layout.Fill(),              // 2x2 Participant Cards or Big Stream Stage
		layout.Fixed(footerHeight), // Controls & Mini Logs
	)
	vSplits := fl.Split(area)
	if len(vSplits) < 3 {
		return
	}

	r.renderHeader(frame, vSplits[0], node)
	r.renderGrid(frame, vSplits[1], node, audio)
	r.renderFooter(frame, vSplits[2], node, audio)
}

func (r *RoomView) renderHeader(frame *terminal.Frame, area cell.Rect, node *P2PNode) {
	block := widgets.Block{
		Title:         " LIMONI VOICE ROOM ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: cell.NewColorRGB(0x6C, 0x5C, 0xE7)},
		Style:         cell.Style{Bg: cell.NewColorRGB(0x10, 0x14, 0x20)},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}})
		}
	}

	duration := time.Since(r.StartTime).Round(time.Second)

	peers := node.GetPeersList()
	totalCount := len(peers) + 1 // +1 for self

	titleStr := "LIMONI VOICE ROOM"
	buf.SetString(inner.X+1, inner.Y, titleStr, cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0xD2, 0xD3),
		Bg:       cell.NewColorRGB(0x10, 0x14, 0x20),
		Modifier: cell.ModifierBold,
	})

	codeBadge := fmt.Sprintf(" Oda: %s ", node.RoomCode)
	codeX := inner.X + 22
	buf.SetString(codeX, inner.Y, codeBadge, cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       cell.NewColorRGB(0xFF, 0xE6, 0x6D),
		Modifier: cell.ModifierBold,
	})
	frame.RegisterClickHandler(cell.NewRect(codeX, inner.Y, uint16(len([]rune(codeBadge))), 1), func(_ backend.MouseEvent) {
		CopyToClipboard(node.RoomCode)
		r.SetToast(fmt.Sprintf("Oda kodu kopyalandi: %s", node.RoomCode))
	})

	var roleBadge string
	var roleStyle cell.Style
	if node.IsHost {
		roleBadge = " 👑 HOST (SEN) "
		roleStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0xFF, 0x9F, 0x43),
			Modifier: cell.ModifierBold,
		}
	} else {
		hostName := node.HostNick
		if hostName == "" {
			hostName = "Host"
		}
		roleBadge = fmt.Sprintf(" 👤 UYE (Host: %s) ", hostName)
		roleStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
			Modifier: cell.ModifierBold,
		}
	}

	roleX := codeX + uint16(len([]rune(codeBadge))) + 2
	buf.SetString(roleX, inner.Y, roleBadge, roleStyle)

	countStr := fmt.Sprintf("Katilimci: %d/4", totalCount)
	countX := roleX + uint16(len([]rune(roleBadge))) + 2
	if countX+14 <= inner.X+inner.Width {
		buf.SetString(countX, inner.Y, countStr, cell.Style{
			Fg:       cell.NewColorRGB(0x55, 0xEF, 0xC4),
			Bg:       cell.NewColorRGB(0x10, 0x14, 0x20),
			Modifier: cell.ModifierBold,
		})
	}

	e2eeBadge := " [E2EE: AES-256] "
	if inner.Width > countX+30 {
		buf.SetString(countX+16, inner.Y, e2eeBadge, cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Bg:       cell.NewColorRGB(0x13, 0x27, 0x22),
			Modifier: cell.ModifierBold,
		})
	}

	durStr := fmt.Sprintf("Süre: %s", duration)
	if inner.Width > uint16(len([]rune(durStr)))+2 {
		buf.SetString(inner.X+inner.Width-uint16(len([]rune(durStr)))-2, inner.Y, durStr, cell.Style{
			Fg: cell.NewColorRGB(0xDF, 0xE6, 0xE9),
			Bg: cell.NewColorRGB(0x10, 0x14, 0x20),
		})
	}
}

func (r *RoomView) renderGrid(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	peers := node.GetPeersList()

	// Find if any peer or local user is sharing screen
	var streamingPeer *PeerInfo
	for _, p := range peers {
		if p.IsSharingScreen {
			streamingPeer = p
			break
		}
	}

	isStreamActive := (streamingPeer != nil) || node.IsSharingScreen || node.IsWatchingScreen

	// If a screen is being shared, switch to Discord-style Layout (Sidebar Members on Left + Big Stream Stage on Right)
	if isStreamActive {
		fl := layout.NewFlexLayout(layout.Horizontal, 0,
			layout.Percentage(32), // Left: Discord Voice Channel Members
			layout.Percentage(68), // Right: Big Stream Stage
		)
		splits := fl.Split(area)
		if len(splits) >= 2 {
			r.renderSidebarMembers(frame, splits[0], node, audio, peers)
			r.renderStreamStage(frame, splits[1], streamingPeer, node)
			return
		}
	}

	// Classic 2x2 Grid Layout when no screen is shared
	r.renderClassicGrid(frame, area, node, audio, peers)
}

func (r *RoomView) renderClassicGrid(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine, peers []*PeerInfo) {
	rowFl := layout.NewFlexLayout(layout.Vertical, 0,
		layout.Percentage(50),
		layout.Percentage(50),
	)
	rowSplits := rowFl.Split(area)
	if len(rowSplits) < 2 {
		return
	}

	topRow := rowSplits[0]
	botRow := rowSplits[1]

	colFl := layout.NewFlexLayout(layout.Horizontal, 0,
		layout.Percentage(50),
		layout.Percentage(50),
	)
	topCols := colFl.Split(topRow)
	botCols := colFl.Split(botRow)
	if len(topCols) < 2 || len(botCols) < 2 {
		return
	}

	// Slot 0 (Top-Left): Local User (Self)
	r.renderLocalSlot(frame, topCols[0], node, audio)

	// Slot 1 (Top-Right): Peer 1
	if len(peers) > 0 {
		r.renderPeerSlot(frame, topCols[1], peers[0], node, audio, 2)
	} else {
		r.renderEmptySlot(frame, topCols[1], node.RoomCode, 2)
	}

	// Slot 2 (Bottom-Left): Peer 2
	if len(peers) > 1 {
		r.renderPeerSlot(frame, botCols[0], peers[1], node, audio, 3)
	} else {
		r.renderEmptySlot(frame, botCols[0], node.RoomCode, 3)
	}

	// Slot 3 (Bottom-Right): Peer 3
	if len(peers) > 2 {
		r.renderPeerSlot(frame, botCols[1], peers[2], node, audio, 4)
	} else {
		r.renderEmptySlot(frame, botCols[1], node.RoomCode, 4)
	}
}

func (r *RoomView) renderSidebarMembers(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine, peers []*PeerInfo) {
	block := widgets.Block{
		Title:         " SES KANALI UYELERI ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: cell.NewColorRGB(0x6C, 0x5C, 0xE7)},
		Style:         cell.Style{Bg: cell.NewColorRGB(0x0E, 0x11, 0x1A)},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x0E, 0x11, 0x1A)}})
		}
	}

	// Calculate height per slot
	totalSlots := len(peers) + 1
	slotHeight := int(inner.Height) / totalSlots
	if slotHeight < 3 {
		slotHeight = 3
	}

	currY := inner.Y

	// 1. Self Slot
	selfCard := cell.Rect{X: inner.X, Y: currY, Width: inner.Width, Height: uint16(slotHeight)}
	r.renderMemberMiniCard(frame, selfCard, node.Nickname+" (SEN)", audio.LocalRMS, audio.IsSpeaking, audio.Muted, audio.Deafened, node.IsSharingScreen, 0, true)
	currY += uint16(slotHeight)

	// 2. Peers Slots
	for _, peer := range peers {
		if currY+uint16(slotHeight) > inner.Y+inner.Height {
			break
		}
		peerCard := cell.Rect{X: inner.X, Y: currY, Width: inner.Width, Height: uint16(slotHeight)}
		r.renderMemberMiniCard(frame, peerCard, peer.Nickname, peer.RMS, peer.Speaking, peer.IsMuted, peer.IsDeafened, peer.IsSharingScreen, peer.PingMs, false)
		currY += uint16(slotHeight)
	}
}

func (r *RoomView) renderMemberMiniCard(frame *terminal.Frame, area cell.Rect, name string, rms float64, isSpeaking, isMuted, isDeafened, isSharing bool, pingMs int64, isSelf bool) {
	buf := frame.Buffer

	// Icon & Color
	icon := "👤"
	nameStyle := cell.Style{Fg: cell.NewColorRGB(0xDF, 0xE6, 0xE9), Bg: cell.NewColorRGB(0x0E, 0x11, 0x1A), Modifier: cell.ModifierBold}

	if isSelf {
		nameStyle.Fg = cell.NewColorRGB(0x00, 0xD2, 0xD3)
	}

	if isSharing {
		icon = "🔴"
		nameStyle.Fg = cell.NewColorRGB(0x00, 0xFF, 0x88)
	} else if isSpeaking {
		icon = "🟢"
		nameStyle.Fg = cell.NewColorRGB(0x55, 0xEF, 0xC4)
	} else if isDeafened {
		icon = "🔇"
		nameStyle.Fg = cell.NewColorRGB(0xFD, 0xCB, 0x6E)
	} else if isMuted {
		icon = "🎙️"
		nameStyle.Fg = cell.NewColorRGB(0xFF, 0x76, 0x75)
	}

	titleText := fmt.Sprintf("%s %s", icon, name)
	if isSharing {
		titleText += " 📺 [CANLI]"
	}
	buf.SetString(area.X+1, area.Y, titleText, nameStyle)

	// Status Line / Ping
	statusStr := ""
	statusStyle := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x0E, 0x11, 0x1A)}
	if isDeafened {
		statusStr = "[Sagir]"
		statusStyle.Fg = cell.NewColorRGB(0xFD, 0xCB, 0x6E)
	} else if isMuted {
		statusStr = "[Mute]"
		statusStyle.Fg = cell.NewColorRGB(0xFF, 0x76, 0x75)
	} else if isSpeaking {
		statusStr = "[Konusuyor]"
		statusStyle.Fg = cell.NewColorRGB(0x00, 0xFF, 0x88)
	} else {
		statusStr = "[Bagli]"
	}

	if pingMs > 0 {
		statusStr += fmt.Sprintf(" • %dms", pingMs)
	}

	if area.Height >= 2 {
		buf.SetString(area.X+2, area.Y+1, statusStr, statusStyle)
	}

	// Audio Level bar at bottom of mini-card
	if area.Height >= 3 && area.Width > 6 {
		meterRect := cell.Rect{X: area.X + 2, Y: area.Y + 2, Width: area.Width - 4, Height: 1}
		DrawHorizontalLevelMeter(buf, meterRect, rms, isSpeaking, isMuted)
	}
}

func (r *RoomView) renderStreamStage(frame *terminal.Frame, area cell.Rect, streamingPeer *PeerInfo, node *P2PNode) {
	stageTitle := " 🔴 CANLI YAYIN SAHNESI (STREAM STAGE) "
	borderCol := cell.NewColorRGB(0x00, 0xFF, 0x88)
	if !node.IsWatchingScreen && !node.IsSharingScreen {
		borderCol = cell.NewColorRGB(0x6C, 0x5C, 0xE7)
	}

	block := widgets.Block{
		Title:         stageTitle,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: borderCol, Modifier: cell.ModifierBold},
		Style:         cell.Style{Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17)},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)
	buf := frame.Buffer

	r.LastStageArea = inner

	// 2. Case: We are watching a peer's stream (Frameless Native GPU Player Active)
	if node.IsWatchingScreen && streamingPeer != nil {
		for y := inner.Y; y < inner.Y+inner.Height; y++ {
			for x := inner.X; x < inner.X+inner.Width; x++ {
				buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17)}})
			}
		}

		centerY := inner.Y + inner.Height/2
		msg1 := fmt.Sprintf("🎬 %s CANLI YAYINI ACILDI (60 FPS - GPU)", streamingPeer.Nickname)
		msg2 := "Çerçevesiz Donanım Hızlandırmalı Video Sahnesi Aktif."
		btnText := "   ⏹️ [W] IZLEMEYI KAPAT (Tikla veya Esc)   "

		buf.SetString(inner.X+4, centerY-3, msg1, cell.Style{Fg: cell.NewColorRGB(0x00, 0xF5, 0xD4), Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17), Modifier: cell.ModifierBold})
		buf.SetString(inner.X+4, centerY-1, msg2, cell.Style{Fg: cell.NewColorRGB(0x55, 0xEF, 0xC4), Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17)})

		btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: cell.NewColorRGB(0xFF, 0x9F, 0x43), Modifier: cell.ModifierBold}
		buf.SetString(inner.X+4, centerY+2, btnText, btnStyle)

		frame.RegisterClickHandler(cell.NewRect(inner.X+4, centerY+2, uint16(len([]rune(btnText))), 1), func(_ backend.MouseEvent) {
			_ = node.StopWatchingScreen()
			r.SetToast("Ekran izleyici kapatildi")
		})
		return
	}
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17)}})
		}
	}

	centerY := inner.Y + inner.Height/2

	// 1. Case: Local User is Broadcasting
	if node.IsSharingScreen {
		msg1 := "🔴 EKRANIN CANLI YAYINDA (60 FPS - 720p HD)"
		msg2 := "Odadaki tum katilimcilar ekranini ultra dusuk gecikmeyle izleyebiliyor."
		btnText := "   ⏹️ [V] YAYINI DURDUR (Tikla)   "

		buf.SetString(inner.X+4, centerY-3, msg1, cell.Style{Fg: cell.NewColorRGB(0xFF, 0x76, 0x75), Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17), Modifier: cell.ModifierBold})
		buf.SetString(inner.X+4, centerY-1, msg2, cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17)})

		btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: cell.NewColorRGB(0xFF, 0x76, 0x75), Modifier: cell.ModifierBold}
		buf.SetString(inner.X+4, centerY+2, btnText, btnStyle)

		frame.RegisterClickHandler(cell.NewRect(inner.X+4, centerY+2, uint16(len([]rune(btnText))), 1), func(_ backend.MouseEvent) {
			_ = node.StopScreenShare()
			r.SetToast("Ekran paylasimi durduruldu")
		})
		return
	}

	// 3. Case: A peer is broadcasting and waiting to be watched
	if streamingPeer != nil {
		msg1 := fmt.Sprintf("🔴 %s EKRANINI PAYLASIYOR (60 FPS)", streamingPeer.Nickname)
		msg2 := "Yayini terminalinde 20ms ultra dusuk gecikmeyle izlemek icin asagidaki butona tikla:"
		btnText := fmt.Sprintf("   ► [W] %s YAYININI IZLE (Tikla) 📺   ", streamingPeer.Nickname)

		buf.SetString(inner.X+4, centerY-3, msg1, cell.Style{Fg: cell.NewColorRGB(0x00, 0xFF, 0x88), Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17), Modifier: cell.ModifierBold})
		buf.SetString(inner.X+4, centerY-1, msg2, cell.Style{Fg: cell.NewColorRGB(0xDF, 0xE6, 0xE9), Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17)})

		btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: cell.NewColorRGB(0x00, 0xFF, 0x88), Modifier: cell.ModifierBold}
		buf.SetString(inner.X+4, centerY+2, btnText, btnStyle)

		frame.RegisterClickHandler(cell.NewRect(inner.X+4, centerY+2, uint16(len([]rune(btnText))), 1), func(_ backend.MouseEvent) {
			port := streamingPeer.VideoPort
			if port <= 0 {
				port = 50100
			}
			opts := CalculateStageVideoBounds(inner)
			err := node.StartWatchingScreen(port, opts)
			if err != nil {
				r.SetToast(fmt.Sprintf("Hata: %v", err))
			} else {
				r.SetToast(fmt.Sprintf("%s yayini acildi (Kitty 60 FPS)", streamingPeer.Nickname))
			}
		})
		return
	}
}

// CalculateStageVideoBounds computes the exact inner cell bounds for embedding player directly inside Stage Block
func CalculateStageVideoBounds(inner cell.Rect) screenshare.ReceiverOptions {
	cols := int(inner.Width) - 2
	rows := int(inner.Height) - 2
	if cols < 10 {
		cols = 10
	}
	if rows < 4 {
		rows = 4
	}

	left := int(inner.X) + 1
	top := int(inner.Y) + 1

	return screenshare.ReceiverOptions{
		Left: left,
		Top:  top,
		Cols: cols,
		Rows: rows,
	}
}

func DrawHorizontalLevelMeter(buf *buffer.Buffer, area cell.Rect, rms float64, isSpeaking, isMuted bool) {
	if area.Width == 0 || area.Height == 0 {
		return
	}

	filled := int(rms * float64(area.Width))
	if filled > int(area.Width) {
		filled = int(area.Width)
	}

	meterStyle := cell.Style{Fg: cell.NewColorRGB(0x55, 0xEF, 0xC4), Bg: cell.NewColorRGB(0x1A, 0x22, 0x32)}
	if isMuted {
		meterStyle.Fg = cell.NewColorRGB(0x63, 0x6E, 0x72)
	} else if isSpeaking {
		meterStyle.Fg = cell.NewColorRGB(0x00, 0xFF, 0x88)
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

func (r *RoomView) renderLocalSlot(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	borderStyle := cell.Style{Fg: cell.NewColorRGB(0x4E, 0xCD, 0xC4)}
	statusText := "[DINLIYOR]"
	statusStyle := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}

	if audio.Deafened {
		borderStyle = cell.Style{Fg: cell.NewColorRGB(0xFD, 0xCB, 0x6E)}
		statusText = "[SAGIRLASTIRILDI]"
		statusStyle = cell.Style{Fg: cell.NewColorRGB(0xFD, 0xCB, 0x6E), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}
	} else if audio.Muted {
		borderStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0x76, 0x75)}
		statusText = "[MIKROFON KAPALI]"
		statusStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0x76, 0x75), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}
	} else if audio.IsSpeaking {
		borderStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Modifier: cell.ModifierBold,
		}
		statusText = "[KONUSUYOR...]"
		statusStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Bg:       cell.NewColorRGB(0x0F, 0x11, 0x1A),
			Modifier: cell.ModifierBold,
		}
	}

	title := fmt.Sprintf(" [1] %s (SEN) ", node.Nickname)
	block := widgets.Block{
		Title:         title,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   borderStyle,
		Style:         cell.Style{Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}})
		}
	}

	buf.SetString(inner.X+1, inner.Y, "Durum: ", cell.Style{Fg: cell.NewColorRGB(0xDF, 0xE6, 0xE9), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)})
	buf.SetString(inner.X+8, inner.Y, statusText, statusStyle)

	gainStr := fmt.Sprintf("Ses: %.0f%%", audio.Gain*100)
	if inner.Width > uint16(len([]rune(gainStr)))+1 {
		buf.SetString(inner.X+inner.Width-uint16(len([]rune(gainStr)))-1, inner.Y, gainStr, cell.Style{Fg: cell.NewColorRGB(0x74, 0xB9, 0xFF), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)})
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
		DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, "SES SEVIYESI")

		// Broadcast Banner
		bannerY := inner.Y + meterHeight + 1
		bannerW := inner.Width - 2
		bannerStyle := cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0x76, 0x75),
			Bg:       cell.NewColorRGB(0x22, 0x14, 0x16),
			Modifier: cell.ModifierBold,
		}
		for bx := uint16(0); bx < bannerW; bx++ {
			buf.SetCell(inner.X+1+bx, bannerY, cell.Cell{Content: ' ', Style: bannerStyle})
			buf.SetCell(inner.X+1+bx, bannerY+1, cell.Cell{Content: ' ', Style: bannerStyle})
		}

		bTitle := " 🔴 CANLI: Ekranini Paylasiyorsun (60 FPS) "
		if uint16(len([]rune(bTitle))) > bannerW {
			bTitle = " 🔴 CANLI YAYINDASIN "
		}
		buf.SetString(inner.X+2, bannerY, bTitle, bannerStyle)

		bAction := "   ⏹️ [V] Yayini Durdur (Tikla)   "
		bActionStyle := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0xFF, 0x76, 0x75),
			Modifier: cell.ModifierBold,
		}
		buf.SetString(inner.X+2, bannerY+1, bAction, bActionStyle)

		frame.RegisterClickHandler(cell.NewRect(inner.X+1, bannerY, bannerW, 2), func(_ backend.MouseEvent) {
			_ = node.StopScreenShare()
			r.SetToast("Ekran paylasimi durduruldu")
		})

	} else if inner.Height >= 2 && inner.Width >= 4 {
		meterRect := cell.Rect{
			X:      inner.X + 1,
			Y:      inner.Y + 1,
			Width:  inner.Width - 2,
			Height: inner.Height - 1,
		}
		DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, "SES SEVIYESI")
	}
}

func (r *RoomView) renderPeerSlot(frame *terminal.Frame, area cell.Rect, peer *PeerInfo, node *P2PNode, audio *AudioEngine, slotNum int) {
	borderStyle := cell.Style{Fg: cell.NewColorRGB(0x6C, 0x5C, 0xE7)}
	statusText := "[DINLIYOR]"
	statusStyle := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}

	if peer.IsSharingScreen {
		borderStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Modifier: cell.ModifierBold,
		}
	}

	if peer.IsDeafened {
		borderStyle = cell.Style{Fg: cell.NewColorRGB(0xFD, 0xCB, 0x6E)}
		statusText = "[SAGIRLASTIRILDI]"
		statusStyle = cell.Style{Fg: cell.NewColorRGB(0xFD, 0xCB, 0x6E), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}
	} else if peer.IsMuted {
		borderStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0x76, 0x75)}
		statusText = "[MIKROFON KAPALI]"
		statusStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0x76, 0x75), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}
	} else if peer.Speaking {
		borderStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Modifier: cell.ModifierBold,
		}
		statusText = "[KONUSUYOR...]"
		statusStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Bg:       cell.NewColorRGB(0x0F, 0x11, 0x1A),
			Modifier: cell.ModifierBold,
		}
	}

	title := fmt.Sprintf(" [%d] %s ", slotNum, peer.Nickname)
	if peer.IsSharingScreen {
		title = fmt.Sprintf(" [%d] %s 🔴 [CANLI YAYINDA] ", slotNum, peer.Nickname)
	}

	block := widgets.Block{
		Title:         title,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   borderStyle,
		Style:         cell.Style{Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}})
		}
	}

	buf.SetString(inner.X+1, inner.Y, "Durum: ", cell.Style{Fg: cell.NewColorRGB(0xDF, 0xE6, 0xE9), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)})
	buf.SetString(inner.X+8, inner.Y, statusText, statusStyle)

	pingStr := fmt.Sprintf("PING: %dms", peer.PingMs)
	if inner.Width > uint16(len([]rune(pingStr)))+1 {
		buf.SetString(inner.X+inner.Width-uint16(len([]rune(pingStr)))-1, inner.Y, pingStr, cell.Style{Fg: cell.NewColorRGB(0x55, 0xEF, 0xC4), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)})
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
		DrawVerticalLevelMeter(buf, meterRect, peer.RMS, peer.Speaking, peer.IsMuted, "SES SEVIYESI")

		// Stream Preview Card Box
		bannerY := inner.Y + meterHeight + 1
		bannerW := inner.Width - 2

		bannerBg := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
			Bg:       cell.NewColorRGB(0x13, 0x22, 0x28),
			Modifier: cell.ModifierBold,
		}

		for bx := uint16(0); bx < bannerW; bx++ {
			buf.SetCell(inner.X+1+bx, bannerY, cell.Cell{Content: ' ', Style: bannerBg})
			buf.SetCell(inner.X+1+bx, bannerY+1, cell.Cell{Content: ' ', Style: bannerBg})
		}

		bTitle := fmt.Sprintf(" 🔴 %s Ekranini Paylasiyor (60 FPS)", peer.Nickname)
		if uint16(len([]rune(bTitle))) > bannerW {
			bTitle = fmt.Sprintf(" 🔴 %s CANLI YAYIN", peer.Nickname)
		}
		buf.SetString(inner.X+2, bannerY, bTitle, bannerBg)

		if node.IsWatchingScreen {
			bBtnText := "   ⏹️ [W] Izlemeyi Kapat (Tikla)   "
			bBtnStyle := cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       cell.NewColorRGB(0xFF, 0x9F, 0x43),
				Modifier: cell.ModifierBold,
			}
			buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
		} else {
			bBtnText := "   ► [W] YAYINI IZLE (Tikla) 📺   "
			bBtnStyle := cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
				Modifier: cell.ModifierBold,
			}
			buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
		}

		// Click on preview banner to watch / stop watching
		frame.RegisterClickHandler(cell.NewRect(inner.X+1, bannerY, bannerW, 2), func(_ backend.MouseEvent) {
			if node.IsWatchingScreen {
				_ = node.StopWatchingScreen()
				r.SetToast("Ekran izleyici kapatildi")
			} else {
				port := peer.VideoPort
				if port <= 0 {
					port = 50100
				}
				err := node.StartWatchingScreen(port)
				if err != nil {
					r.SetToast(fmt.Sprintf("Hata: %v", err))
				} else {
					r.SetToast(fmt.Sprintf("%s yayini acildi (Kitty 60 FPS)", peer.Nickname))
				}
			}
		})

	} else if inner.Height >= 2 && inner.Width >= 4 {
		meterRect := cell.Rect{
			X:      inner.X + 1,
			Y:      inner.Y + 1,
			Width:  inner.Width - 2,
			Height: inner.Height - 1,
		}
		DrawVerticalLevelMeter(buf, meterRect, peer.RMS, peer.Speaking, peer.IsMuted, "SES SEVIYESI")
	}
}

func (r *RoomView) renderEmptySlot(frame *terminal.Frame, area cell.Rect, roomCode string, slotNum int) {
	block := widgets.Block{
		Title:         fmt.Sprintf(" [%d] BOS YER (BEKLENIYOR) ", slotNum),
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: cell.NewColorRGB(0x4A, 0x4B, 0x6E)},
		Style:         cell.Style{Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}})
		}
	}

	txt1 := "Arkadasini Davet Et:"
	txt2 := fmt.Sprintf("Oda Kodu: %s", roomCode)
	txt3 := "[C] tusuna basarak kodu kopyala"

	frame.RegisterClickHandler(area, func(_ backend.MouseEvent) {
		CopyToClipboard(roomCode)
		r.SetToast(fmt.Sprintf("Oda Kodu Kopyalandi: %s", roomCode))
	})

	yCenter := inner.Y + inner.Height/2
	if yCenter > inner.Y+1 {
		yCenter--
	}

	buf.SetString(inner.X+2, yCenter, txt1, cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)})
	buf.SetString(inner.X+2, yCenter+1, txt2, cell.Style{
		Fg:       cell.NewColorRGB(0xFF, 0xE6, 0x6D),
		Bg:       cell.NewColorRGB(0x0F, 0x11, 0x1A),
		Modifier: cell.ModifierBold,
	})
	buf.SetString(inner.X+2, yCenter+2, txt3, cell.Style{Fg: cell.NewColorRGB(0x74, 0xB9, 0xFF), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)})
}

func (r *RoomView) renderFooter(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	fl := layout.NewFlexLayout(layout.Horizontal, 0,
		layout.Percentage(60), // controls
		layout.Percentage(40), // mini logs
	)
	cols := fl.Split(area)
	if len(cols) < 2 {
		return
	}

	ctrlArea := cols[0]
	logArea := cols[1]

	// 1. Controls Panel
	ctrlBlock := widgets.Block{
		Title:         " KONTROLLER ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: cell.NewColorRGB(0x6C, 0x5C, 0xE7)},
		Style:         cell.Style{Bg: cell.NewColorRGB(0x10, 0x14, 0x20)},
	}
	frame.RenderWidget(ctrlBlock, ctrlArea)
	ctrlInner := ctrlBlock.Inner(ctrlArea)

	buf := frame.Buffer
	for y := ctrlInner.Y; y < ctrlInner.Y+ctrlInner.Height; y++ {
		for x := ctrlInner.X; x < ctrlInner.X+ctrlInner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}})
		}
	}

	row1Y := ctrlInner.Y
	row2Y := ctrlInner.Y + 2
	if ctrlInner.Height < 3 {
		row2Y = ctrlInner.Y + 1
	}

	// --- ROW 1: Audio Toggles & Noise Filter ---
	// Mute Button
	muteLabel := "[M] Mikrofon Kapat"
	muteStyle := cell.Style{Fg: cell.NewColorRGB(0x55, 0xEF, 0xC4), Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}
	if audio.Muted {
		muteLabel = "[M] Mikrofon AC"
		muteStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0xFF, 0x76, 0x75),
			Modifier: cell.ModifierBold,
		}
	}
	muteX := ctrlInner.X + 1
	muteLen := uint16(len([]rune(muteLabel)))
	buf.SetString(muteX, row1Y, muteLabel, muteStyle)
	frame.RegisterClickHandler(cell.NewRect(muteX, row1Y, muteLen, 1), func(_ backend.MouseEvent) {
		isMuted := audio.ToggleMute()
		node.SendMuteState(isMuted)
		if isMuted {
			r.SetToast("Mikrofon Kapatildi (Muted)")
		} else {
			r.SetToast("Mikrofon Acildi")
		}
	})

	// Deafen Button
	deafenLabel := "[D] Kulaklik Kapat"
	deafenStyle := cell.Style{Fg: cell.NewColorRGB(0x74, 0xB9, 0xFF), Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}
	if audio.Deafened {
		deafenLabel = "[D] Kulaklik AC"
		deafenStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0xFD, 0xCB, 0x6E),
			Modifier: cell.ModifierBold,
		}
	}
	deafenX := muteX + muteLen + 2
	deafenLen := uint16(len([]rune(deafenLabel)))
	if deafenX+deafenLen <= ctrlInner.X+ctrlInner.Width {
		buf.SetString(deafenX, row1Y, deafenLabel, deafenStyle)
		frame.RegisterClickHandler(cell.NewRect(deafenX, row1Y, deafenLen, 1), func(_ backend.MouseEvent) {
			isDeaf := audio.ToggleDeafen()
			node.SendDeafenState(isDeaf)
			node.SendMuteState(audio.Muted)
			if isDeaf {
				r.SetToast("Kulaklik Kapatildi (Sagir)")
			} else {
				r.SetToast("Kulaklik Acildi")
			}
		})
	}

	// Noise Suppression Button [N]
	noiseStr := audio.SuppressionModeString()
	noiseLabel := fmt.Sprintf("[N] Gurultu: %s", noiseStr)
	noiseStyle := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}
	if audio.SuppressionMode > 0 {
		noiseStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x55, 0xEF, 0xC4),
			Bg:       cell.NewColorRGB(0x10, 0x14, 0x20),
			Modifier: cell.ModifierBold,
		}
	}
	noiseX := deafenX + deafenLen + 2
	noiseLen := uint16(len([]rune(noiseLabel)))
	if noiseX+noiseLen <= ctrlInner.X+ctrlInner.Width {
		buf.SetString(noiseX, row1Y, noiseLabel, noiseStyle)
		frame.RegisterClickHandler(cell.NewRect(noiseX, row1Y, noiseLen, 1), func(_ backend.MouseEvent) {
			audio.CycleSuppressionMode()
			r.SetToast(fmt.Sprintf("Gurultu Filtresi: %s", audio.SuppressionModeString()))
		})
	}

	// Screen Share Button [V]
	screenLabel := "[V] Ekran Paylas"
	screenStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0xF5, 0xD4), Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}
	if node.IsSharingScreen {
		screenLabel = "[V] Yayini Durdur"
		screenStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0xFF, 0x76, 0x75),
			Modifier: cell.ModifierBold,
		}
	}
	screenX := noiseX + noiseLen + 2
	screenLen := uint16(len([]rune(screenLabel)))
	if screenX+screenLen <= ctrlInner.X+ctrlInner.Width {
		buf.SetString(screenX, row1Y, screenLabel, screenStyle)
		frame.RegisterClickHandler(cell.NewRect(screenX, row1Y, screenLen, 1), func(_ backend.MouseEvent) {
			if node.IsSharingScreen {
				_ = node.StopScreenShare()
				r.SetToast("Ekran paylasimi durduruldu")
			} else {
				err := node.StartScreenShare("", 50100)
				if err != nil {
					r.SetToast(fmt.Sprintf("Hata: %v", err))
				} else {
					r.SetToast("Ekran paylasimi baslatildi (60 FPS)")
				}
			}
		})
	}

	// --- ROW 2: Tools & Room Actions ---
	if ctrlInner.Height >= 2 {
		// Watch Stream Button [W] (if any peer is streaming or we are watching)
		var streamingPeer *PeerInfo
		for _, p := range node.Peers {
			if p.IsSharingScreen {
				streamingPeer = p
				break
			}
		}

		watchLabel := "[W] Ekran Izle"
		watchStyle := cell.Style{Fg: cell.NewColorRGB(0x63, 0x6E, 0x72), Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}
		if node.IsWatchingScreen {
			watchLabel = "[W] Izlemeyi Kapat"
			watchStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       cell.NewColorRGB(0xFF, 0x9F, 0x43),
				Modifier: cell.ModifierBold,
			}
		} else if streamingPeer != nil {
			watchLabel = fmt.Sprintf("[W] %s Izle 🔴", streamingPeer.Nickname)
			watchStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       cell.NewColorRGB(0x55, 0xEF, 0xC4),
				Modifier: cell.ModifierBold,
			}
		}

		watchX := ctrlInner.X + 1
		watchLen := uint16(len([]rune(watchLabel)))
		buf.SetString(watchX, row2Y, watchLabel, watchStyle)
		frame.RegisterClickHandler(cell.NewRect(watchX, row2Y, watchLen, 1), func(_ backend.MouseEvent) {
			if node.IsWatchingScreen {
				_ = node.StopWatchingScreen()
				r.SetToast("Ekran izleyici kapatildi")
			} else if streamingPeer != nil {
				port := streamingPeer.VideoPort
				if port <= 0 {
					port = 50100
				}
				err := node.StartWatchingScreen(port)
				if err != nil {
					r.SetToast(fmt.Sprintf("Hata: %v", err))
				} else {
					r.SetToast(fmt.Sprintf("%s yayini acildi (Kitty 60 FPS)", streamingPeer.Nickname))
				}
			} else {
				r.SetToast("Odada su an ekran paylasan kimse yok")
			}
		})

		// Sound Test Panel Button [T]
		testLabel := "[T] Test"
		testStyle := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
			Bg:       cell.NewColorRGB(0x10, 0x14, 0x20),
			Modifier: cell.ModifierBold,
		}
		testX := watchX + watchLen + 2
		testLen := uint16(len([]rune(testLabel)))
		if testX+testLen <= ctrlInner.X+ctrlInner.Width {
			buf.SetString(testX, row2Y, testLabel, testStyle)
			frame.RegisterClickHandler(cell.NewRect(testX, row2Y, testLen, 1), func(_ backend.MouseEvent) {
				if r.OnOpenTestModal != nil {
					r.OnOpenTestModal()
				}
			})
		}

		// Volume Controls [+/-]
		gainText := fmt.Sprintf("[+/-] Ses: %.0f%%", audio.Gain*100)
		gainX := testX + testLen + 2
		gainLen := uint16(len([]rune(gainText)))
		if gainX+gainLen <= ctrlInner.X+ctrlInner.Width {
			buf.SetString(gainX, row2Y, gainText, cell.Style{Fg: cell.NewColorRGB(0xFF, 0xE6, 0x6D), Bg: cell.NewColorRGB(0x10, 0x14, 0x20)})
			frame.RegisterClickHandler(cell.NewRect(gainX, row2Y, gainLen, 1), func(_ backend.MouseEvent) {
				gain := audio.AdjustGain(0.1)
				if gain > 2.0 {
					audio.AdjustGain(-1.5)
				}
				r.SetToast(fmt.Sprintf("Mikrofon Sesi: %.0f%%", audio.Gain*100))
			})
		}

		// Copy Code [C]
		copyText := "[C] Kopyala"
		copyX := gainX + gainLen + 2
		copyLen := uint16(len([]rune(copyText)))
		if copyX+copyLen <= ctrlInner.X+ctrlInner.Width {
			buf.SetString(copyX, row2Y, copyText, cell.Style{Fg: cell.NewColorRGB(0x00, 0xD2, 0xD3), Bg: cell.NewColorRGB(0x10, 0x14, 0x20)})
			frame.RegisterClickHandler(cell.NewRect(copyX, row2Y, copyLen, 1), func(_ backend.MouseEvent) {
				CopyToClipboard(node.RoomCode)
				r.SetToast(fmt.Sprintf("Oda Kodu Kopyalandi: %s", node.RoomCode))
			})
		}

		// Leave Room [Esc]
		leaveText := "[Esc] Ayril"
		leaveLen := uint16(len([]rune(leaveText)))
		leaveX := copyX + copyLen + 2
		if ctrlInner.Width >= leaveX-ctrlInner.X+leaveLen {
			if ctrlInner.X+ctrlInner.Width-leaveLen-1 > leaveX {
				leaveX = ctrlInner.X + ctrlInner.Width - leaveLen - 1
			}
			buf.SetString(leaveX, row2Y, leaveText, cell.Style{Fg: cell.NewColorRGB(0xD6, 0x30, 0x31), Bg: cell.NewColorRGB(0x10, 0x14, 0x20), Modifier: cell.ModifierBold})
			frame.RegisterClickHandler(cell.NewRect(leaveX, row2Y, leaveLen, 1), func(_ backend.MouseEvent) {
				if r.OnLeave != nil {
					r.OnLeave()
				}
			})
		}
	}

	// 2. Logs & Toast Area
	logBlock := widgets.Block{
		Title:         " ODA GUNLUGU VE ETKINLIKLER ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: cell.NewColorRGB(0x63, 0x6E, 0x72)},
		Style:         cell.Style{Bg: cell.NewColorRGB(0x10, 0x14, 0x20)},
	}
	frame.RenderWidget(logBlock, logArea)
	logInner := logBlock.Inner(logArea)

	for y := logInner.Y; y < logInner.Y+logInner.Height; y++ {
		for x := logInner.X; x < logInner.X+logInner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}})
		}
	}

	maxW := int(logInner.Width) - 2
	if maxW < 5 {
		maxW = 5
	}

	truncate := func(s string, limit int) string {
		runes := []rune(s)
		if len(runes) <= limit {
			return s
		}
		if limit <= 3 {
			return string(runes[:limit])
		}
		return string(runes[:limit-1]) + "…"
	}

	if r.ToastMsg != "" {
		toastStyle := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Modifier: cell.ModifierBold,
		}
		toastText := truncate("  "+r.ToastMsg+"  ", maxW)
		buf.SetString(logInner.X+1, logInner.Y, toastText, toastStyle)
	} else if len(r.Logs) > 0 {
		startIdx := 0
		if len(r.Logs) > int(logInner.Height) {
			startIdx = len(r.Logs) - int(logInner.Height)
		}
		for i, line := range r.Logs[startIdx:] {
			if uint16(i) < logInner.Height {
				safeLine := truncate(line, maxW)
				buf.SetString(logInner.X+1, logInner.Y+uint16(i), safeLine, cell.Style{Fg: cell.NewColorRGB(0xB2, 0xBE, 0xC3), Bg: cell.NewColorRGB(0x10, 0x14, 0x20)})
			}
		}
	} else {
		placeholder := truncate("Baglanti bekleniyor... Arkadasiniz odaya katildiginda burada gorunecek.", maxW)
		buf.SetString(logInner.X+1, logInner.Y, placeholder, cell.Style{Fg: cell.NewColorRGB(0x63, 0x6E, 0x72), Bg: cell.NewColorRGB(0x10, 0x14, 0x20)})
	}
}
