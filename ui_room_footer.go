// Room view: the footer with chat input, logs and controls.

package main

import (
	"fmt"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/p2p"
	"github.com/thebanri/limoni-voice/screenshare"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/layout"
	"github.com/thebanri/limoni/widgets"
)

// footerGap is the column gap between control buttons.
const footerGap = 2

// footerSplit divides the footer into the controls and the chat panel.
func footerSplit(area cell.Rect) []cell.Rect {
	return layout.NewFlexLayout(layout.Horizontal, 0,
		layout.Percentage(52), // controls
		layout.Percentage(48), // chat & room logs
	).Split(area)
}

// controlsArea is where the buttons go inside the controls panel: the block's inner area
// less a column of padding on each side.
func controlsArea(panel cell.Rect) cell.Rect {
	if panel.Width < 5 || panel.Height < 3 {
		return cell.Rect{}
	}
	return cell.NewRect(panel.X+2, panel.Y+1, panel.Width-4, panel.Height-2)
}

func (r *RoomView) renderFooter(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine, items []flowItem, fl flowLayout) {
	cols := footerSplit(area)
	if len(cols) < 2 {
		return
	}

	ctrlArea := cols[0]
	logArea := cols[1]
	theme := CurrentTheme()

	// 1. Controls Panel
	ctrlBlock := widgets.Block{
		Title:         T(" CONTROLS "),
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
	drawFlow(frame, controlsArea(ctrlArea), items, fl, footerGap)

	// 2. Chat & Room Logs Area
	r.mu.Lock()
	r.LastLogArea = logArea
	isFocused := r.IsChatFocused
	toastMsg := tr(r.ToastMsg)
	messagesCopy := make([]RoomMessage, len(r.Messages))
	copy(messagesCopy, r.Messages)
	scrollOffset := r.ChatScrollOffset
	r.mu.Unlock()

	blockTitle := T(" CHAT & ROOM LOG ")
	borderStyle := cell.Style{Fg: theme.Border}
	if isFocused {
		blockTitle = T(" CHAT & LOG [Enter: Send | Esc: Exit] ")
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
		placeholder := truncate(T("Waiting for connections... Messages & events will appear here."), maxW)
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
					Placeholder: T("Type message... (Enter: Send, Esc: Exit)"),
					Style:       cell.Style{Fg: theme.Text, Bg: theme.InputBg},
					FocusedStyle: cell.Style{
						Fg:       theme.Text,
						Bg:       theme.InputBg,
						Modifier: cell.ModifierBold,
					},
					PlaceholderStyle: cell.Style{Fg: theme.TextMuted, Bg: theme.InputBg},
				}
				renderTextInput(frame, chatInput, inputArea)
			}
		} else {
			if r.UnreadChatCount > 0 {
				unfocusedPrompt := Tf(" %d New Messages - [Enter] to Chat ", r.UnreadChatCount)
				buf.SetString(logInner.X+1, inputY, clipToWidth(unfocusedPrompt, int(logInner.Width)-2), cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Warning,
					Modifier: cell.ModifierBold,
				})
			} else {
				unfocusedPrompt := T("[ Press Enter or / to Chat ]")
				buf.SetString(logInner.X+1, inputY, clipToWidth(unfocusedPrompt, int(logInner.Width)-2), cell.Style{
					Fg: theme.TextMuted,
					Bg: theme.SurfaceBg,
				})
			}
		}
	}
}

