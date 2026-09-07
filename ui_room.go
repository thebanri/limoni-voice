package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/thebanri/limoni/core/backend"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/layout"
	"github.com/thebanri/limoni/widgets"
	"github.com/thebanri/limoni-voice/screenshare"
)

type RoomMessage struct {
	Timestamp time.Time
	Sender    string
	SenderID  string
	Text      string
	IsChat    bool
	IsSelf    bool
}

type RoomView struct {
	mu                     sync.Mutex
	StartTime              time.Time
	ToastMsg               string
	ToastTimer             int
	Logs                   []string
	Messages               []RoomMessage
	ChatInputState         *widgets.TextInputState
	IsChatFocused          bool
	ChatScrollOffset       int
	UnreadChatCount        int
	chatHistory            []string
	historyIndex           int
	savedCurrentChat       string
	IsCompactMode          bool
	OnLeave                func()
	OnOpenTestModal        func()
	OnOpenScreenShareModal func()
	OnSendChat             func(text string)
	OnSendFile             func(filePath string)
	OnSendCode             func(title, code string)
	OnLockRoom             func(pin string)
	OnUnlockRoom           func()
	OnSetPeerVolume        func(target string, vol float64)
	OnAdjustPeerVolume     func(peerID string, delta float64)
	OnOpenEditor           func(filePath string)
	OnOpenFolder           func(dirPath string)
	OnTriggerHop           func()
	OnChangeNick           func(newNick string)
	OnTriggerMute          func()
	OnTriggerDeafen        func()
	OnTriggerSFX           func()
	LastStageArea          cell.Rect
	LastLogArea            cell.Rect
	SelectionActive        bool
	SelectionDragging      bool
	SelectionStartX        int
	SelectionStartY        int
	SelectionEndX          int
	SelectionEndY          int
	SelectedText           string
	renderedLines          []renderedChatLine
}

type renderedChatChar struct {
	X uint16
	Y uint16
	R rune
}

type renderedChatLine struct {
	RowY   uint16
	StartX uint16
	EndX   uint16
	Chars  []renderedChatChar
}

func NewRoomView() *RoomView {
	return &RoomView{
		StartTime:      time.Now(),
		Logs:           make([]string, 0),
		Messages:       make([]RoomMessage, 0, 64),
		ChatInputState: widgets.NewTextInputState(),
		chatHistory:    make([]string, 0, 32),
		renderedLines:  make([]renderedChatLine, 0, 64),
	}
}

func (r *RoomView) AddLog(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	ts := now.Format("15:04:05")
	formatted := fmt.Sprintf("[%s] %s", ts, msg)
	r.Logs = append(r.Logs, formatted)
	if len(r.Logs) > 200 {
		r.Logs = r.Logs[len(r.Logs)-100:]
	}
	r.Messages = append(r.Messages, RoomMessage{
		Timestamp: now,
		Text:      msg,
		IsChat:    false,
	})
	if len(r.Messages) > 300 {
		r.Messages = r.Messages[len(r.Messages)-200:]
	}
	r.ChatScrollOffset = 0
}

func (r *RoomView) AddChatMessage(nickname string, senderID string, text string, isSelf bool, ts time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ts.IsZero() {
		ts = time.Now()
	}
	formattedTs := ts.Format("15:04:05")
	dispName := nickname
	if isSelf {
		dispName = "You"
	}
	r.Logs = append(r.Logs, fmt.Sprintf("[%s] %s: %s", formattedTs, dispName, text))
	if len(r.Logs) > 200 {
		r.Logs = r.Logs[len(r.Logs)-100:]
	}
	r.Messages = append(r.Messages, RoomMessage{
		Timestamp: ts,
		Sender:    nickname,
		SenderID:  senderID,
		Text:      text,
		IsChat:    true,
		IsSelf:    isSelf,
	})
	if len(r.Messages) > 300 {
		r.Messages = r.Messages[len(r.Messages)-200:]
	}
	if !r.IsChatFocused && !isSelf {
		r.UnreadChatCount++
	}
	r.ChatScrollOffset = 0
}

func (r *RoomView) SetChatFocused(focused bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.IsChatFocused = focused
	if focused {
		r.UnreadChatCount = 0
		r.historyIndex = len(r.chatHistory)
		r.savedCurrentChat = ""
	}
}

func (r *RoomView) HistoryUp() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.chatHistory) == 0 {
		return
	}
	if r.historyIndex == len(r.chatHistory) {
		r.savedCurrentChat = r.ChatInputState.Value()
	}
	if r.historyIndex > 0 {
		r.historyIndex--
		r.ChatInputState.SetValue(r.chatHistory[r.historyIndex])
	}
}

func (r *RoomView) HistoryDown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.chatHistory) == 0 || r.historyIndex >= len(r.chatHistory) {
		return
	}
	r.historyIndex++
	if r.historyIndex < len(r.chatHistory) {
		r.ChatInputState.SetValue(r.chatHistory[r.historyIndex])
	} else {
		r.ChatInputState.SetValue(r.savedCurrentChat)
	}
}

func (r *RoomView) SendCurrentChat() {
	r.mu.Lock()
	text := strings.TrimSpace(r.ChatInputState.Value())
	if text == "" {
		r.mu.Unlock()
		return
	}
	r.ChatInputState.SetValue("")
	r.chatHistory = append(r.chatHistory, text)
	if len(r.chatHistory) > 100 {
		r.chatHistory = r.chatHistory[len(r.chatHistory)-50:]
	}
	r.historyIndex = len(r.chatHistory)
	r.savedCurrentChat = ""

	// Slash commands
	if strings.HasPrefix(text, "/") {
		parts := strings.Fields(text)
		cmd := strings.ToLower(parts[0])
		switch cmd {
		case "/clear", "/c":
			r.Messages = make([]RoomMessage, 0, 64)
			r.Logs = make([]string, 0)
			r.ToastMsg = "Chat & room logs cleared"
			r.ToastTimer = 60
			r.mu.Unlock()
			return

		case "/help", "/?":
			r.Messages = append(r.Messages, RoomMessage{
				Timestamp: time.Now(),
				Text:      "Commands: /vol [user] [0-200], /send <path>, /code <snippet>, /folder, /lock [pin], /unlock, /compact, /mute, /deafen, /sfx, /hop, /nick <name>, /clear",
				IsChat:    false,
			})
			r.mu.Unlock()
			return

		case "/folder", "/downloads", "/files", "/dir":
			openFolderCb := r.OnOpenFolder
			r.mu.Unlock()
			if openFolderCb != nil {
				openFolderCb(GetLimoniTransfersDir())
			} else {
				_ = OpenFolder(GetLimoniTransfersDir())
			}
			return

		case "/vol", "/volume":
			if len(parts) < 2 {
				r.Messages = append(r.Messages, RoomMessage{
					Timestamp: time.Now(),
					Text:      "Usage: /vol <user> <0-200> (e.g. /vol alice 150) or /vol <0-200>",
					IsChat:    false,
				})
				r.mu.Unlock()
				return
			}
			var target string
			var volVal int
			var err error
			if len(parts) == 2 {
				volVal, err = strconv.Atoi(parts[1])
				if err != nil {
					target = strings.TrimPrefix(parts[1], "@")
					volVal = 100
				}
			} else {
				target = strings.TrimPrefix(parts[1], "@")
				volVal, err = strconv.Atoi(parts[2])
				if err != nil {
					r.Messages = append(r.Messages, RoomMessage{
						Timestamp: time.Now(),
						Text:      "Invalid volume percentage. Use 0 - 200.",
						IsChat:    false,
					})
					r.mu.Unlock()
					return
				}
			}
			if volVal < 0 {
				volVal = 0
			} else if volVal > 200 {
				volVal = 200
			}
			setPeerVol := r.OnSetPeerVolume
			r.mu.Unlock()
			if setPeerVol != nil {
				setPeerVol(target, float64(volVal)/100.0)
			}
			return

		case "/send", "/file":
			if len(parts) < 2 {
				r.Messages = append(r.Messages, RoomMessage{
					Timestamp: time.Now(),
					Text:      "Usage: /send <file_path> (e.g. /send ./main.go)",
					IsChat:    false,
				})
				r.mu.Unlock()
				return
			}
			filePath := strings.Join(parts[1:], " ")
			sendFileCb := r.OnSendFile
			r.mu.Unlock()
			if sendFileCb != nil {
				go sendFileCb(filePath)
			}
			return

		case "/code", "/paste":
			if len(parts) < 2 {
				r.Messages = append(r.Messages, RoomMessage{
					Timestamp: time.Now(),
					Text:      "Usage: /code <snippet_or_file> (e.g. /code main.go or /code fmt.Println(\"hi\"))",
					IsChat:    false,
				})
				r.mu.Unlock()
				return
			}
			rawSnippet := strings.TrimSpace(strings.TrimPrefix(text, parts[0]+" "))
			sendCodeCb := r.OnSendCode
			r.mu.Unlock()
			if sendCodeCb != nil {
				// Check if rawSnippet is a path to an existing local file
				trimmedPath := strings.Trim(rawSnippet, "\"'")
				if fi, err := os.Stat(trimmedPath); err == nil && !fi.IsDir() {
					if content, err := os.ReadFile(trimmedPath); err == nil {
						go sendCodeCb(filepath.Base(trimmedPath), string(content))
						return
					}
				}

				// Infer file extension from snippet content
				title := "snippet.txt"
				if strings.Contains(rawSnippet, "package ") || strings.Contains(rawSnippet, "func ") {
					title = "snippet.go"
				} else if strings.Contains(rawSnippet, "def ") || strings.Contains(rawSnippet, "import ") {
					title = "snippet.py"
				} else if strings.Contains(rawSnippet, "#include") {
					title = "snippet.c"
				} else if strings.Contains(rawSnippet, "function") || strings.Contains(rawSnippet, "const ") || strings.Contains(rawSnippet, "let ") {
					title = "snippet.js"
				} else if strings.Contains(rawSnippet, "<html>") || strings.Contains(rawSnippet, "</div>") {
					title = "snippet.html"
				}

				go sendCodeCb(title, rawSnippet)
			}
			return

		case "/lock":
			var pin string
			if len(parts) >= 2 {
				pin = parts[1]
			}
			lockCb := r.OnLockRoom
			r.mu.Unlock()
			if lockCb != nil {
				lockCb(pin)
			}
			return

		case "/unlock":
			unlockCb := r.OnUnlockRoom
			r.mu.Unlock()
			if unlockCb != nil {
				unlockCb()
			}
			return

		case "/compact", "/hud", "/mini":
			newVal := ToggleCompactHUD()
			r.IsCompactMode = newVal
			if newVal {
				r.ToastMsg = "Compact HUD Mode Enabled"
			} else {
				r.ToastMsg = "Full UI Restored"
			}
			r.ToastTimer = 90
			r.mu.Unlock()
			return

		case "/mute", "/m":
			if len(parts) >= 2 {
				sub := strings.ToLower(parts[1])
				if sub == "sfx" || sub == "sound" || sub == "chat" {
					sfxCb := r.OnTriggerSFX
					r.mu.Unlock()
					if sfxCb != nil {
						sfxCb()
					}
					return
				} else if sub == "deafen" || sub == "all" {
					deafenCb := r.OnTriggerDeafen
					r.mu.Unlock()
					if deafenCb != nil {
						deafenCb()
					}
					return
				}
			}
			muteCb := r.OnTriggerMute
			r.mu.Unlock()
			if muteCb != nil {
				muteCb()
			}
			return

		case "/deafen", "/d":
			deafenCb := r.OnTriggerDeafen
			r.mu.Unlock()
			if deafenCb != nil {
				deafenCb()
			}
			return

		case "/sound", "/sfx":
			sfxCb := r.OnTriggerSFX
			r.mu.Unlock()
			if sfxCb != nil {
				sfxCb()
			}
			return

		case "/hop":
			hopCb := r.OnTriggerHop
			r.ToastMsg = "[SECURITY] Port hop triggered"
			r.ToastTimer = 90
			r.mu.Unlock()
			if hopCb != nil {
				go hopCb()
			}
			return
		case "/nick":
			if len(parts) >= 2 {
				newNick := strings.Join(parts[1:], " ")
				changeNick := r.OnChangeNick
				r.ToastMsg = fmt.Sprintf("Nickname changed to %s", newNick)
				r.ToastTimer = 90
				r.mu.Unlock()
				if changeNick != nil {
					go changeNick(newNick)
				}
				return
			}
			r.Messages = append(r.Messages, RoomMessage{
				Timestamp: time.Now(),
				Text:      "⚠️ Usage: /nick <new_name>",
				IsChat:    false,
			})
			r.mu.Unlock()
			return
		}
	}

	onSend := r.OnSendChat
	r.mu.Unlock()

	if onSend != nil {
		onSend(text)
	}
}

