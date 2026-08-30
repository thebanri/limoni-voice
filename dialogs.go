package main

import (
	"fmt"
	"math"

	"github.com/thebanri/limoni/core/backend"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
	"github.com/thebanri/limoni-voice/screenshare"
)

// DrawVerticalLevelMeter renders a sleek multi-column equalizer VU bar
// that rises and falls with live voice volume level without using any emojis or icons.
func DrawVerticalLevelMeter(buf *buffer.Buffer, area cell.Rect, rms float64, isSpeaking bool, isMuted bool, label string) {
	if area.Width < 2 || area.Height < 2 {
		return
	}

	// 1. Top Header / Percentage Bar
	pct := int(math.Min(rms*300.0, 100.0))
	if isMuted || !isSpeaking || pct < 1 {
		pct = 0
	}

	topStyle := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}
	if isSpeaking && !isMuted {
		topStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Bg:       cell.NewColorRGB(0x0F, 0x11, 0x1A),
			Modifier: cell.ModifierBold,
		}
	}

	headerText := fmt.Sprintf("%s: [ %2d%% ]", label, pct)
	if isMuted {
		headerText = fmt.Sprintf("%s: [ SESSIZ ]", label)
	}

	buf.SetString(area.X, area.Y, headerText, topStyle)
	headerLen := uint16(len([]rune(headerText)))
	for x := area.X + headerLen; x < area.X+area.Width; x++ {
		buf.SetCell(x, area.Y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A)}})
	}

	// 2. Vertical Multi-Column Equalizer Bars
	barRows := int(area.Height) - 1
	if barRows <= 0 {
		return
	}

	numCols := int(area.Width)
	colWidth := 1

	colLevelRatio := math.Min(rms*3.0, 1.0)
	if isMuted || !isSpeaking {
		colLevelRatio = 0
	}

	for r := 0; r < barRows; r++ {
		rowThreshold := float64(barRows-1-r) / float64(barRows)
		rowY := area.Y + 1 + uint16(r)

		var activeStyle cell.Style
		if rowThreshold < 0.50 {
			activeStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
				Bg:       cell.NewColorRGB(0x0F, 0x11, 0x1A),
				Modifier: cell.ModifierBold,
			}
		} else if rowThreshold < 0.80 {
			activeStyle = cell.Style{
				Fg:       cell.NewColorRGB(0xFF, 0xE6, 0x6D),
				Bg:       cell.NewColorRGB(0x0F, 0x11, 0x1A),
				Modifier: cell.ModifierBold,
			}
		} else {
			activeStyle = cell.Style{
				Fg:       cell.NewColorRGB(0xFF, 0x55, 0x77),
				Bg:       cell.NewColorRGB(0x0F, 0x11, 0x1A),
				Modifier: cell.ModifierBold,
			}
		}

		dimStyle := cell.Style{
			Fg: cell.NewColorRGB(0x2D, 0x37, 0x48),
			Bg: cell.NewColorRGB(0x0F, 0x11, 0x1A),
		}

		for c := 0; c < numCols; c++ {
			colX := area.X + uint16(c*colWidth)
			variance := math.Sin(float64(c)*0.45) * 0.08
			effLevel := math.Max(0.0, math.Min(1.0, colLevelRatio+variance))

			isLit := effLevel >= rowThreshold && isSpeaking && !isMuted && colLevelRatio > 0.005
			var rChar rune = '█'
			var st cell.Style = activeStyle

			if !isLit {
				rChar = '░'
				st = dimStyle
			}

			if colX < area.X+area.Width {
				buf.SetCell(colX, rowY, cell.Cell{
					Content: rChar,
					Style:   st,
				})
			}
		}
	}
}

