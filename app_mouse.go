package main

import (
	"strings"

	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
)

func (a *App) handleMouse(m driver.MouseEvent) {
	if a.showRelayModal {
		a.handleRelayModalMouse(m)
	}

	roomInteractive := a.currentScreen == ScreenRoom && !a.showTestModal && !a.showLeaveModal && !a.showExitModal && !a.showScreenShareModal && !a.showDebugModal
	if roomInteractive && m.Button == driver.MouseLeft && !m.Drag {
		a.room.mu.Lock()
		if a.room.IsChatFocused && !a.room.LastLogArea.Contains(m.X, m.Y) {
			a.room.IsChatFocused = false
		}
		a.room.mu.Unlock()
	}

	// Before the click regions: a drag passing over a button would press it.
	if roomInteractive && a.handleChatResize(m) {
		return
	}
	if a.term.RouteMouseEvent(m) {
		return
	}

	switch {
	case a.showDebugModal:
		if a.debugView == nil {
			break
		}
		if m.Button == driver.MouseScrollUp {
			a.debugView.ScrollBy(chatWheelLines)
		} else if m.Button == driver.MouseScrollDown {
			a.debugView.ScrollBy(-chatWheelLines)
		}
	case a.currentScreen == ScreenLobby && !a.showTestModal && !a.showExitModal:
		a.handleLobbyMouse(m)
	case a.currentScreen == ScreenRoom && !a.showTestModal && !a.showLeaveModal && !a.showExitModal && !a.showScreenShareModal:
		a.handleRoomMouse(m)
	}
}

func (a *App) handleRelayModalMouse(m driver.MouseEvent) {
	modalW, modalH := uint16(72), uint16(15)
	screenArea := a.lastScreen
	if screenArea.Width == 0 || screenArea.Height == 0 {
		w, h, _ := a.backend.Size()
		screenArea = cell.NewRect(0, 0, w, h)
	}
	if screenArea.Width < modalW+2 {
		modalW = screenArea.Width - 2
	}
	if screenArea.Height < modalH+2 {
		modalH = screenArea.Height - 2
	}
	modalArea := terminal.CenterRect(screenArea, modalW, modalH)
	inner := cell.NewRect(modalArea.X+1, modalArea.Y+1, modalArea.Width-2, modalArea.Height-2)
	inputs := []cell.Rect{
		cell.NewRect(inner.X+1, inner.Y+3, inner.Width-2, 1), // URL
		cell.NewRect(inner.X+1, inner.Y+6, inner.Width-2, 1), // Token
	}

	for field, rect := range inputs {
		if !rect.Contains(m.X, m.Y) {
			continue
		}
		target := a.relayField(field)
		switch m.Button {
		case driver.MouseLeft:
			visW := int(rect.Width)
			startOffset := 0
			if target.Cursor >= visW {
				startOffset = target.Cursor - visW + 1
			}
			col := max(0, min(startOffset+int(m.X)-int(rect.X), len(target.Text)))
			a.relayModalActiveField = field
			target.Cursor = col
			if !m.Drag {
				a.relaySelField, a.relaySelStart, a.relaySelEnd = field, col, col
			} else {
				a.relaySelEnd = col
			}
		case driver.MouseRight:
			if clip := strings.TrimSpace(GetClipboardText()); clip != "" {
				a.relayInsert(field, clip, "")
			}
		}
		return
	}
}

func (a *App) handleLobbyMouse(m driver.MouseEvent) {
	lobby := a.lobby
	switch m.Button {
	case driver.MouseLeft:
		if m.Drag {
			if lobby.DragActive {
				lobby.RotY += float64(int(m.X)-lobby.LastDragX) * 1.6
				lobby.RotX += float64(int(m.Y)-lobby.LastDragY) * 1.6
			}
			lobby.LastDragX, lobby.LastDragY = int(m.X), int(m.Y)
			lobby.DragActive = true
		} else {
			lobby.DragActive = false
		}
	case driver.MouseNone:
		lobby.DragActive = false
	case driver.MouseRight:
		if !m.Drag {
			a.pasteIntoLobby(readClipboard())
		}
	case driver.MouseScrollUp:
		if lobby.Scale < 12.0 {
			lobby.Scale += 0.3
		}
	case driver.MouseScrollDown:
		if lobby.Scale > 1.5 {
			lobby.Scale -= 0.3
		}
	}
}

func (a *App) handleRoomMouse(m driver.MouseEvent) {
	room := a.room
	room.mu.Lock()
	lastLog := room.LastLogArea
	isDragging := room.SelectionDragging
	room.mu.Unlock()

	inChatLog := lastLog.Contains(m.X, m.Y)
	switch m.Button {
	case driver.MouseLeft:
		if m.Drag {
			if inChatLog || isDragging {
				room.HandleMouseDrag(m.X, m.Y)
			}
		} else if inChatLog {
			// Clicking anywhere in the chat panel opens the input; a click on a
			// [Copy: …] message copies it and one on a link opens it, anything else
			// may start a selection.
			room.SetChatFocused(true)
			if !room.HandleChatClick(m.X, m.Y) {
				room.HandleMousePress(m.X, m.Y)
			}
		} else {
			room.ClearSelection()
		}
	case driver.MouseRelease, driver.MouseNone:
		if isDragging {
			if copied := room.HandleMouseRelease(m.X, m.Y); copied != "" {
				CopyToClipboard(copied)
				room.SetToast(copiedToast)
			}
		}
	case driver.MouseRight:
		// With mouse reporting on, the terminal hands its right-click paste to us.
		if !m.Drag {
			a.pasteIntoChat(readClipboard())
		}
	case driver.MouseScrollUp:
		room.ScrollChat(chatWheelLines)
	case driver.MouseScrollDown:
		room.ScrollChat(-chatWheelLines)
	}
}

// chatWheelLines is how far one wheel step scrolls the chat.
const chatWheelLines = 3

// handleChatResize lets the chat panel's borders be dragged: the top one up or down for its
// height, the left one sideways for its width. It reports whether it took the event.
func (a *App) handleChatResize(m driver.MouseEvent) bool {
	switch {
	case m.Button == driver.MouseLeft && !m.Drag:
		return a.room.StartChatResize(m.X, m.Y)
	case m.Button == driver.MouseLeft:
		return a.room.DragChatResize(m.X, m.Y)
	case m.Button == driver.MouseRelease || m.Button == driver.MouseNone:
		return a.room.EndChatResize()
	}
	return false
}
