package main

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/thebanri/limoni/core/driver"
)

func (a *App) handlePaste(pasted string) {
	// Discard residual bracketed paste sequences sent during terminal startup
	if time.Since(a.appStartTime) < 1200*time.Millisecond {
		return
	}
	if a.showRelayModal {
		toastMsg := "Pasted URL"
		if a.relayModalActiveField == 1 {
			toastMsg = "Pasted token"
		}
		a.relayInsert(a.relayModalActiveField, strings.TrimSpace(pasted), toastMsg)
		return
	}
	if pasted == "" {
		return
	}
	if a.currentScreen == ScreenLobby && !a.showTestModal && !a.showExitModal {
		cleanPasted := strings.TrimSpace(pasted)
		switch a.lobby.ActiveInput {
		case 0:
			a.lobby.NickState.SetValue(cleanPasted)
			a.lobby.SetToast("Username pasted")
		case 1:
			if cleanCode := NormalizeCode(cleanPasted); cleanCode != "" {
				a.lobby.CodeState.SetValue(cleanCode)
				a.lobby.SetToast(fmt.Sprintf("Room key pasted: %s", cleanCode))
			}
		}
	} else if a.currentScreen == ScreenRoom && !a.showTestModal && !a.showLeaveModal && !a.showExitModal && !a.showScreenShareModal {
		a.room.SetChatFocused(true)
		for _, r := range pasted {
			if r != '\r' {
				a.room.ChatInputState.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: r})
			}
		}
	}
}

func (a *App) handleKey(e driver.KeyEvent) {
	if a.showRelayModal {
		a.handleRelayModalKey(e)
		return
	}

	// Ctrl+C: copy chat selection, otherwise exit / leave prompt
	if e.Ctrl && (e.Ch == 'c' || e.Ch == 'C') {
		if a.currentScreen == ScreenRoom {
			a.room.mu.Lock()
			selText := a.room.SelectedText
			selActive := a.room.SelectionActive
			a.room.mu.Unlock()
			if selActive && selText != "" {
				CopyToClipboard(selText)
				a.room.SetToast(fmt.Sprintf("✓ Copied: %s", preview(selText)))
				return
			}
		}
		if a.showExitModal || a.showLeaveModal {
			a.cleanExit()
		}
		if a.currentScreen == ScreenRoom {
			a.openLeaveModal()
		} else {
			a.openExitModal()
		}
		return
	}

	// Ctrl+V in active room chat
	if e.Ctrl && (e.Ch == 'v' || e.Ch == 'V') && a.currentScreen == ScreenRoom &&
		!a.showTestModal && !a.showLeaveModal && !a.showExitModal && !a.showScreenShareModal && !a.showDebugModal {
		a.room.SetChatFocused(true)
		for _, r := range GetClipboardText() {
			if r != '\r' {
				a.room.ChatInputState.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: r})
			}
		}
		return
	}

	// Global debug modal (F12)
	if e.Type == driver.KeyF12 {
		if a.showDebugModal {
			a.closeDebugModal()
		} else {
			a.openDebugModal()
		}
		return
	}

	switch {
	case a.showDebugModal:
		a.handleDebugModalKey(e)
		return
	case a.activeFileOffer() != nil:
		a.handleFileOfferKey(e)
		return
	case a.showExitModal:
		a.handleConfirmModalKey(e, "exit_app_dialog_btn_0", a.cleanExit, a.closeExitModal)
		return
	case a.showLeaveModal:
		a.handleConfirmModalKey(e, "leave_room_dialog_btn_0", a.leaveRoom, a.closeLeaveModal)
		return
	case a.showTestModal:
		a.handleTestModalKey(e)
		return
	case a.showScreenShareModal:
		a.handleScreenShareModalKey(e)
		return
	}

	switch e.Type {
	case driver.KeyF4:
		a.openTestModal()
		return
	case driver.KeyF5:
		a.openRelayModal()
		return
	}

	if a.currentScreen == ScreenLobby {
		a.handleLobbyKey(e)
	} else {
		a.handleRoomKey(e)
	}
}

func preview(s string) string {
	if r := []rune(s); len(r) > 30 {
		return string(r[:30]) + "…"
	}
	return s
}