// DrawTestModal renders the interactive Microphone & Sound Test Dialog without any icons or emojis.
func DrawTestModal(frame *terminal.Frame, screenArea cell.Rect, audio *AudioEngine, node *P2PNode, onClose func()) {
	modalW, modalH := uint16(64), uint16(17)
	modalArea := terminal.CenterRect(screenArea, modalW, modalH)
	widgets.DrawShadow(frame.Buffer, modalArea, 2, 1)

	frame.RegisterModal("sound_test_modal", modalArea, onClose)

	mainBlock := widgets.Block{
		Title:          " MICROPHONE & SOUND TEST PANEL ",
		TitleAlignment: widgets.AlignCenter,
		Borders:        widgets.BorderAll,
		BorderSymbols:  widgets.SymbolsRounded,
		BorderStyle:    cell.Style{Fg: cell.NewColorRGB(0x00, 0xF5, 0xD4)},
		Style:          cell.Style{Bg: cell.NewColorRGB(0x13, 0x17, 0x22)},
	}
	frame.RenderWidget(mainBlock, modalArea)

	inner := mainBlock.Inner(modalArea)
	buf := frame.Buffer

	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x13, 0x17, 0x22)}})
		}
	}

	// 1. Status Indicator
	statusText := "[IDLE (SILENT)]"
	statusStyle := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x13, 0x17, 0x22)}
	if audio.Muted {
		statusText = "[MIC OFF (MUTED)]"
		statusStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0x76, 0x75), Bg: cell.NewColorRGB(0x13, 0x17, 0x22), Modifier: cell.ModifierBold}
	} else if audio.IsSpeaking {
		statusText = "[SPEAKING (AUDIO ACTIVE...)]"
		statusStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0xFF, 0x88), Bg: cell.NewColorRGB(0x13, 0x17, 0x22), Modifier: cell.ModifierBold}
	}

	buf.SetString(inner.X+1, inner.Y, "Status: ", cell.Style{Fg: cell.NewColorRGB(0xDF, 0xE6, 0xE9), Bg: cell.NewColorRGB(0x13, 0x17, 0x22)})
	buf.SetString(inner.X+8, inner.Y, statusText, statusStyle)

	// 2. Vertical VU Level Meter
	meterRect := cell.Rect{
		X:      inner.X + 1,
		Y:      inner.Y + 1,
		Width:  inner.Width - 2,
		Height: 3,
	}
	DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, "MICROPHONE INPUT LEVEL")

	// 3. Loopback / Echo test toggle
	loopbackY := inner.Y + 5
	loopBox := "[ ] Hear My Own Voice (Loopback Test) [Space]"
	loopStyle := cell.Style{Fg: cell.NewColorRGB(0xDF, 0xE6, 0xE9), Bg: cell.NewColorRGB(0x13, 0x17, 0x22)}
	if audio.Loopback {
		loopBox = "[X] Hear My Own Voice (Loopback ACTIVE) [Space]"
		loopStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
			Bg:       cell.NewColorRGB(0x13, 0x17, 0x22),
			Modifier: cell.ModifierBold,
		}
	}
	buf.SetString(inner.X+1, loopbackY, loopBox, loopStyle)
	frame.RegisterClickHandler(cell.NewRect(inner.X+1, loopbackY, uint16(len([]rune(loopBox))), 1), func(_ backend.MouseEvent) {
		audio.ToggleLoopback()
	})

	// 4. Suppression Mode Toggle Buttons [N]
	noiseY := inner.Y + 7
	buf.SetString(inner.X+1, noiseY, "Noise Suppression [N]:", cell.Style{
		Fg: cell.NewColorRGB(0x55, 0xEF, 0xC4),
		Bg: cell.NewColorRGB(0x13, 0x17, 0x22),
	})

	optOff := " [ OFF ] "
	optStd := " [ ON (Standard) ] "
	optHi := " [ HIGH ] "

	curMode := audio.SuppressionMode
	styleOff := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x22, 0x27, 0x36)}
	styleStd := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x22, 0x27, 0x36)}
	styleHi := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x22, 0x27, 0x36)}

	activeStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
		Modifier: cell.ModifierBold,
	}

	if curMode == 0 {
		styleOff = activeStyle
	} else if curMode == 1 {
		styleStd = activeStyle
	} else {
		styleHi = activeStyle
	}

	optOffX := inner.X + 24
	buf.SetString(optOffX, noiseY, optOff, styleOff)
	frame.RegisterClickHandler(cell.NewRect(optOffX, noiseY, uint16(len([]rune(optOff))), 1), func(_ backend.MouseEvent) {
		audio.SetSuppressionMode(0)
	})

	optStdX := optOffX + uint16(len([]rune(optOff))) + 1
	buf.SetString(optStdX, noiseY, optStd, styleStd)
	frame.RegisterClickHandler(cell.NewRect(optStdX, noiseY, uint16(len([]rune(optStd))), 1), func(_ backend.MouseEvent) {
		audio.SetSuppressionMode(1)
	})

	optHiX := optStdX + uint16(len([]rune(optStd))) + 1
	if optHiX+uint16(len([]rune(optHi))) <= inner.X+inner.Width {
		buf.SetString(optHiX, noiseY, optHi, styleHi)
		frame.RegisterClickHandler(cell.NewRect(optHiX, noiseY, uint16(len([]rune(optHi))), 1), func(_ backend.MouseEvent) {
			audio.SetSuppressionMode(2)
		})
	}

	// 5. Volume Slider (Limoni widgets.Slider)
	gainY := inner.Y + 9
	gainPct := int(math.Round(audio.Gain * 100))
	gainLabel := fmt.Sprintf("Mic Volume:    [ %3d%% ]", gainPct)
	buf.SetString(inner.X+1, gainY, gainLabel, cell.Style{
		Fg:       cell.NewColorRGB(0xFF, 0xE6, 0x6D),
		Bg:       cell.NewColorRGB(0x13, 0x17, 0x22),
		Modifier: cell.ModifierBold,
	})

	if audio.GainSliderState == nil {
		audio.GainSliderState = widgets.NewSliderState(gainPct)
	} else {
		audio.GainSliderState.Set(gainPct, 0, 200)
	}

	sliderWidth := uint16(26)
	if inner.Width > 32 {
		sliderWidth = inner.Width - 30
	}
	gainSliderArea := cell.Rect{
		X:      inner.X + 28,
		Y:      gainY,
		Width:  sliderWidth,
		Height: 1,
	}
	gainSlider := widgets.Slider{
		ID:    "mic_gain_slider",
		State: audio.GainSliderState,
		Min:   0,
		Max:   200,
		TrackStyle: cell.Style{
			Fg: cell.NewColorRGB(0x3B, 0x42, 0x52),
			Bg: cell.NewColorRGB(0x13, 0x17, 0x22),
		},
		FilledStyle: cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
			Bg:       cell.NewColorRGB(0x13, 0x17, 0x22),
			Modifier: cell.ModifierBold,
		},
		ThumbStyle: cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
			Bg:       cell.NewColorRGB(0x13, 0x17, 0x22),
			Modifier: cell.ModifierBold,
		},
		FocusedStyle: cell.Style{
			Fg: cell.NewColorRGB(0x55, 0xEF, 0xC4),
			Bg: cell.NewColorRGB(0x13, 0x17, 0x22),
		},
		OnChange: func(value int) {
			audio.mu.Lock()
			audio.Gain = float64(value) / 100.0
			audio.mu.Unlock()
		},
	}
	frame.RenderWidget(gainSlider, gainSliderArea)

	// 6. Sensitivity / VAD Threshold Slider (Limoni widgets.Slider)
	vadY := inner.Y + 11
	vadVal := int(math.Round(audio.VADThreshold * 1000))
	vadLabel := fmt.Sprintf("Sensitivity:   [ %3d ]", vadVal)
	buf.SetString(inner.X+1, vadY, vadLabel, cell.Style{
		Fg:       cell.NewColorRGB(0x74, 0xB9, 0xFF),
		Bg:       cell.NewColorRGB(0x13, 0x17, 0x22),
		Modifier: cell.ModifierBold,
	})

	if audio.VADSliderState == nil {
		audio.VADSliderState = widgets.NewSliderState(vadVal)
	} else {
		audio.VADSliderState.Set(vadVal, 1, 50)
	}

	vadSliderArea := cell.Rect{
		X:      inner.X + 28,
		Y:      vadY,
		Width:  sliderWidth,
		Height: 1,
	}
	vadSlider := widgets.Slider{
		ID:    "mic_vad_slider",
		State: audio.VADSliderState,
		Min:   1,
		Max:   50,
		TrackStyle: cell.Style{
			Fg: cell.NewColorRGB(0x3B, 0x42, 0x52),
			Bg: cell.NewColorRGB(0x13, 0x17, 0x22),
		},
		FilledStyle: cell.Style{
			Fg:       cell.NewColorRGB(0x74, 0xB9, 0xFF),
			Bg:       cell.NewColorRGB(0x13, 0x17, 0x22),
			Modifier: cell.ModifierBold,
		},
		ThumbStyle: cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
			Bg:       cell.NewColorRGB(0x13, 0x17, 0x22),
			Modifier: cell.ModifierBold,
		},
		FocusedStyle: cell.Style{
			Fg: cell.NewColorRGB(0x00, 0xD2, 0xD3),
			Bg: cell.NewColorRGB(0x13, 0x17, 0x22),
		},
		OnChange: func(value int) {
			audio.mu.Lock()
			audio.VADThreshold = float64(value) / 1000.0
			audio.mu.Unlock()
		},
	}
	frame.RenderWidget(vadSlider, vadSliderArea)

	// 7. Action Buttons (Mute, Close)
	btnY := inner.Y + 13
	muteBtn := "[M] Mute Mic"
	muteBtnStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       cell.NewColorRGB(0x55, 0xEF, 0xC4),
		Modifier: cell.ModifierBold,
	}
	if audio.Muted {
		muteBtn = "[M] Unmute Mic"
		muteBtnStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0xFF, 0x76, 0x75),
			Modifier: cell.ModifierBold,
		}
	}
	buf.SetString(inner.X+1, btnY, "  "+muteBtn+"  ", muteBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(inner.X+1, btnY, uint16(len([]rune(muteBtn)))+4, 1), func(_ backend.MouseEvent) {
		isMuted := audio.ToggleMute()
		if node != nil {
			node.SendMuteState(isMuted)
		}
	})

	closeBtn := "[ Close (Esc) ]"
	closeBtnStyle := cell.Style{
		Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
		Bg:       cell.NewColorRGB(0x6C, 0x5C, 0xE7),
		Modifier: cell.ModifierBold,
	}
	closeX := inner.X + inner.Width - uint16(len([]rune(closeBtn))) - 2
	buf.SetString(closeX, btnY, " "+closeBtn+" ", closeBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(closeX, btnY, uint16(len([]rune(closeBtn)))+2, 1), func(_ backend.MouseEvent) {
		if onClose != nil {
			onClose()
		}
	})
}