func (r *RoomView) ScrollChat(delta int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ChatScrollOffset += delta
	if r.ChatScrollOffset < 0 {
		r.ChatScrollOffset = 0
	}
	if r.ChatScrollOffset > len(r.Messages) {
		r.ChatScrollOffset = len(r.Messages)
	}
}

func (r *RoomView) SetToast(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ToastMsg = msg
	r.ToastTimer = 90 // ~3 seconds at 30 FPS
}

func (r *RoomView) Update() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ToastTimer > 0 {
		r.ToastTimer--
		if r.ToastTimer == 0 {
			r.ToastMsg = ""
		}
	}
}

func (r *RoomView) Render(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	if r.IsCompactMode || GetCompactHUD() || area.Height <= 6 {
		r.renderCompactHUD(frame, area, node, audio)
		return
	}

	footerHeight := uint16(8)
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
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         " LIMONI VOICE ROOM ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.BorderFocused},
		Style:         cell.Style{Bg: theme.HeaderBg},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.HeaderBg}})
		}
	}

	peers := node.GetPeersList()
	totalCount := len(peers) + 1 // +1 for self

	durStr := fmt.Sprintf("Duration: %s", formatDuration(time.Since(r.StartTime)))
	durLen := uint16(len([]rune(durStr)))
	durX := inner.X + inner.Width - durLen - 1

	if inner.Width > durLen+4 {
		buf.SetString(durX, inner.Y, durStr, cell.Style{
			Fg: theme.TextMuted,
			Bg: theme.HeaderBg,
		})
	}

	curX := inner.X + 1
	limitX := durX - 2
	if inner.Width <= durLen+4 {
		limitX = inner.X + inner.Width
	}

	// 1. App Title
	titleStr := "LIMONI VOICE"
	titleLen := uint16(len([]rune(titleStr)))
	if curX+titleLen <= limitX {
		buf.SetString(curX, inner.Y, titleStr, cell.Style{
			Fg:       theme.Accent,
			Bg:       theme.HeaderBg,
			Modifier: cell.ModifierBold,
		})
		curX += titleLen + 2
	}

	// 2. Room Code Badge
	codeBadge := fmt.Sprintf(" Room: %s ", node.RoomCode)
	codeLen := uint16(len([]rune(codeBadge)))
	if curX+codeLen <= limitX {
		buf.SetString(curX, inner.Y, codeBadge, cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		})
		badgeRect := cell.NewRect(curX, inner.Y, codeLen, 1)
		frame.RegisterClickHandler(badgeRect, func(_ backend.MouseEvent) {
			CopyToClipboard(node.RoomCode)
			r.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))
		})
		curX += codeLen + 2
	}

	// 3. Role Badge
	var roleBadge string
	var roleStyle cell.Style
	if node.IsHost {
		roleBadge = " HOST (YOU) "
		roleStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}
	} else {
		hostName := node.HostNick
		if hostName == "" {
			hostName = "Host"
		}
		roleBadge = fmt.Sprintf(" MEMBER (Host: %s) ", hostName)
		roleStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Secondary,
			Modifier: cell.ModifierBold,
		}
	}
	roleLen := uint16(len([]rune(roleBadge)))
	if curX+roleLen <= limitX {
		buf.SetString(curX, inner.Y, roleBadge, roleStyle)
		curX += roleLen + 2
	}

	// 4. Member Count
	countStr := fmt.Sprintf("Members: %d/4", totalCount)
	countLen := uint16(len([]rune(countStr)))
	if curX+countLen <= limitX {
		buf.SetString(curX, inner.Y, countStr, cell.Style{
			Fg:       theme.Success,
			Bg:       theme.HeaderBg,
			Modifier: cell.ModifierBold,
		})
		curX += countLen + 2
	}

	// 5. Port / Security Badge
	remHop := node.NextHopRemaining()
	var hopMin int
	if remHop > 0 {
		hopMin = int(remHop.Minutes())
	}
	portBadge := fmt.Sprintf(" Port: :%d (%dm) ", node.Port, hopMin)
	portLen := uint16(len([]rune(portBadge)))
	if curX+portLen <= limitX {
		buf.SetString(curX, inner.Y, portBadge, cell.Style{
			Fg:       theme.Accent,
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		})
		pRect := cell.NewRect(curX, inner.Y, portLen, 1)
		frame.RegisterClickHandler(pRect, func(_ backend.MouseEvent) {
			r.SetToast(fmt.Sprintf("Port Hopping Active: Port :%d (Next in %dm, Epoch %d)", node.Port, hopMin, node.currentEpoch))
		})
		curX += portLen + 2
	}

	// 6. Lock Status Badge
	if node.IsLocked {
		var lockBadge string
		if node.IsHost && node.RoomPIN != "" {
			lockBadge = fmt.Sprintf(" LOCKED (PIN: %s) ", node.RoomPIN)
		} else {
			lockBadge = " LOCKED "
		}
		lockLen := uint16(len([]rune(lockBadge)))
		if curX+lockLen <= limitX {
			buf.SetString(curX, inner.Y, lockBadge, cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Danger,
				Modifier: cell.ModifierBold,
			})
			lRect := cell.NewRect(curX, inner.Y, lockLen, 1)
			frame.RegisterClickHandler(lRect, func(_ backend.MouseEvent) {
				if node.IsHost {
					node.UnlockRoom()
					r.SetToast("Room unlocked")
				} else {
					r.SetToast("Room is locked by host")
				}
			})
			curX += lockLen + 2
		}
	}
}

