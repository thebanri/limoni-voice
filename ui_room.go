package main

import (
	"fmt"
	"time"

	"github.com/thebanri/limoni/core/backend"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/layout"
	"github.com/thebanri/limoni/widgets"
)

type RoomView struct {
	StartTime       time.Time
	ToastMsg        string
	ToastTimer      int
	Logs            []string
	OnLeave         func()
	OnOpenTestModal func()
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
	fl := layout.NewFlexLayout(layout.Vertical, 0,
		layout.Fixed(3), // Header
		layout.Fill(),   // 2x2 Participant Cards
		layout.Fixed(7), // Controls & Mini Logs
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

	peers := node.GetPeersList()

	// Slot 0 (Top-Left): Local User (Self)
	r.renderLocalSlot(frame, topCols[0], node, audio)

	// Slot 1 (Top-Right): Peer 1
	if len(peers) > 0 {
		r.renderPeerSlot(frame, topCols[1], peers[0], audio, 2)
	} else {
		r.renderEmptySlot(frame, topCols[1], node.RoomCode, 2)
	}

	// Slot 2 (Bottom-Left): Peer 2
	if len(peers) > 1 {
		r.renderPeerSlot(frame, botCols[0], peers[1], audio, 3)
	} else {
		r.renderEmptySlot(frame, botCols[0], node.RoomCode, 3)
	}

	// Slot 3 (Bottom-Right): Peer 3
	if len(peers) > 2 {
		r.renderPeerSlot(frame, botCols[1], peers[2], audio, 4)
	} else {
		r.renderEmptySlot(frame, botCols[1], node.RoomCode, 4)
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

	if inner.Height >= 2 && inner.Width >= 4 {
		meterRect := cell.Rect{
			X:      inner.X + 1,
			Y:      inner.Y + 1,
			Width:  inner.Width - 2,
			Height: inner.Height - 1,
		}
		DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, "SES SEVIYESI")
	}
}

func (r *RoomView) renderPeerSlot(frame *terminal.Frame, area cell.Rect, peer *PeerInfo, audio *AudioEngine, slotNum int) {
	borderStyle := cell.Style{Fg: cell.NewColorRGB(0x6C, 0x5C, 0xE7)}
	statusText := "[DINLIYOR]"
	statusStyle := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}

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

	if inner.Height >= 2 && inner.Width >= 4 {
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

	// --- ROW 2: Tools & Room Actions ---
	if ctrlInner.Height >= 2 {
		// Sound Test Panel Button [T]
		testLabel := "[T] Ses Testi"
		testStyle := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
			Bg:       cell.NewColorRGB(0x10, 0x14, 0x20),
			Modifier: cell.ModifierBold,
		}
		testX := ctrlInner.X + 1
		testLen := uint16(len([]rune(testLabel)))
		buf.SetString(testX, row2Y, testLabel, testStyle)
		frame.RegisterClickHandler(cell.NewRect(testX, row2Y, testLen, 1), func(_ backend.MouseEvent) {
			if r.OnOpenTestModal != nil {
				r.OnOpenTestModal()
			}
		})

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
		copyText := "[C] Kodu Kopyala"
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
		leaveText := "[Esc] Odadan Ayril"
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