// controlItems lists the room's control buttons in display order.
func (r *RoomView) controlItems(node *p2p.P2PNode, audio *engine.AudioEngine) []flowItem {
	theme := CurrentTheme()
	black := cell.NewColorRGB(0x00, 0x00, 0x00)
	filled := func(bg cell.Color) cell.Style {
		return cell.Style{Fg: black, Bg: bg, Modifier: cell.ModifierBold}
	}
	plain := func(fg cell.Color) cell.Style {
		return cell.Style{Fg: fg, Bg: theme.SurfaceBg}
	}

	mute := flowItem{label: T("[M] Mute Mic"), short: T("[M] Mute"), style: plain(theme.Success)}
	if audio.Muted {
		mute = flowItem{label: T("[M] Unmute Mic"), short: T("[M] Unmute"), style: filled(theme.Danger)}
	}
	mute.onClick = func(_ driver.MouseEvent) {
		isMuted := audio.ToggleMute()
		node.SendMuteState(isMuted)
		if isMuted {
			r.SetToast("Microphone Off (Muted)")
		} else {
			r.SetToast("Microphone On")
		}
	}

	deafen := flowItem{label: T("[D] Deafen"), style: plain(theme.Secondary)}
	if audio.Deafened {
		deafen = flowItem{label: T("[D] Undeafen"), style: filled(theme.Warning)}
	}
	deafen.onClick = func(_ driver.MouseEvent) {
		isDeaf := audio.ToggleDeafen()
		node.SendDeafenState(isDeaf)
		node.SendMuteState(audio.Muted)
		if isDeaf {
			r.SetToast("Audio Off (Deafened)")
		} else {
			r.SetToast("Audio On")
		}
	}

	mode := flowItem{label: T("[P] Voice"), style: plain(theme.TextMuted)}
	if audio.InputMode == engine.InputModePushToTalk {
		mode = flowItem{label: T("[P] PTT"), style: filled(theme.Warning)}
		if audio.IsTransmitting() {
			mode.style = filled(theme.Success)
		}
	}
	mode.onClick = func(_ driver.MouseEvent) {
		if audio.CycleInputMode() == engine.InputModePushToTalk {
			r.SetToast(fmt.Sprintf("Mode: Push-to-Talk (Hold %s to talk)", audio.GetPTTKeyName()))
		} else {
			r.SetToast("Mode: Voice Activity (Always on / VAD)")
		}
	}

	noiseStr := tr(audio.SuppressionModeString())
	noise := flowItem{
		label: Tf("[N] Noise: %s", noiseStr),
		style: plain(theme.TextMuted),
		onClick: func(_ driver.MouseEvent) {
			audio.CycleSuppressionMode()
			r.SetToast(fmt.Sprintf("Noise Filter: %s", audio.SuppressionModeString()))
		},
	}
	if audio.SuppressionMode > 0 {
		noise.style = cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
	}

	share := flowItem{label: T("[V] Share Screen"), short: T("[V] Share"), style: plain(theme.Secondary)}
	if node.IsSharingScreen {
		share = flowItem{label: T("[V] Stop Sharing"), short: T("[V] Stop"), style: filled(theme.Danger)}
	}
	share.onClick = func(_ driver.MouseEvent) {
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
	}

	var streamingPeer *p2p.PeerInfo
	for _, p := range node.GetPeersList() {
		if p.IsSharingScreen {
			streamingPeer = p
			break
		}
	}
	watch := flowItem{label: T("[W] Watch Screen"), short: T("[W] Watch"), style: plain(theme.TextMuted)}
	if node.IsWatchingScreen {
		watch = flowItem{label: T("[W] Stop Watching"), short: T("[W] Stop"), style: filled(theme.Warning)}
	} else if streamingPeer != nil {
		watch = flowItem{label: Tf("[W] Watch %s", streamingPeer.Nickname), short: T("[W] Watch"), style: filled(theme.Success)}
	}
	watch.onClick = func(_ driver.MouseEvent) {
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
			opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", streamingPeer.Nickname, fps)
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
	}

	test := flowItem{
		label: T("[T] Test"),
		style: cell.Style{Fg: theme.Secondary, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold},
		onClick: func(_ driver.MouseEvent) {
			if r.OnOpenTestModal != nil {
				r.OnOpenTestModal()
			}
		},
	}

	// "[+" raises, "/-]" lowers, clicking the value itself raises.
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
	volume := flowItem{
		label: Tf("[+/-] Vol: %.0f%%", audio.Gain*100),
		short: Tf("[+/-] %.0f%%", audio.Gain*100),
		style: plain(theme.Warning),
		zones: []flowZone{{0, 2, increase}, {2, 3, decrease}, {5, 64, increase}},
	}

	copyCode := flowItem{
		label: T("[C] Copy"),
		style: plain(theme.Accent),
		onClick: func(_ driver.MouseEvent) {
			CopyToClipboard(node.RoomCode)
			r.SetToast(fmt.Sprintf("Room Code Copied: %s", node.RoomCode))
		},
	}

	leave := flowItem{
		label:    T("[Esc] Leave"),
		style:    cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold},
		pinRight: true,
		onClick: func(_ driver.MouseEvent) {
			if r.OnLeave != nil {
				r.OnLeave()
			}
		},
	}

	return []flowItem{mute, deafen, mode, noise, share, watch, test, volume, copyCode, leave}
}
