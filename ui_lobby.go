package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
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
	ToastMsg         string
	ToastTimer       int
	ActiveInput      int // 0: Nickname, 1: RoomCode to Join, 2: Host / General
	IsConnecting     bool
	ConnectingTarget string

	// Action Callbacks
	OnStartHost     func()
	OnJoinRoom      func(code string)
	OnCancelJoin    func()
	OnCopyCode      func(code string)
	OnNewCode       func()
	OnOpenTestModal func()
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
	block := widgets.Block{
		Title:         " 3D STUDIO MICROPHONE (OBJ) ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: cell.NewColorRGB(0x00, 0xF5, 0xD4)},
		Style:         cell.Style{Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17)},
	}
	frame.RenderWidget(block, area)
	innerArea := block.Inner(area)

	buf := frame.Buffer
	for y := innerArea.Y; y < innerArea.Y+innerArea.Height; y++ {
		for x := innerArea.X; x < innerArea.X+innerArea.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x0A, 0x0E, 0x17)}})
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
	mainBlock := widgets.Block{
		Title:         " P2P ODA VE BAGLANTI (CROC ENGINE) ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: cell.NewColorRGB(0x6C, 0x5C, 0xE7)},
		Style:         cell.Style{Bg: cell.NewColorRGB(0x10, 0x14, 0x20)},
	}
	frame.RenderWidget(mainBlock, area)
	inner := mainBlock.Inner(area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}})
		}
	}

	vl := layout.NewFlexLayout(layout.Vertical, 0,
		layout.Fixed(5), // Nickname block
		layout.Fixed(6), // Host room block
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

	unfocusedBorder := cell.Style{Fg: cell.NewColorRGB(0x4A, 0x55, 0x68)}
	unfocusedBg := cell.Style{Bg: cell.NewColorRGB(0x10, 0x14, 0x20)}

	// 1. Nickname Block
	isNickFocused := (l.ActiveInput == 0)
	nickTitle := " [1] KULLANICI ADINIZ "
	nickBorderStyle := unfocusedBorder
	nickBgStyle := unfocusedBg
	if isNickFocused {
		nickTitle = " ► [1] KULLANICI ADINIZ (ODAKLI) ◄ "
		nickBorderStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
			Modifier: cell.ModifierBold,
		}
		nickBgStyle = cell.Style{Bg: cell.NewColorRGB(0x13, 0x1E, 0x28)}
	}

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
		Placeholder: "Takma adinizi girin...",
	}
	frame.RenderWidget(nickInput, nickInner)

	frame.RegisterClickHandler(nickArea, func(_ backend.MouseEvent) {
		l.ActiveInput = 0
	})

	// 2. Host Room Block
	isHostFocused := (l.ActiveInput == 2)
	hostTitle := " [2] ODA OLUSTUR (SEN HOST OL) "
	hostBorderStyle := unfocusedBorder
	hostBgStyle := unfocusedBg
	keyStyle := cell.Style{
		Fg: cell.NewColorRGB(0x88, 0x92, 0xB0),
		Bg: cell.NewColorRGB(0x22, 0x28, 0x34),
	}
	hostBtnStyle := cell.Style{
		Fg: cell.NewColorRGB(0x63, 0x6E, 0x72),
		Bg: nickBgStyle.Bg,
	}

	if isHostFocused {
		hostTitle = " ► [2] ODA OLUSTUR (SEN HOST OL) [SECILI] ◄ "
		hostBorderStyle = cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0xE6, 0x6D),
			Modifier: cell.ModifierBold,
		}
		hostBgStyle = cell.Style{Bg: cell.NewColorRGB(0x1A, 0x1D, 0x26)}
		keyStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0xFF, 0xE6, 0x6D),
			Modifier: cell.ModifierBold,
		}
		hostBtnStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
			Bg:       hostBgStyle.Bg,
			Modifier: cell.ModifierBold,
		}
	}

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

	codeLabel := "Oda Anahtariniz (Arkadasina Gonder):"
	codeLabelStyle := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: hostBgStyle.Bg}
	if isHostFocused {
		codeLabelStyle = cell.Style{Fg: cell.NewColorRGB(0xDF, 0xE6, 0xE9), Bg: hostBgStyle.Bg}
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

	hostBtns := "[Enter] Bu Odayi Ac   •   [F2] Kodu Kopyala   •   [F3] Yeni Kod"
	buf.SetString(hostInner.X, hostInner.Y+3, hostBtns, hostBtnStyle)

	frame.RegisterClickHandler(cell.NewRect(hostInner.X, hostInner.Y+3, 19, 1), func(_ backend.MouseEvent) {
		l.ActiveInput = 2
		if l.OnStartHost != nil {
			l.OnStartHost()
		}
	})
	frame.RegisterClickHandler(cell.NewRect(hostInner.X+22, hostInner.Y+3, 17, 1), func(_ backend.MouseEvent) {
		l.ActiveInput = 2
		if l.OnCopyCode != nil {
			l.OnCopyCode(l.CurrentCode)
		}
	})
	frame.RegisterClickHandler(cell.NewRect(hostInner.X+42, hostInner.Y+3, 14, 1), func(_ backend.MouseEvent) {
		l.ActiveInput = 2
		if l.OnNewCode != nil {
			l.OnNewCode()
		}
	})

	frame.RegisterClickHandler(hostArea, func(_ backend.MouseEvent) {
		l.ActiveInput = 2
	})

	// 3. Join Room Block
	isJoinFocused := (l.ActiveInput == 1)
	joinTitle := " [3] MEVCUT ODAYA KATIL "
	joinBorderStyle := unfocusedBorder
	joinBgStyle := unfocusedBg
	joinBtnStyle := cell.Style{
		Fg: cell.NewColorRGB(0x63, 0x6E, 0x72),
		Bg: joinBgStyle.Bg,
	}

	if l.IsConnecting {
		joinTitle = fmt.Sprintf(" ► [3] ODAYA BAGLANILIYOR (Host Dogrulaniyor: %s) ◄ ", l.ConnectingTarget)
		joinBorderStyle = cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0x9F, 0x43),
			Modifier: cell.ModifierBold,
		}
		joinBgStyle = cell.Style{Bg: cell.NewColorRGB(0x22, 0x1A, 0x14)}
		joinBtnStyle = cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0xE6, 0x6D),
			Bg:       joinBgStyle.Bg,
			Modifier: cell.ModifierBold,
		}
	} else if isJoinFocused {
		joinTitle = " ► [3] MEVCUT ODAYA KATIL (ODAKLI - Yapistirmak icin Ctrl+V) ◄ "
		joinBorderStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Modifier: cell.ModifierBold,
		}
		joinBgStyle = cell.Style{Bg: cell.NewColorRGB(0x11, 0x20, 0x24)}
		joinBtnStyle = cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Bg:       joinBgStyle.Bg,
			Modifier: cell.ModifierBold,
		}
	}

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

	joinLabel := "Arkadasinin Gonderdigi Anahtari Yapistir (Ctrl+V):"
	if l.IsConnecting {
		joinLabel = "Oda dogrulaniyor, host aranıyor ve E2EE baglantisi kuruluyor..."
	}
	joinLabelStyle := cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: joinBgStyle.Bg}
	if isJoinFocused || l.IsConnecting {
		joinLabelStyle = cell.Style{Fg: cell.NewColorRGB(0xDF, 0xE6, 0xE9), Bg: joinBgStyle.Bg}
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
		Placeholder: "orn: 7492-neon-falcon",
	}
	frame.RenderWidget(codeInput, joinInputRect)

	joinBtns := "[Enter] Girilen Odaya Baglan (Maks: 4 Kisi)"
	if l.IsConnecting {
		joinBtns = "⏳ [Bekleyin] Host ile baglanti kuruluyor...   •   [Esc] Iptal Et"
	}
	buf.SetString(joinInner.X, joinInner.Y+3, joinBtns, joinBtnStyle)

	frame.RegisterClickHandler(cell.NewRect(joinInner.X, joinInner.Y+3, uint16(len([]rune(joinBtns))), 1), func(_ backend.MouseEvent) {
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
			l.SetToast("Lutfen baglanmak icin bir oda anahtari girin")
		}
	})

	frame.RegisterClickHandler(joinArea, func(_ backend.MouseEvent) {
		l.ActiveInput = 1
	})

	// 4. Info and Help Block
	bottomArea := vSplits[3]
	botBlock := widgets.Block{
		Title:         " BILGI VE KISAYOLLAR ",
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: cell.NewColorRGB(0x3B, 0x42, 0x52)},
		Style:         cell.Style{Bg: cell.NewColorRGB(0x0E, 0x11, 0x1A)},
	}
	frame.RenderWidget(botBlock, bottomArea)
	botInner := botBlock.Inner(bottomArea)

	for y := botInner.Y; y < botInner.Y+botInner.Height; y++ {
		for x := botInner.X; x < botInner.X+botInner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: cell.NewColorRGB(0x13, 0x17, 0x22)}})
		}
	}

	if l.ToastMsg != "" {
		toastStyle := cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       cell.NewColorRGB(0x00, 0xFF, 0x88),
			Modifier: cell.ModifierBold,
		}
		buf.SetString(botInner.X+1, botInner.Y, "  "+l.ToastMsg+"  ", toastStyle)
	} else {
		testBtn := "[T] Mikrofon & Ses Test Paneli (Yanki / Giris Testi)"
		buf.SetString(botInner.X+1, botInner.Y, testBtn, cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0xF5, 0xD4),
			Bg:       cell.NewColorRGB(0x13, 0x17, 0x22),
			Modifier: cell.ModifierBold,
		})
		frame.RegisterClickHandler(cell.NewRect(botInner.X+1, botInner.Y, uint16(len([]rune(testBtn))), 1), func(_ backend.MouseEvent) {
			if l.OnOpenTestModal != nil {
				l.OnOpenTestModal()
			}
		})

		helpLines := []string{
			"• [T] veya [F4] Mikrofon test panelini ac (Kendi sesini dinle)",
			"• [Fare] Istediginiz bolume veya butona tiklayarak odaklanin",
			"• [Tab] veya [Shift+Tab] Alanlar arasinda gecis yap",
			"• [Ctrl+V] veya [Shift+Insert] Panodan anahtar yapistir",
			"• [F2] / [C] Kopyala • [F3] / [G] Yeni anahtar • [Esc] Cikis",
		}
		for i, h := range helpLines {
			if uint16(i+1) < botInner.Height {
				buf.SetString(botInner.X+1, botInner.Y+uint16(i+1), h, cell.Style{Fg: cell.NewColorRGB(0x88, 0x92, 0xB0), Bg: cell.NewColorRGB(0x13, 0x17, 0x22)})
			}
		}
	}
}
