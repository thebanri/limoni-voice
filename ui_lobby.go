package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thebanri/limoni/core/backend"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/graphics"
	"github.com/thebanri/limoni/layout"
	"github.com/thebanri/limoni/widgets"
)

type LobbyView struct {
	// 3D OBJ Microphone State
	MicModel         graphics.Model3D
	Scale            float64
	RotX, RotY, RotZ float64
	AutoRotate       bool
	AutoRotateSpeed  float64
	StartTime        time.Time

	// Mouse Drag State
	DragActive bool
	LastDragX  int
	LastDragY  int

	// Room & Inputs
	CurrentCode      string
	NickState        *widgets.TextInputState
	CodeState        *widgets.TextInputState
	PinState         *widgets.TextInputState
	IsPinProtected   bool
	ToastMsg         string
	ToastTimer       int
	ActiveInput      int // 0: Nickname, 1: RoomCode to Join, 2: Host / General, 3: Host PIN
	IsConnecting     bool
	ConnectingTarget string

	// Action Callbacks
	OnStartHost     func()
	OnJoinRoom      func(code string)
	OnCancelJoin    func()
	OnCopyCode      func(code string)
	OnNewCode        func()
	OnOpenTestModal  func()
	OnOpenRelayModal func()
	RelayURL         string
	RelayOnline      bool
	RelayStatus      string
}