func (a *App) handleRelayModalKey(e driver.KeyEvent) {
	field := a.relayModalActiveField
	target := a.relayField(field)

	if e.Ctrl {
		switch e.Ch {
		case 'v', 'V':
			a.relayPasteAction(field)
			return
		case 'c', 'C':
			a.relayCopyAction(field)
			return
		case 'x', 'X':
			if target != nil {
				if a.hasRelaySelection(field) {
					s, end := min(a.relaySelStart, a.relaySelEnd), max(a.relaySelStart, a.relaySelEnd)
					if s < len(target.Text) && end <= len(target.Text) {
						CopyToClipboard(string(target.Text[s:end]))
						deleteSelectedRange(target, s, end)
						a.clearRelaySelection()
					}
				} else {
					CopyToClipboard(target.Value())
					target.SetValue("")
				}
				a.toast("Cut to clipboard")
			}
			return
		case 'a', 'A':
			if target != nil {
				a.relaySelField, a.relaySelStart, a.relaySelEnd = field, 0, len(target.Text)
				target.Cursor = len(target.Text)
			}
			return
		case 'u', 'U':
			a.relayClearAction(field)
			return
		}
	}

	// Shift+Insert paste
	if e.Type == driver.KeyInsert && (e.Shift || e.Ctrl) {
		a.relayPasteAction(field)
		return
	}

	extendSelection := func(move func()) {
		if a.relaySelField != field || a.relaySelStart == -1 {
			a.relaySelField, a.relaySelStart, a.relaySelEnd = field, target.Cursor, target.Cursor
		}
		move()
		a.relaySelEnd = target.Cursor
	}

	switch e.Type {
	case driver.KeyEsc:
		a.closeRelayModal()
	case driver.KeyTab:
		a.clearRelaySelection()
		if e.Shift {
			a.relayModalActiveField = (field + 4) % 5
		} else {
			a.relayModalActiveField = (field + 1) % 5
		}
	case driver.KeyArrowUp:
		a.clearRelaySelection()
		if field > 0 {
			a.relayModalActiveField--
		}
	case driver.KeyArrowDown:
		a.clearRelaySelection()
		if field < 4 {
			a.relayModalActiveField++
		}
	case driver.KeyArrowLeft:
		if field > 2 {
			a.relayModalActiveField--
		} else if target != nil {
			if e.Shift {
				extendSelection(func() {
					if target.Cursor > 0 {
						target.Cursor--
					}
				})
			} else {
				a.clearRelaySelection()
				target.HandleKey(e)
			}
		}
	case driver.KeyArrowRight:
		if field >= 2 && field < 4 {
			a.relayModalActiveField++
		} else if target != nil {
			if e.Shift {
				extendSelection(func() {
					if target.Cursor < len(target.Text) {
						target.Cursor++
					}
				})
			} else {
				a.clearRelaySelection()
				target.HandleKey(e)
			}
		}
	case driver.KeyHome, driver.KeyEnd:
		if target != nil {
			if e.Shift {
				extendSelection(func() { target.HandleKey(e) })
			} else {
				a.clearRelaySelection()
				target.HandleKey(e)
			}
		}
	case driver.KeyBackspace, driver.KeyDelete:
		if target != nil {
			if a.hasRelaySelection(field) {
				deleteSelectedRange(target, a.relaySelStart, a.relaySelEnd)
				a.clearRelaySelection()
			} else {
				target.HandleKey(e)
			}
		}
	case driver.KeyEnter:
		switch field {
		case 0, 1, 2:
			a.saveRelaySettings(a.relayURLInput.Value(), a.relayTokenInput.Value())
		case 3:
			a.toggleRelayMode()
		case 4:
			a.closeRelayModal()
		}
	default: // KeySpace and runes
		if target != nil {
			if a.hasRelaySelection(field) {
				deleteSelectedRange(target, a.relaySelStart, a.relaySelEnd)
				a.clearRelaySelection()
			}
			target.HandleKey(e)
		}
	}
}

