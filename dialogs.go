package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

	theme := CurrentTheme()

	// 1. Live Input Level & Status Indicator
	pct := int(math.Min(rms*350.0, 100.0))
	if isMuted {
		pct = 0
	}

	topStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
	var badgeText string
	var badgeStyle cell.Style

	if isMuted {
		badgeText = "[ MUTED ]"
		badgeStyle = cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
	} else if isSpeaking {
		topStyle = cell.Style{
			Fg:       theme.Success,
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		}
		badgeText = "[ ● VOICE ACTIVE (GATE OPEN) ]"
		badgeStyle = cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
	} else {
		badgeText = "[ ○ NOISE GATED (GATE CLOSED) ]"
		badgeStyle = cell.Style{Fg: theme.Secondary, Bg: theme.SurfaceBg}
	}

	headerText := fmt.Sprintf("%s: [ %2d%% ]", label, pct)
	buf.SetString(area.X, area.Y, headerText, topStyle)
	headerLen := uint16(len([]rune(headerText)))

	badgeX := area.X + headerLen + 2
	badgeLen := uint16(len([]rune(badgeText)))
	if badgeX+badgeLen <= area.X+area.Width {
		buf.SetString(badgeX, area.Y, badgeText, badgeStyle)
		for x := badgeX + badgeLen; x < area.X+area.Width; x++ {
			buf.SetCell(x, area.Y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	} else {
		for x := area.X + headerLen; x < area.X+area.Width; x++ {
			buf.SetCell(x, area.Y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	// 2. Vertical Multi-Column Equalizer Bars
	barRows := int(area.Height) - 1
	if barRows <= 0 {
		return
	}

	numCols := int(area.Width)
	colWidth := 1

	colLevelRatio := math.Min(rms*3.5, 1.0)
	if isMuted {
		colLevelRatio = 0
	}

	for r := 0; r < barRows; r++ {
		rowThreshold := float64(barRows-1-r) / float64(barRows)
		rowY := area.Y + 1 + uint16(r)

		var activeStyle cell.Style
		if isSpeaking {
			if rowThreshold < 0.50 {
				activeStyle = cell.Style{
					Fg:       theme.WaveColor,
					Bg:       theme.SurfaceBg,
					Modifier: cell.ModifierBold,
				}
			} else if rowThreshold < 0.80 {
				activeStyle = cell.Style{
					Fg:       theme.Warning,
					Bg:       theme.SurfaceBg,
					Modifier: cell.ModifierBold,
				}
			} else {
				activeStyle = cell.Style{
					Fg:       theme.Danger,
					Bg:       theme.SurfaceBg,
					Modifier: cell.ModifierBold,
				}
			}
		} else {
			// Dim bars showing ambient room sound below threshold
			activeStyle = cell.Style{
				Fg: theme.Border,
				Bg: theme.SurfaceBg,
			}
		}

		dimStyle := cell.Style{
			Fg: theme.Border,
			Bg: theme.SurfaceBg,
		}

		for c := 0; c < numCols; c++ {
			colX := area.X + uint16(c*colWidth)
			variance := math.Sin(float64(c)*0.45) * 0.06
			effLevel := math.Max(0.0, math.Min(1.0, colLevelRatio+variance))

			isLit := effLevel >= rowThreshold && !isMuted && colLevelRatio > 0.002
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

// DrawTestModal renders the interactive Microphone & Audio Device Settings panel without any icons or emojis.
func DrawTestModal(frame *terminal.Frame, screenArea cell.Rect, audio *AudioEngine, node *P2PNode, onClose func()) {
	modalW, modalH := uint16(68), uint16(26)
	if screenArea.Width < modalW+2 {
		modalW = screenArea.Width - 2
	}
	if screenArea.Height < modalH+2 {
		modalH = screenArea.Height - 2
	}

	modalArea := terminal.CenterRect(screenArea, modalW, modalH)
	widgets.DrawShadow(frame.Buffer, modalArea, 2, 1)

	frame.RegisterModal("sound_test_modal", modalArea, onClose)

	theme := CurrentTheme()
	mainBlock := widgets.Block{
		Title:          " MICROPHONE & AUDIO SETTINGS ",
		TitleAlignment: widgets.AlignCenter,
		Borders:        widgets.BorderAll,
		BorderSymbols:  widgets.SymbolsRounded,
		BorderStyle:    cell.Style{Fg: theme.BorderFocused},
		Style:          cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(mainBlock, modalArea)

	inner := mainBlock.Inner(modalArea)
	buf := frame.Buffer

	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	// 1. Status Indicator
	statusText := "[IDLE (SILENT)]"
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
	if audio.Muted {
		statusText = "[MIC OFF (MUTED)]"
		statusStyle = cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
	} else if audio.InputMode == InputModePushToTalk {
		if audio.IsTransmitting() {
			statusText = "[PTT ACTIVE (TRANSMITTING...)]"
			statusStyle = cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
		} else {
			statusText = "[PTT IDLE (PRESS SPACE/P TO TALK)]"
			statusStyle = cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg}
		}
	} else if audio.IsSpeaking {
		statusText = "[SPEAKING (AUDIO ACTIVE...)]"
		statusStyle = cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
	}

	buf.SetString(inner.X+1, inner.Y, "Status: ", cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg})
	buf.SetString(inner.X+8, inner.Y, statusText, statusStyle)

	// 2. Vertical VU Level Meter
	meterRect := cell.Rect{
		X:      inner.X + 1,
		Y:      inner.Y + 1,
		Width:  inner.Width - 2,
		Height: 3,
	}
	DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, "MIC INPUT LEVEL")

	// 3. Microphone Input Device Selection Row
	micDevY := inner.Y + 4
	buf.SetString(inner.X+1, micDevY, "Microphone [1]:", cell.Style{
		Fg:       theme.Accent,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierBold,
	})

	prevBtn := "[◀]"
	nextBtn := "[▶]"
	micBtnStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Accent,
		Modifier: cell.ModifierBold,
	}

	curMicName := audio.GetSelectedInputName()
	micTotal := len(audio.InputDevices)
	if micTotal == 0 {
		micTotal = 1
	}
	micCurIdx := audio.SelectedInputIdx + 1
	micDisplay := fmt.Sprintf("(%d/%d) %s", micCurIdx, micTotal, curMicName)

	boxX := inner.X + 17
	buf.SetString(boxX, micDevY, prevBtn, micBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(boxX, micDevY, uint16(len([]rune(prevBtn))), 1), func(_ backend.MouseEvent) {
		audio.CycleInputDevice(-1)
	})

	boxWidth := uint16(24)
	if inner.Width > 32 {
		boxWidth = inner.Width - 28
	}
	micNameX := boxX + uint16(len([]rune(prevBtn))) + 1
	micRunes := []rune(micDisplay)
	if len(micRunes) > int(boxWidth) {
		if boxWidth > 3 {
			micDisplay = string(micRunes[:boxWidth-3]) + "..."
		} else {
			micDisplay = string(micRunes[:boxWidth])
		}
	}
	nameStyle := cell.Style{
		Fg: theme.Text,
		Bg: theme.InputBg,
	}
	for x := micNameX; x < micNameX+boxWidth; x++ {
		buf.SetCell(x, micDevY, cell.Cell{Content: ' ', Style: nameStyle})
	}
	buf.SetString(micNameX, micDevY, micDisplay, nameStyle)
	frame.RegisterClickHandler(cell.NewRect(micNameX, micDevY, boxWidth, 1), func(_ backend.MouseEvent) {
		audio.CycleInputDevice(1)
	})

	nextMicX := micNameX + boxWidth + 1
	buf.SetString(nextMicX, micDevY, nextBtn, micBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(nextMicX, micDevY, uint16(len([]rune(nextBtn))), 1), func(_ backend.MouseEvent) {
		audio.CycleInputDevice(1)
	})

	// 4. Output (Speaker/Headphone) Device Selection Row
	outDevY := inner.Y + 6
	buf.SetString(inner.X+1, outDevY, "Output Dev [2]:", cell.Style{
		Fg:       theme.Secondary,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierBold,
	})

	outBtnStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Secondary,
		Modifier: cell.ModifierBold,
	}

	curOutName := audio.GetSelectedOutputName()
	outTotal := len(audio.OutputDevices)
	if outTotal == 0 {
		outTotal = 1
	}
	outCurIdx := audio.SelectedOutputIdx + 1
	outDisplay := fmt.Sprintf("(%d/%d) %s", outCurIdx, outTotal, curOutName)

	buf.SetString(boxX, outDevY, prevBtn, outBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(boxX, outDevY, uint16(len([]rune(prevBtn))), 1), func(_ backend.MouseEvent) {
		audio.CycleOutputDevice(-1)
	})

	outNameX := boxX + uint16(len([]rune(prevBtn))) + 1
	outRunes := []rune(outDisplay)
	if len(outRunes) > int(boxWidth) {
		if boxWidth > 3 {
			outDisplay = string(outRunes[:boxWidth-3]) + "..."
		} else {
			outDisplay = string(outRunes[:boxWidth])
		}
	}
	for x := outNameX; x < outNameX+boxWidth; x++ {
		buf.SetCell(x, outDevY, cell.Cell{Content: ' ', Style: nameStyle})
	}
	buf.SetString(outNameX, outDevY, outDisplay, nameStyle)
	frame.RegisterClickHandler(cell.NewRect(outNameX, outDevY, boxWidth, 1), func(_ backend.MouseEvent) {
		audio.CycleOutputDevice(1)
	})

	nextOutX := outNameX + boxWidth + 1
	buf.SetString(nextOutX, outDevY, nextBtn, outBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(nextOutX, outDevY, uint16(len([]rune(nextBtn))), 1), func(_ backend.MouseEvent) {
		audio.CycleOutputDevice(1)
	})

	// 5. Mic Volume Slider
	gainY := inner.Y + 8
	gainPct := int(math.Round(audio.Gain * 100))
	gainLabel := fmt.Sprintf("Mic Volume:    [ %3d%% ]", gainPct)
	buf.SetString(inner.X+1, gainY, gainLabel, cell.Style{
		Fg:       theme.Warning,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierBold,
	})

	if audio.GainSliderState == nil {
		audio.GainSliderState = widgets.NewSliderState(gainPct)
	} else {
		audio.GainSliderState.Set(gainPct, 0, 300)
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
		Max:   300,
		TrackStyle: cell.Style{
			Fg: theme.Border,
			Bg: theme.SurfaceBg,
		},
		FilledStyle: cell.Style{
			Fg:       theme.Accent,
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		},
		ThumbStyle: cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		},
		FocusedStyle: cell.Style{
			Fg: theme.Success,
			Bg: theme.SurfaceBg,
		},
		OnChange: func(value int) {
			audio.mu.Lock()
			audio.Gain = float64(value) / 100.0
			audio.mu.Unlock()
		},
	}
	frame.RenderWidget(gainSlider, gainSliderArea)

	// 6. Speaker Output Volume Slider
	outVolY := inner.Y + 10
	outPct := int(math.Round(audio.OutputVolume * 100))
	outVolLabel := fmt.Sprintf("Speaker Vol:   [ %3d%% ]", outPct)
	buf.SetString(inner.X+1, outVolY, outVolLabel, cell.Style{
		Fg:       theme.Secondary,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierBold,
	})

	if audio.OutputSliderState == nil {
		audio.OutputSliderState = widgets.NewSliderState(outPct)
	} else {
		audio.OutputSliderState.Set(outPct, 0, 200)
	}

	outSliderArea := cell.Rect{
		X:      inner.X + 28,
		Y:      outVolY,
		Width:  sliderWidth,
		Height: 1,
	}
	outSlider := widgets.Slider{
		ID:    "speaker_vol_slider",
		State: audio.OutputSliderState,
		Min:   0,
		Max:   200,
		TrackStyle: cell.Style{
			Fg: theme.Border,
			Bg: theme.SurfaceBg,
		},
		FilledStyle: cell.Style{
			Fg:       theme.Secondary,
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		},
		ThumbStyle: cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		},
		FocusedStyle: cell.Style{
			Fg: theme.Accent,
			Bg: theme.SurfaceBg,
		},
		OnChange: func(value int) {
			audio.mu.Lock()
			audio.OutputVolume = float64(value) / 100.0
			audio.mu.Unlock()
		},
	}
	frame.RenderWidget(outSlider, outSliderArea)

	// 7. Suppression Mode Toggle Buttons [N]
	noiseY := inner.Y + 12
	buf.SetString(inner.X+1, noiseY, "Noise Filter [N]:", cell.Style{
		Fg: theme.Success,
		Bg: theme.SurfaceBg,
	})

	optOff := " [ OFF ] "
	optStd := " [ ON (Standard) ] "
	optHi := " [ HIGH ] "

	curMode := audio.SuppressionMode
	styleOff := cell.Style{Fg: theme.TextMuted, Bg: theme.InputBg}
	styleStd := cell.Style{Fg: theme.TextMuted, Bg: theme.InputBg}
	styleHi := cell.Style{Fg: theme.TextMuted, Bg: theme.InputBg}

	activeStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Success,
		Modifier: cell.ModifierBold,
	}

	if curMode == 0 {
		styleOff = activeStyle
	} else if curMode == 1 {
		styleStd = activeStyle
	} else {
		styleHi = activeStyle
	}

	optOffX := inner.X + 20
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

	// 8. Input Mode Selection Row [P]
	inputModeY := inner.Y + 14
	buf.SetString(inner.X+1, inputModeY, "Input Mode [P]:", cell.Style{
		Fg:       theme.Warning,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierBold,
	})

	modeVa := " [ Voice ] "
	modePtt := " [ PTT ] "

	styleVa := cell.Style{Fg: theme.TextMuted, Bg: theme.InputBg}
	stylePtt := cell.Style{Fg: theme.TextMuted, Bg: theme.InputBg}
	activeModeStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Warning,
		Modifier: cell.ModifierBold,
	}

	if audio.InputMode == InputModePushToTalk {
		stylePtt = activeModeStyle
	} else {
		styleVa = activeModeStyle
	}

	modeVaX := inner.X + 18
	buf.SetString(modeVaX, inputModeY, modeVa, styleVa)
	frame.RegisterClickHandler(cell.NewRect(modeVaX, inputModeY, uint16(len([]rune(modeVa))), 1), func(_ backend.MouseEvent) {
		audio.SetInputMode(InputModeVoiceActivity)
	})

	modePttX := modeVaX + uint16(len([]rune(modeVa))) + 1
	buf.SetString(modePttX, inputModeY, modePtt, stylePtt)
	frame.RegisterClickHandler(cell.NewRect(modePttX, inputModeY, uint16(len([]rune(modePtt))), 1), func(_ backend.MouseEvent) {
		audio.SetInputMode(InputModePushToTalk)
	})

	if audio.InputMode == InputModePushToTalk {
		keyLabel := fmt.Sprintf(" [ Key [K]: %s ] ", audio.GetPTTKeyName())
		keyStyle := cell.Style{
			Fg:       theme.Accent,
			Bg:       theme.InputBg,
			Modifier: cell.ModifierBold,
		}
		if audio.PTTListeningKey {
			keyLabel = " [ Press Key... ] "
			keyStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Warning,
				Modifier: cell.ModifierBold,
			}
		}
		keyX := modePttX + uint16(len([]rune(modePtt))) + 1
		if keyX+uint16(len([]rune(keyLabel))) <= inner.X+inner.Width {
			buf.SetString(keyX, inputModeY, keyLabel, keyStyle)
			frame.RegisterClickHandler(cell.NewRect(keyX, inputModeY, uint16(len([]rune(keyLabel))), 1), func(_ backend.MouseEvent) {
				audio.mu.Lock()
				audio.PTTListeningKey = !audio.PTTListeningKey
				audio.mu.Unlock()
			})
		}
	}

	// 9. Sensitivity / VAD Threshold Slider
	vadY := inner.Y + 16
	vadSens := audio.GetVADSensitivity()
	vadLabel := fmt.Sprintf("Sensitivity:   [ %3d%% ]", vadSens)
	buf.SetString(inner.X+1, vadY, vadLabel, cell.Style{
		Fg:       theme.Secondary,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierBold,
	})

	if audio.VADSliderState == nil {
		audio.VADSliderState = widgets.NewSliderState(vadSens)
	} else {
		audio.VADSliderState.Set(vadSens, 1, 100)
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
		Max:   100,
		TrackStyle: cell.Style{
			Fg: theme.Border,
			Bg: theme.SurfaceBg,
		},
		FilledStyle: cell.Style{
			Fg:       theme.Secondary,
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		},
		ThumbStyle: cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		},
		FocusedStyle: cell.Style{
			Fg: theme.Accent,
			Bg: theme.SurfaceBg,
		},
		OnChange: func(value int) {
			audio.SetVADSensitivity(value)
		},
	}
	frame.RenderWidget(vadSlider, vadSliderArea)

	// 10. Loopback / Echo test toggle
	loopbackY := inner.Y + 18
	loopBox := "[ ] Hear My Own Voice (Loopback Test) [L]"
	loopStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
	if audio.Loopback {
		loopBox = "[X] Hear My Own Voice (Loopback ACTIVE) [L]"
		loopStyle = cell.Style{
			Fg:       theme.Accent,
			Bg:       theme.SurfaceBg,
			Modifier: cell.ModifierBold,
		}
	}
	buf.SetString(inner.X+1, loopbackY, loopBox, loopStyle)
	frame.RegisterClickHandler(cell.NewRect(inner.X+1, loopbackY, uint16(len([]rune(loopBox))), 1), func(_ backend.MouseEvent) {
		audio.ToggleLoopback()
	})

	// 11. Theme & Mini HUD Options
	themeY := inner.Y + 20
	buf.SetString(inner.X+1, themeY, "Theme [T]:", cell.Style{
		Fg:       theme.Accent,
		Bg:       theme.SurfaceBg,
		Modifier: cell.ModifierBold,
	})

	themeBtnStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Accent,
		Modifier: cell.ModifierBold,
	}

	curTheme := CurrentTheme()
	themeDisplay := curTheme.Name
	themeBoxW := uint16(18)

	themePrevX := inner.X + 13
	buf.SetString(themePrevX, themeY, prevBtn, themeBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(themePrevX, themeY, uint16(len([]rune(prevBtn))), 1), func(_ backend.MouseEvent) {
		CycleTheme()
	})

	themeNameX := themePrevX + uint16(len([]rune(prevBtn))) + 1
	for x := themeNameX; x < themeNameX+themeBoxW; x++ {
		buf.SetCell(x, themeY, cell.Cell{Content: ' ', Style: nameStyle})
	}
	buf.SetString(themeNameX, themeY, themeDisplay, nameStyle)
	frame.RegisterClickHandler(cell.NewRect(themeNameX, themeY, themeBoxW, 1), func(_ backend.MouseEvent) {
		CycleTheme()
	})

	themeNextX := themeNameX + themeBoxW + 1
	buf.SetString(themeNextX, themeY, nextBtn, themeBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(themeNextX, themeY, uint16(len([]rune(nextBtn))), 1), func(_ backend.MouseEvent) {
		CycleTheme()
	})

	// Compact HUD mode toggle
	hudOptX := themeNextX + uint16(len([]rune(nextBtn))) + 2
	if hudOptX+16 <= inner.X+inner.Width {
		hudLabel := "[ ] Mini HUD [H]"
		hudStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
		if GetCompactHUD() {
			hudLabel = "[X] Mini HUD ON"
			hudStyle = cell.Style{
				Fg:       theme.Accent,
				Bg:       theme.SurfaceBg,
				Modifier: cell.ModifierBold,
			}
		}
		buf.SetString(hudOptX, themeY, hudLabel, hudStyle)
		frame.RegisterClickHandler(cell.NewRect(hudOptX, themeY, uint16(len([]rune(hudLabel))), 1), func(_ backend.MouseEvent) {
			ToggleCompactHUD()
		})
	}

	// 12. Action Buttons (Mute, Deafen, Close)
	btnY := inner.Y + 22
	muteBtn := "[M] Mute Mic"
	muteBtnStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Success,
		Modifier: cell.ModifierBold,
	}
	if audio.Muted {
		muteBtn = "[M] Unmute Mic"
		muteBtnStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Danger,
			Modifier: cell.ModifierBold,
		}
	}
	buf.SetString(inner.X+1, btnY, " "+muteBtn+" ", muteBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(inner.X+1, btnY, uint16(len([]rune(muteBtn)))+2, 1), func(_ backend.MouseEvent) {
		isMuted := audio.ToggleMute()
		if node != nil {
			node.SendMuteState(isMuted)
		}
	})

	deafBtn := "[D] Deafen"
	deafBtnStyle := cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Secondary,
		Modifier: cell.ModifierBold,
	}
	if audio.Deafened {
		deafBtn = "[D] Undeafen"
		deafBtnStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}
	}
	deafX := inner.X + uint16(len([]rune(muteBtn))) + 4
	buf.SetString(deafX, btnY, " "+deafBtn+" ", deafBtnStyle)
	frame.RegisterClickHandler(cell.NewRect(deafX, btnY, uint16(len([]rune(deafBtn)))+2, 1), func(_ backend.MouseEvent) {
		isDeaf := audio.ToggleDeafen()
		if node != nil {
			node.SendDeafenState(isDeaf)
			node.SendMuteState(audio.Muted)
		}
	})

	closeBtn := "[ Close (Esc) ]"
	closeBtnStyle := cell.Style{
		Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
		Bg:       theme.BorderFocused,
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

	theme := CurrentTheme()
	leaveDialog := widgets.Dialog{
		ID:          "leave_room_dialog",
		Title:       " LEAVE ROOM ",
		Message:     "Do you want to leave the current voice room?",
		SubMessage:  "Your voice connection with other participants will be terminated.",
		Style:       cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg},
		HeaderStyle: cell.Style{Fg: cell.NewColorRGB(255, 255, 255), Bg: theme.Danger},
		BorderStyle: cell.Style{Fg: theme.Danger},
		ButtonStyle: cell.Style{Fg: theme.Text, Bg: theme.InputBg},
		ButtonFocusedStyle: cell.Style{
			Fg:       cell.NewColorRGB(255, 255, 255),
			Bg:       theme.Danger,
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

	theme := CurrentTheme()
	exitDialog := widgets.Dialog{
		ID:          "exit_app_dialog",
		Title:       " EXIT APPLICATION ",
		Message:     "Do you want to exit Limoni Voice?",
		SubMessage:  "Your current session and voice connection will be terminated.",
		Style:       cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg},
		HeaderStyle: cell.Style{Fg: cell.NewColorRGB(255, 255, 255), Bg: theme.Danger},
		BorderStyle: cell.Style{Fg: theme.Danger},
		ButtonStyle: cell.Style{Fg: theme.Text, Bg: theme.InputBg},
		ButtonFocusedStyle: cell.Style{
			Fg:       cell.NewColorRGB(255, 255, 255),
			Bg:       theme.Danger,
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

// drawBoundedString renders text up to maxX, preventing any overflow past boundaries.
func drawBoundedString(buf *buffer.Buffer, x, y uint16, text string, style cell.Style, maxX uint16) {
	if y >= buf.Area.Height || x >= maxX {
		return
	}
	runes := []rune(text)
	avail := maxX - x
	if uint16(len(runes)) > avail {
		runes = runes[:avail]
	}
	buf.SetString(x, y, string(runes), style)
}

// DrawRelayModal renders the animated Relay Server & Security configuration modal with scale opening/closing animation.
func DrawRelayModal(
	frame *terminal.Frame,
	screenArea cell.Rect,
	progress float64,
	currentURL string,
	currentToken string,
	urlState *widgets.TextInputState,
	tokenState *widgets.TextInputState,
	activeField int, // 0: URL input, 1: Token input, 2: Save btn, 3: Reset btn, 4: Cancel btn
	selField int,
	selStart int,
	selEnd int,
	onSelectField func(field int),
	onSave func(newURL, newToken string),
	onReset func(),
	onCancel func(),
) {
	if progress <= 0.001 {
		return
	}

	modalW, modalH := uint16(72), uint16(15)
	if screenArea.Width < modalW+2 {
		modalW = screenArea.Width - 2
	}
	if screenArea.Height < modalH+2 {
		modalH = screenArea.Height - 2
	}

	modalArea := terminal.CenterRect(screenArea, modalW, modalH)
	animatedArea := terminal.ScaleRect(modalArea, progress)

	if animatedArea.Width < 8 || animatedArea.Height < 5 {
		return
	}

	// 1. Drop shadow behind the dialog
	widgets.DrawShadow(frame.Buffer, animatedArea, 2, 1)
	frame.RegisterModal("relay_settings_dialog", animatedArea, onCancel)

	theme := CurrentTheme()
	dialogBg := theme.SurfaceBg

	// 2. Clear entire dialog area
	buf := frame.Buffer
	for dy := animatedArea.Y; dy < animatedArea.Y+animatedArea.Height; dy++ {
		for dx := animatedArea.X; dx < animatedArea.X+animatedArea.Width; dx++ {
			buf.SetCell(dx, dy, cell.Cell{Content: ' ', Style: cell.Style{Bg: dialogBg}})
		}
	}

	// 3. Render Block with rounded borders
	block := widgets.Block{
		Title:          " RELAY SERVER & SECURITY SETTINGS ",
		TitleAlignment: widgets.AlignCenter,
		Borders:        widgets.BorderAll,
		BorderSymbols:  widgets.SymbolsRounded,
		BorderStyle:    cell.Style{Fg: theme.BorderFocused},
		Style:          cell.Style{Bg: dialogBg},
	}
	frame.RenderWidget(block, animatedArea)
	inner := block.Inner(animatedArea)

	// Guard against tiny areas during animation
	if inner.Width < 20 || inner.Height < 8 {
		return
	}

	maxX := inner.X + inner.Width

	// 4. Status Row
	isCustom := IsCustomRelayActive(currentURL)
	statusPrefix := "Active Server: "
	drawBoundedString(buf, inner.X+1, inner.Y, statusPrefix, cell.Style{Fg: theme.TextMuted, Bg: dialogBg}, maxX)
	statusX := inner.X + 1 + uint16(len([]rune(statusPrefix)))
	if isCustom {
		drawBoundedString(buf, statusX, inner.Y, "[CUSTOM RELAY SERVER ACTIVE]", cell.Style{
			Fg:       theme.Success,
			Bg:       dialogBg,
			Modifier: cell.ModifierBold,
		}, maxX)
	} else {
		drawBoundedString(buf, statusX, inner.Y, "[OFFICIAL PUBLIC RELAY (Railway)]", cell.Style{
			Fg:       theme.Accent,
			Bg:       dialogBg,
			Modifier: cell.ModifierBold,
		}, maxX)
	}

	// 5. URL Input Row
	urlLabelY := inner.Y + 2
	urlLabelStyle := cell.Style{Fg: theme.TextMuted, Bg: dialogBg}
	if activeField == 0 {
		urlLabelStyle = cell.Style{Fg: theme.BorderFocused, Bg: dialogBg, Modifier: cell.ModifierBold}
	}
	urlLabelText := "Server WebSocket URL (Relay URL):"
	drawBoundedString(buf, inner.X+1, urlLabelY, urlLabelText, urlLabelStyle, maxX)

	urlInputY := urlLabelY + 1
	urlInputW := inner.Width - 2
	urlInputRect := cell.NewRect(inner.X+1, urlInputY, urlInputW, 1)

	urlSelS, urlSelE := -1, -1
	if selField == 0 {
		urlSelS, urlSelE = selStart, selEnd
	}

	urlInput := widgets.TextInput{
		ID:               "relay_url_input",
		State:            urlState,
		Placeholder:      "e.g. wss://voice.yourdomain.com/ws or ws://192.168.1.50:27850/ws",
		PlaceholderStyle: cell.Style{Fg: theme.TextMuted, Bg: theme.InputBg},
		Style:            cell.Style{Fg: theme.Text, Bg: theme.InputBg},
		FocusedStyle:     cell.Style{Fg: theme.Text, Bg: theme.InputBg, Modifier: cell.ModifierBold},
		SelectionStart:   urlSelS,
		SelectionEnd:     urlSelE,
		SelectionStyle:   cell.Style{Fg: cell.NewColorRGB(0, 0, 0), Bg: cell.NewColorRGB(255, 255, 255), Modifier: cell.ModifierBold},
		Focused:          activeField == 0,
	}
	if activeField == 0 {
		urlInput.Style = cell.Style{Fg: theme.Text, Bg: theme.InputBg, Modifier: cell.ModifierBold}
	}
	frame.RenderWidget(urlInput, urlInputRect)

	frame.RegisterClickHandler(urlInputRect, func(_ backend.MouseEvent) {
		if onSelectField != nil {
			onSelectField(0)
		}
	})

	// 6. Token / Password Input Row
	tokenLabelY := inner.Y + 5
	tokenLabelStyle := cell.Style{Fg: theme.TextMuted, Bg: dialogBg}
	if activeField == 1 {
		tokenLabelStyle = cell.Style{Fg: theme.BorderFocused, Bg: dialogBg, Modifier: cell.ModifierBold}
	}
	tokenLabelText := "Server Password / Token (RELAY_AUTH_TOKEN):"
	drawBoundedString(buf, inner.X+1, tokenLabelY, tokenLabelText, tokenLabelStyle, maxX)

	tokenInputY := tokenLabelY + 1
	tokenInputRect := cell.NewRect(inner.X+1, tokenInputY, urlInputW, 1)

	tokenSelS, tokenSelE := -1, -1
	if selField == 1 {
		tokenSelS, tokenSelE = selStart, selEnd
	}

	tokenInput := widgets.TextInput{
		ID:               "relay_token_input",
		State:            tokenState,
		Placeholder:      "Optional (leave empty if your server does not require a password)",
		PlaceholderStyle: cell.Style{Fg: theme.TextMuted, Bg: theme.InputBg},
		Style:            cell.Style{Fg: theme.Text, Bg: theme.InputBg},
		FocusedStyle:     cell.Style{Fg: theme.Text, Bg: theme.InputBg, Modifier: cell.ModifierBold},
		SelectionStart:   tokenSelS,
		SelectionEnd:     tokenSelE,
		SelectionStyle:   cell.Style{Fg: cell.NewColorRGB(0, 0, 0), Bg: cell.NewColorRGB(255, 255, 255), Modifier: cell.ModifierBold},
		Focused:          activeField == 1,
	}
	if activeField == 1 {
		tokenInput.Style = cell.Style{Fg: theme.Text, Bg: theme.InputBg, Modifier: cell.ModifierBold}
	}
	frame.RenderWidget(tokenInput, tokenInputRect)

	frame.RegisterClickHandler(tokenInputRect, func(_ backend.MouseEvent) {
		if onSelectField != nil {
			onSelectField(1)
		}
	})

	// 7. Buttons Row
	btnY := inner.Y + 8
	saveBtnText := "[ Save & Connect ]"
	resetBtnText := "[ Reset to Default ]"
	cancelBtnText := "[ Cancel ]"

	saveBtnStyle := cell.Style{Fg: theme.Text, Bg: theme.InputBg}
	if activeField == 2 {
		saveBtnStyle = cell.Style{Fg: cell.NewColorRGB(0, 0, 0), Bg: theme.Success, Modifier: cell.ModifierBold}
	}
	resetBtnStyle := cell.Style{Fg: theme.Text, Bg: theme.InputBg}
	if activeField == 3 {
		resetBtnStyle = cell.Style{Fg: cell.NewColorRGB(0, 0, 0), Bg: theme.Warning, Modifier: cell.ModifierBold}
	}
	cancelBtnStyle := cell.Style{Fg: theme.Text, Bg: theme.InputBg}
	if activeField == 4 {
		cancelBtnStyle = cell.Style{Fg: cell.NewColorRGB(255, 255, 255), Bg: theme.Danger, Modifier: cell.ModifierBold}
	}

	bX := inner.X + 1
	if bX+uint16(len([]rune(saveBtnText))) <= maxX {
		saveRect := cell.NewRect(bX, btnY, uint16(len([]rune(saveBtnText))), 1)
		drawBoundedString(buf, bX, btnY, saveBtnText, saveBtnStyle, maxX)
		frame.RegisterClickHandler(saveRect, func(_ backend.MouseEvent) {
			if onSave != nil {
				onSave(urlState.Value(), tokenState.Value())
			}
		})
	}

	bX += uint16(len([]rune(saveBtnText))) + 2
	if bX+uint16(len([]rune(resetBtnText))) <= maxX {
		resetRect := cell.NewRect(bX, btnY, uint16(len([]rune(resetBtnText))), 1)
		drawBoundedString(buf, bX, btnY, resetBtnText, resetBtnStyle, maxX)
		frame.RegisterClickHandler(resetRect, func(_ backend.MouseEvent) {
			if onReset != nil {
				onReset()
			}
		})
	}

	bX += uint16(len([]rune(resetBtnText))) + 2
	if bX+uint16(len([]rune(cancelBtnText))) <= maxX {
		cancelRect := cell.NewRect(bX, btnY, uint16(len([]rune(cancelBtnText))), 1)
		drawBoundedString(buf, bX, btnY, cancelBtnText, cancelBtnStyle, maxX)
		frame.RegisterClickHandler(cancelRect, func(_ backend.MouseEvent) {
			if onCancel != nil {
				onCancel()
			}
		})
	}

	// 8. Info & Keyboard Shortcuts
	helpY1 := inner.Y + 10
	if helpY1 < inner.Y+inner.Height {
		helpText1 := "• [Ctrl+V] Paste   • [Ctrl+C] Copy   • [Ctrl+A] Select All"
		drawBoundedString(buf, inner.X+1, helpY1, helpText1, cell.Style{Fg: theme.BorderFocused, Bg: dialogBg}, maxX)
	}
	helpY2 := inner.Y + 11
	if helpY2 < inner.Y+inner.Height {
		helpText2 := "• [Tab] Switch Field   • [Enter] Save & Connect   • [Esc] Close"
		drawBoundedString(buf, inner.X+1, helpY2, helpText2, cell.Style{Fg: theme.TextMuted, Bg: dialogBg}, maxX)
	}
}

// DrawScreenShareModal renders the screen and window selection modal with opening/closing scale animation and drop shadow
func DrawScreenShareModal(frame *terminal.Frame, screenArea cell.Rect, progress float64, selectedIdx int, targets []screenshare.WindowInfo, onSelect func(target screenshare.WindowInfo), onCancel func()) {
	if progress <= 0.001 {
		return
	}

	modalW, modalH := uint16(64), uint16(15)
	if screenArea.Width < modalW+2 {
		modalW = screenArea.Width - 2
	}
	if screenArea.Height < modalH+2 {
		modalH = screenArea.Height - 2
	}

	modalArea := terminal.CenterRect(screenArea, modalW, modalH)
	animatedArea := terminal.ScaleRect(modalArea, progress)

	if animatedArea.Width < 8 || animatedArea.Height < 5 {
		return
	}

	// 1. Draw Drop Shadow behind the dialog
	widgets.DrawShadow(frame.Buffer, animatedArea, 2, 1)

	frame.RegisterModal("screenshare_select_dialog", animatedArea, onCancel)

	theme := CurrentTheme()
	dialogBg := theme.SurfaceBg
	buf := frame.Buffer

	// 2. Clear entire dialog area with solid dark background
	for y := animatedArea.Y; y < animatedArea.Y+animatedArea.Height; y++ {
		for x := animatedArea.X; x < animatedArea.X+animatedArea.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: dialogBg}})
		}
	}

	// 3. Draw dialog Block with rounded borders and centered title
	block := widgets.Block{
		Title:          " SELECT SCREEN OR WINDOW TO BROADCAST ",
		TitleAlignment: widgets.AlignCenter,
		Borders:        widgets.BorderAll,
		BorderSymbols:  widgets.SymbolsRounded,
		BorderStyle:    cell.Style{Fg: theme.BorderFocused, Modifier: cell.ModifierBold},
		Style:          cell.Style{Bg: dialogBg},
	}
	frame.RenderWidget(block, animatedArea)

	inner := block.Inner(animatedArea)
	if inner.Height < 3 || inner.Width < 4 {
		return
	}

	// 4. Header title
	headerText := fmt.Sprintf("Select target to broadcast (%d available):", len(targets))
	if maxH := int(inner.Width - 2); len([]rune(headerText)) > maxH {
		headerText = string([]rune(headerText)[:maxH])
	}
	buf.SetString(inner.X+1, inner.Y, headerText, cell.Style{
		Fg:       theme.Accent,
		Bg:       dialogBg,
		Modifier: cell.ModifierBold,
	})

	// 5. List targets with scrolling window around selectedIdx
	listY := inner.Y + 2
	maxDisplay := int(inner.Height - 3)
	if maxDisplay < 1 {
		maxDisplay = 1
	}

	startIdx := 0
	if selectedIdx >= maxDisplay {
		startIdx = selectedIdx - maxDisplay + 1
	}
	endIdx := startIdx + maxDisplay
	if endIdx > len(targets) {
		endIdx = len(targets)
	}

	hasScrollbar := len(targets) > maxDisplay && inner.Width > 8
	listWidth := inner.Width - 2
	if hasScrollbar {
		listWidth = inner.Width - 4 // Leave margin for scrollbar
	}

	for i := startIdx; i < endIdx; i++ {
		t := targets[i]
		rowY := listY + uint16(i-startIdx)
		isSel := (i == selectedIdx)

		itemStyle := cell.Style{
			Fg: theme.Text,
			Bg: theme.InputBg,
		}
		prefix := "  "
		if isSel {
			itemStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Accent,
				Modifier: cell.ModifierBold,
			}
			prefix = "▶ "
		}

		itemText := prefix + t.Title
		maxChars := int(listWidth)
		runes := []rune(itemText)
		if len(runes) > maxChars {
			if maxChars > 3 {
				itemText = string(runes[:maxChars-3]) + "..."
			} else {
				itemText = string(runes[:maxChars])
			}
		}

		// Clear full row
		for x := inner.X + 1; x < inner.X+1+listWidth; x++ {
			buf.SetCell(x, rowY, cell.Cell{Content: ' ', Style: itemStyle})
		}
		buf.SetString(inner.X+1, rowY, itemText, itemStyle)

		targetItem := t
		frame.RegisterClickHandler(cell.NewRect(inner.X+1, rowY, listWidth, 1), func(_ backend.MouseEvent) {
			onSelect(targetItem)
		})
	}

	// 6. Draw Vertical Scrollbar on the right border margin
	if hasScrollbar {
		scrollX := inner.X + inner.Width - 2
		trackHeight := maxDisplay
		thumbHeight := int(math.Max(1, math.Round(float64(trackHeight*trackHeight)/float64(len(targets)))))
		if thumbHeight >= trackHeight {
			thumbHeight = trackHeight - 1
		}
		maxScroll := len(targets) - maxDisplay
		thumbY := 0
		if maxScroll > 0 {
			thumbY = int(math.Round(float64(startIdx) / float64(maxScroll) * float64(trackHeight-thumbHeight)))
		}

		for r := 0; r < trackHeight; r++ {
			curY := listY + uint16(r)
			if r >= thumbY && r < thumbY+thumbHeight {
				// Scrollbar Thumb
				buf.SetCell(scrollX, curY, cell.Cell{
					Content: '█',
					Style:   cell.Style{Fg: theme.BorderFocused, Bg: dialogBg},
				})
			} else {
				// Scrollbar Track
				buf.SetCell(scrollX, curY, cell.Cell{
					Content: '░',
					Style:   cell.Style{Fg: theme.Border, Bg: dialogBg},
				})
			}
		}
	}

	// 7. Bottom Navigation Guide
	bottomY := inner.Y + inner.Height - 1
	guideText := "[ENTER/CLICK] Select   [↑/↓] Navigate   [ESC] Cancel"
	if len(targets) > maxDisplay {
		guideText = fmt.Sprintf("[%d/%d]  %s", selectedIdx+1, len(targets), guideText)
	}
	if maxG := int(inner.Width - 2); len([]rune(guideText)) > maxG {
		guideText = string([]rune(guideText)[:maxG])
	}
	buf.SetString(inner.X+1, bottomY, guideText, cell.Style{
		Fg: theme.TextMuted,
		Bg: dialogBg,
	})
}