func (r *RoomView) renderGrid(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	peers := node.GetPeersList()

	// Find all peers sharing screen in the room
	var streamingPeers []*PeerInfo
	for _, p := range peers {
		if p.IsSharingScreen {
			streamingPeers = append(streamingPeers, p)
		}
	}

	isStreamActive := (len(streamingPeers) > 0) || node.IsSharingScreen || node.IsWatchingScreen

	// If a screen is being shared, switch to Discord-style Layout (Sidebar Members on Left + Big Stream Stage on Right)
	if isStreamActive {
		fl := layout.NewFlexLayout(layout.Horizontal, 0,
			layout.Percentage(32), // Left: Discord Voice Channel Members
			layout.Percentage(68), // Right: Big Stream Stage
		)
		splits := fl.Split(area)
		if len(splits) >= 2 {
			r.renderSidebarMembers(frame, splits[0], node, audio, peers)
			r.renderStreamStage(frame, splits[1], streamingPeers, node)
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
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         " VOICE CHANNEL MEMBERS ",
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

	// Calculate height per slot
	totalSlots := len(peers) + 1
	slotHeight := int(inner.Height) / totalSlots
	if slotHeight < 3 {
		slotHeight = 3
	}

	currY := inner.Y

	// 1. Self Slot
	selfCard := cell.Rect{X: inner.X, Y: currY, Width: inner.Width, Height: uint16(slotHeight)}
	r.renderMemberMiniCard(frame, selfCard, node.Nickname+" (YOU)", audio.LocalRMS, audio.IsSpeaking, audio.Muted, audio.Deafened, node.IsSharingScreen, false, 0, false, true)
	currY += uint16(slotHeight)

	// 2. Peers Slots
	for _, peer := range peers {
		if currY+uint16(slotHeight) > inner.Y+inner.Height {
			break
		}
		peerCard := cell.Rect{X: inner.X, Y: currY, Width: inner.Width, Height: uint16(slotHeight)}
		isReconnecting := time.Since(peer.LastSeen) > 8000*time.Millisecond
		isBeingWatched := node.IsWatchingScreen && node.WatchingPeerID == peer.ID
		r.renderMemberMiniCard(frame, peerCard, peer.Nickname, peer.RMS, peer.Speaking, peer.IsMuted, peer.IsDeafened, peer.IsSharingScreen, isBeingWatched, peer.PingMs, isReconnecting, false)

		if peer.IsSharingScreen {
			targetPeer := peer
			frame.RegisterClickHandler(peerCard, func(_ backend.MouseEvent) {
				port := targetPeer.VideoPort
				if port <= 0 {
					port = 50100
				}
				opts := screenshare.ReceiverOptions{
					WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (HD 60 FPS)", targetPeer.Nickname),
				}
				r.SetToast(fmt.Sprintf("Starting %s stream...", targetPeer.Nickname))
				go func() {
					err := node.StartWatchingScreen(targetPeer.ID, port, opts)
					if err != nil {
						r.SetToast(fmt.Sprintf("Error: %v", err))
					} else {
						r.SetToast(fmt.Sprintf("%s stream opened (HD 60 FPS)", targetPeer.Nickname))
					}
				}()
			})
		}
		currY += uint16(slotHeight)
	}
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

func (r *RoomView) renderMemberMiniCard(frame *terminal.Frame, area cell.Rect, name string, rms float64, isSpeaking, isMuted, isDeafened, isSharing, isBeingWatched bool, pingMs int64, isReconnecting bool, isSelf bool) {
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
		titleText += " [WATCHING]"
	} else if isSharing {
		titleText += " [LIVE]"
	}
	buf.SetString(area.X+1, area.Y, titleText, nameStyle)

	// Status Line / Ping
	statusStr := ""
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
	if isReconnecting {
		statusStr = "[Reconnecting...]"
		statusStyle.Fg = theme.Warning
	} else if isDeafened {
		statusStr = "[Deafened]"
		statusStyle.Fg = theme.Warning
	} else if isMuted {
		statusStr = "[Muted]"
		statusStyle.Fg = theme.Danger
	} else if isSpeaking {
		statusStr = "[Speaking]"
		statusStyle.Fg = theme.Success
	} else {
		statusStr = "[Connected]"
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

func (r *RoomView) renderStreamStage(frame *terminal.Frame, area cell.Rect, streamingPeers []*PeerInfo, node *P2PNode) {
	theme := CurrentTheme()
	stageTitle := " LIVE STREAM STAGE "
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

	// 1. Case: We are watching a peer's stream (Active in native HD player window)
	if node.IsWatchingScreen {
		watchedNick := node.WatchingPeerNick
		if watchedNick == "" {
			if p := node.GetPeer(node.WatchingPeerID); p != nil {
				watchedNick = p.Nickname
			} else if len(streamingPeers) > 0 {
				watchedNick = streamingPeers[0].Nickname
			} else {
				watchedNick = "Stream"
			}
		}

		topBarText := fmt.Sprintf(" %s'S LIVE STREAM ACTIVE (HD 60 FPS) ", strings.ToUpper(watchedNick))
		buf.SetString(inner.X+3, inner.Y+1, topBarText, cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})

		msg1 := "Playing in high-performance hardware-accelerated video window."
		msg2 := "Press [W] / [Esc] to close viewer, or click the stop button below."
		buf.SetString(inner.X+3, inner.Y+3, msg1, cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg})
		buf.SetString(inner.X+3, inner.Y+4, msg2, cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg})

		btnText := "   [W] STOP WATCHING (Click)   "
		btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Danger, Modifier: cell.ModifierBold}
		buf.SetString(inner.X+3, inner.Y+6, btnText, btnStyle)

		frame.RegisterClickHandler(cell.NewRect(inner.X+3, inner.Y+6, uint16(len([]rune(btnText))), 1), func(_ backend.MouseEvent) {
			_ = node.StopWatchingScreen()
			r.SetToast("Screen viewer closed")
		})

		// Show other streams in room to switch easily
		otherPeers := make([]*PeerInfo, 0)
		for _, p := range streamingPeers {
			if p.ID != node.WatchingPeerID {
				otherPeers = append(otherPeers, p)
			}
		}

		if len(otherPeers) > 0 {
			switchY := inner.Y + 8
			buf.SetString(inner.X+3, switchY, "Switch to another live stream:", cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
			switchY += 1
			for idx, p := range otherPeers {
				btnRowY := switchY + uint16(idx*2)
				if btnRowY >= inner.Y+inner.Height {
					break
				}
				swBtnText := fmt.Sprintf("   ► Switch to %s's Stream (HD 60 FPS)   ", p.Nickname)
				swBtnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Secondary, Modifier: cell.ModifierBold}
				buf.SetString(inner.X+3, btnRowY, swBtnText, swBtnStyle)

				targetPeer := p
				frame.RegisterClickHandler(cell.NewRect(inner.X+3, btnRowY, uint16(len([]rune(swBtnText))), 1), func(_ backend.MouseEvent) {
					port := targetPeer.VideoPort
					if port <= 0 {
						port = 50100
					}
					opts := screenshare.ReceiverOptions{
						WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (HD 60 FPS)", targetPeer.Nickname),
					}
					r.SetToast(fmt.Sprintf("Switching to %s...", targetPeer.Nickname))
					go func() {
						err := node.StartWatchingScreen(targetPeer.ID, port, opts)
						if err != nil {
							r.SetToast(fmt.Sprintf("Error: %v", err))
						} else {
							r.SetToast(fmt.Sprintf("Switched to %s (HD 60 FPS)", targetPeer.Nickname))
						}
					}()
				})
			}
		}
		return
	}

	// 2. Case: Local User is Broadcasting
	if node.IsSharingScreen {
		msg1 := "YOUR SCREEN IS LIVE (60 FPS - 1080p Full HD)"
		msg2 := "All room participants can watch your screen with ultra-low latency."
		btnText := "   [V] STOP BROADCAST (Click)   "

		buf.SetString(inner.X+3, inner.Y+2, msg1, cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
		buf.SetString(inner.X+3, inner.Y+4, msg2, cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg})

		btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Danger, Modifier: cell.ModifierBold}
		buf.SetString(inner.X+3, inner.Y+6, btnText, btnStyle)

		frame.RegisterClickHandler(cell.NewRect(inner.X+3, inner.Y+6, uint16(len([]rune(btnText))), 1), func(_ backend.MouseEvent) {
			_ = node.StopScreenShare()
			r.SetToast("Screen share stopped")
		})

		// If other peers are ALSO broadcasting, allow watching them too
		if len(streamingPeers) > 0 {
			switchY := inner.Y + 9
			buf.SetString(inner.X+3, switchY, "Other Members Streaming in Room (Click to watch):", cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
			switchY += 1
			for idx, p := range streamingPeers {
				if switchY+uint16(idx*2) >= inner.Y+inner.Height {
					break
				}
				btnRowY := switchY + uint16(idx*2)
				swBtnText := fmt.Sprintf("   ► Watch %s's Stream   ", p.Nickname)
				swBtnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold}
				buf.SetString(inner.X+3, btnRowY, swBtnText, swBtnStyle)

				targetPeer := p
				frame.RegisterClickHandler(cell.NewRect(inner.X+3, btnRowY, uint16(len([]rune(swBtnText))), 1), func(_ backend.MouseEvent) {
					port := targetPeer.VideoPort
					if port <= 0 {
						port = 50100
					}
					opts := screenshare.ReceiverOptions{
						WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (HD 60 FPS)", targetPeer.Nickname),
					}
					r.SetToast(fmt.Sprintf("Starting %s stream...", targetPeer.Nickname))
					go func() {
						err := node.StartWatchingScreen(targetPeer.ID, port, opts)
						if err != nil {
							r.SetToast(fmt.Sprintf("Error: %v", err))
						} else {
							r.SetToast(fmt.Sprintf("%s stream opened (HD 60 FPS)", targetPeer.Nickname))
						}
					}()
				})
			}
		}
		return
	}

	// 3. Case: One or More Peers are Broadcasting (Idle watcher)
	if len(streamingPeers) == 1 {
		p := streamingPeers[0]
		msg1 := fmt.Sprintf("%s IS SHARING SCREEN (60 FPS)", strings.ToUpper(p.Nickname))
		msg2 := "Click the button below to watch with 20ms ultra-low latency:"
		btnText := fmt.Sprintf("   ► [W] WATCH %s STREAM (Click)   ", strings.ToUpper(p.Nickname))

		buf.SetString(inner.X+3, inner.Y+2, msg1, cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
		buf.SetString(inner.X+3, inner.Y+4, msg2, cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg})

		btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold}
		buf.SetString(inner.X+3, inner.Y+6, btnText, btnStyle)

		frame.RegisterClickHandler(cell.NewRect(inner.X+3, inner.Y+6, uint16(len([]rune(btnText))), 1), func(_ backend.MouseEvent) {
			port := p.VideoPort
			if port <= 0 {
				port = 50100
			}
			opts := screenshare.ReceiverOptions{
				WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (HD 60 FPS)", p.Nickname),
			}
			r.SetToast("Starting stream viewer...")
			go func() {
				err := node.StartWatchingScreen(p.ID, port, opts)
				if err != nil {
					r.SetToast(fmt.Sprintf("Error: %v", err))
				} else {
					r.SetToast(fmt.Sprintf("%s stream opened (HD 60 FPS)", p.Nickname))
				}
			}()
		})
		return
	} else if len(streamingPeers) > 1 {
		msg1 := fmt.Sprintf("%d MEMBERS ARE SHARING SCREEN IN THIS ROOM", len(streamingPeers))
		msg2 := "Select which member's live stream you want to watch:"

		buf.SetString(inner.X+3, inner.Y+2, msg1, cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
		buf.SetString(inner.X+3, inner.Y+3, msg2, cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg})

		listY := inner.Y + 5
		for idx, p := range streamingPeers {
			if listY+uint16(idx*2) >= inner.Y+inner.Height {
				break
			}
			btnRowY := listY + uint16(idx*2)
			btnText := fmt.Sprintf("   ► WATCH %s'S LIVE STREAM (HD 60 FPS)   ", strings.ToUpper(p.Nickname))
			btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold}
			buf.SetString(inner.X+3, btnRowY, btnText, btnStyle)

			targetPeer := p
			frame.RegisterClickHandler(cell.NewRect(inner.X+3, btnRowY, uint16(len([]rune(btnText))), 1), func(_ backend.MouseEvent) {
				port := targetPeer.VideoPort
				if port <= 0 {
					port = 50100
				}
				opts := screenshare.ReceiverOptions{
					WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (HD 60 FPS)", targetPeer.Nickname),
				}
				r.SetToast(fmt.Sprintf("Starting %s stream...", targetPeer.Nickname))
				go func() {
					err := node.StartWatchingScreen(targetPeer.ID, port, opts)
					if err != nil {
						r.SetToast(fmt.Sprintf("Error: %v", err))
					} else {
						r.SetToast(fmt.Sprintf("%s stream opened (HD 60 FPS)", targetPeer.Nickname))
					}
				}()
			})
		}
		return
	}
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