func (a *App) handleDebugModalKey(e driver.KeyEvent) {
	switch e.Type {
	case driver.KeyEsc:
		a.closeDebugModal()
	case driver.KeyRune:
		switch e.Ch {
		case 'c', 'C':
			CopyToClipboard(a.debugText())
			a.toast("Copied network diagnostics and debug logs to clipboard")
		case 'x', 'X':
			ClearDebugLogs()
		}
	case driver.KeyDelete:
		ClearDebugLogs()
	case driver.KeyArrowUp:
		a.debugScrollOffset++
	case driver.KeyArrowDown:
		if a.debugScrollOffset > 0 {
			a.debugScrollOffset--
		}
	case driver.KeyPageUp:
		a.debugScrollOffset += 10
	case driver.KeyPageDown:
		a.debugScrollOffset = max(0, a.debugScrollOffset-10)
	case driver.KeyHome:
		a.debugScrollOffset = len(GetDebugLogs())
	case driver.KeyEnd:
		a.debugScrollOffset = 0
	}
}

func (a *App) handleFileOfferKey(e driver.KeyEvent) {
	switch e.Type {
	case driver.KeyEnter:
		a.acceptCurrentOffer(false)
	case driver.KeyEsc:
		a.declineCurrentOffer()
	case driver.KeyRune:
		switch e.Ch {
		case 'y', 'Y':
			a.acceptCurrentOffer(false)
		case 'n', 'N':
			a.declineCurrentOffer()
		case 'o', 'O':
			if offer := a.activeFileOffer(); offer != nil && offer.IsCode {
				a.acceptCurrentOffer(true)
			}
		}
	}
}

// handleConfirmModalKey drives the exit / leave confirmation dialogs.
func (a *App) handleConfirmModalKey(e driver.KeyEvent, confirmID string, confirm, cancel func()) {
	fm := a.term.FocusManager()
	switch e.Type {
	case driver.KeyTab:
		if e.Shift {
			fm.Prev()
		} else {
			fm.Next()
		}
	case driver.KeyArrowLeft:
		fm.Prev()
	case driver.KeyArrowRight:
		fm.Next()
	case driver.KeyEnter, driver.KeySpace:
		if fm.Focused() == confirmID {
			confirm()
		} else {
			cancel()
		}
	case driver.KeyEsc:
		cancel()
	case driver.KeyRune:
		switch e.Ch {
		case 'e', 'E', 'y', 'Y':
			confirm()
		case 'h', 'H', 'n', 'N':
			cancel()
		}
	}
}

// pttKeyName returns the configured PTT key name for a terminal key event (or "").
func pttKeyName(e driver.KeyEvent) (rune, string) {
	switch e.Type {
	case driver.KeySpace:
		return ' ', "Space"
	case driver.KeyEnter:
		return '\n', "Enter"
	case driver.KeyTab:
		return '\t', "Tab"
	case driver.KeyF1, driver.KeyF2, driver.KeyF3, driver.KeyF4, driver.KeyF5, driver.KeyF6,
		driver.KeyF7, driver.KeyF8, driver.KeyF9, driver.KeyF10, driver.KeyF11:
		return 0, fmt.Sprintf("F%d", int(e.Type-driver.KeyF1)+1)
	case driver.KeyRune:
		ch := unicode.ToLower(e.Ch)
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			return ch, strings.ToUpper(string(e.Ch))
		}
	}
	return 0, ""
}

// isPTTKeyEvent reports whether a terminal key event matches the configured PTT key.
func (a *App) isPTTKeyEvent(e driver.KeyEvent) bool {
	if a.audio.InputMode != InputModePushToTalk {
		return false
	}
	_, name := pttKeyName(e)
	return name != "" && strings.EqualFold(name, a.audio.GetPTTKeyName())
}