// DrawLeaveModal renders the official Limoni widgets.Dialog confirmation dialog for leaving the room with opening/closing scale animation.
func DrawLeaveModal(frame *terminal.Frame, screenArea cell.Rect, progress float64, onConfirm func(), onCancel func()) {
	if progress <= 0.001 {
		return
	}

	modalW, modalH := uint16(48), uint16(9)
	modalArea := terminal.CenterRect(screenArea, modalW, modalH)
	animatedArea := terminal.ScaleRect(modalArea, progress)

	if animatedArea.Width < 4 || animatedArea.Height < 3 {
		return
	}

	frame.RegisterModal("leave_room_dialog", animatedArea, onCancel)

	leaveDialog := widgets.Dialog{
		ID:          "leave_room_dialog",
		Title:       " LEAVE ROOM ",
		Message:     "Do you want to leave the current voice room?",
		SubMessage:  "Your voice connection with other participants will be terminated.",
		Style:       cell.Style{Fg: cell.NewColorRGB(220, 220, 220), Bg: cell.NewColorRGB(20, 22, 28)},
		HeaderStyle: cell.Style{Fg: cell.NewColorRGB(255, 255, 255), Bg: cell.NewColorRGB(235, 94, 40)},
		BorderStyle: cell.Style{Fg: cell.NewColorRGB(235, 94, 40)},
		ButtonStyle: cell.Style{Fg: cell.NewColorRGB(220, 220, 220), Bg: cell.NewColorRGB(45, 45, 45)},
		ButtonFocusedStyle: cell.Style{
			Fg:       cell.NewColorRGB(255, 255, 255),
			Bg:       cell.NewColorRGB(235, 94, 40),
			Modifier: cell.ModifierBold,
		},
		Shadow: true,
		Buttons: []widgets.DialogButton{
			{
				Text:    "Yes, Leave",
				Handler: onConfirm,
			},
			{
				Text:    "No, Stay",
				Handler: onCancel,
			},
		},
	}

	frame.BeginFocusScope("leave_room_dialog")
	frame.RenderWidget(leaveDialog, animatedArea)
}