func (r *RoomView) renderLocalSlot(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	theme := CurrentTheme()
	borderStyle := cell.Style{Fg: theme.BorderFocused}
	statusText := "[LISTENING]"
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.CardBg}

	if audio.Deafened {
		borderStyle = cell.Style{Fg: theme.Warning}
		statusText = "[DEAFENED]"
		statusStyle = cell.Style{Fg: theme.Warning, Bg: theme.CardBg}
	} else if audio.Muted {
		borderStyle = cell.Style{Fg: theme.Danger}
		statusText = "[MIC OFF]"
		statusStyle = cell.Style{Fg: theme.Danger, Bg: theme.CardBg}
	} else if audio.InputMode == InputModePushToTalk {
		if audio.IsTransmitting() {
			borderStyle = cell.Style{
				Fg:       theme.Success,
				Modifier: cell.ModifierBold,
			}
			statusText = "[PTT TALKING...]"
			statusStyle = cell.Style{
				Fg:       theme.Success,
				Bg:       theme.CardBg,
				Modifier: cell.ModifierBold,
			}
		} else {
			borderStyle = cell.Style{Fg: theme.BorderFocused}
			statusText = "[PTT IDLE (SPACE)]"
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
		statusText = "[SPEAKING...]"
		statusStyle = cell.Style{
			Fg:       theme.Success,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}
	}

	title := fmt.Sprintf(" [1] %s (YOU) ", node.Nickname)
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

	buf.SetString(inner.X+1, inner.Y, "Status: ", cell.Style{Fg: theme.Text, Bg: theme.CardBg})
	buf.SetString(inner.X+8, inner.Y, statusText, statusStyle)

	gainStr := fmt.Sprintf("Vol: %.0f%%", audio.Gain*100)
	if inner.Width > uint16(len([]rune(gainStr)))+1 {
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
		DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, "AUDIO LEVEL")

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

		bTitle := " LIVE: Sharing Your Screen (60 FPS) "
		if uint16(len([]rune(bTitle))) > bannerW {
			bTitle = " LIVE STREAMING "
		}
		buf.SetString(inner.X+2, bannerY, bTitle, bannerStyle)

		bAction := "   [V] Stop Broadcast (Click)   "
		bActionStyle := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Danger,
			Modifier: cell.ModifierBold,
		}
		buf.SetString(inner.X+2, bannerY+1, bAction, bActionStyle)

		frame.RegisterClickHandler(cell.NewRect(inner.X+1, bannerY, bannerW, 2), func(_ backend.MouseEvent) {
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
		DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, "AUDIO LEVEL")
	}
}