func (a *App) handleTestModalKey(e driver.KeyEvent) {
	audio := a.audio
	if audio.PTTListeningKey {
		if e.Type == driver.KeyEsc {
			audio.mu.Lock()
			audio.PTTListeningKey = false
			audio.mu.Unlock()
			return
		}
		if key, name := pttKeyName(e); name != "" {
			audio.SetPTTKey(key, name)
			a.syncGlobalPTT()
		}
		return
	}

	if a.isPTTKeyEvent(e) {
		audio.PulsePTT(350 * time.Millisecond)
		return
	}

	switch e.Type {
	case driver.KeyEsc:
		a.closeTestModal()
	case driver.KeySpace:
		if audio.InputMode == InputModeVoiceActivity {
			audio.ToggleLoopback()
		}
	case driver.KeyArrowLeft:
		audio.CycleInputDevice(-1)
	case driver.KeyArrowRight:
		audio.CycleInputDevice(1)
	case driver.KeyRune:
		switch e.Ch {
		case 'k', 'K':
			if audio.InputMode == InputModePushToTalk {
				audio.mu.Lock()
				audio.PTTListeningKey = true
				audio.mu.Unlock()
			}
		case 'p', 'P':
			audio.CycleInputMode()
			a.syncGlobalPTT()
		case 'g', 'G':
			a.toggleGlobalPTT()
		case 'e', 'E':
			if audio.ToggleEchoCancellation() {
				a.toast("Echo cancellation ON")
			} else {
				a.toast("Echo cancellation OFF")
			}
		case 'l', 'L':
			audio.ToggleLoopback()
		case 'n', 'N':
			audio.CycleSuppressionMode()
		case 'm', 'M':
			a.node.SendMuteState(audio.ToggleMute())
		case 'd', 'D':
			a.node.SendDeafenState(audio.ToggleDeafen())
			a.node.SendMuteState(audio.Muted)
		case '1', '[', ']':
			audio.CycleInputDevice(1)
		case '2', 'o', 'O':
			audio.CycleOutputDevice(1)
		case '-', '_':
			audio.AdjustGain(-0.1)
		case '+', '=':
			audio.AdjustGain(0.1)
		case '9', '(':
			audio.AdjustOutputVolume(-0.1)
		case '0', ')':
			audio.AdjustOutputVolume(0.1)
		}
	}
}

func (a *App) handleScreenShareModalKey(e driver.KeyEvent) {
	cycleFPS := func() {
		switch a.selectedScreenShareFPS {
		case 30:
			a.selectedScreenShareFPS = 60
		case 60:
			a.selectedScreenShareFPS = 120
		default:
			a.selectedScreenShareFPS = 30
		}
	}
	switch e.Type {
	case driver.KeyEsc:
		a.closeScreenShareModal()
	case driver.KeyArrowUp:
		if a.selectedScreenShareIdx > 0 {
			a.selectedScreenShareIdx--
		}
	case driver.KeyArrowDown:
		if a.selectedScreenShareIdx < len(a.screenShareTargets)-1 {
			a.selectedScreenShareIdx++
		}
	case driver.KeyArrowLeft:
		if a.selectedScreenShareFPS == 120 {
			a.selectedScreenShareFPS = 60
		} else if a.selectedScreenShareFPS == 60 {
			a.selectedScreenShareFPS = 30
		}
	case driver.KeyArrowRight:
		if a.selectedScreenShareFPS == 30 {
			a.selectedScreenShareFPS = 60
		} else if a.selectedScreenShareFPS == 60 {
			a.selectedScreenShareFPS = 120
		}
	case driver.KeyTab:
		cycleFPS()
	case driver.KeyRune:
		switch e.Ch {
		case '1':
			a.selectedScreenShareFPS = 30
		case '2':
			a.selectedScreenShareFPS = 60
		case '3':
			a.selectedScreenShareFPS = 120
		case 'f', 'F':
			cycleFPS()
		}
	case driver.KeyEnter, driver.KeySpace:
		if a.selectedScreenShareIdx >= 0 && a.selectedScreenShareIdx < len(a.screenShareTargets) {
			a.startSelectedScreenShare(a.screenShareTargets[a.selectedScreenShareIdx])
		} else {
			a.closeScreenShareModal()
		}
	}
}