// DrawExitModal renders the official Limoni widgets.Dialog confirmation dialog for exiting the application with opening/closing scale animation.
func DrawExitModal(frame *terminal.Frame, screenArea cell.Rect, progress float64, onConfirm func(), onCancel func()) {
	if progress <= 0.001 {
		return
	}

	modalW, modalH := uint16(48), uint16(9)
	modalArea := terminal.CenterRect(screenArea, modalW, modalH)
	animatedArea := terminal.ScaleRect(modalArea, progress)

	if animatedArea.Width < 4 || animatedArea.Height < 3 {
		return
	}

	frame.RegisterModal("exit_app_dialog", animatedArea, onCancel)

	exitDialog := widgets.Dialog{
		ID:          "exit_app_dialog",
		Title:       " EXIT APPLICATION ",
		Message:     "Do you want to exit Limoni Voice?",
		SubMessage:  "Your current session and voice connection will be terminated.",
		Style:       cell.Style{Fg: cell.NewColorRGB(220, 220, 220), Bg: cell.NewColorRGB(20, 22, 28)},
		HeaderStyle: cell.Style{Fg: cell.NewColorRGB(255, 255, 255), Bg: cell.NewColorRGB(220, 60, 60)},
		BorderStyle: cell.Style{Fg: cell.NewColorRGB(220, 60, 60)},
		ButtonStyle: cell.Style{Fg: cell.NewColorRGB(220, 220, 220), Bg: cell.NewColorRGB(45, 45, 45)},
		ButtonFocusedStyle: cell.Style{
			Fg:       cell.NewColorRGB(255, 255, 255),
			Bg:       cell.NewColorRGB(220, 60, 60),
			Modifier: cell.ModifierBold,
		},
		Shadow: true,
		Buttons: []widgets.DialogButton{
			{
				Text:    "Yes, Exit",
				Handler: onConfirm,
			},
			{
				Text:    "No, Continue",
				Handler: onCancel,
			},
		},
	}

	frame.BeginFocusScope("exit_app_dialog")
	frame.RenderWidget(exitDialog, animatedArea)
}