func (r *RoomView) renderPeerSlot(frame *terminal.Frame, area cell.Rect, peer *PeerInfo, node *P2PNode, audio *AudioEngine, slotNum int) {
	theme := CurrentTheme()
	borderStyle := cell.Style{Fg: theme.Border}
	statusText := "[LISTENING]"
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.CardBg}

	if peer.IsSharingScreen {
		borderStyle = cell.Style{
			Fg:       theme.Accent,
			Modifier: cell.ModifierBold,
		}
	}

	if time.Since(peer.LastSeen) > 3500*time.Millisecond {
		borderStyle = cell.Style{Fg: theme.Warning}
		statusText = "[RECONNECTING...]"
		statusStyle = cell.Style{
			Fg:       theme.Warning,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}
	} else if peer.IsDeafened {
		borderStyle = cell.Style{Fg: theme.Warning}
		statusText = "[DEAFENED]"
		statusStyle = cell.Style{Fg: theme.Warning, Bg: theme.CardBg}
	} else if peer.IsMuted {
		borderStyle = cell.Style{Fg: theme.Danger}
		statusText = "[MIC OFF]"
		statusStyle = cell.Style{Fg: theme.Danger, Bg: theme.CardBg}
	} else if peer.Speaking {
		borderStyle = cell.Style{
			Fg:       theme.Success,
			Modifier: cell.ModifierBold,
		}
		statusText = "[SPEAKING...]"
		statusStyle = cell.Style{
			Fg:       theme.Success,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}
	}

	title := fmt.Sprintf(" [%d] %s ", slotNum, peer.Nickname)
	if peer.IsSharingScreen {
		title = fmt.Sprintf(" [%d] %s 🔴 [LIVE STREAMING] ", slotNum, peer.Nickname)
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

	buf.SetString(inner.X+1, inner.Y, "Status: ", cell.Style{Fg: theme.Text, Bg: theme.CardBg})
	buf.SetString(inner.X+8, inner.Y, statusText, statusStyle)

	pingStr := fmt.Sprintf("PING: %dms", peer.PingMs)
	volVal := 1.0
	if audio != nil {
		volVal = audio.GetPeerVolume(peer.ID)
	}
	volPct := int(math.Round(volVal * 100))
	volStr := fmt.Sprintf("[VOL: %d%%]", volPct)
	volLen := uint16(len([]rune(volStr)))
	pingLen := uint16(len([]rune(pingStr)))

	if inner.Width > pingLen+volLen+4 {
		volX := inner.X + inner.Width - pingLen - volLen - 2
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
		frame.RegisterClickHandler(cell.NewRect(volX, inner.Y, volLen, 1), func(_ backend.MouseEvent) {
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

		buf.SetString(inner.X+inner.Width-pingLen-1, inner.Y, pingStr, cell.Style{Fg: theme.Success, Bg: theme.CardBg})
	} else if inner.Width > pingLen+1 {
		buf.SetString(inner.X+inner.Width-pingLen-1, inner.Y, pingStr, cell.Style{Fg: theme.Success, Bg: theme.CardBg})
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
		DrawVerticalLevelMeter(buf, meterRect, peer.RMS, peer.Speaking, peer.IsMuted, "AUDIO LEVEL")

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

		bTitle := fmt.Sprintf(" %s Sharing Screen (60 FPS)", peer.Nickname)
		if uint16(len([]rune(bTitle))) > bannerW {
			bTitle = fmt.Sprintf(" %s LIVE STREAM", peer.Nickname)
		}
		buf.SetString(inner.X+2, bannerY, bTitle, bannerBg)

		if node.IsWatchingScreen && node.WatchingPeerID == peer.ID {
			bBtnText := "   [W] Stop Watching (Click)   "
			bBtnStyle := cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Danger,
				Modifier: cell.ModifierBold,
			}
			buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
		} else if node.IsWatchingScreen {
			bBtnText := "   ► Switch to Stream (Click)   "
			bBtnStyle := cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Secondary,
				Modifier: cell.ModifierBold,
			}
			buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
		} else {
			bBtnText := "   ► [W] WATCH STREAM (Click)   "
			bBtnStyle := cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Accent,
				Modifier: cell.ModifierBold,
			}
			buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
		}

		// Click on preview banner to watch / stop watching
		frame.RegisterClickHandler(cell.NewRect(inner.X+1, bannerY, bannerW, 2), func(_ backend.MouseEvent) {
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
				opts := screenshare.ReceiverOptions{
					WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (HD 60 FPS)", peer.Nickname),
				}
				r.SetToast("🎬 Starting stream viewer...")
				go func() {
					err := node.StartWatchingScreen(peer.ID, port, opts)
					if err != nil {
						r.SetToast(fmt.Sprintf("Error: %v", err))
					} else {
						r.SetToast(fmt.Sprintf("%s stream opened (HD 60 FPS)", peer.Nickname))
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
		DrawVerticalLevelMeter(buf, meterRect, peer.RMS, peer.Speaking, peer.IsMuted, "AUDIO LEVEL")
	}
}

func (r *RoomView) renderEmptySlot(frame *terminal.Frame, area cell.Rect, roomCode string, slotNum int) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         fmt.Sprintf(" [%d] EMPTY SLOT (WAITING) ", slotNum),
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

	txt1 := "Invite Your Friend:"
	txt2 := fmt.Sprintf("Room Code: %s", roomCode)
	txt3 := "Press [C] to copy the code"

	frame.RegisterClickHandler(area, func(_ backend.MouseEvent) {
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
}

func (r *RoomView) renderFooter(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	fl := layout.NewFlexLayout(layout.Horizontal, 0,
		layout.Percentage(52), // controls
		layout.Percentage(48), // chat & room logs
	)
	cols := fl.Split(area)
	if len(cols) < 2 {
		return
	}

	ctrlArea := cols[0]
	logArea := cols[1]
	theme := CurrentTheme()

	// 1. Controls Panel
	ctrlBlock := widgets.Block{
		Title:         " CONTROLS ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.Accent},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(ctrlBlock, ctrlArea)
	ctrlInner := ctrlBlock.Inner(ctrlArea)

	buf := frame.Buffer
	for y := ctrlInner.Y; y < ctrlInner.Y+ctrlInner.Height; y++ {
		for x := ctrlInner.X; x < ctrlInner.X+ctrlInner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	row1Y := ctrlInner.Y
	row2Y := ctrlInner.Y + 2
	if ctrlInner.Height < 3 {
		row2Y = ctrlInner.Y + 1
	}

	// --- ROW 1: Audio Toggles & Noise Filter ---
	// Mute Button
	muteLabel := "[M] Mute Mic"
	muteStyle := cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg}
	if audio.Muted {
		muteLabel = "[M] Unmute Mic"
		muteStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Danger,
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
			r.SetToast("Microphone Off (Muted)")
		} else {
			r.SetToast("Microphone On")
		}
	})

	// Deafen Button
	deafenLabel := "[D] Deafen"
	deafenStyle := cell.Style{Fg: theme.Secondary, Bg: theme.SurfaceBg}
	if audio.Deafened {
		deafenLabel = "[D] Undeafen"
		deafenStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
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
				r.SetToast("Audio Off (Deafened)")
			} else {
				r.SetToast("Audio On")
			}
		})
	}

	// Push-to-Talk / Voice Activity Mode Button [P]
	modeLabel := "[P] Voice"
	modeStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
	if audio.InputMode == InputModePushToTalk {
		modeLabel = "[P] PTT"
		if audio.IsTransmitting() {
			modeStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Success,
				Modifier: cell.ModifierBold,
			}
		} else {
			modeStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Warning,
				Modifier: cell.ModifierBold,
			}
		}
	}
	modeX := deafenX + deafenLen + 2
	modeLen := uint16(len([]rune(modeLabel)))
	if modeX+modeLen <= ctrlInner.X+ctrlInner.Width {
		buf.SetString(modeX, row1Y, modeLabel, modeStyle)
		frame.RegisterClickHandler(cell.NewRect(modeX, row1Y, modeLen, 1), func(_ backend.MouseEvent) {
			m := audio.CycleInputMode()
			if m == InputModePushToTalk {
				r.SetToast(fmt.Sprintf("Mode: Push-to-Talk (Hold %s to talk)", audio.GetPTTKeyName()))
			} else {
				r.SetToast("Mode: Voice Activity (Always on / VAD)")
			}
		})
	}

	// Noise Suppression Button [N]
	noiseStr := audio.SuppressionModeString()
	noiseLabel := fmt.Sprintf("[N] Noise: %s", noiseStr)
	noiseStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
	if audio.SuppressionMode > 0 {
		noiseStyle = cell.Style{
			Fg:       theme.Success,
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		}
	}
	noiseX := modeX + modeLen + 2
	noiseLen := uint16(len([]rune(noiseLabel)))
	if noiseX+noiseLen <= ctrlInner.X+ctrlInner.Width {
		buf.SetString(noiseX, row1Y, noiseLabel, noiseStyle)
		frame.RegisterClickHandler(cell.NewRect(noiseX, row1Y, noiseLen, 1), func(_ backend.MouseEvent) {
			audio.CycleSuppressionMode()
			r.SetToast(fmt.Sprintf("Noise Filter: %s", audio.SuppressionModeString()))
		})
	}

	// Screen Share Button [V]
	screenLabel := "[V] Share Screen"
	screenStyle := cell.Style{Fg: theme.Secondary, Bg: theme.SurfaceBg}
	if node.IsSharingScreen {
		screenLabel = "[V] Stop Sharing"
		screenStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Danger,
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
				r.SetToast("Screen share stopped")
			} else if r.OnOpenScreenShareModal != nil {
				r.OnOpenScreenShareModal()
			} else {
				go func() {
					err := node.StartScreenShare("", 50100)
					if err != nil {
						r.SetToast(fmt.Sprintf("Error: %v", err))
					} else {
						r.SetToast("Screen share started (60 FPS)")
					}
				}()
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

		watchLabel := "[W] Watch Screen"
		watchStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
		if node.IsWatchingScreen {
			watchLabel = "[W] Stop Watching"
			watchStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Warning,
				Modifier: cell.ModifierBold,
			}
		} else if streamingPeer != nil {
			watchLabel = fmt.Sprintf("[W] Watch %s", streamingPeer.Nickname)
			watchStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Success,
				Modifier: cell.ModifierBold,
			}
		}

		watchX := ctrlInner.X + 1
		watchLen := uint16(len([]rune(watchLabel)))
		buf.SetString(watchX, row2Y, watchLabel, watchStyle)
		frame.RegisterClickHandler(cell.NewRect(watchX, row2Y, watchLen, 1), func(_ backend.MouseEvent) {
			if node.IsWatchingScreen {
				go func() {
					_ = node.StopWatchingScreen()
					r.SetToast("Screen viewer closed")
				}()
			} else if streamingPeer != nil {
				port := streamingPeer.VideoPort
				if port <= 0 {
					port = 50100
				}
				opts := screenshare.ReceiverOptions{
					WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (HD 60 FPS)", streamingPeer.Nickname),
				}
				r.SetToast("Starting stream viewer...")
				go func() {
					err := node.StartWatchingScreen(streamingPeer.ID, port, opts)
					if err != nil {
						r.SetToast(fmt.Sprintf("Error: %v", err))
					} else {
						r.SetToast(fmt.Sprintf("%s stream opened (HD 60 FPS)", streamingPeer.Nickname))
					}
				}()
			} else {
				r.SetToast("No one is sharing screen in this room")
			}
		})

		// Sound Test Panel Button [T]
		testLabel := "[T] Test"
		testStyle := cell.Style{
			Fg:       theme.Secondary,
			Bg:       theme.SurfaceBg,
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
		gainText := fmt.Sprintf("[+/-] Vol: %.0f%%", audio.Gain*100)
		gainX := testX + testLen + 2
		gainLen := uint16(len([]rune(gainText)))
		if gainX+gainLen <= ctrlInner.X+ctrlInner.Width {
			buf.SetString(gainX, row2Y, gainText, cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg})
			frame.RegisterClickHandler(cell.NewRect(gainX, row2Y, gainLen, 1), func(_ backend.MouseEvent) {
				gain := audio.AdjustGain(0.1)
				if gain > 3.0 {
					audio.AdjustGain(-2.5) // loop back from 300% to 50%
				}
				r.SetToast(fmt.Sprintf("Mic Volume: %.0f%%", audio.Gain*100))
			})
		}

		// Copy Code [C]
		copyText := "[C] Copy"
		copyX := gainX + gainLen + 2
		copyLen := uint16(len([]rune(copyText)))
		if copyX+copyLen <= ctrlInner.X+ctrlInner.Width {
			buf.SetString(copyX, row2Y, copyText, cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg})
			frame.RegisterClickHandler(cell.NewRect(copyX, row2Y, copyLen, 1), func(_ backend.MouseEvent) {
				CopyToClipboard(node.RoomCode)
				r.SetToast(fmt.Sprintf("Room Code Copied: %s", node.RoomCode))
			})
		}

		// Leave Room [Esc]
		leaveText := "[Esc] Leave"
		leaveLen := uint16(len([]rune(leaveText)))
		leaveX := copyX + copyLen + 2
		if ctrlInner.Width >= leaveX-ctrlInner.X+leaveLen {
			if ctrlInner.X+ctrlInner.Width-leaveLen-1 > leaveX {
				leaveX = ctrlInner.X + ctrlInner.Width - leaveLen - 1
			}
			buf.SetString(leaveX, row2Y, leaveText, cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
			frame.RegisterClickHandler(cell.NewRect(leaveX, row2Y, leaveLen, 1), func(_ backend.MouseEvent) {
				if r.OnLeave != nil {
					r.OnLeave()
				}
			})
		}
	}

	// 2. Chat & Room Logs Area
	r.mu.Lock()
	r.LastLogArea = logArea
	isFocused := r.IsChatFocused
	toastMsg := r.ToastMsg
	messagesCopy := make([]RoomMessage, len(r.Messages))
	copy(messagesCopy, r.Messages)
	scrollOffset := r.ChatScrollOffset
	r.mu.Unlock()

	blockTitle := " CHAT & ROOM LOG "
	borderStyle := cell.Style{Fg: theme.Border}
	if isFocused {
		blockTitle = " CHAT & LOG [Enter: Send | Esc: Exit] "
		borderStyle = cell.Style{Fg: theme.BorderFocused, Modifier: cell.ModifierBold}
	}

	logBlock := widgets.Block{
		Title:         blockTitle,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   borderStyle,
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(logBlock, logArea)
	logInner := logBlock.Inner(logArea)

	for y := logInner.Y; y < logInner.Y+logInner.Height; y++ {
		for x := logInner.X; x < logInner.X+logInner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	// Register click handler to focus chat when clicking the area
	frame.RegisterClickHandler(logArea, func(_ backend.MouseEvent) {
		r.mu.Lock()
		r.IsChatFocused = true
		r.mu.Unlock()
	})

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

	hasInputLine := logInner.Height >= 2
	msgHeight := int(logInner.Height)
	inputY := logInner.Y + logInner.Height - 1
	if hasInputLine {
		msgHeight = int(logInner.Height) - 1
	}

	// Draw Toast if active
	if toastMsg != "" && msgHeight > 0 {
		toastStyle := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Success,
			Modifier: cell.ModifierBold,
		}
		toastText := truncate(" 🔔 "+toastMsg+" ", maxW)
		buf.SetString(logInner.X+1, logInner.Y, toastText, toastStyle)
	}

	// Message rendering
	startRow := logInner.Y
	availRows := msgHeight
	if toastMsg != "" && msgHeight > 1 {
		startRow++
		availRows--
	}

	lines := r.buildDisplayLines(messagesCopy, maxW)

	if len(lines) > 0 && availRows > 0 {
		startIdx := 0
		if len(lines) > availRows {
			startIdx = len(lines) - availRows - scrollOffset
			if startIdx < 0 {
				startIdx = 0
			}
		}
		endIdx := startIdx + availRows
		if endIdx > len(lines) {
			endIdx = len(lines)
		}

		visibleLines := lines[startIdx:endIdx]
		var currentRenderedLines []renderedChatLine
		for i, line := range visibleLines {
			rowY := startRow + uint16(i)
			if hasInputLine && rowY >= inputY {
				break
			}
			timeStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}

			curX := logInner.X + 1
			startX := curX
			var lineChars []renderedChatChar

			appendChars := func(str string, fromCol uint16) uint16 {
				col := fromCol
				for _, r := range str {
					w := cell.RuneWidth(r)
					if w > 0 {
						lineChars = append(lineChars, renderedChatChar{X: col, Y: rowY, R: r})
						col += uint16(w)
					}
				}
				return col
			}

			buf.SetString(curX, rowY, line.Timestamp, timeStyle)
			curX = appendChars(line.Timestamp, curX)

			if line.Badge != "" {
				buf.SetString(curX, rowY, line.Badge, line.BadgeStyle)
				curX = appendChars(line.Badge, curX)
			}

			remW := int(logInner.X+logInner.Width) - int(curX) - 1
			if remW > 0 {
				if line.IsChat {
					spanChars := r.renderChatSpans(frame, buf, curX, rowY, line.Spans, remW)
					lineChars = append(lineChars, spanChars...)
				} else {
					buf.SetString(curX, rowY, line.Text, line.TextStyle)
					curX = appendChars(line.Text, curX)
				}
			}

			currentRenderedLines = append(currentRenderedLines, renderedChatLine{
				RowY:   rowY,
				StartX: startX,
				EndX:   curX,
				Chars:  lineChars,
			})
		}

		r.mu.Lock()
		r.renderedLines = currentRenderedLines
		selActive := r.SelectionActive
		r.mu.Unlock()

		// If selection is active, highlight the selected cells
		if selActive {
			for _, rl := range currentRenderedLines {
				for _, ch := range rl.Chars {
					if r.isCellSelected(ch.X, ch.Y) {
						if cellPtr := buf.Get(ch.X, ch.Y); cellPtr != nil {
							cellPtr.Style.Fg = cell.NewColorRGB(0x00, 0x00, 0x00)
							cellPtr.Style.Bg = theme.Accent
							cellPtr.Style.Modifier |= cell.ModifierBold
						}
					}
				}
			}
		}

		if scrollOffset > 0 {
			scrollBadge := fmt.Sprintf(" ↑ +%d ", scrollOffset)
			badgeLen := uint16(len([]rune(scrollBadge)))
			if logInner.Width > badgeLen+2 {
				badgeX := logInner.X + logInner.Width - badgeLen - 1
				buf.SetString(badgeX, logInner.Y, scrollBadge, cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Warning,
					Modifier: cell.ModifierBold,
				})
			}
		}
	} else if len(lines) == 0 && toastMsg == "" && availRows > 0 {
		placeholder := truncate("Waiting for connections... Messages & events will appear here.", maxW)
		buf.SetString(logInner.X+1, startRow, placeholder, cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg})
	}

	// Bottom Input Row
	if hasInputLine {
		if isFocused {
			inputBg := cell.Style{Bg: theme.InputBg}
			for x := logInner.X; x < logInner.X+logInner.Width; x++ {
				buf.SetCell(x, inputY, cell.Cell{Content: ' ', Style: inputBg})
			}
			prompt := "> "
			promptLen := uint16(len([]rune(prompt)))
			buf.SetString(logInner.X+1, inputY, prompt, cell.Style{
				Fg:       theme.Accent,
				Bg:       theme.InputBg,
				Modifier: cell.ModifierBold,
			})
			if logInner.Width > promptLen+3 {
				inputArea := cell.NewRect(logInner.X+promptLen+1, inputY, logInner.Width-promptLen-2, 1)
				chatInput := widgets.TextInput{
					ID:          "room_chat_input",
					State:       r.ChatInputState,
					Placeholder: "Type message... (Enter: Send, Esc: Exit)",
					Style:       cell.Style{Fg: theme.Text, Bg: theme.InputBg},
					FocusedStyle: cell.Style{
						Fg:       theme.Text,
						Bg:       theme.InputBg,
						Modifier: cell.ModifierBold,
					},
					PlaceholderStyle: cell.Style{Fg: theme.TextMuted, Bg: theme.InputBg},
				}
				frame.RenderWidget(chatInput, inputArea)
			}
		} else {
			if r.UnreadChatCount > 0 {
				unfocusedPrompt := fmt.Sprintf(" %d New Messages - [Enter] to Chat ", r.UnreadChatCount)
				buf.SetString(logInner.X+1, inputY, unfocusedPrompt, cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Warning,
					Modifier: cell.ModifierBold,
				})
			} else {
				unfocusedPrompt := "[ Press Enter or / to Chat ]"
				buf.SetString(logInner.X+1, inputY, unfocusedPrompt, cell.Style{
					Fg: theme.TextMuted,
					Bg: theme.SurfaceBg,
				})
			}
		}
	}
}