var (
	debugLogsMu   sync.Mutex
	debugLogsList = make([]string, 0, 500)
)

func AddDebugLog(msg string) {
	debugLogsMu.Lock()
	defer debugLogsMu.Unlock()
	ts := time.Now().Format("15:04:05.000")
	debugLogsList = append(debugLogsList, fmt.Sprintf("[%s] %s", ts, msg))
	if len(debugLogsList) > 1000 {
		debugLogsList = debugLogsList[len(debugLogsList)-500:]
	}
}

func GetDebugLogs() []string {
	debugLogsMu.Lock()
	defer debugLogsMu.Unlock()
	res := make([]string, len(debugLogsList))
	copy(res, debugLogsList)
	return res
}

func ClearDebugLogs() {
	debugLogsMu.Lock()
	defer debugLogsMu.Unlock()
	debugLogsList = debugLogsList[:0]
}

func GetAllDebugLogsText() string {
	debugLogsMu.Lock()
	defer debugLogsMu.Unlock()
	return strings.Join(debugLogsList, "\n")
}

// DrawDebugModal renders a full-featured technical debug log viewer modal
func DrawDebugModal(frame *terminal.Frame, area cell.Rect, scrollOffset int, onClose func(), onClear func(), onCopy func()) {
	if area.Width < 20 || area.Height < 10 {
		return
	}

	dialogW := uint16(math.Min(float64(area.Width-4), 110))
	dialogH := uint16(math.Min(float64(area.Height-4), 32))
	if dialogW < 30 {
		dialogW = area.Width
	}
	if dialogH < 10 {
		dialogH = area.Height
	}

	x := (area.Width - dialogW) / 2
	y := (area.Height - dialogH) / 2
	dialogArea := cell.NewRect(area.X+x, area.Y+y, dialogW, dialogH)

	theme := CurrentTheme()
	dialogBg := theme.SurfaceBg
	block := widgets.Block{
		Title:         " DEBUG & SYSTEM LOGS ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.BorderFocused, Modifier: cell.ModifierBold},
		Style:         cell.Style{Bg: dialogBg},
	}
	frame.RenderWidget(block, dialogArea)
	inner := block.Inner(dialogArea)

	buf := frame.Buffer
	for dy := inner.Y; dy < inner.Y+inner.Height; dy++ {
		for dx := inner.X; dx < inner.X+inner.Width; dx++ {
			buf.SetCell(dx, dy, cell.Cell{Content: ' ', Style: cell.Style{Bg: dialogBg}})
		}
	}

	logs := GetDebugLogs()

	// 1. Top Action Bar
	// [Esc] Close button
	closeBtn := "[Esc] Close"
	closeLen := uint16(len([]rune(closeBtn)))
	buf.SetString(inner.X+1, inner.Y, closeBtn, cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Danger,
		Modifier: cell.ModifierBold,
	})
	frame.RegisterClickHandler(cell.NewRect(inner.X+1, inner.Y, closeLen, 1), func(_ backend.MouseEvent) {
		if onClose != nil {
			onClose()
		}
	})

	// [C] Copy All button
	copyBtn := "[C] Copy All"
	copyLen := uint16(len([]rune(copyBtn)))
	copyX := inner.X + closeLen + 3
	buf.SetString(copyX, inner.Y, copyBtn, cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Secondary,
		Modifier: cell.ModifierBold,
	})
	frame.RegisterClickHandler(cell.NewRect(copyX, inner.Y, copyLen, 1), func(_ backend.MouseEvent) {
		if onCopy != nil {
			onCopy()
		}
	})

	// [Del] Clear button
	clearBtn := "[Del] Clear"
	clearLen := uint16(len([]rune(clearBtn)))
	clearX := copyX + copyLen + 2
	buf.SetString(clearX, inner.Y, clearBtn, cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Warning,
		Modifier: cell.ModifierBold,
	})
	frame.RegisterClickHandler(cell.NewRect(clearX, inner.Y, clearLen, 1), func(_ backend.MouseEvent) {
		if onClear != nil {
			onClear()
		}
	})

	// Log Count info on the right
	countInfo := fmt.Sprintf("Total: %d logs", len(logs))
	countLen := uint16(len([]rune(countInfo)))
	if inner.Width > countLen+2 {
		buf.SetString(inner.X+inner.Width-countLen-1, inner.Y, countInfo, cell.Style{
			Fg: theme.TextMuted,
			Bg: dialogBg,
		})
	}

	// 2. Divider line
	divY := inner.Y + 1
	for dx := inner.X; dx < inner.X+inner.Width; dx++ {
		buf.SetCell(dx, divY, cell.Cell{
			Content: '─',
			Style:   cell.Style{Fg: theme.Border, Bg: dialogBg},
		})
	}

	// 3. Log lines display area
	listY := inner.Y + 2
	maxDisplay := int(inner.Height - 3)
	if maxDisplay < 1 {
		maxDisplay = 1
	}

	listWidth := inner.Width - 2
	hasScrollbar := len(logs) > maxDisplay && inner.Width > 8
	if hasScrollbar {
		listWidth = inner.Width - 4
	}

	if len(logs) == 0 {
		buf.SetString(inner.X+2, listY, "No debug logs recorded yet.", cell.Style{
			Fg: theme.TextMuted,
			Bg: dialogBg,
		})
	} else {
		startIdx := 0
		if len(logs) > maxDisplay {
			startIdx = len(logs) - maxDisplay - scrollOffset
			if startIdx < 0 {
				startIdx = 0
			}
		}
		endIdx := startIdx + maxDisplay
		if endIdx > len(logs) {
			endIdx = len(logs)
		}

		visibleLogs := logs[startIdx:endIdx]
		for i, line := range visibleLogs {
			rowY := listY + uint16(i)
			logColor := theme.Text
			if strings.Contains(line, "[ERROR]") || strings.Contains(line, "[ERR]") || strings.Contains(line, "failed") {
				logColor = theme.Danger
			} else if strings.Contains(line, "[WARN]") {
				logColor = theme.Warning
			} else if strings.Contains(line, "[SCREEN]") || strings.Contains(line, "[SHARE]") || strings.Contains(line, "[WATCH]") || strings.Contains(line, "[VIEWER]") {
				logColor = theme.Secondary
			} else if strings.Contains(line, "[NET]") || strings.Contains(line, "[RELAY]") || strings.Contains(line, "[UDP]") || strings.Contains(line, "[TCP]") || strings.Contains(line, "[SECURITY]") || strings.Contains(line, "[HOST]") {
				logColor = theme.BorderFocused
			} else if strings.Contains(line, "[+]") || strings.Contains(line, "joined") {
				logColor = theme.Success
			}

			rLine := []rune(line)
			if len(rLine) > int(listWidth) {
				line = string(rLine[:listWidth-1]) + "…"
			}
			buf.SetString(inner.X+1, rowY, line, cell.Style{
				Fg: logColor,
				Bg: dialogBg,
			})
		}

		// Draw Vertical Scrollbar
		if hasScrollbar {
			scrollX := inner.X + inner.Width - 2
			trackHeight := maxDisplay
			thumbHeight := int(math.Max(1, math.Round(float64(trackHeight*trackHeight)/float64(len(logs)))))
			if thumbHeight >= trackHeight {
				thumbHeight = trackHeight - 1
			}
			maxScroll := len(logs) - maxDisplay
			thumbY := 0
			if maxScroll > 0 {
				thumbY = int(math.Round(float64(startIdx) / float64(maxScroll) * float64(trackHeight-thumbHeight)))
			}

			for r := 0; r < trackHeight; r++ {
				curY := listY + uint16(r)
				if r >= thumbY && r < thumbY+thumbHeight {
					buf.SetCell(scrollX, curY, cell.Cell{
						Content: '█',
						Style:   cell.Style{Fg: theme.BorderFocused, Bg: dialogBg},
					})
				} else {
					buf.SetCell(scrollX, curY, cell.Cell{
						Content: '░',
						Style:   cell.Style{Fg: theme.Border, Bg: dialogBg},
					})
				}
			}
		}
	}

	// 4. Bottom Hint
	bottomY := inner.Y + inner.Height - 1
	guide := "[ESC/F12] Close   [↑/↓ / PgUp/PgDn] Scroll   [C] Copy   [Del] Clear"
	if scrollOffset > 0 {
		guide = fmt.Sprintf("↑ +%d earlier logs   %s", scrollOffset, guide)
	}
	if maxG := int(inner.Width - 2); len([]rune(guide)) > maxG {
		guide = string([]rune(guide)[:maxG])
	}
	buf.SetString(inner.X+1, bottomY, guide, cell.Style{
		Fg: theme.TextMuted,
		Bg: dialogBg,
	})
}