func (a *App) handleLobbyKey(e driver.KeyEvent) {
	lobby := a.lobby
	switch e.Type {
	case driver.KeyF2:
		CopyToClipboard(lobby.CurrentCode)
		lobby.SetToast(fmt.Sprintf("Room key copied: %s", lobby.CurrentCode))
		return
	case driver.KeyF3:
		lobby.CurrentCode = GenerateRoomCode()
		lobby.SetToast(fmt.Sprintf("New room key generated: %s", lobby.CurrentCode))
		return
	}

	if e.Ctrl && (e.Ch == 'v' || e.Ch == 'V') {
		clipText := GetClipboardText()
		if clipText == "" {
			lobby.SetToast("Clipboard empty or unreadable")
			return
		}
		switch lobby.ActiveInput {
		case 0:
			lobby.NickState.SetValue(clipText)
			lobby.SetToast("Username pasted")
		case 1:
			cleanCode := NormalizeCode(clipText)
			lobby.CodeState.SetValue(cleanCode)
			lobby.SetToast(fmt.Sprintf("Room key pasted: %s", cleanCode))
		}
		return
	}

	if lobby.IsConnecting && e.Type == driver.KeyEsc {
		a.cancelJoin()
		return
	}

	if e.Type == driver.KeyTab {
		numInputs := 3
		if lobby.IsPinProtected {
			numInputs = 4
		}
		if lobby.ActiveInput >= numInputs {
			lobby.ActiveInput = 0
		}
		if e.Shift {
			lobby.ActiveInput = (lobby.ActiveInput + numInputs - 1) % numInputs
		} else {
			lobby.ActiveInput = (lobby.ActiveInput + 1) % numInputs
		}
		return
	}

	if e.Type == driver.KeyEnter {
		if joinCode := NormalizeCode(lobby.CodeState.Value()); lobby.ActiveInput == 1 && joinCode != "" {
			a.joinRoom(joinCode)
		} else {
			a.startHost()
		}
		return
	}

	switch lobby.ActiveInput {
	case 0:
		if e.Type == driver.KeyEsc {
			lobby.ActiveInput = 2
		} else {
			lobby.NickState.HandleKey(e)
		}
	case 1:
		if e.Type == driver.KeyEsc {
			lobby.ActiveInput = 2
		} else {
			lobby.CodeState.HandleKey(e)
		}
	case 2:
		if e.Type == driver.KeyEsc {
			a.openExitModal()
			return
		}
		if e.Type != driver.KeyRune {
			return
		}
		switch e.Ch {
		case '1':
			lobby.ActiveInput = 0
		case '2':
			a.startHost()
		case '3':
			lobby.ActiveInput = 1
		case 'p', 'P', 'x', 'X':
			lobby.IsPinProtected = !lobby.IsPinProtected
			if lobby.IsPinProtected {
				if lobby.PinState.Value() == "" {
					lobby.PinState.SetValue("1234")
				}
				lobby.ActiveInput = 3
				lobby.SetToast("PIN Protection Enabled (4 Digits)")
			} else {
				lobby.PinState.SetValue("")
				lobby.ActiveInput = 2
				lobby.SetToast("PIN Protection Disabled")
			}
		case 'c', 'C':
			CopyToClipboard(lobby.CurrentCode)
			lobby.SetToast(fmt.Sprintf("Room key copied: %s", lobby.CurrentCode))
		case 'g', 'G':
			lobby.CurrentCode = GenerateRoomCode()
			lobby.SetToast("New room key generated!")
		case ' ':
			lobby.AutoRotate = !lobby.AutoRotate
			if lobby.AutoRotate {
				lobby.SetToast("3D Auto-Rotate: Enabled")
			} else {
				lobby.SetToast("3D Auto-Rotate: Paused")
			}
		case 't', 'T':
			a.openTestModal()
		case 'r', 'R':
			a.openRelayModal()
		case 'q', 'Q':
			a.openExitModal()
		}
	case 3:
		switch e.Type {
		case driver.KeyEsc:
			lobby.ActiveInput = 2
		case driver.KeyEnter:
			a.startHost()
		default:
			lobby.PinState.HandleKey(e)
		}
	}
}