type chatSpan struct {
	Text     string
	IsLink   bool
	ClickURL string
}

type roomDisplayLine struct {
	Timestamp      string
	Badge          string
	BadgeStyle     cell.Style
	Text           string
	TextStyle      cell.Style
	Spans          []chatSpan
	IsChat         bool
	IsContinuation bool
}

var reChatURL = regexp.MustCompile(`https?://[^\s<>"]+|www\.[^\s<>"]+`)

func cleanClickURL(raw string) string {
	clean := strings.TrimSpace(raw)
	for len(clean) > 0 {
		last := clean[len(clean)-1]
		if last == '.' || last == ',' || last == '!' || last == '?' || last == ';' || last == ':' || last == ')' || last == ']' || last == '>' || last == '"' || last == '\'' {
			clean = clean[:len(clean)-1]
		} else {
			break
		}
	}
	return clean
}

func splitWordsAndSpaces(s string) []string {
	var tokens []string
	var current strings.Builder
	var inSpace bool

	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace && current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			inSpace = true
			current.WriteRune(r)
		} else {
			if inSpace && current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			inSpace = false
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

func parseMessageSpans(text string) []chatSpan {
	locs := reChatURL.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return []chatSpan{{Text: text, IsLink: false}}
	}

	var spans []chatSpan
	lastIdx := 0
	for _, loc := range locs {
		if loc[0] > lastIdx {
			spans = append(spans, chatSpan{
				Text:   text[lastIdx:loc[0]],
				IsLink: false,
			})
		}
		rawLink := text[loc[0]:loc[1]]
		clickURL := cleanClickURL(rawLink)
		spans = append(spans, chatSpan{
			Text:     rawLink,
			IsLink:   true,
			ClickURL: clickURL,
		})
		lastIdx = loc[1]
	}
	if lastIdx < len(text) {
		spans = append(spans, chatSpan{
			Text:   text[lastIdx:],
			IsLink: false,
		})
	}
	return spans
}

func wrapSpansToLines(spans []chatSpan, availWidth int) [][]chatSpan {
	if availWidth < 5 {
		availWidth = 5
	}

	var lines [][]chatSpan
	var currentLine []chatSpan
	currentWidth := 0

	flushLine := func() {
		if len(currentLine) > 0 {
			lines = append(lines, currentLine)
			currentLine = nil
			currentWidth = 0
		}
	}

	appendSpan := func(span chatSpan, width int) {
		if len(currentLine) > 0 && !currentLine[len(currentLine)-1].IsLink && !span.IsLink {
			currentLine[len(currentLine)-1].Text += span.Text
		} else {
			currentLine = append(currentLine, span)
		}
		currentWidth += width
	}

	for _, span := range spans {
		if !span.IsLink {
			tokens := splitWordsAndSpaces(span.Text)
			for _, tok := range tokens {
				tRunes := []rune(tok)
				tLen := len(tRunes)

				if unicode.IsSpace(tRunes[0]) && currentWidth == 0 {
					continue
				}

				if currentWidth+tLen <= availWidth {
					appendSpan(chatSpan{Text: tok, IsLink: false}, tLen)
				} else {
					if currentWidth > 0 {
						flushLine()
					}
					if unicode.IsSpace(tRunes[0]) {
						continue
					}
					if tLen <= availWidth {
						appendSpan(chatSpan{Text: tok, IsLink: false}, tLen)
					} else {
						for len(tRunes) > 0 {
							chunkLen := min(len(tRunes), availWidth)
							chunkStr := string(tRunes[:chunkLen])
							if len(tRunes) > availWidth {
								lines = append(lines, []chatSpan{{Text: chunkStr, IsLink: false}})
								tRunes = tRunes[chunkLen:]
							} else {
								appendSpan(chatSpan{Text: chunkStr, IsLink: false}, chunkLen)
								tRunes = tRunes[chunkLen:]
							}
						}
					}
				}
			}
		} else {
			lRunes := []rune(span.Text)
			lLen := len(lRunes)

			if currentWidth+lLen <= availWidth {
				appendSpan(span, lLen)
			} else {
				if currentWidth > 0 {
					flushLine()
				}
				if lLen <= availWidth {
					appendSpan(span, lLen)
				} else {
					for len(lRunes) > 0 {
						chunkLen := min(len(lRunes), availWidth)
						chunkStr := string(lRunes[:chunkLen])
						if len(lRunes) > availWidth {
							lines = append(lines, []chatSpan{{
								Text:     chunkStr,
								IsLink:   true,
								ClickURL: span.ClickURL,
							}})
							lRunes = lRunes[chunkLen:]
						} else {
							appendSpan(chatSpan{
								Text:     chunkStr,
								IsLink:   true,
								ClickURL: span.ClickURL,
							}, chunkLen)
							lRunes = lRunes[chunkLen:]
						}
					}
				}
			}
		}
	}

	flushLine()
	if len(lines) == 0 {
		lines = append(lines, []chatSpan{{Text: "", IsLink: false}})
	}
	return lines
}