// DrawScreenShareModal renders the screen and window selection modal with opening/closing scale animation
func DrawScreenShareModal(frame *terminal.Frame, screenArea cell.Rect, progress float64, selectedIdx int, targets []screenshare.WindowInfo, onSelect func(target screenshare.WindowInfo), onCancel func()) {
	if progress <= 0.001 {
		return
	}

	modalW, modalH := uint16(56), uint16(14)
	modalArea := terminal.CenterRect(screenArea, modalW, modalH)
	animatedArea := terminal.ScaleRect(modalArea, progress)

	if animatedArea.Width < 6 || animatedArea.Height < 4 {
		return
	}

	frame.RegisterModal("screenshare_select_dialog", animatedArea, onCancel)

	// Draw dialog backdrop and block
	block := widgets.Block{
		Title:       " 📺 SELECT SCREEN OR WINDOW ",
		Style:       cell.Style{Fg: cell.NewColorRGB(220, 220, 220), Bg: cell.NewColorRGB(20, 22, 28)},
		BorderStyle: cell.Style{Fg: cell.NewColorRGB(0x00, 0xD2, 0xD3), Modifier: cell.ModifierBold},
	}
	frame.RenderWidget(block, animatedArea)

	inner := cell.Rect{
		X:      animatedArea.X + 1,
		Y:      animatedArea.Y + 1,
		Width:  animatedArea.Width - 2,
		Height: animatedArea.Height - 2,
	}
	if inner.Height < 2 || inner.Width < 2 {
		return
	}

	buf := frame.Buffer
	headerText := "Select the screen or window you want to share:"
	buf.SetString(inner.X+1, inner.Y, headerText, cell.Style{Fg: cell.NewColorRGB(150, 160, 180)})

	// List targets
	listY := inner.Y + 2
	maxDisplay := int(inner.Height - 3)
	if maxDisplay < 1 {
		maxDisplay = 1
	}

	for i := 0; i < len(targets) && i < maxDisplay; i++ {
		t := targets[i]
		rowY := listY + uint16(i)
		isSel := (i == selectedIdx)

		itemStyle := cell.Style{Fg: cell.NewColorRGB(200, 210, 225), Bg: cell.NewColorRGB(26, 30, 40)}
		prefix := "  "
		if isSel {
			itemStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       cell.NewColorRGB(0x00, 0xD2, 0xD3),
				Modifier: cell.ModifierBold,
			}
			prefix = "▶ "
		}

		itemText := fmt.Sprintf("%s%s", prefix, t.Title)
		if len([]rune(itemText)) > int(inner.Width-2) {
			itemText = string([]rune(itemText)[:inner.Width-5]) + "..."
		}

		// Clear row
		for x := inner.X + 1; x < inner.X+inner.Width-1; x++ {
			buf.SetCell(x, rowY, cell.Cell{Content: ' ', Style: itemStyle})
		}
		buf.SetString(inner.X+1, rowY, itemText, itemStyle)

		targetItem := t
		frame.RegisterClickHandler(cell.NewRect(inner.X+1, rowY, inner.Width-2, 1), func(_ backend.MouseEvent) {
			onSelect(targetItem)
		})
	}

	// Bottom Cancel Guide
	guideText := "[ENTER / CLICK] Select  [ESC] Cancel"
	buf.SetString(inner.X+1, inner.Y+inner.Height-1, guideText, cell.Style{Fg: cell.NewColorRGB(120, 130, 150)})
}