func (a *App) handleRoomKey(e driver.KeyEvent) {
	room, audio, node := a.room, a.audio, a.node

	if room.IsChatFocused {
		switch e.Type {
		case driver.KeyEsc:
			room.SetChatFocused(false)
		case driver.KeyEnter:
			if e.Shift || e.Alt || e.Ctrl {
				room.ChatInputState.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: '\n'})
				return
			}
			val := room.ChatInputState.Value()
			if strings.HasSuffix(val, `\`) && !strings.HasSuffix(val, `\\`) {
				// Trailing backslash line continuation
				room.ChatInputState.SetValue(strings.TrimSuffix(val, `\`) + "\n")
				return
			}
			if strings.HasSuffix(val, `\\`) {
				room.ChatInputState.SetValue(strings.TrimSuffix(val, `\`))
			}
			if strings.TrimSpace(room.ChatInputState.Value()) != "" {
				room.SendCurrentChat()
			} else {
				room.SetChatFocused(false)
			}
		case driver.KeyTab:
			room.SetChatFocused(false)
		case driver.KeyArrowUp:
			if e.Alt || e.Ctrl {
				room.ScrollChat(1)
			} else {
				room.HistoryUp()
			}
		case driver.KeyArrowDown:
			if e.Alt || e.Ctrl {
				room.ScrollChat(-1)
			} else {
				room.HistoryDown()
			}
		case driver.KeyPageUp:
			room.ScrollChat(5)
		case driver.KeyPageDown:
			room.ScrollChat(-5)
		default:
			room.ChatInputState.HandleKey(e)
		}
		return
	}

	if a.isPTTKeyEvent(e) {
		audio.PulsePTT(350 * time.Millisecond)
		return
	}

	switch e.Type {
	case driver.KeyEnter, driver.KeyTab:
		room.SetChatFocused(true)
	case driver.KeyPageUp, driver.KeyArrowUp:
		room.ScrollChat(1)
	case driver.KeyPageDown, driver.KeyArrowDown:
		room.ScrollChat(-1)
	case driver.KeyEsc:
		switch {
		case node.IsWatchingScreen:
			_ = node.StopWatchingScreen()
			room.SetToast("Screen viewer closed")
		case node.IsSharingScreen:
			_ = node.StopScreenShare()
			room.SetToast("Screen share stopped")
		default:
			a.openLeaveModal()
		}
	case driver.KeyF2:
		CopyToClipboard(node.RoomCode)
		room.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))
	case driver.KeyRune:
		switch e.Ch {
		case '/':
			room.SetChatFocused(true)
		case 'p', 'P':
			if audio.CycleInputMode() == InputModePushToTalk {
				room.SetToast(fmt.Sprintf("Mode: Push-to-Talk (Hold %s to talk)", audio.GetPTTKeyName()))
			} else {
				room.SetToast("Mode: Voice Activity (Always on / VAD)")
			}
			a.syncGlobalPTT()
			a.saveAudioSettings()
		case 't', 'T':
			a.openTestModal()
		case 'h', 'H':
			newVal := ToggleCompactHUD()
			room.IsCompactMode = newVal
			if newVal {
				room.SetToast("Mini HUD Mode ON")
			} else {
				room.SetToast("Full UI Restored")
			}
		case 'n', 'N':
			audio.CycleSuppressionMode()
			room.SetToast(fmt.Sprintf("Noise Filter: %s", audio.SuppressionModeString()))
			a.saveAudioSettings()
		case 'e', 'E':
			if audio.ToggleEchoCancellation() {
				room.SetToast("Echo cancellation ON")
			} else {
				room.SetToast("Echo cancellation OFF")
			}
			a.saveAudioSettings()
		case 'm', 'M':
			isMuted := audio.ToggleMute()
			node.SendMuteState(isMuted)
			if isMuted {
				room.SetToast("Microphone Off (Muted)")
			} else {
				room.SetToast("Microphone On")
			}
		case 'd', 'D':
			isDeaf := audio.ToggleDeafen()
			node.SendDeafenState(isDeaf)
			node.SendMuteState(audio.Muted)
			if isDeaf {
				room.SetToast("Audio Off (Deafened)")
			} else {
				room.SetToast("Audio On")
			}
		case 'v', 'V':
			a.openScreenShareModal()
		case 'w', 'W':
			a.watchFirstStream()
		case '+', '=':
			room.SetToast(fmt.Sprintf("Mic Volume: %.0f%%", audio.AdjustGain(0.1)*100))
		case '-', '_':
			room.SetToast(fmt.Sprintf("Mic Volume: %.0f%%", audio.AdjustGain(-0.1)*100))
		case 'c', 'C':
			CopyToClipboard(node.RoomCode)
			room.SetToast(fmt.Sprintf("Room Code Copied: %s", node.RoomCode))
		case 'q', 'Q':
			a.openLeaveModal()
		}
	}
}