func wrapWordsToLines(text string, maxW int) []string {
	if maxW < 5 {
		maxW = 5
	}
	tokens := splitWordsAndSpaces(text)
	var lines []string
	var cur strings.Builder
	curLen := 0

	flush := func() {
		if cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curLen = 0
		}
	}

	for _, tok := range tokens {
		tRunes := []rune(tok)
		tLen := len(tRunes)
		if unicode.IsSpace(tRunes[0]) && curLen == 0 {
			continue
		}
		if curLen+tLen <= maxW {
			cur.WriteString(tok)
			curLen += tLen
		} else {
			if curLen > 0 {
				flush()
			}
			if unicode.IsSpace(tRunes[0]) {
				continue
			}
			if tLen <= maxW {
				cur.WriteString(tok)
				curLen = tLen
			} else {
				for len(tRunes) > 0 {
					cLen := min(len(tRunes), maxW)
					if len(tRunes) > maxW {
						lines = append(lines, string(tRunes[:cLen]))
						tRunes = tRunes[cLen:]
					} else {
						cur.WriteString(string(tRunes[:cLen]))
						curLen = cLen
						tRunes = tRunes[cLen:]
					}
				}
			}
		}
	}
	flush()
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

func (r *RoomView) buildDisplayLines(messages []RoomMessage, maxW int) []roomDisplayLine {
	var lines []roomDisplayLine
	if maxW < 10 {
		maxW = 10
	}
	theme := CurrentTheme()

	for _, msg := range messages {
		tsStr := fmt.Sprintf("[%s] ", msg.Timestamp.Format("15:04:05"))
		tsLen := len([]rune(tsStr))

		if msg.IsChat {
			var senderBadge string
			var senderStyle cell.Style
			if msg.IsSelf {
				senderBadge = "You: "
				senderStyle = cell.Style{
					Fg:       theme.Success,
					Bg:       theme.SurfaceBg,
					Modifier: cell.ModifierBold,
				}
			} else {
				senderBadge = msg.Sender + ": "
				senderStyle = cell.Style{
					Fg:       theme.Accent,
					Bg:       theme.SurfaceBg,
					Modifier: cell.ModifierBold,
				}
			}

			badgeLen := tsLen + len([]rune(senderBadge))
			availFirst := maxW - badgeLen
			if availFirst < 10 {
				availFirst = 10
			}

			indentSpaces := strings.Repeat(" ", badgeLen)
			paragraphs := strings.Split(msg.Text, "\n")
			firstLineOverall := true

			for _, para := range paragraphs {
				spans := parseMessageSpans(para)
				wrappedLines := wrapSpansToLines(spans, availFirst)

				for _, lSpans := range wrappedLines {
					if firstLineOverall {
						lines = append(lines, roomDisplayLine{
							Timestamp:      tsStr,
							Badge:          senderBadge,
							BadgeStyle:     senderStyle,
							Spans:          lSpans,
							IsChat:         true,
							IsContinuation: false,
						})
						firstLineOverall = false
					} else {
						lines = append(lines, roomDisplayLine{
							Timestamp:      indentSpaces,
							Spans:          lSpans,
							IsChat:         true,
							IsContinuation: true,
						})
					}
				}
			}
		} else {
			logColor := theme.TextMuted
			if strings.Contains(msg.Text, "[+]") || strings.Contains(msg.Text, "joined") {
				logColor = theme.Success
			} else if strings.Contains(msg.Text, "[-]") || strings.Contains(msg.Text, "left") {
				logColor = theme.Warning
			} else if strings.Contains(msg.Text, "[WARN]") || strings.Contains(msg.Text, "[ERROR]") || strings.Contains(msg.Text, "[SECURITY]") {
				logColor = theme.Danger
			}

			availFirst := maxW - tsLen
			if availFirst < 10 {
				availFirst = 10
			}

			logLines := wrapWordsToLines(msg.Text, availFirst)
			indentSpaces := strings.Repeat(" ", tsLen)

			for idx, lText := range logLines {
				if idx == 0 {
					lines = append(lines, roomDisplayLine{
						Timestamp:      tsStr,
						Text:           lText,
						TextStyle:      cell.Style{Fg: logColor, Bg: theme.SurfaceBg},
						IsChat:         false,
						IsContinuation: false,
					})
				} else {
					lines = append(lines, roomDisplayLine{
						Timestamp:      indentSpaces,
						Text:           lText,
						TextStyle:      cell.Style{Fg: logColor, Bg: theme.SurfaceBg},
						IsChat:         false,
						IsContinuation: true,
					})
				}
			}
		}
	}

	return lines
}

func (r *RoomView) renderChatSpans(frame *terminal.Frame, buf *buffer.Buffer, startX, rowY uint16, spans []chatSpan, maxW int) []renderedChatChar {
	var chars []renderedChatChar
	if maxW <= 0 || len(spans) == 0 {
		return chars
	}
	theme := CurrentTheme()
	plainStyle := cell.Style{
		Fg: theme.Text,
		Bg: theme.SurfaceBg,
	}
	linkStyle := cell.Style{
		Fg:       theme.Secondary,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierUnderline | cell.ModifierBold,
	}

	curX := startX
	endX := startX + uint16(maxW)

	for _, span := range spans {
		if curX >= endX {
			break
		}
		sRunes := []rune(span.Text)
		rem := int(endX - curX)
		drawnLen := len(sRunes)
		if drawnLen > rem {
			sRunes = sRunes[:rem]
			drawnLen = rem
		}
		if drawnLen <= 0 {
			continue
		}

		if span.IsLink {
			buf.SetString(curX, rowY, string(sRunes), linkStyle)
			clickURL := span.ClickURL
			if clickURL == "" {
				clickURL = span.Text
			}
			linkRect := cell.NewRect(curX, rowY, uint16(drawnLen), 1)
			frame.RegisterClickHandler(linkRect, func(_ backend.MouseEvent) {
				r.mu.Lock()
				isSelActive := r.SelectionActive
				r.mu.Unlock()
				if isSelActive {
					return
				}
				_ = OpenBrowserURL(clickURL)
				CopyToClipboard(clickURL)
				r.SetToast(fmt.Sprintf("🔗 Link opened: %s", clickURL))
			})
		} else {
			buf.SetString(curX, rowY, string(sRunes), plainStyle)
		}

		col := curX
		for _, ru := range sRunes {
			w := cell.RuneWidth(ru)
			if w > 0 {
				chars = append(chars, renderedChatChar{X: col, Y: rowY, R: ru})
				col += uint16(w)
			}
		}

		curX += uint16(drawnLen)
	}
	return chars
}

func (r *RoomView) isCellSelected(x, y uint16) bool {
	sX, sY := r.SelectionStartX, r.SelectionStartY
	eX, eY := r.SelectionEndX, r.SelectionEndY

	if sY > eY || (sY == eY && sX > eX) {
		sX, eX = eX, sX
		sY, eY = eY, sY
	}

	iy := int(y)
	ix := int(x)

	if iy < sY || iy > eY {
		return false
	}
	if sY == eY {
		return ix >= sX && ix <= eX
	}
	if iy == sY {
		return ix >= sX
	}
	if iy == eY {
		return ix <= eX
	}
	return true
}

func (r *RoomView) extractSelectedText() string {
	if len(r.renderedLines) == 0 {
		return ""
	}
	sX, sY := r.SelectionStartX, r.SelectionStartY
	eX, eY := r.SelectionEndX, r.SelectionEndY

	if sY > eY || (sY == eY && sX > eX) {
		sX, eX = eX, sX
		sY, eY = eY, sY
	}

	var result strings.Builder
	for _, rl := range r.renderedLines {
		iy := int(rl.RowY)
		if iy < sY || iy > eY {
			continue
		}
		var lineStr strings.Builder
		for _, ch := range rl.Chars {
			ix := int(ch.X)
			var inSel bool
			if sY == eY {
				inSel = (ix >= sX && ix <= eX)
			} else if iy == sY {
				inSel = (ix >= sX)
			} else if iy == eY {
				inSel = (ix <= eX)
			} else {
				inSel = true
			}
			if inSel {
				lineStr.WriteRune(ch.R)
			}
		}
		extracted := strings.TrimRight(lineStr.String(), " ")
		if extracted != "" {
			if result.Len() > 0 {
				result.WriteRune('\n')
			}
			result.WriteString(extracted)
		}
	}
	return result.String()
}

func (r *RoomView) HandleMousePress(x, y uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SelectionDragging = true
	r.SelectionStartX = int(x)
	r.SelectionStartY = int(y)
	r.SelectionEndX = int(x)
	r.SelectionEndY = int(y)
	r.SelectionActive = false
	r.SelectedText = ""
}

func (r *RoomView) HandleMouseDrag(x, y uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.SelectionDragging {
		r.SelectionDragging = true
		r.SelectionStartX = int(x)
		r.SelectionStartY = int(y)
	}
	r.SelectionEndX = int(x)
	r.SelectionEndY = int(y)
	if r.SelectionStartX != r.SelectionEndX || r.SelectionStartY != r.SelectionEndY {
		r.SelectionActive = true
		r.SelectedText = r.extractSelectedText()
	}
}

func (r *RoomView) HandleMouseRelease(x, y uint16) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SelectionDragging = false
	if r.SelectionActive {
		r.SelectionEndX = int(x)
		r.SelectionEndY = int(y)
		r.SelectedText = r.extractSelectedText()
		return r.SelectedText
	}
	return ""
}

func (r *RoomView) ClearSelection() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SelectionActive = false
	r.SelectionDragging = false
	r.SelectedText = ""
}