func GenerateMicrophoneModel() graphics.Model3D {
	var model graphics.Model3D
	model.Name = "Studio Microphone"
	model.Vertices = []graphics.Vertex3D{}
	model.Faces = [][]int{}
	model.FaceColors = []cell.Color{}

	// Helper to add a circular ring of vertices
	addRing := func(y float64, r float64, segs int) int {
		start := len(model.Vertices)
		for i := 0; i < segs; i++ {
			angle := float64(i) * 2.0 * math.Pi / float64(segs)
			model.Vertices = append(model.Vertices, graphics.Vertex3D{
				X: r * math.Cos(angle),
				Y: y,
				Z: r * math.Sin(angle),
			})
		}
		return start
	}

	// Helper to bridge two rings with quad faces
	bridgeRings := func(r1Start, r2Start int, segs int, col cell.Color) {
		for i := 0; i < segs; i++ {
			next := (i + 1) % segs
			face := []int{r1Start + i, r1Start + next, r2Start + next, r2Start + i}
			model.Faces = append(model.Faces, face)
			model.FaceColors = append(model.FaceColors, col)
		}
	}

	// Helper to cap a ring
	capRing := func(startIdx int, segs int, col cell.Color, pointingUp bool) {
		face := make([]int, segs)
		for i := 0; i < segs; i++ {
			if pointingUp {
				face[i] = startIdx + (segs - 1 - i)
			} else {
				face[i] = startIdx + i
			}
		}
		model.Faces = append(model.Faces, face)
		model.FaceColors = append(model.FaceColors, col)
	}

	// Helper to create a dome to a peak point
	domeToPoint := func(ringStart int, peakIdx int, segs int, col cell.Color) {
		for i := 0; i < segs; i++ {
			next := (i + 1) % segs
			face := []int{ringStart + i, ringStart + next, peakIdx}
			model.Faces = append(model.Faces, face)
			model.FaceColors = append(model.FaceColors, col)
		}
	}

	segs := 12

	// Palette
	darkMetal := cell.NewColorRGB(0x2D, 0x34, 0x36)      // Gunmetal gray
	goldMetal := cell.NewColorRGB(0xFD, 0xCB, 0x6E)      // Warm polished gold
	silverChassis := cell.NewColorRGB(220, 225, 235)  // Chrome body
	cyanGrill := cell.NewColorRGB(0x00, 0xF5, 0xD4)      // Glowing cyan inner element
	blackCord := cell.NewColorRGB(0x0A, 0x0E, 0x17)      // Elastic cords

	// 1. BEVELED BASE
	baseRing1 := addRing(-1.8, 1.5, segs)
	baseRing2 := addRing(-1.65, 1.3, segs)
	baseRing3 := addRing(-1.5, 0.4, segs)

	capRing(baseRing1, segs, darkMetal, false)
	bridgeRings(baseRing1, baseRing2, segs, darkMetal)
	bridgeRings(baseRing2, baseRing3, segs, darkMetal)
	capRing(baseRing3, segs, darkMetal, true)

	// 2. TELESCOPING STEM
	stemRing1 := addRing(-1.5, 0.12, segs)
	stemRing2 := addRing(-0.4, 0.12, segs)
	bridgeRings(stemRing1, stemRing2, segs, goldMetal)

	// 3. SHOCKMOUNT OUTER RING (Suspension)
	outerRing1 := addRing(-0.05, 1.35, segs)
	outerRing2 := addRing(0.15, 1.35, segs)
	bridgeRings(outerRing1, outerRing2, segs, darkMetal)

	// 4. MICROPHONE TAPERED CAPSULE
	capsuleRing1 := addRing(-0.3, 0.48, segs) // Bottom
	capsuleRing2 := addRing(0.4, 0.82, segs)  // Mid body
	capsuleRing3 := addRing(0.5, 0.88, segs)  // Grill start
	capsuleRing4 := addRing(1.4, 0.75, segs)  // Grill top
	capsuleRing5 := addRing(1.65, 0.35, segs) // Dome ring

	domePeakIdx := len(model.Vertices)
	model.Vertices = append(model.Vertices, graphics.Vertex3D{X: 0.0, Y: 1.8, Z: 0.0})

	// Bridge capsule capsule parts
	capRing(capsuleRing1, segs, silverChassis, false)
	bridgeRings(capsuleRing1, capsuleRing2, segs, silverChassis)
	bridgeRings(capsuleRing2, capsuleRing3, segs, goldMetal) // Gold middle band
	bridgeRings(capsuleRing3, capsuleRing4, segs, cyanGrill)  // Main microphone grill
	bridgeRings(capsuleRing4, capsuleRing5, segs, silverChassis)
	domeToPoint(capsuleRing5, domePeakIdx, segs, silverChassis)

	// 5. SHOCKMOUNT ELASTIC SPONS / CORDS
	// We construct physical visual cords connecting outer ring to the mic body
	for i := 0; i < 4; i++ {
		angle := float64(i) * math.Pi / 2.0
		cosA := math.Cos(angle)
		sinA := math.Sin(angle)

		// Outer cord attachment point
		pOuter := graphics.Vertex3D{X: 1.35 * cosA, Y: 0.05, Z: 1.35 * sinA}
		pInner := graphics.Vertex3D{X: 0.65 * cosA, Y: 0.05, Z: 0.65 * sinA}

		idxOuter := len(model.Vertices)
		model.Vertices = append(model.Vertices, pOuter)
		idxInner := len(model.Vertices)
		model.Vertices = append(model.Vertices, pInner)

		// Face forming the cord line
		model.Faces = append(model.Faces, []int{idxOuter, idxInner, idxInner})
		model.FaceColors = append(model.FaceColors, blackCord)
	}

	// 6. YOKE STEM TO SHOCKMOUNT CONNECTOR
	// Visual support bar from the stand stem up to the shockmount bottom
	connectorIdx1 := len(model.Vertices)
	model.Vertices = append(model.Vertices, graphics.Vertex3D{X: 0.0, Y: -0.4, Z: 0.0})
	connectorIdx2 := len(model.Vertices)
	model.Vertices = append(model.Vertices, graphics.Vertex3D{X: 0.0, Y: -0.05, Z: -1.35})

	model.Faces = append(model.Faces, []int{connectorIdx1, connectorIdx2, connectorIdx1})
	model.FaceColors = append(model.FaceColors, darkMetal)

	model.Normalize(2.0)
	return model
}

