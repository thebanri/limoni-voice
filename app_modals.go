package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/thebanri/limoni-voice/screenshare"
	"github.com/thebanri/limoni/animation"
	"github.com/thebanri/limoni/widgets"
)

// --- simple modals ---

func (a *App) openTestModal() {
	a.audio.EnterTestMode()
	a.showTestModal = true
}

func (a *App) closeTestModal() {
	a.audio.LeaveTestMode()
	a.showTestModal = false
	a.saveAudioSettings()
	a.term.ForceFullRedraw()
}

func (a *App) openExitModal() {
	a.showExitModal = true
	a.exitDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
	a.term.FocusManager().SetFocused("exit_app_dialog_btn_1")
}

func (a *App) closeExitModal() {
	a.showExitModal = false
	a.exitDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
	a.term.FocusManager().SetFocused("")
	a.term.ForceFullRedraw()
}

func (a *App) openLeaveModal() {
	a.showLeaveModal = true
	a.leaveDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
	a.term.FocusManager().SetFocused("leave_room_dialog_btn_1")
}

func (a *App) closeLeaveModal() {
	a.showLeaveModal = false
	a.leaveDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
	a.term.FocusManager().SetFocused("")
	a.term.ForceFullRedraw()
}

func (a *App) openDebugModal() {
	a.showDebugModal = true
	a.debugScrollOffset = 0
}

func (a *App) closeDebugModal() {
	a.showDebugModal = false
	a.term.ForceFullRedraw()
}

// debugText returns network diagnostics followed by all debug logs (for clipboard export).
func (a *App) debugText() string {
	return strings.Join(a.node.Diagnostics().Lines(), "\n") + "\n\n" + GetAllDebugLogsText()
}

// --- screen share ---

func (a *App) closeScreenShareModal() {
	a.showScreenShareModal = false
	a.screenShareDialogAnim.AnimateTo(0.0, 160*time.Millisecond, animation.EaseInCubic)
	a.term.ForceFullRedraw()
}

func (a *App) startSelectedScreenShare(target screenshare.WindowInfo) {
	presetIdx := a.screenPreset
	preset := screenshare.PresetByIndex(presetIdx)
	withAudio := a.shareSystemAudio
	a.closeScreenShareModal()
	a.saveScreenSettings()
	a.room.SetToast(fmt.Sprintf("🎬 Starting %s (%s)...", target.Title, preset.Name))
	go func() {
		cfg := ScreenShareConfig{TargetID: target.ID, Preset: presetIdx, SystemAudio: withAudio}
		if err := a.node.StartScreenShareWith(cfg); err != nil {
			a.room.SetToast(fmt.Sprintf("Error: %v", err))
			return
		}
		a.room.SetToast(fmt.Sprintf("%s sharing started (%s)", target.Title, preset.Name))
		if st := a.node.ScreenStats(); withAudio && !st.Audio {
			a.room.SetToast("System audio could not be shared: " + strings.TrimPrefix(st.AudioStatus, "unavailable: "))
		}
	}()
}

func (a *App) selectScreenPreset(idx int) {
	if idx < 0 || idx >= len(screenshare.Presets) {
		return
	}
	a.screenPreset = idx
}

func (a *App) toggleShareSystemAudio() {
	a.shareSystemAudio = !a.shareSystemAudio
}

func (a *App) saveScreenSettings() {
	preset, audio := a.screenPreset, a.shareSystemAudio
	_ = UpdateAppConfig(func(c *AppConfig) { c.Screen = &ScreenSettings{Preset: preset, SystemAudio: audio} })
}