func (r *RoomView) renderCompactHUD(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         " LIMONI MINI HUD ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.BorderFocused},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	if inner.Height < 1 || inner.Width < 10 {
		return
	}

	peers := node.GetPeersList()
	totalCount := len(peers) + 1

	// Row 1: App Info + Room Code + Members + Ping + Mic Status + Audio VU
	row1Y := inner.Y
	curX := inner.X + 1

	// Room badge
	roomBadge := fmt.Sprintf(" Room: %s ", node.RoomCode)
	roomLen := uint16(len([]rune(roomBadge)))
	if curX+roomLen < inner.X+inner.Width {
		buf.SetString(curX, row1Y, roomBadge, cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		})
		frame.RegisterClickHandler(cell.NewRect(curX, row1Y, roomLen, 1), func(_ backend.MouseEvent) {
			CopyToClipboard(node.RoomCode)
			r.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))
		})
		curX += roomLen + 1
	}

	// Members
	memBadge := fmt.Sprintf("Members: %d/4", totalCount)
	memLen := uint16(len([]rune(memBadge)))
	if curX+memLen < inner.X+inner.Width {
		buf.SetString(curX, row1Y, memBadge, cell.Style{
			Fg:       theme.Success,
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		})
		curX += memLen + 1
	}

	// Lock badge if locked
	if node.IsLocked {
		lockBadge := " [LOCKED] "
		if node.RoomPIN != "" && node.IsHost {
			lockBadge = fmt.Sprintf(" [PIN: %s] ", node.RoomPIN)
		}
		lockLen := uint16(len([]rune(lockBadge)))
		if curX+lockLen < inner.X+inner.Width {
			buf.SetString(curX, row1Y, lockBadge, cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Danger,
				Modifier: cell.ModifierBold,
			})
			curX += lockLen + 1
		}
	}

	// Mic status button
	micLabel := " [MIC: ON] "
	micStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Success,
		Modifier: cell.ModifierBold,
	}
	if audio != nil && audio.Muted {
		micLabel = " [MIC: OFF] "
		micStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Danger,
			Modifier: cell.ModifierBold,
		}
	} else if audio != nil && audio.IsSpeaking {
		micLabel = " [SPEAKING] "
		micStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Modifier: cell.ModifierBold,
		}
	}
	micLen := uint16(len([]rune(micLabel)))
	if curX+micLen < inner.X+inner.Width {
		buf.SetString(curX, row1Y, micLabel, micStyle)
		frame.RegisterClickHandler(cell.NewRect(curX, row1Y, micLen, 1), func(_ backend.MouseEvent) {
			if audio != nil {
				isMuted := audio.ToggleMute()
				node.SendMuteState(isMuted)
			}
		})
		curX += micLen + 1
	}

	// Deafen button
	deafLabel := " [SPK: ON] "
	deafStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Secondary,
		Modifier: cell.ModifierBold,
	}
	if audio != nil && audio.Deafened {
		deafLabel = " [DEAFENED] "
		deafStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}
	}
	deafLen := uint16(len([]rune(deafLabel)))
	if curX+deafLen < inner.X+inner.Width {
		buf.SetString(curX, row1Y, deafLabel, deafStyle)
		frame.RegisterClickHandler(cell.NewRect(curX, row1Y, deafLen, 1), func(_ backend.MouseEvent) {
			if audio != nil {
				isDeaf := audio.ToggleDeafen()
				node.SendDeafenState(isDeaf)
				node.SendMuteState(audio.Muted)
			}
		})
		curX += deafLen + 1
	}

	// Screen Share / Stream Viewer button in HUD
	var streamBtn string
	var streamStyle cell.Style
	var streamAction func()

	var streamingPeers []*PeerInfo
	for _, p := range peers {
		if p.IsSharingScreen {
			streamingPeers = append(streamingPeers, p)
		}
	}

	if node.IsSharingScreen {
		streamBtn = " [📺 SHARING [V]] "
		streamStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Accent,
			Modifier: cell.ModifierBold,
		}
		streamAction = func() {
			_ = node.StopScreenShare()
			r.SetToast("Screen share stopped")
		}
	} else if node.IsWatchingScreen {
		streamBtn = " [📺 WATCHING [W]] "
		streamStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Secondary,
			Modifier: cell.ModifierBold,
		}
		streamAction = func() {
			_ = node.StopWatchingScreen()
			r.SetToast("Stream viewer closed")
		}
	} else if len(streamingPeers) > 0 {
		targetPeer := streamingPeers[0]
		streamBtn = fmt.Sprintf(" [🔴 WATCH %s [W]] ", targetPeer.Nickname)
		streamStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Danger,
			Modifier: cell.ModifierBold,
		}
		streamAction = func() {
			port := targetPeer.VideoPort
			if port <= 0 {
				port = 50100
			}
			opts := screenshare.ReceiverOptions{
				WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (HD 60 FPS)", targetPeer.Nickname),
			}
			r.SetToast(fmt.Sprintf("Opening %s stream...", targetPeer.Nickname))
			go func() {
				_ = node.StartWatchingScreen(targetPeer.ID, port, opts)
			}()
		}
	} else {
		streamBtn = " [📺 SHARE [V]] "
		streamStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Secondary,
			Modifier: cell.ModifierBold,
		}
		streamAction = func() {
			if r.OnOpenScreenShareModal != nil {
				r.OnOpenScreenShareModal()
			}
		}
	}

	streamLen := uint16(len([]rune(streamBtn)))
	if curX+streamLen < inner.X+inner.Width {
		buf.SetString(curX, row1Y, streamBtn, streamStyle)
		frame.RegisterClickHandler(cell.NewRect(curX, row1Y, streamLen, 1), func(_ backend.MouseEvent) {
			if streamAction != nil {
				streamAction()
			}
		})
		curX += streamLen + 1
	}

	// Full UI restore button
	hudExitLabel := " [▲ FULL UI [H]] "
	hudExitStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Accent,
		Modifier: cell.ModifierBold,
	}
	hudExitLen := uint16(len([]rune(hudExitLabel)))
	if curX+hudExitLen <= inner.X+inner.Width {
		buf.SetString(curX, row1Y, hudExitLabel, hudExitStyle)
		frame.RegisterClickHandler(cell.NewRect(curX, row1Y, hudExitLen, 1), func(_ backend.MouseEvent) {
			ToggleCompactHUD()
			r.IsCompactMode = false
		})
	}

	// Row 2 (if height >= 2): Live VU meter / Active speakers line
	if inner.Height >= 2 {
		row2Y := inner.Y + 1
		meterW := inner.Width - 2
		if meterW > 10 {
			var spkNames []string
			if audio != nil && audio.IsSpeaking && !audio.Muted {
				spkNames = append(spkNames, "You")
			}
			for _, p := range peers {
				if p.Speaking && !p.IsMuted {
					spkNames = append(spkNames, p.Nickname)
				}
			}
			spkText := "Voice: [IDLE]"
			if len(spkNames) > 0 {
				spkText = fmt.Sprintf("Talking: ● %s", strings.Join(spkNames, ", "))
			}
			buf.SetString(inner.X+1, row2Y, spkText, cell.Style{
				Fg:       theme.Accent,
				Bg:       theme.SurfaceBg,
				Modifier: cell.ModifierBold,
			})
		}
	}
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

// OpenInEditor opens the specified file in the system's default or preferred GUI/terminal editor
func OpenInEditor(filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err == nil {
		filePath = absPath
	}

	switch runtime.GOOS {
	case "windows":
		// 1. Try VS Code if installed
		if p, err := exec.LookPath("code"); err == nil && p != "" {
			cmd := exec.Command("code", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
		// 2. Try Windows default association
		cmd := exec.Command("cmd", "/c", "start", "", filePath)
		if err := cmd.Start(); err == nil {
			return nil
		}
		// 3. Fallback to notepad
		cmdNotepad := exec.Command("notepad.exe", filePath)
		return cmdNotepad.Start()

	case "darwin":
		// 1. Try VS Code if installed
		if p, err := exec.LookPath("code"); err == nil && p != "" {
			cmd := exec.Command("code", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
		// 2. Try macOS default text editor
		cmd := exec.Command("open", "-t", filePath)
		if err := cmd.Start(); err == nil {
			return nil
		}
		// 3. Fallback to open
		cmdOpen := exec.Command("open", filePath)
		return cmdOpen.Start()

	default: // Linux, BSD, etc.
		// 1. Check custom GUI editor environment variable
		if customEditor := os.Getenv("VISUAL"); customEditor != "" {
			cmd := exec.Command(customEditor, filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 2. Try VS Code if installed
		if p, err := exec.LookPath("code"); err == nil && p != "" {
			cmd := exec.Command("code", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 3. Try xdg-open (opens default desktop editor e.g. Kate, Gedit, Text Editor)
		if p, err := exec.LookPath("xdg-open"); err == nil && p != "" {
			cmd := exec.Command("xdg-open", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 4. Try gio open
		if p, err := exec.LookPath("gio"); err == nil && p != "" {
			cmd := exec.Command("gio", "open", filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 5. Try known common Linux GUI editors
		for _, guiEditor := range []string{"cursor", "vscodium", "code-oss", "zed", "gedit", "kate", "gnome-text-editor", "mousepad", "xed", "pluma", "subl", "sublime-text", "atom", "kwrite"} {
			if p, err := exec.LookPath(guiEditor); err == nil && p != "" {
				cmd := exec.Command(guiEditor, filePath)
				if err := cmd.Start(); err == nil {
					return nil
				}
			}
		}

		// 6. If $TERMINAL and $EDITOR are set, spawn a separate terminal window
		term := os.Getenv("TERMINAL")
		cliEditor := os.Getenv("EDITOR")
		if cliEditor == "" {
			cliEditor = "nano"
		}
		if term != "" {
			cmd := exec.Command(term, "-e", cliEditor, filePath)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}

		// 7. Try common terminal emulators to spawn the CLI editor in a new window
		for _, termEmulator := range []string{"x-terminal-emulator", "gnome-terminal", "konsole", "xfce4-terminal", "alacritty", "kitty", "foot", "wezterm", "tilix", "terminator", "xterm"} {
			if p, err := exec.LookPath(termEmulator); err == nil && p != "" {
				var cmd *exec.Cmd
				if termEmulator == "gnome-terminal" {
					cmd = exec.Command(termEmulator, "--", cliEditor, filePath)
				} else {
					cmd = exec.Command(termEmulator, "-e", cliEditor, filePath)
				}
				if err := cmd.Start(); err == nil {
					return nil
				}
			}
		}

		return errors.New("no suitable editor or terminal found to open file")
	}
}

// OpenFolder opens the system file manager at the specified directory
func OpenFolder(dirPath string) error {
	absPath, err := filepath.Abs(dirPath)
	if err == nil {
		dirPath = absPath
	}

	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", dirPath).Start()
	case "darwin":
		return exec.Command("open", dirPath).Start()
	default:
		return exec.Command("xdg-open", dirPath).Start()
	}
}
