package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/p2p"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/layout"
	"github.com/thebanri/limoni/widgets"
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
	OnCopyInvite           func()
	OnToggleKnock          func()
	OnKickMember           func(target string, ban bool)
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
	RowY           uint16
	StartX         uint16
	EndX           uint16
	Chars          []renderedChatChar
	IsContinuation bool
	RawMessage     string
	CopyText       string
	ClickURL       string
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
				Text:      "Commands: /invite, /knock, /copy <text>, /vol [user] [0-200], /send <path>, /code <snippet>, /folder, /lock [pin], /unlock, /kick <user>, /ban <user>, /compact, /mute, /deafen, /sfx, /hop, /nick <name>, /clear",
				IsChat:    false,
			})
			r.mu.Unlock()
			return

		case "/copy", "/cp", "/kopyala":
			if len(parts) < 2 {
				r.Messages = append(r.Messages, RoomMessage{
					Timestamp: time.Now(),
					Text:      "📋 Usage: /copy <text_to_copy> (e.g. /copy 192.168.1.50:9000 or /copy git clone ...)",
					IsChat:    false,
				})
				r.mu.Unlock()
				return
			}
			rawCopyText := strings.TrimSpace(strings.TrimPrefix(text, parts[0]))
			if rawCopyText == "" {
				r.mu.Unlock()
				return
			}
			formattedMsg := fmt.Sprintf("📋 [Copy: %s]", rawCopyText)
			onSend := r.OnSendChat
			r.mu.Unlock()
			if onSend != nil {
				onSend(formattedMsg)
			}
			return

		case "/kick", "/ban":
			if len(parts) < 2 {
				r.Messages = append(r.Messages, RoomMessage{
					Timestamp: time.Now(),
					Text:      "Usage: " + cmd + " <user> (host only; /ban also keeps them out while the room is open)",
					IsChat:    false,
				})
				r.mu.Unlock()
				return
			}
			kick := r.OnKickMember
			r.mu.Unlock()
			if kick != nil {
				kick(strings.Join(parts[1:], " "), cmd == "/ban")
			}
			return

		case "/knock", "/kapi":
			toggle := r.OnToggleKnock
			r.mu.Unlock()
			if toggle != nil {
				toggle()
			}
			return

		case "/invite", "/davet":
			copyInvite := r.OnCopyInvite
			r.mu.Unlock()
			if copyInvite != nil {
				copyInvite()
			}
			return

		case "/folder", "/downloads", "/files", "/dir":
			openFolderCb := r.OnOpenFolder
			r.mu.Unlock()
			if openFolderCb != nil {
				openFolderCb(p2p.GetLimoniTransfersDir())
			} else {
				_ = OpenFolder(p2p.GetLimoniTransfersDir())
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

// minGridHeight is the least room the member grid needs before the room falls back to
// the mini HUD.
const minGridHeight = 4

func (r *RoomView) Render(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine) {
	if r.IsCompactMode || GetCompactHUD() || area.Height <= 6 {
		r.renderCompactHUD(frame, area, node, audio)
		return
	}

	// The controls wrap onto as many rows as the width needs; the footer grows to fit
	// them, spaced out when there is room and packed when the terminal is short.
	items := r.controlItems(node, audio)
	var ctrlWidth int
	if cols := footerSplit(cell.NewRect(area.X, 0, area.Width, 3)); len(cols) == 2 {
		ctrlWidth = int(controlsArea(cols[0]).Width)
	}
	footerInner, maxRows := 6, 3 // three spaced rows; the chat panel wants the lines too
	if node.IsWatchingScreen {
		footerInner, maxRows = 2, 2 // compact footer so the stream stage gets the height
	}
	fl := layoutFlow(items, ctrlWidth, footerGap, maxRows)
	rows := len(fl.rows)
	footerInner = max(footerInner, rows+(rows-1)*flowSpacing(rows, footerInner))
	if int(area.Height)-3-(footerInner+2) < minGridHeight {
		footerInner = max(rows, 2)
	}
	if int(area.Height)-3-(footerInner+2) < minGridHeight {
		r.renderCompactHUD(frame, area, node, audio)
		return
	}

	vSplits := layout.NewFlexLayout(layout.Vertical, 0,
		layout.Fixed(3),                     // Header
		layout.Fill(),                       // 2x2 Participant Cards or Big Stream Stage
		layout.Fixed(uint16(footerInner+2)), // Controls & Mini Logs
	).Split(area)
	if len(vSplits) < 3 {
		return
	}

	r.renderHeader(frame, vSplits[0], node)
	// The cards and the stream stage are laid out for more height than a short window
	// leaves them; keep what they draw off their area from spilling over.
	drawClipped(frame, vSplits[1], func() { r.renderGrid(frame, vSplits[1], node, audio) })
	r.renderFooter(frame, vSplits[2], node, audio, items, fl)
}

func (r *RoomView) renderHeader(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         T(" LIMONI VOICE ROOM "),
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

	durStr := Tf("Duration: %s", formatDuration(time.Since(r.StartTime)))
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
	codeBadge := Tf(" Room: %s ", node.RoomCode)
	codeLen := uint16(len([]rune(codeBadge)))
	if curX+codeLen <= limitX {
		buf.SetString(curX, inner.Y, codeBadge, cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		})
		badgeRect := cell.NewRect(curX, inner.Y, codeLen, 1)
		frame.RegisterClickHandler(badgeRect, func(_ driver.MouseEvent) {
			CopyToClipboard(node.RoomCode)
			r.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))
		})
		curX += codeLen + 2
	}

	// 3. Role Badge
	var roleBadge string
	var roleStyle cell.Style
	if node.IsHost {
		roleBadge = T(" HOST (YOU) ")
		roleStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}
	} else {
		hostName := node.HostNick
		if hostName == "" {
			hostName = T("Host")
		}
		roleBadge = Tf(" MEMBER (Host: %s) ", hostName)
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
	countStr := Tf("Members: %d/4", totalCount)
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
	portBadge := Tf(" Port: :%d (%dm) ", node.Port, hopMin)
	portLen := uint16(len([]rune(portBadge)))
	if curX+portLen <= limitX {
		buf.SetString(curX, inner.Y, portBadge, cell.Style{
			Fg:       theme.Accent,
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		})
		pRect := cell.NewRect(curX, inner.Y, portLen, 1)
		frame.RegisterClickHandler(pRect, func(_ driver.MouseEvent) {
			r.SetToast(fmt.Sprintf("Port Hopping Active: Port :%d (Next in %dm, Epoch %d)", node.Port, hopMin, node.HopEpoch()))
		})
		curX += portLen + 2
	}

	// 6. Lock Status Badge
	if node.IsLocked {
		var lockBadge string
		if node.IsHost && node.RoomPIN != "" {
			lockBadge = Tf(" LOCKED (PIN: %s) ", node.RoomPIN)
		} else {
			lockBadge = T(" LOCKED ")
		}
		lockLen := uint16(len([]rune(lockBadge)))
		if curX+lockLen <= limitX {
			buf.SetString(curX, inner.Y, lockBadge, cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Danger,
				Modifier: cell.ModifierBold,
			})
			lRect := cell.NewRect(curX, inner.Y, lockLen, 1)
			frame.RegisterClickHandler(lRect, func(_ driver.MouseEvent) {
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

	// 7. Relay Status Badge
	var relayBadge string
	var relayStyle cell.Style
	if node.IsRelayConnected() {
		relayBadge = T(" 🌐 RELAY: CONNECTED ")
		relayStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Success,
			Modifier: cell.ModifierBold,
		}
	} else if node.LanOnly || node.RelayURL == "" || strings.EqualFold(node.RelayURL, "none") || strings.EqualFold(node.RelayURL, "off") || strings.EqualFold(node.RelayURL, "lan") {
		relayBadge = T(" 🏠 LAN ONLY ")
		relayStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Secondary,
			Modifier: cell.ModifierBold,
		}
	} else if node.Connecting || node.RelayStatus() == "Connecting..." {
		relayBadge = T(" ⏳ RELAY: CONNECTING... ")
		relayStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}
	} else {
		relayBadge = T(" ⚠ RELAY: OFFLINE (LAN MODE) ")
		relayStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Danger,
			Modifier: cell.ModifierBold,
		}
	}
	relayLen := uint16(len([]rune(relayBadge)))
	if curX+relayLen <= limitX {
		buf.SetString(curX, inner.Y, relayBadge, relayStyle)
		curX += relayLen + 2
	}
}

func (r *RoomView) renderGrid(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine) {
	peers := node.GetPeersList()

	// Find all peers sharing screen in the room
	var streamingPeers []*p2p.PeerInfo
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