func (a *App) openScreenShareModal() {
	if a.node.IsSharingScreen {
		go func() {
			_ = a.node.StopScreenShare()
			a.room.SetToast("⏹️ Screen share stopped")
		}()
		return
	}
	a.screenShareTargets = screenshare.ListWindows()
	if len(a.screenShareTargets) == 0 {
		a.screenShareTargets = []screenshare.WindowInfo{{ID: "desktop", Title: "[Desktop] Entire Screen (Primary View)"}}
	}
	a.selectedScreenShareIdx = 0
	a.screenShareDeps = screenshare.CheckDependencies()
	a.showScreenShareModal = true
	a.screenShareDialogAnim.AnimateTo(1.0, 200*time.Millisecond, animation.EaseOutCubic)
}

func (a *App) watchFirstStream() {
	if a.node.IsWatchingScreen {
		_ = a.node.StopWatchingScreen()
		a.room.SetToast("Screen viewer closed")
		return
	}
	var target *PeerInfo
	for _, p := range a.node.GetPeersList() {
		if p.IsSharingScreen {
			target = p
			break
		}
	}
	if target == nil {
		a.room.SetToast("No active screen share in the room")
		return
	}
	port := target.VideoPort
	if port <= 0 {
		port = 50100
	}
	fps := target.VideoFPS
	if fps <= 0 {
		fps = 60
	}
	opts := screenshare.DefaultReceiverOptions(fps)
	opts.WindowTitle = fmt.Sprintf("Limoni Voice - %s Live Stream (%d FPS)", target.Nickname, fps)
	a.room.SetToast(fmt.Sprintf("🎬 Starting %s stream (%d FPS)...", target.Nickname, fps))
	go func() {
		if err := a.node.StartWatchingScreen(target.ID, port, opts); err != nil {
			a.room.SetToast(fmt.Sprintf("Error: %v", err))
		} else {
			a.room.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", target.Nickname, fps))
		}
	}()
}

// --- file offers ---

func (a *App) showNextFileOffer() {
	a.fileOfferMu.Lock()
	if len(a.pendingFileOffers) > 0 {
		a.currentFileOffer = a.pendingFileOffers[0]
		a.pendingFileOffers = a.pendingFileOffers[1:]
		a.fileOfferDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
	} else {
		a.currentFileOffer = nil
		a.fileOfferDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
	}
	a.fileOfferMu.Unlock()
	a.term.ForceFullRedraw()
}

func (a *App) enqueueFileOffer(offer *FileOffer) {
	a.fileOfferMu.Lock()
	a.pendingFileOffers = append(a.pendingFileOffers, offer)
	if a.currentFileOffer == nil {
		a.currentFileOffer = a.pendingFileOffers[0]
		a.pendingFileOffers = a.pendingFileOffers[1:]
		a.fileOfferDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
	}
	a.fileOfferMu.Unlock()
	a.audio.PlaySound(SoundChat)
	targetLabel := "file"
	if offer.IsCode {
		targetLabel = "code snippet"
	}
	a.toast(fmt.Sprintf("📥 Incoming %s from %s: %s", targetLabel, offer.SenderNick, offer.FileName))
	a.term.ForceFullRedraw()
}

func (a *App) activeFileOffer() *FileOffer {
	a.fileOfferMu.Lock()
	defer a.fileOfferMu.Unlock()
	return a.currentFileOffer
}

func (a *App) acceptCurrentOffer(openInEditor bool) {
	offer := a.activeFileOffer()
	if offer == nil {
		return
	}
	savedPath, err := SaveAcceptedFile(offer)
	switch {
	case err != nil:
		a.toast(fmt.Sprintf("Save error: %v", err))
		if a.currentScreen == ScreenRoom {
			a.room.AddLog(fmt.Sprintf("[FILE] Error saving '%s': %v", offer.FileName, err))
		}
	case offer.IsCode:
		if a.currentScreen == ScreenRoom {
			a.room.AddLog(fmt.Sprintf("[CODE] ✓ Accepted code snippet '%s' from %s (saved to %s)", offer.FileName, offer.SenderNick, savedPath))
		}
		a.toast(fmt.Sprintf("✓ Code snippet saved: %s", offer.FileName))
		if openInEditor {
			if err := OpenInEditor(savedPath); err != nil {
				a.toast(fmt.Sprintf("Could not open editor: %v", err))
			}
		}
	default:
		if a.currentScreen == ScreenRoom {
			a.room.AddLog(fmt.Sprintf("[FILE] ✓ Accepted file '%s' (%s) from %s (saved to %s)", offer.FileName, formatBytes(offer.FileSize), offer.SenderNick, savedPath))
		}
		a.toast(fmt.Sprintf("✓ File saved to Downloads: %s", offer.FileName))
	}
	a.showNextFileOffer()
}