func loadMicrophoneModel() graphics.Model3D {
	// Search current directory and common paths
	searchPaths := []string{
		"microphone.obj",
		"../limoni-voice/microphone.obj",
		"/home/thebanri/Projects/limoni-voice/microphone.obj",
	}

	execPath, err := os.Executable()
	if err == nil {
		searchPaths = append([]string{filepath.Join(filepath.Dir(execPath), "microphone.obj")}, searchPaths...)
	}

	for _, p := range searchPaths {
		// Verify file exists and is less than 2.5MB to avoid rendering lag
		if info, err := os.Stat(p); err == nil && info.Size() < 2500000 {
			if model, err := graphics.LoadOBJ(p); err == nil && len(model.Vertices) > 0 {
				model.Normalize(2.0)
				return model
			}
		}
	}

	// Use our high-performance, beautiful procedurally generated 3D vintage mic
	return GenerateMicrophoneModel()
}

func NewLobbyView() *LobbyView {
	code := GenerateRoomCode()
	nickState := widgets.NewTextInputState()
	nickState.SetValue("User_" + code[:4])

	codeState := widgets.NewTextInputState()
	pinState := widgets.NewTextInputState()
	pinState.SetValue("1234")

	return &LobbyView{
		MicModel:        loadMicrophoneModel(),
		Scale:           4.8,
		RotX:            15.0,
		RotY:            0.0,
		RotZ:            0.0,
		AutoRotate:      true,
		AutoRotateSpeed: 1.8,
		StartTime:       time.Now(),
		CurrentCode:     code,
		NickState:       nickState,
		CodeState:       codeState,
		PinState:        pinState,
		ActiveInput:     2,
	}
}

func (l *LobbyView) SetToast(msg string) {
	l.ToastMsg = msg
	l.ToastTimer = 90 // ~3 seconds at 30 FPS
}

func (l *LobbyView) Update(dt float64) {
	if l.ToastTimer > 0 {
		l.ToastTimer--
		if l.ToastTimer == 0 {
			l.ToastMsg = ""
		}
	}

	if l.AutoRotate && !l.DragActive {
		l.RotY += l.AutoRotateSpeed * (dt / 33.3)
		if l.RotY >= 360.0 {
			l.RotY -= 360.0
		}
	}
}

func (l *LobbyView) Render(frame *terminal.Frame, area cell.Rect) {
	hl := layout.NewFlexLayout(layout.Horizontal, 0,
		layout.Percentage(50), // 3D Mic View
		layout.Percentage(50), // Settings Panel
	)
	splits := hl.Split(area)
	if len(splits) < 2 {
		return
	}

	l.render3DMic(frame, splits[0])
	l.renderControls(frame, splits[1])
}

func (l *LobbyView) render3DMic(frame *terminal.Frame, area cell.Rect) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         " 3D STUDIO MICROPHONE (OBJ) ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.BorderFocused},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	innerArea := block.Inner(area)

	buf := frame.Buffer
	for y := innerArea.Y; y < innerArea.Y+innerArea.Height; y++ {
		for x := innerArea.X; x < innerArea.X+innerArea.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	if innerArea.Width < 4 || innerArea.Height < 4 {
		return
	}

	asciiWidget := widgets.Ascii3D{
		Model:                l.MicModel,
		Mode:                 widgets.ModeBraille,
		Scale:                l.Scale,
		XOffset:              0.0,
		YOffset:              0.0,
		CameraDistance:       4.8,
		FOV:                  60.0,
		RotX:                 l.RotX,
		RotY:                 l.RotY,
		LightDirection:       graphics.Vector3D{X: 1.0, Y: 1.0, Z: 1.0},
		EnvironmentIntensity: 0.2,
		Contrast:             1.3,
		EdgeContrast:         2.5,
		Exposure:             1.1,
		Roughness:            0.2,
		Ascii:                false,
		Colored:              true,
		Invert:               false,
		Color:                cell.NewColorRGB(220, 225, 235),
		Highlight:            cell.NewColorRGB(255, 255, 255),
	}

	frame.RenderWidget(asciiWidget, innerArea)
}