// DrawFileOfferModal renders an interactive confirmation modal for incoming P2P file transfers and code snippets
func DrawFileOfferModal(frame *terminal.Frame, screenArea cell.Rect, progress float64, offer *FileOffer, onAccept func(), onDecline func(), onOpenEditor func()) {
	if progress <= 0.001 || offer == nil {
		return
	}

	modalW, modalH := uint16(64), uint16(11)
	if offer.IsCode {
		modalH = 12
	}
	if screenArea.Width < modalW+2 {
		modalW = screenArea.Width - 2
	}
	if screenArea.Height < modalH+2 {
		modalH = screenArea.Height - 2
	}

	modalArea := terminal.CenterRect(screenArea, modalW, modalH)
	animatedArea := terminal.ScaleRect(modalArea, progress)

	if animatedArea.Width < 8 || animatedArea.Height < 5 {
		return
	}

	// 1. Drop shadow behind the dialog
	widgets.DrawShadow(frame.Buffer, animatedArea, 2, 1)

	frame.RegisterModal("file_offer_dialog", animatedArea, onDecline)

	theme := CurrentTheme()
	dialogBg := theme.SurfaceBg
	buf := frame.Buffer

	// 2. Clear entire dialog area
	for y := animatedArea.Y; y < animatedArea.Y+animatedArea.Height; y++ {
		for x := animatedArea.X; x < animatedArea.X+animatedArea.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: dialogBg}})
		}
	}

	// 3. Dialog block
	title := " 📥 INCOMING FILE TRANSFER "
	if offer.IsCode {
		title = " 📥 INCOMING CODE SNIPPET "
	}
	block := widgets.Block{
		Title:          title,
		TitleAlignment: widgets.AlignCenter,
		Borders:        widgets.BorderAll,
		BorderSymbols:  widgets.SymbolsRounded,
		BorderStyle:    cell.Style{Fg: theme.BorderFocused, Modifier: cell.ModifierBold},
		Style:          cell.Style{Bg: dialogBg},
	}
	frame.RenderWidget(block, animatedArea)

	inner := block.Inner(animatedArea)
	if inner.Height < 3 || inner.Width < 4 {
		return
	}

	// 4. Content lines
	// Sender
	senderText := fmt.Sprintf("From: %s", offer.SenderNick)
	buf.SetString(inner.X+1, inner.Y, senderText, cell.Style{
		Fg:       theme.Accent,
		Bg:       dialogBg,
		Modifier: cell.ModifierBold,
	})

	// File name and size
	fileInfo := fmt.Sprintf("File: %s  (%s)", offer.FileName, formatBytes(offer.FileSize))
	if maxW := int(inner.Width - 2); len([]rune(fileInfo)) > maxW {
		fileInfo = string([]rune(fileInfo)[:maxW])
	}
	buf.SetString(inner.X+1, inner.Y+1, fileInfo, cell.Style{
		Fg:       theme.Text,
		Bg:       dialogBg,
		Modifier: cell.ModifierBold,
	})

	// Checksum SHA-256
	hashStr := offer.Checksum
	if len(hashStr) > 16 {
		hashStr = hashStr[:16] + "..."
	}
	hashText := fmt.Sprintf("SHA-256: %s", hashStr)
	buf.SetString(inner.X+1, inner.Y+2, hashText, cell.Style{
		Fg: theme.TextMuted,
		Bg: dialogBg,
	})

	// Warning or Preview
	ext := strings.ToLower(filepath.Ext(offer.FileName))
	isDangerous := DangerousFileExtensions[ext]

	curRow := inner.Y + 3
	if isDangerous {
		warnText := "⚠️ Caution: Executable file. Accept only if you trust the sender."
		if maxW := int(inner.Width - 2); len([]rune(warnText)) > maxW {
			warnText = string([]rune(warnText)[:maxW])
		}
		buf.SetString(inner.X+1, curRow, warnText, cell.Style{
			Fg:       theme.Danger,
			Bg:       dialogBg,
			Modifier: cell.ModifierBold,
		})
		curRow++
	} else if offer.IsCode {
		// Code preview
		codePreview := strings.TrimSpace(string(offer.Data))
		lines := strings.Split(codePreview, "\n")
		previewLine := ""
		if len(lines) > 0 {
			previewLine = strings.TrimSpace(lines[0])
		}
		if len(lines) > 1 {
			previewLine += "  |  " + strings.TrimSpace(lines[1])
		}
		if maxW := int(inner.Width - 6); len([]rune(previewLine)) > maxW {
			previewLine = string([]rune(previewLine)[:maxW]) + "..."
		}
		previewStr := fmt.Sprintf("Code: %s", previewLine)
		buf.SetString(inner.X+1, curRow, previewStr, cell.Style{
			Fg: theme.Secondary,
			Bg: dialogBg,
		})
		curRow++
	} else {
		saveLoc := fmt.Sprintf("Save to: %s", GetLimoniTransfersDir())
		if maxW := int(inner.Width - 2); len([]rune(saveLoc)) > maxW {
			saveLoc = string([]rune(saveLoc)[:maxW])
		}
		buf.SetString(inner.X+1, curRow, saveLoc, cell.Style{
			Fg: theme.TextMuted,
			Bg: dialogBg,
		})
		curRow++
	}

	// 5. Action Buttons at the bottom
	btnRow := inner.Y + inner.Height - 1
	if btnRow <= curRow {
		btnRow = curRow + 1
	}

	// Accept Button
	acceptLabel := " [Y] Accept & Save "
	acceptLen := uint16(len([]rune(acceptLabel)))
	acceptX := inner.X + 1
	buf.SetString(acceptX, btnRow, acceptLabel, cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Success,
		Modifier: cell.ModifierBold,
	})
	frame.RegisterClickHandler(cell.NewRect(acceptX, btnRow, acceptLen, 1), func(_ backend.MouseEvent) {
		if onAccept != nil {
			onAccept()
		}
	})

	// Decline Button
	declineLabel := " [N] Decline "
	declineLen := uint16(len([]rune(declineLabel)))
	declineX := acceptX + acceptLen + 2
	if declineX+declineLen <= inner.X+inner.Width {
		buf.SetString(declineX, btnRow, declineLabel, cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Danger,
			Modifier: cell.ModifierBold,
		})
		frame.RegisterClickHandler(cell.NewRect(declineX, btnRow, declineLen, 1), func(_ backend.MouseEvent) {
			if onDecline != nil {
				onDecline()
			}
		})
	}

	// Optional Open in Editor button for code
	if offer.IsCode {
		editorLabel := " [O] Open Editor "
		editorLen := uint16(len([]rune(editorLabel)))
		editorX := declineX + declineLen + 2
		if editorX+editorLen <= inner.X+inner.Width {
			buf.SetString(editorX, btnRow, editorLabel, cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Accent,
				Modifier: cell.ModifierBold,
			})
			frame.RegisterClickHandler(cell.NewRect(editorX, btnRow, editorLen, 1), func(_ backend.MouseEvent) {
				if onOpenEditor != nil {
					onOpenEditor()
				}
			})
		}
	}
}