func (a *App) declineCurrentOffer() {
	if offer := a.activeFileOffer(); offer != nil {
		if a.currentScreen == ScreenRoom {
			a.room.AddLog(fmt.Sprintf("[FILE] ✗ Declined file transfer '%s' from %s", offer.FileName, offer.SenderNick))
		}
		a.toast(fmt.Sprintf("✗ Declined: %s", offer.FileName))
	}
	a.showNextFileOffer()
}

// --- relay settings modal ---

func (a *App) probeRelayStatus(u, tok string) {
	target := NormalizeRelayURL(u)
	if target == "" {
		a.lobby.RelayOnline = false
		a.lobby.RelayStatus = "LAN Mode"
		return
	}
	a.lobby.RelayStatus = "Connecting..."
	a.lobby.RelayOnline = false
	go func() {
		online, status := ProbeRelayServer(target, tok, 5*time.Second)
		a.lobby.RelayOnline = online
		a.lobby.RelayStatus = status
	}()
}

func (a *App) openRelayModal() {
	a.showRelayModal = true
	currURL := a.node.RelayURL
	if currURL == "" && !a.node.LanOnly {
		currURL = DefaultRelayURL
	}
	a.relayURLInput.SetValue(currURL)
	a.relayTokenInput.SetValue(a.node.RelayToken)
	a.relayModalActiveField = 0
	a.relaySelStart = 0
	a.relaySelEnd = len(a.relayURLInput.Text)
	a.relaySelField = 0
	a.probeRelayStatus(currURL, a.node.RelayToken)
	a.relayDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
}

func (a *App) closeRelayModal() {
	a.showRelayModal = false
	a.clearRelaySelection()
	a.relayDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
	a.term.ForceFullRedraw()
}

func (a *App) clearRelaySelection() {
	a.relaySelStart, a.relaySelEnd, a.relaySelField = -1, -1, -1
}

func (a *App) relayField(field int) *widgets.TextInputState {
	switch field {
	case 0:
		return a.relayURLInput
	case 1:
		return a.relayTokenInput
	}
	return nil
}

func (a *App) hasRelaySelection(field int) bool {
	return a.relaySelStart != -1 && a.relaySelStart != a.relaySelEnd && a.relaySelField == field
}

func deleteSelectedRange(state *widgets.TextInputState, start, end int) {
	if state == nil || start < 0 || end < 0 || start == end {
		return
	}
	if start > end {
		start, end = end, start
	}
	start = min(start, len(state.Text))
	end = min(end, len(state.Text))
	state.Text = append(state.Text[:start], state.Text[end:]...)
	state.Cursor = start
}

func insertStringAtCursor(state *widgets.TextInputState, s string) {
	if state == nil || s == "" {
		return
	}
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	runes := []rune(s)
	newText := make([]rune, len(state.Text)+len(runes))
	copy(newText, state.Text[:state.Cursor])
	copy(newText[state.Cursor:], runes)
	copy(newText[state.Cursor+len(runes):], state.Text[state.Cursor:])
	state.Text = newText
	state.Cursor += len(runes)
}