func (l *LobbyView) renderControls(frame *terminal.Frame, area cell.Rect) {
	theme := CurrentTheme()
	mainTitle := " P2P ROOM & CONNECTION (CROC ENGINE) "
	if IsCustomRelayActive(l.RelayURL) {
		if l.RelayOnline {
			mainTitle = " P2P ROOM & CONNECTION [⚡ CUSTOM RELAY: ONLINE - (R)] "
		} else if l.RelayStatus == "Offline" {
			mainTitle = " P2P ROOM & CONNECTION [⚠ CUSTOM RELAY: OFFLINE (LAN ONLY) - (R)] "
		} else if l.RelayStatus != "" {
			mainTitle = fmt.Sprintf(" P2P ROOM & CONNECTION [⚡ CUSTOM RELAY: %s - (R)] ", strings.ToUpper(l.RelayStatus))
		} else {
			mainTitle = " P2P ROOM & CONNECTION [⚡ CUSTOM RELAY ACTIVE - (R)] "
		}
	} else {
		if l.RelayStatus == "Offline" {
			mainTitle = " P2P ROOM & CONNECTION [⚠ RELAY: OFFLINE (LAN ONLY) - (R)] "
		} else if l.RelayOnline {
			mainTitle = " P2P ROOM & CONNECTION [⚡ RELAY: ONLINE - (R)] "
		}
	}
	mainBlock := widgets.Block{
		Title:         mainTitle,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.BorderFocused},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(mainBlock, area)
	inner := mainBlock.Inner(area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	vl := layout.NewFlexLayout(layout.Vertical, 0,
		layout.Fixed(4), // Nickname block
		layout.Fixed(7), // Host room block (Key + PIN + Buttons)
		layout.Fixed(6), // Join room block
		layout.Fill(),   // Information block
	)
	vSplits := vl.Split(inner)
	if len(vSplits) < 4 {
		return
	}

	nickArea := vSplits[0]
	hostArea := vSplits[1]
	joinArea := vSplits[2]

	unfocusedBorder := cell.Style{Fg: theme.Border}
	unfocusedBg := cell.Style{Bg: theme.SurfaceBg}

	// 1. Nickname Block
	isNickFocused := (l.ActiveInput == 0)
	nickTitle := " [1] YOUR NICKNAME "
	nickBorderStyle := unfocusedBorder
	nickBgStyle := unfocusedBg
	if isNickFocused {
		nickTitle = " ► [1] YOUR NICKNAME (FOCUSED - Type to Change) ◄ "
		nickBorderStyle = cell.Style{
			Fg:       theme.Accent,
			Modifier: cell.ModifierBold,
		}
		nickBgStyle = cell.Style{Bg: theme.InputBg}
	}

	frame.RegisterClickHandler(nickArea, func(_ backend.MouseEvent) {
		l.ActiveInput = 0
	})

	nickBlock := widgets.Block{
		Title:         nickTitle,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   nickBorderStyle,
		Style:         nickBgStyle,
	}
	frame.RenderWidget(nickBlock, nickArea)
	nickInner := nickBlock.Inner(nickArea)

	for y := nickInner.Y; y < nickInner.Y+nickInner.Height; y++ {
		for x := nickInner.X; x < nickInner.X+nickInner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: nickBgStyle})
		}
	}

	nickInput := widgets.TextInput{
		ID:          "nickname_input",
		State:       l.NickState,
		Placeholder: "Enter your nickname...",
	}
	frame.RenderWidget(nickInput, nickInner)

	// 2. Host Room Block
	isHostFocused := (l.ActiveInput == 2 || l.ActiveInput == 3)
	hostTitle := " [2] CREATE ROOM (YOU HOST) "
	hostBorderStyle := unfocusedBorder
	hostBgStyle := unfocusedBg
	keyStyle := cell.Style{
		Fg: theme.TextMuted,
		Bg: theme.CardBg,
	}
	hostBtnStyle := cell.Style{
		Fg: theme.TextMuted,
		Bg: nickBgStyle.Bg,
	}

	if isHostFocused {
		hostTitle = " ► [2] CREATE ROOM (YOU HOST) [SELECTED] ◄ "
		hostBorderStyle = cell.Style{
			Fg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}
		hostBgStyle = cell.Style{Bg: theme.CardBg}
		keyStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}
		hostBtnStyle = cell.Style{
			Fg:       theme.Accent,
			Bg:       hostBgStyle.Bg,
			Modifier: cell.ModifierBold,
		}
	}

	// Register card container click handler FIRST so child widgets take priority
	frame.RegisterClickHandler(hostArea, func(_ backend.MouseEvent) {
		if l.ActiveInput != 3 {
			l.ActiveInput = 2
		}
	})

	hostBlock := widgets.Block{
		Title:         hostTitle,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   hostBorderStyle,
		Style:         hostBgStyle,
	}
	frame.RenderWidget(hostBlock, hostArea)
	hostInner := hostBlock.Inner(hostArea)

	for y := hostInner.Y; y < hostInner.Y+hostInner.Height; y++ {
		for x := hostInner.X; x < hostInner.X+hostInner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: hostBgStyle})
		}
	}

	codeLabel := "Your Room Key (Share with Friends):"
	codeLabelStyle := cell.Style{Fg: theme.TextMuted, Bg: hostBgStyle.Bg}
	if isHostFocused {
		codeLabelStyle = cell.Style{Fg: theme.Text, Bg: hostBgStyle.Bg}
	}
	buf.SetString(hostInner.X, hostInner.Y, codeLabel, codeLabelStyle)

	keyBoxStr := fmt.Sprintf("  [ %s ]  ", l.CurrentCode)
	buf.SetString(hostInner.X+2, hostInner.Y+1, keyBoxStr, keyStyle)

	frame.RegisterClickHandler(cell.NewRect(hostInner.X+2, hostInner.Y+1, uint16(len([]rune(keyBoxStr))), 1), func(_ backend.MouseEvent) {
		l.ActiveInput = 2
		if l.OnCopyCode != nil {
			l.OnCopyCode(l.CurrentCode)
		}
	})

	// PIN Protection Checkbox row
	pinCheckStr := "[ ] PIN / Password Protected (4 Digits)"
	pinCheckStyle := cell.Style{Fg: theme.TextMuted, Bg: hostBgStyle.Bg}
	if l.IsPinProtected {
		pinCheckStr = "[X] PIN Protected (4 Digits):"
		pinCheckStyle = cell.Style{
			Fg:       theme.Accent,
			Bg:       hostBgStyle.Bg,
			Modifier: cell.ModifierBold,
		}
	}
	buf.SetString(hostInner.X, hostInner.Y+3, pinCheckStr, pinCheckStyle)
	frame.RegisterClickHandler(cell.NewRect(hostInner.X, hostInner.Y+3, uint16(len([]rune(pinCheckStr))), 1), func(_ backend.MouseEvent) {
		l.IsPinProtected = !l.IsPinProtected
		if l.IsPinProtected {
			if l.PinState.Value() == "" {
				l.PinState.SetValue("1234")
			}
			l.ActiveInput = 3
		} else {
			l.ActiveInput = 2
		}
	})

	if l.IsPinProtected {
		pinInputRect := cell.Rect{
			X:      hostInner.X + uint16(len([]rune(pinCheckStr))) + 1,
			Y:      hostInner.Y + 3,
			Width:  8,
			Height: 1,
		}
		pinInput := widgets.TextInput{
			ID:          "host_pin_input",
			State:       l.PinState,
			Placeholder: "1234",
		}
		frame.RenderWidget(pinInput, pinInputRect)
		frame.RegisterClickHandler(pinInputRect, func(_ backend.MouseEvent) {
			l.ActiveInput = 3
		})
	}

	hostBtns := "[Enter] Open This Room   •   [F2] Copy Code   •   [F3] New Code"
	buf.SetString(hostInner.X, hostInner.Y+4, hostBtns, hostBtnStyle)

	frame.RegisterClickHandler(cell.NewRect(hostInner.X, hostInner.Y+4, 19, 1), func(_ backend.MouseEvent) {
		l.ActiveInput = 2
		if l.OnStartHost != nil {
			l.OnStartHost()
		}
	})
	frame.RegisterClickHandler(cell.NewRect(hostInner.X+22, hostInner.Y+4, 17, 1), func(_ backend.MouseEvent) {
		l.ActiveInput = 2
		if l.OnCopyCode != nil {
			l.OnCopyCode(l.CurrentCode)
		}
	})
	frame.RegisterClickHandler(cell.NewRect(hostInner.X+42, hostInner.Y+4, 14, 1), func(_ backend.MouseEvent) {
		l.ActiveInput = 2
		if l.OnNewCode != nil {
			l.OnNewCode()
		}
	})

	// 3. Join Room Block
	isJoinFocused := (l.ActiveInput == 1)
	joinTitle := " [3] JOIN EXISTING ROOM "
	joinBorderStyle := unfocusedBorder
	joinBgStyle := unfocusedBg
	joinBtnStyle := cell.Style{
		Fg: theme.TextMuted,
		Bg: joinBgStyle.Bg,
	}

	if l.IsConnecting {
		joinTitle = fmt.Sprintf(" ► [3] CONNECTING TO ROOM (Verifying Host: %s) ◄ ", l.ConnectingTarget)
		joinBorderStyle = cell.Style{
			Fg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}
		joinBgStyle = cell.Style{Bg: theme.CardBg}
		joinBtnStyle = cell.Style{
			Fg:       theme.Warning,
			Bg:       joinBgStyle.Bg,
			Modifier: cell.ModifierBold,
		}
	} else if isJoinFocused {
		joinTitle = " ► [3] JOIN EXISTING ROOM (FOCUSED - Paste with Ctrl+V) ◄ "
		joinBorderStyle = cell.Style{
			Fg:       theme.Accent,
			Modifier: cell.ModifierBold,
		}
		joinBgStyle = cell.Style{Bg: theme.InputBg}
		joinBtnStyle = cell.Style{
			Fg:       theme.Accent,
			Bg:       joinBgStyle.Bg,
			Modifier: cell.ModifierBold,
		}
	}

	// Register card container click handler FIRST so input and buttons take priority
	frame.RegisterClickHandler(joinArea, func(_ backend.MouseEvent) {
		l.ActiveInput = 1
	})

	joinBlock := widgets.Block{
		Title:         joinTitle,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   joinBorderStyle,
		Style:         joinBgStyle,
	}
	frame.RenderWidget(joinBlock, joinArea)
	joinInner := joinBlock.Inner(joinArea)

	for y := joinInner.Y; y < joinInner.Y+joinInner.Height; y++ {
		for x := joinInner.X; x < joinInner.X+joinInner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: joinBgStyle})
		}
	}

	joinLabel := "Paste the Room Key from Your Friend (Ctrl+V):"
	if l.IsConnecting {
		joinLabel = "Verifying room, searching host and establishing E2EE connection..."
	}
	joinLabelStyle := cell.Style{Fg: theme.TextMuted, Bg: joinBgStyle.Bg}
	if isJoinFocused || l.IsConnecting {
		joinLabelStyle = cell.Style{Fg: theme.Text, Bg: joinBgStyle.Bg}
	}
	buf.SetString(joinInner.X, joinInner.Y, joinLabel, joinLabelStyle)

	joinInputRect := cell.Rect{
		X:      joinInner.X + 1,
		Y:      joinInner.Y + 1,
		Width:  joinInner.Width - 2,
		Height: 1,
	}

	codeInput := widgets.TextInput{
		ID:          "roomcode_input",
		State:       l.CodeState,
		Placeholder: "e.g. 7492-neon-falcon (or 7492-neon-falcon:1234)",
	}
	frame.RenderWidget(codeInput, joinInputRect)

	joinBtns := "[Enter] Connect to Room (Max: 4 Members)"
	if l.IsConnecting {
		joinBtns = "⏳ [Wait] Connecting to host...   •   [Esc] Cancel"
	}
	buf.SetString(joinInner.X, joinInner.Y+3, joinBtns, joinBtnStyle)

	frame.RegisterClickHandler(cell.NewRect(joinInner.X, joinInner.Y+3, joinInner.Width, 1), func(_ backend.MouseEvent) {
		l.ActiveInput = 1
		if l.IsConnecting {
			if l.OnCancelJoin != nil {
				l.OnCancelJoin()
			}
			return
		}
		cleanCode := NormalizeCode(l.CodeState.Value())
		if cleanCode != "" {
			if l.OnJoinRoom != nil {
				l.OnJoinRoom(cleanCode)
			}
		} else {
			l.SetToast("Please enter a room key to connect")
		}
	})

	frame.RegisterClickHandler(joinInputRect, func(_ backend.MouseEvent) {
		l.ActiveInput = 1
	})

	// 4. Info and Help Block
	bottomArea := vSplits[3]
	botBlock := widgets.Block{
		Title:         " INFO & SHORTCUTS ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.Border},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(botBlock, bottomArea)
	botInner := botBlock.Inner(bottomArea)

	for y := botInner.Y; y < botInner.Y+botInner.Height; y++ {
		for x := botInner.X; x < botInner.X+botInner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	if l.ToastMsg != "" {
		toastStyle := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Accent,
			Modifier: cell.ModifierBold,
		}
		buf.SetString(botInner.X+1, botInner.Y, "  "+l.ToastMsg+"  ", toastStyle)
	} else {
		isCustom := IsCustomRelayActive(l.RelayURL)
		relayBtn := "[ ⚡ R : RELAY & SECURITY SETTINGS ]"
		relayBtnStyle := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.BorderFocused,
			Modifier: cell.ModifierBold,
		}
		if isCustom {
			relayBtn = "[ ⚡ R : CUSTOM RELAY ACTIVE (Click to edit) ]"
			relayBtnStyle = cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Success,
				Modifier: cell.ModifierBold,
			}
		}

		buf.SetString(botInner.X+1, botInner.Y, relayBtn, relayBtnStyle)
		frame.RegisterClickHandler(cell.NewRect(botInner.X+1, botInner.Y, uint16(len([]rune(relayBtn))), 1), func(_ backend.MouseEvent) {
			if l.OnOpenRelayModal != nil {
				l.OnOpenRelayModal()
			}
		})

		testBtn := "[ 🎤 T : Mic Test ]"
		testBtnStyle := cell.Style{
			Fg:       theme.Text,
			Bg:       theme.InputBg,
			Modifier: cell.ModifierBold,
		}
		testBtnX := botInner.X + 1 + uint16(len([]rune(relayBtn))) + 2
		if testBtnX+uint16(len([]rune(testBtn))) <= botInner.X+botInner.Width {
			buf.SetString(testBtnX, botInner.Y, testBtn, testBtnStyle)
			frame.RegisterClickHandler(cell.NewRect(testBtnX, botInner.Y, uint16(len([]rune(testBtn))), 1), func(_ backend.MouseEvent) {
				if l.OnOpenTestModal != nil {
					l.OnOpenTestModal()
				}
			})
		}

		rowOffset := uint16(1)
		if botInner.Height > 3 {
			relayInfo := "Active Relay: " + l.RelayURL
			if l.RelayURL == "" {
				relayInfo = "Active Relay: Official Public Server (Railway)"
			}
			buf.SetString(botInner.X+1, botInner.Y+1, relayInfo, cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg})
			rowOffset = 2
		}

		helpLines := []string{
			"• [R] Custom relay server & security settings (Click or press R)",
			"• [T] or [F4] Microphone & sound test panel",
			"• [Tab] or [Shift+Tab] Switch input field",
			"• [F2] / [C] Copy key • [F3] / [G] New key • [Esc] Exit",
		}
		for i, h := range helpLines {
			lineY := botInner.Y + rowOffset + uint16(i)
			if lineY < botInner.Y+botInner.Height {
				lineStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
				if i == 0 {
					lineStyle = cell.Style{Fg: theme.BorderFocused, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
				}
				buf.SetString(botInner.X+1, lineY, h, lineStyle)
			}
		}
	}
}
