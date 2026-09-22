// Room view: the footer with chat input, logs and controls.

package main

import (
	"fmt"

	"github.com/thebanri/limoni-voice/screenshare"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/layout"
	"github.com/thebanri/limoni/widgets"
)

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
	frame.RegisterClickHandler(cell.NewRect(muteX, row1Y, muteLen, 1), func(_ driver.MouseEvent) {
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
		frame.RegisterClickHandler(cell.NewRect(deafenX, row1Y, deafenLen, 1), func(_ driver.MouseEvent) {
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
		frame.RegisterClickHandler(cell.NewRect(modeX, row1Y, modeLen, 1), func(_ driver.MouseEvent) {
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
		frame.RegisterClickHandler(cell.NewRect(noiseX, row1Y, noiseLen, 1), func(_ driver.MouseEvent) {
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
		frame.RegisterClickHandler(cell.NewRect(screenX, row1Y, screenLen, 1), func(_ driver.MouseEvent) {
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
						localFPS := node.ActiveScreenShareFPS
						if localFPS <= 0 {
							localFPS = 60
						}
						r.SetToast(fmt.Sprintf("Screen share started (%d FPS)", localFPS))
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
		frame.RegisterClickHandler(cell.NewRect(watchX, row2Y, watchLen, 1), func(_ driver.MouseEvent) {
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
				fps := streamingPeer.VideoFPS
				if fps <= 0 {
					fps = 60
				}
				opts := screenshare.DefaultReceiverOptions(fps)
				opts.WindowTitle = fmt.Sprintf("Limoni Voice - %s Live Stream (%d FPS)", streamingPeer.Nickname, fps)
				r.SetToast("Starting stream viewer...")
				go func() {
					err := node.StartWatchingScreen(streamingPeer.ID, port, opts)
					if err != nil {
						r.SetToast(fmt.Sprintf("Error: %v", err))
					} else {
						r.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", streamingPeer.Nickname, fps))
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
			frame.RegisterClickHandler(cell.NewRect(testX, row2Y, testLen, 1), func(_ driver.MouseEvent) {
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
			// "[+" raises, "/-]" lowers, clicking the value itself raises (as before).
			increase := func(_ driver.MouseEvent) {
				gain := audio.AdjustGain(0.1)
				if gain > 3.0 {
					audio.AdjustGain(-2.5) // loop back from 300% to 50%
				}
				r.SetToast(fmt.Sprintf("Mic Volume: %.0f%%", audio.Gain*100))
			}
			decrease := func(_ driver.MouseEvent) {
				r.SetToast(fmt.Sprintf("Mic Volume: %.0f%%", audio.AdjustGain(-0.1)*100))
			}
			frame.RegisterClickHandler(cell.NewRect(gainX, row2Y, 2, 1), increase)
			frame.RegisterClickHandler(cell.NewRect(gainX+2, row2Y, 3, 1), decrease)
			frame.RegisterClickHandler(cell.NewRect(gainX+5, row2Y, gainLen-5, 1), increase)
		}

		// Copy Code [C]
		copyText := "[C] Copy"
		copyX := gainX + gainLen + 2
		copyLen := uint16(len([]rune(copyText)))
		if copyX+copyLen <= ctrlInner.X+ctrlInner.Width {
			buf.SetString(copyX, row2Y, copyText, cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg})
			frame.RegisterClickHandler(cell.NewRect(copyX, row2Y, copyLen, 1), func(_ driver.MouseEvent) {
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
			frame.RegisterClickHandler(cell.NewRect(leaveX, row2Y, leaveLen, 1), func(_ driver.MouseEvent) {
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
			if !line.IsContinuation {
				curX = appendChars(line.Timestamp, curX)
			} else {
				curX += uint16(len([]rune(line.Timestamp)))
			}

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

			var lineCopyText, lineClickURL string
			if line.IsChat {
				for _, s := range line.Spans {
					if s.IsCopy && lineCopyText == "" {
						lineCopyText = s.CopyText
					}
					if s.IsLink && lineClickURL == "" {
						lineClickURL = s.ClickURL
					}
				}
			}

			currentRenderedLines = append(currentRenderedLines, renderedChatLine{
				RowY:           rowY,
				StartX:         startX,
				EndX:           curX,
				Chars:          lineChars,
				IsContinuation: line.IsContinuation,
				RawMessage:     line.RawMessage,
				CopyText:       lineCopyText,
				ClickURL:       lineClickURL,
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
