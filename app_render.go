package main

import (
	"time"

	"github.com/thebanri/limoni/animation"

	"github.com/thebanri/limoni-voice/screenshare"
	"github.com/thebanri/limoni/core/terminal"
)

func (a *App) render(now time.Time) {
	dt := float64(now.Sub(a.lastTime).Milliseconds())
	a.lastTime = now

	for _, anim := range []*animation.Float{a.exitDialogAnim, a.leaveDialogAnim, a.screenShareDialogAnim, a.fileOfferDialogAnim, a.relayDialogAnim} {
		anim.Update(now)
	}

	exitProg := a.exitDialogAnim.Value()
	if exitProg <= 0.001 && !a.exitDialogAnim.IsAnimating() {
		a.showExitModal = false
	}
	leaveProg := a.leaveDialogAnim.Value()
	if leaveProg <= 0.001 && !a.leaveDialogAnim.IsAnimating() {
		a.showLeaveModal = false
	}
	screenShareProg := a.screenShareDialogAnim.Value()
	if screenShareProg <= 0.001 && !a.screenShareDialogAnim.IsAnimating() {
		a.showScreenShareModal = false
	}
	relayProg := a.relayDialogAnim.Value()
	if relayProg <= 0.001 && !a.relayDialogAnim.IsAnimating() {
		a.showRelayModal = false
	}
	fileOfferProg := a.fileOfferDialogAnim.Value()

	fm := a.term.FocusManager()
	focusRelayInputs := func() {
		switch a.relayModalActiveField {
		case 0:
			fm.SetFocused("relay_url_input")
		case 1:
			fm.SetFocused("relay_token_input")
		default:
			fm.SetFocused("")
		}
	}

	drawRelay := func(f *terminal.Frame, status string) {
		DrawRelayModal(
			f, f.Area(), relayProg,
			a.node.RelayURL, a.node.RelayToken,
			a.relayURLInput, a.relayTokenInput,
			a.relayModalActiveField,
			a.relaySelField, a.relaySelStart, a.relaySelEnd,
			func(field int) { a.relayModalActiveField = field },
			a.saveRelaySettings,
			a.resetRelayToDefault,
			a.closeRelayModal,
			status,
		)
	}
	drawDebug := func(f *terminal.Frame) {
		DrawDebugModal(f, f.Area(), a.debugScrollOffset, a.node.Diagnostics().Lines(), a.closeDebugModal, ClearDebugLogs, func() {
			CopyToClipboard(a.debugText())
			a.toast("Copied network diagnostics and debug logs to clipboard")
		})
	}
	drawFileOffer := func(f *terminal.Frame) {
		if offer := a.activeFileOffer(); offer != nil || fileOfferProg > 0.001 {
			DrawFileOfferModal(f, f.Area(), fileOfferProg, offer, func() {
				a.acceptCurrentOffer(false)
			}, a.declineCurrentOffer, func() {
				a.acceptCurrentOffer(true)
			})
		}
	}

	if a.currentScreen == ScreenLobby {
		if a.showRelayModal {
			focusRelayInputs()
		} else if !a.showTestModal && !a.showExitModal {
			switch a.lobby.ActiveInput {
			case 0:
				fm.SetFocused("nick_input")
			case 1:
				fm.SetFocused("roomcode_input")
			case 2:
				fm.SetFocused("")
			}
		}
		a.lobby.Update(dt)
		_ = a.term.Draw(func(f *terminal.Frame) {
			a.lastScreen = f.Area()
			a.lobby.Render(f, f.Area())
			switch {
			case a.showDebugModal:
				drawDebug(f)
			case a.showRelayModal || relayProg > 0.001:
				drawRelay(f, a.lobby.RelayStatus)
			case a.showTestModal:
				DrawTestModal(f, f.Area(), a.audio, a.node, a.toggleGlobalPTT, a.notifier.enabled.Load(), a.toggleNotificationsToast, a.closeTestModal)
			case a.showExitModal || exitProg > 0.001:
				DrawExitModal(f, f.Area(), exitProg, a.cleanExit, a.closeExitModal)
			}
			drawFileOffer(f)
		})
		return
	}

	if !a.showTestModal && !a.showLeaveModal && !a.showExitModal && !a.showScreenShareModal && !a.showDebugModal && !a.showRelayModal {
		if a.room.IsChatFocused {
			fm.SetFocused("room_chat_input")
		} else {
			fm.SetFocused("")
		}
	}
	a.wireRoomCallbacks()
	a.room.Update()
	if a.showRelayModal {
		focusRelayInputs()
	}
	_ = a.term.Draw(func(f *terminal.Frame) {
		a.lastScreen = f.Area()
		a.room.Render(f, f.Area(), a.node, a.audio)
		switch {
		case a.showDebugModal:
			drawDebug(f)
		case a.showRelayModal || relayProg > 0.001:
			drawRelay(f, a.node.RelayStatus())
		case a.showTestModal:
			DrawTestModal(f, f.Area(), a.audio, a.node, a.toggleGlobalPTT, a.notifier.enabled.Load(), a.toggleNotificationsToast, a.closeTestModal)
		case a.showLeaveModal || leaveProg > 0.001:
			DrawLeaveModal(f, f.Area(), leaveProg, a.leaveRoom, a.closeLeaveModal)
		case a.showScreenShareModal || screenShareProg > 0.001:
			DrawScreenShareModal(f, f.Area(), ScreenShareDialogState{
				Progress:    screenShareProg,
				SelectedIdx: a.selectedScreenShareIdx,
				Preset:      a.screenPreset,
				SystemAudio: a.shareSystemAudio,
				Deps:        a.screenShareDeps,
				Targets:     a.screenShareTargets,
			},
				a.selectScreenPreset,
				a.toggleShareSystemAudio,
				func(target screenshare.WindowInfo) { a.startSelectedScreenShare(target) },
				a.closeScreenShareModal)
		}
		drawFileOffer(f)
		if r := a.activeKnock(); r != nil && a.activeFileOffer() == nil {
			DrawKnockModal(f, f.Area(), r, knockWindow-time.Since(r.at),
				func() { a.answerKnock(true) }, func() { a.answerKnock(false) })
		}
	})
}