func (a *App) relayInsert(field int, text string, toastMsg string) {
	target := a.relayField(field)
	if target == nil || text == "" {
		return
	}
	if a.hasRelaySelection(field) {
		deleteSelectedRange(target, a.relaySelStart, a.relaySelEnd)
	}
	insertStringAtCursor(target, text)
	a.clearRelaySelection()
	a.relayModalActiveField = field
	if toastMsg != "" {
		a.toast(toastMsg)
	}
}

func (a *App) relayPasteAction(field int) {
	toastMsg := "Pasted URL"
	if field == 1 {
		toastMsg = "Pasted token"
	}
	a.relayInsert(field, strings.TrimSpace(GetClipboardText()), toastMsg)
}

func (a *App) relayCopyAction(field int) {
	target := a.relayField(field)
	if target == nil {
		return
	}
	toastMsg := "Copied server URL"
	if field == 1 {
		toastMsg = "Copied token"
	}
	text := target.Value()
	if a.hasRelaySelection(field) {
		s, e := min(a.relaySelStart, a.relaySelEnd), max(a.relaySelStart, a.relaySelEnd)
		if s < len(target.Text) && e <= len(target.Text) {
			text = string(target.Text[s:e])
		}
	}
	if text != "" {
		CopyToClipboard(text)
		a.toast(toastMsg)
	}
}

func (a *App) relayClearAction(field int) {
	if target := a.relayField(field); target != nil {
		target.SetValue("")
	}
	a.clearRelaySelection()
	a.relayModalActiveField = field
}

func isLanKeyword(raw string) bool {
	v := strings.TrimSpace(raw)
	return strings.EqualFold(v, "lan") || strings.EqualFold(v, "none") || strings.EqualFold(v, "off") || strings.EqualFold(v, "local")
}

// saveRelaySettings applies the relay URL / token from the modal and persists them.
func (a *App) saveRelaySettings(rawURL, rawToken string) {
	if isLanKeyword(rawURL) {
		a.switchToLAN()
		return
	}
	newURL := NormalizeRelayURL(rawURL)
	newToken := strings.TrimSpace(rawToken)
	a.node.UpdateRelaySettings(newURL, newToken)
	_ = UpdateAppConfig(func(c *AppConfig) {
		if newURL == DefaultRelayURL && newToken == "" {
			c.RelayURL, c.RelayToken = "", ""
		} else {
			c.RelayURL, c.RelayToken = newURL, newToken
		}
	})
	a.probeRelayStatus(newURL, newToken)
	a.relayURLInput.SetValue(newURL)
	a.lobby.RelayURL = newURL
	a.toast("Relay server settings saved!")
	a.closeRelayModal()
}

func (a *App) switchToLAN() {
	a.node.UpdateRelaySettings("lan", "")
	_ = UpdateAppConfig(func(c *AppConfig) { c.RelayURL, c.RelayToken = "none", "" })
	a.relayURLInput.SetValue("")
	a.relayTokenInput.SetValue("")
	a.probeRelayStatus("", "")
	a.lobby.RelayURL = ""
	a.toast("Switched to Local LAN Mode (Offline)")
	a.closeRelayModal()
}

func (a *App) resetRelayToDefault() {
	a.node.UpdateRelaySettings(DefaultRelayURL, "")
	_ = UpdateAppConfig(func(c *AppConfig) { c.RelayURL, c.RelayToken = "", "" })
	a.relayURLInput.SetValue(DefaultRelayURL)
	a.relayTokenInput.SetValue("")
	a.probeRelayStatus(DefaultRelayURL, "")
	a.lobby.RelayURL = DefaultRelayURL
	a.toast("Restored official default relay server!")
	a.closeRelayModal()
}

// toggleRelayMode implements the modal's reset button: LAN ↔ official relay.
func (a *App) toggleRelayMode() {
	isLan := a.node.LanOnly || a.node.RelayURL == "" || isLanKeyword(a.node.RelayURL)
	if isLan {
		a.resetRelayToDefault()
	} else {
		a.switchToLAN()
	}
}
