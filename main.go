package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/thebanri/limoni/animation"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
	"github.com/thebanri/limoni-voice/screenshare"
)

type AppScreen int

const (
	ScreenLobby AppScreen = iota
	ScreenRoom
)

func printUsage() {
	fmt.Println(`🍋 Limoni Voice - Terminal-Native P2P Voice Chat & Screen Sharing

Usage:
  limoni-voice [flags]

Flags:
  --relay <url>         Custom WebSocket relay URL for self-hosted servers
                        Example: --relay ws://192.168.1.100:8080/ws
                        (Set to 'none' or 'off' to disable relay)
  --relay-token <token> Authentication token for password-protected relay servers
  --token <token>       Alias for --relay-token
  --lan, --lan-only     Force LAN-only offline mode (disables relay, direct P2P on local network)
  --offline             Alias for --lan
  --peer, --connect     Direct target peer IP/host for cross-subnet or VPN LAN P2P
                        Example: --peer 192.168.1.50:50000
  --version             Show version information
  --help, -h            Show this help message

Environment Variables:
  LIMONI_RELAY_URL      Override default WebSocket relay URL
  LIMONI_RELAY_TOKEN    Authentication token for protected relay servers
  LIMONI_LAN_ONLY       Set to 1 / true to enable LAN-only mode by default
  LIMONI_OFFLINE        Set to 1 / true to enable offline mode
  LIMONI_PEER           Set direct target peer IP/host

Examples:
  # Standard launch (connects to default public relay + LAN auto-discovery):
  limoni-voice

  # Force LAN-only direct P2P communication on local Wi-Fi / Ethernet:
  limoni-voice --lan

  # Connect directly to a specific LAN peer IP:
  limoni-voice --lan --peer 192.168.1.50

  # Connect using your self-hosted Docker relay server:
  limoni-voice --relay ws://192.168.1.100:8080/ws

  # Connect to a password-protected self-hosted relay:
  limoni-voice --relay wss://yourdomain.com/ws --relay-token mysecret123`)
}

func main() {
	var (
		flagRelay      = flag.String("relay", "", "Custom WebSocket relay URL (e.g. ws://192.168.1.100:8080/ws, or 'none' for LAN only)")
		flagRelayToken = flag.String("relay-token", "", "Authentication token for protected relay server (or set LIMONI_RELAY_TOKEN)")
		flagToken      = flag.String("token", "", "Alias for -relay-token")
		flagLAN        = flag.Bool("lan", false, "Force LAN-only offline mode (disables relay connection)")
		flagLANOnly    = flag.Bool("lan-only", false, "Alias for -lan")
		flagOffline    = flag.Bool("offline", false, "Alias for -lan")
		flagPeer       = flag.String("peer", "", "Direct target peer IP / host for LAN / VPN P2P (e.g. 192.168.1.50)")
		flagConnect    = flag.String("connect", "", "Alias for -peer")
		flagHelp       = flag.Bool("help", false, "Show help and usage instructions")
		flagVersion    = flag.Bool("version", false, "Show version information")
	)
	flag.CommandLine.Init(os.Args[0], flag.ContinueOnError)
	flag.CommandLine.SetOutput(os.Stdout)
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	if *flagHelp {
		printUsage()
		os.Exit(0)
	}

	if *flagVersion {
		fmt.Printf("Limoni Voice %s (Go 1.24+ | E2EE AES-256-GCM | P2P Full-Mesh)\n", AppVersion)
		os.Exit(0)
	}

	b := driver.NewBackend(os.Stdin, os.Stdout)
	if err := b.Setup(); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing terminal backend: %v\n", err)
		os.Exit(1)
	}
	defer b.Close()

	// Ensure alternate screen starts completely wiped
	_, _ = os.Stdout.WriteString("\x1b[2J\x1b[H")

	t, err := terminal.New(b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing terminal: %v\n", err)
		os.Exit(1)
	}

	b.StartEventLoop()

	randNum, _ := rand.Int(rand.Reader, big.NewInt(100000))
	localID := fmt.Sprintf("peer_%d_%d", time.Now().Unix()%10000, randNum.Int64())

	audio := NewAudioEngine()
	node := NewP2PNode(localID, "User", audio)

	// Load persistent configuration from settings.json
	cfg := LoadAppConfig()

	if *flagLAN || *flagLANOnly || *flagOffline {
		node.LanOnly = true
		node.RelayURL = ""
	} else if *flagRelay != "" {
		if strings.EqualFold(*flagRelay, "none") || strings.EqualFold(*flagRelay, "off") {
			node.LanOnly = true
			node.RelayURL = ""
		} else {
			node.RelayURL = *flagRelay
		}
	} else if cfg.RelayURL != "" {
		if strings.EqualFold(cfg.RelayURL, "none") || strings.EqualFold(cfg.RelayURL, "off") {
			node.LanOnly = true
			node.RelayURL = ""
		} else {
			node.RelayURL = cfg.RelayURL
		}
	}

	if *flagRelayToken != "" {
		node.RelayToken = *flagRelayToken
	} else if *flagToken != "" {
		node.RelayToken = *flagToken
	} else if cfg.RelayToken != "" {
		node.RelayToken = cfg.RelayToken
	}

	if *flagPeer != "" {
		node.SetTargetPeer(*flagPeer)
	} else if *flagConnect != "" {
		node.SetTargetPeer(*flagConnect)
	}

	if err := node.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting P2P node: %v\n", err)
		os.Exit(1)
	}

	lobby := NewLobbyView()
	room := NewRoomView()
	screenshare.LogCallback = func(msg string) {
		room.AddLog(msg)
	}
	currentScreen := ScreenLobby

	// Background auto-updater check
	go CheckAndUpdateAsync(func(msg string) {
		if currentScreen == ScreenLobby {
			lobby.SetToast(msg)
		} else {
			room.SetToast(msg)
		}
		room.AddLog(fmt.Sprintf("[UPDATE] %s", msg))
	})

	// Modal States & Animations
	showTestModal := false
	showLeaveModal := false
	showExitModal := false
	showScreenShareModal := false
	showDebugModal := false
	debugScrollOffset := 0
	var screenShareTargets []screenshare.WindowInfo
	selectedScreenShareIdx := 0

	showRelayModal := false
	relayDialogAnim := animation.NewFloat(0.0)
	relayURLInput := widgets.NewTextInputState()
	relayTokenInput := widgets.NewTextInputState()
	relayModalActiveField := 0 // 0: URL, 1: Token, 2: Save, 3: Reset, 4: Cancel
	relaySelStart := -1
	relaySelEnd := -1
	relaySelField := -1
	appStartTime := time.Now()
	var lastScreenArea cell.Rect

	exitDialogAnim := animation.NewFloat(0.0)
	leaveDialogAnim := animation.NewFloat(0.0)
	screenShareDialogAnim := animation.NewFloat(0.0)
	fileOfferDialogAnim := animation.NewFloat(0.0)

	var currentFileOffer *FileOffer
	var pendingFileOffers []*FileOffer
	var fileOfferMu sync.Mutex

	showNextFileOffer := func() {
		fileOfferMu.Lock()
		if len(pendingFileOffers) > 0 {
			currentFileOffer = pendingFileOffers[0]
			pendingFileOffers = pendingFileOffers[1:]
			fileOfferDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
		} else {
			currentFileOffer = nil
			fileOfferDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
		}
		fileOfferMu.Unlock()
		t.ForceFullRedraw()
	}

	enqueueFileOffer := func(offer *FileOffer) {
		fileOfferMu.Lock()
		pendingFileOffers = append(pendingFileOffers, offer)
		if currentFileOffer == nil {
			currentFileOffer = pendingFileOffers[0]
			pendingFileOffers = pendingFileOffers[1:]
			fileOfferDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
		}
		fileOfferMu.Unlock()
		audio.PlaySound(SoundChat)
		targetLabel := "file"
		if offer.IsCode {
			targetLabel = "code snippet"
		}
		if currentScreen == ScreenRoom {
			room.SetToast(fmt.Sprintf("📥 Incoming %s from %s: %s", targetLabel, offer.SenderNick, offer.FileName))
		} else {
			lobby.SetToast(fmt.Sprintf("📥 Incoming %s from %s: %s", targetLabel, offer.SenderNick, offer.FileName))
		}
		t.ForceFullRedraw()
	}

	acceptCurrentOffer := func(openInEditor bool) {
		fileOfferMu.Lock()
		offer := currentFileOffer
		fileOfferMu.Unlock()
		if offer == nil {
			return
		}
		savedPath, err := SaveAcceptedFile(offer)
		if err != nil {
			if currentScreen == ScreenRoom {
				room.SetToast(fmt.Sprintf("Save error: %v", err))
				room.AddLog(fmt.Sprintf("[FILE] Error saving '%s': %v", offer.FileName, err))
			} else {
				lobby.SetToast(fmt.Sprintf("Save error: %v", err))
			}
		} else {
			if offer.IsCode {
				if currentScreen == ScreenRoom {
					room.AddLog(fmt.Sprintf("[CODE] ✓ Accepted code snippet '%s' from %s (saved to %s)", offer.FileName, offer.SenderNick, savedPath))
					room.SetToast(fmt.Sprintf("✓ Code snippet saved: %s", offer.FileName))
				} else {
					lobby.SetToast(fmt.Sprintf("✓ Code snippet saved: %s", offer.FileName))
				}
				if openInEditor {
					if err := OpenInEditor(savedPath); err != nil {
						if currentScreen == ScreenRoom {
							room.SetToast(fmt.Sprintf("Could not open editor: %v", err))
						} else {
							lobby.SetToast(fmt.Sprintf("Could not open editor: %v", err))
						}
					}
				}
			} else {
				if currentScreen == ScreenRoom {
					room.AddLog(fmt.Sprintf("[FILE] ✓ Accepted file '%s' (%s) from %s (saved to %s)", offer.FileName, formatBytes(offer.FileSize), offer.SenderNick, savedPath))
					room.SetToast(fmt.Sprintf("✓ File saved to Downloads: %s", offer.FileName))
				} else {
					lobby.SetToast(fmt.Sprintf("✓ File saved to Downloads: %s", offer.FileName))
				}
			}
		}
		showNextFileOffer()
	}

	declineCurrentOffer := func() {
		fileOfferMu.Lock()
		offer := currentFileOffer
		fileOfferMu.Unlock()
		if offer != nil {
			if currentScreen == ScreenRoom {
				room.AddLog(fmt.Sprintf("[FILE] ✗ Declined file transfer '%s' from %s", offer.FileName, offer.SenderNick))
				room.SetToast(fmt.Sprintf("✗ Declined: %s", offer.FileName))
			} else {
				lobby.SetToast(fmt.Sprintf("✗ Declined: %s", offer.FileName))
			}
		}
		showNextFileOffer()
	}

	openTestModal := func() {
		audio.EnterTestMode()
		showTestModal = true
	}

	closeTestModal := func() {
		audio.LeaveTestMode()
		showTestModal = false
		t.ForceFullRedraw()
	}

	openExitModal := func() {
		showExitModal = true
		exitDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
		t.FocusManager().SetFocused("exit_app_dialog_btn_1")
	}

	closeExitModal := func() {
		showExitModal = false
		exitDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
		t.FocusManager().SetFocused("")
		t.ForceFullRedraw()
	}

	openLeaveModal := func() {
		showLeaveModal = true
		leaveDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
		t.FocusManager().SetFocused("leave_room_dialog_btn_1")
	}

	openDebugModal := func() {
		showDebugModal = true
		debugScrollOffset = 0
	}

	closeDebugModal := func() {
		showDebugModal = false
		t.ForceFullRedraw()
	}

	closeLeaveModal := func() {
		showLeaveModal = false
		leaveDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
		t.FocusManager().SetFocused("")
		t.ForceFullRedraw()
	}

	closeScreenShareModal := func() {
		showScreenShareModal = false
		screenShareDialogAnim.AnimateTo(0.0, 160*time.Millisecond, animation.EaseInCubic)
		t.ForceFullRedraw()
	}

	deleteSelectedRange := func(state *widgets.TextInputState, start, end int) {
		if state == nil || start < 0 || end < 0 || start == end {
			return
		}
		if start > end {
			start, end = end, start
		}
		if start > len(state.Text) {
			start = len(state.Text)
		}
		if end > len(state.Text) {
			end = len(state.Text)
		}
		state.Text = append(state.Text[:start], state.Text[end:]...)
		state.Cursor = start
	}

	insertStringAtCursor := func(state *widgets.TextInputState, s string) {
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

	relayPasteAction := func(field int) {
		clip := strings.TrimSpace(GetClipboardText())
		if clip == "" {
			return
		}
		var target *widgets.TextInputState
		toastMsg := "Pasted URL"
		if field == 0 {
			target = relayURLInput
		} else if field == 1 {
			target = relayTokenInput
			toastMsg = "Pasted token"
		}
		if target != nil {
			if relaySelStart != -1 && relaySelStart != relaySelEnd && relaySelField == field {
				deleteSelectedRange(target, relaySelStart, relaySelEnd)
			}
			insertStringAtCursor(target, clip)
			relaySelStart = -1
			relaySelEnd = -1
			relaySelField = -1
			relayModalActiveField = field
			if currentScreen == ScreenLobby {
				lobby.SetToast(toastMsg)
			} else {
				room.SetToast(toastMsg)
			}
		}
	}

	relayCopyAction := func(field int) {
		var target *widgets.TextInputState
		toastMsg := "Copied server URL"
		if field == 0 {
			target = relayURLInput
		} else if field == 1 {
			target = relayTokenInput
			toastMsg = "Copied token"
		}
		if target != nil {
			text := target.Value()
			if relaySelStart != -1 && relaySelStart != relaySelEnd && relaySelField == field {
				s, e := relaySelStart, relaySelEnd
				if s > e {
					s, e = e, s
				}
				if s < len(target.Text) && e <= len(target.Text) {
					text = string(target.Text[s:e])
				}
			}
			if text != "" {
				CopyToClipboard(text)
				if currentScreen == ScreenLobby {
					lobby.SetToast(toastMsg)
				} else {
					room.SetToast(toastMsg)
				}
			}
		}
	}

	relayClearAction := func(field int) {
		if field == 0 {
			relayURLInput.SetValue("")
		} else if field == 1 {
			relayTokenInput.SetValue("")
		}
		relaySelStart = -1
		relaySelEnd = -1
		relaySelField = -1
		relayModalActiveField = field
	}

	probeRelayStatus := func(u, tok string) {
		go func() {
			online, status := ProbeRelayServer(u, tok, 2500*time.Millisecond)
			lobby.RelayOnline = online
			lobby.RelayStatus = status
		}()
	}
	probeRelayStatus(node.RelayURL, node.RelayToken)

	openRelayModal := func() {
		showRelayModal = true
		relayURLInput.SetValue(node.RelayURL)
		relayTokenInput.SetValue(node.RelayToken)
		relayModalActiveField = 0
		relaySelStart = 0
		relaySelEnd = len(relayURLInput.Text)
		relaySelField = 0
		probeRelayStatus(node.RelayURL, node.RelayToken)
		relayDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
	}

	closeRelayModal := func() {
		showRelayModal = false
		relaySelStart = -1
		relaySelEnd = -1
		relaySelField = -1
		relayDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
		t.ForceFullRedraw()
	}

	startSelectedScreenShare := func(target screenshare.WindowInfo) {
		closeScreenShareModal()
		room.SetToast(fmt.Sprintf("🎬 Starting %s stream...", target.Title))
		go func() {
			opts := screenshare.DefaultBroadcastOptions()
			opts.WindowID = target.ID
			err := node.StartScreenShare("", 50100, opts)
			if err != nil {
				room.SetToast(fmt.Sprintf("Error: %v", err))
			} else {
				room.SetToast(fmt.Sprintf("%s sharing started (60 FPS)", target.Title))
			}
		}()
	}

	openScreenShareModal := func() {
		if node.IsSharingScreen {
			go func() {
				_ = node.StopScreenShare()
				room.SetToast("⏹️ Screen share stopped")
			}()
			return
		}

		screenShareTargets = screenshare.ListWindows()
		if len(screenShareTargets) == 0 {
			screenShareTargets = []screenshare.WindowInfo{
				{ID: "desktop", Title: "[Desktop] Entire Screen (Primary View)"},
			}
		}
		selectedScreenShareIdx = 0
		showScreenShareModal = true
		screenShareDialogAnim.AnimateTo(1.0, 200*time.Millisecond, animation.EaseOutCubic)
	}

	screenshare.LogCallback = func(msg string) {
		AddDebugLog("[SCREEN] " + msg)
	}

	node.OnLog = func(msg string) {
		AddDebugLog("[ROOM] " + msg)
		isUserEvent := strings.HasPrefix(msg, "[+]") || strings.HasPrefix(msg, "[-]") ||
			strings.HasPrefix(msg, "[HOST]") || strings.HasPrefix(msg, "[ERROR]") ||
			strings.Contains(msg, "joined") || strings.Contains(msg, "left")
		if isUserEvent {
			room.AddLog(msg)
		}
		if currentScreen == ScreenLobby {
			lobby.SetToast(msg)
		} else {
			if strings.HasPrefix(msg, "[WARN]") || strings.HasPrefix(msg, "[ERROR]") || strings.HasPrefix(msg, "⚠️") || strings.HasPrefix(msg, "❌") {
				room.SetToast(msg)
			}
		}
	}

	node.OnDebugLog = func(msg string) {
		AddDebugLog("[NET] " + msg)
	}

	node.OnPeerEvent = func(event string, peer *PeerInfo) {
		if event == "join" {
			audio.PlaySound(SoundJoin)
			room.AddLog(fmt.Sprintf("[+] %s joined the room.", peer.Nickname))
		} else if event == "leave" {
			audio.PlaySound(SoundLeave)
			room.AddLog(fmt.Sprintf("[-] %s left the room.", peer.Nickname))
		}
	}

	node.OnChatMessage = func(senderID string, nickname string, text string, ts time.Time) {
		audio.PlaySound(SoundChat)
		room.AddChatMessage(nickname, senderID, text, false, ts)
	}

	node.OnFileTransferProgress = func(transferID string, fileName string, transferred int64, total int64, speed float64, isUpload bool, done bool, err error) {
		if err != nil {
			room.SetToast(fmt.Sprintf("File error (%s): %v", fileName, err))
			room.AddLog(fmt.Sprintf("[FILE] Transfer error for %s: %v", fileName, err))
			return
		}
		pct := 0
		if total > 0 {
			pct = int(transferred * 100 / total)
		}
		speedKB := speed / 1024.0
		barLen := 10
		filled := pct * barLen / 100
		if filled > barLen {
			filled = barLen
		}
		bar := strings.Repeat("=", filled) + strings.Repeat(" ", barLen-filled)
		direction := "Downloading"
		if isUpload {
			direction = "Uploading"
		}
		if done {
			room.SetToast(fmt.Sprintf("✓ %s completed: %s", direction, fileName))
			room.AddLog(fmt.Sprintf("[FILE] %s '%s' completed (%s)", direction, fileName, formatBytes(total)))
		} else {
			room.SetToast(fmt.Sprintf("%s %s [%s] %d%% (%.1f KB/s)", direction, fileName, bar, pct, speedKB))
		}
	}

	node.OnFileOfferReceived = enqueueFileOffer

	node.OnFileReceived = func(transferID string, fileName string, filePath string, isCode bool, content string) {
		audio.PlaySound(SoundChat)
		var size int64
		if fi, err := os.Stat(filePath); err == nil {
			size = fi.Size()
		}
		if isCode {
			room.AddLog(fmt.Sprintf("[CODE] Received code snippet '%s' (%s) saved to %s", fileName, formatBytes(size), filePath))
			room.SetToast(fmt.Sprintf("Code snippet received: %s", fileName))
		} else {
			room.AddLog(fmt.Sprintf("[FILE] Received file '%s' (%s) saved to %s", fileName, formatBytes(size), filePath))
			room.SetToast(fmt.Sprintf("File received: %s", fileName))
		}
	}

	// Room Transition Helpers
	startHost := func() {
		nick := strings.TrimSpace(lobby.NickState.Value())
		if nick == "" {
			if len(lobby.CurrentCode) >= 4 {
				nick = "User_" + lobby.CurrentCode[:4]
			} else {
				nick = "User_Host"
			}
		}
		node.Nickname = nick
		node.HostRoom(lobby.CurrentCode)
		if lobby.IsPinProtected {
			pinVal := strings.TrimSpace(lobby.PinState.Value())
			if pinVal == "" {
				pinVal = "1234"
			}
			node.LockRoom(pinVal)
		}
		room = NewRoomView()
		currentScreen = ScreenRoom
		audio.PlaySound(SoundJoin)
	}

	joinRoom := func(code string) {
		cleanCode := NormalizeCode(code)
		if cleanCode == "" {
			lobby.SetToast("Please enter a valid room key")
			return
		}
		nick := strings.TrimSpace(lobby.NickState.Value())
		if nick == "" {
			nick = "User_" + lobby.CurrentCode[:4]
		}
		node.Nickname = nick
		lobby.IsConnecting = true
		lobby.ConnectingTarget = cleanCode
		lobby.SetToast(fmt.Sprintf("Searching for room '%s' and verifying host...", cleanCode))

		node.RequestJoinRoom(cleanCode, 15*time.Second,
			func(hostNick string) {
				lobby.IsConnecting = false
				room = NewRoomView()
				currentScreen = ScreenRoom
				audio.PlaySound(SoundJoin)
				room.AddLog(fmt.Sprintf("[+] Successfully joined room %s! (Host: %s)", cleanCode, hostNick))
				room.SetToast(fmt.Sprintf("Joined Room! Host: %s", hostNick))
			},
			func(reason string) {
				lobby.IsConnecting = false
				lobby.SetToast(fmt.Sprintf("❌ %s", reason))
			},
		)
	}

	cancelJoin := func() {
		node.CancelJoin()
		lobby.IsConnecting = false
		lobby.SetToast("Room search cancelled.")
	}

	leaveRoom := func() {
		audio.PlaySound(SoundLeave)
		node.LeaveRoom()
		lobby.CurrentCode = GenerateRoomCode()
		currentScreen = ScreenLobby
		closeLeaveModal()
	}

	// Wire up lobby action callbacks
	lobby.OnStartHost = startHost
	lobby.OnJoinRoom = joinRoom
	lobby.OnCancelJoin = cancelJoin
	lobby.OnCopyCode = func(code string) {
		CopyToClipboard(code)
		lobby.SetToast(fmt.Sprintf("Room key copied: %s", code))
	}
	lobby.OnNewCode = func() {
		lobby.CurrentCode = GenerateRoomCode()
		lobby.SetToast("New room key generated!")
	}
	lobby.OnOpenTestModal = openTestModal
	lobby.OnOpenRelayModal = openRelayModal
	lobby.RelayURL = node.RelayURL

	node.OnRoomLocked = func(isLocked bool, pin string) {
		if currentScreen == ScreenRoom {
			if isLocked {
				if pin != "" {
					if node.IsHost {
						room.SetToast(fmt.Sprintf("Room locked with PIN: %s", pin))
						room.AddLog(fmt.Sprintf("[ROOM] Room locked with PIN: %s (Host only)", pin))
					} else {
						room.SetToast("Room is locked with PIN")
						room.AddLog("[ROOM] Room is locked with PIN by host")
					}
				} else {
					room.SetToast("Room locked")
					room.AddLog("[ROOM] Room locked by host")
				}
			} else {
				room.SetToast("Room unlocked")
				room.AddLog("[ROOM] Room unlocked by host")
			}
		}
		t.ForceFullRedraw()
	}

	// Audio frame capture and sender loop
	audio.Start(func(rms float64, speaking bool, pcm []byte) {
		if currentScreen == ScreenRoom && !audio.InTestMode && !audio.Muted {
			if audio.InputMode == InputModeVoiceActivity || audio.IsTransmitting() {
				node.SendAudio(rms, speaking, pcm)
			}
		}
	})
	cleanExit := func() {
		node.Close()
		audio.Stop()
		b.Close()
		os.Exit(0)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	renderTicker := time.NewTicker(33 * time.Millisecond) // ~30 FPS
	defer renderTicker.Stop()

	lastTime := time.Now()

	for {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP || sig == syscall.SIGTERM {
				cleanExit()
			} else if sig == os.Interrupt {
				if showExitModal || showLeaveModal {
					cleanExit()
				} else if currentScreen == ScreenRoom {
					openLeaveModal()
				} else {
					openExitModal()
				}
			}
		case ev := <-b.Events():
			switch ev.Type {
			case driver.EventPaste:
				// Discard any residual bracketed paste sequences sent during terminal startup
				if time.Since(appStartTime) < 1200*time.Millisecond {
					continue
				}

				pasted := ev.Paste.Text
				if showRelayModal {
					cleanPasted := strings.TrimSpace(pasted)
					if cleanPasted != "" {
						var target *widgets.TextInputState
						toastMsg := "Pasted URL"
						if relayModalActiveField == 0 {
							target = relayURLInput
						} else if relayModalActiveField == 1 {
							target = relayTokenInput
							toastMsg = "Pasted token"
						}
						if target != nil {
							if relaySelStart != -1 && relaySelStart != relaySelEnd && relaySelField == relayModalActiveField {
								deleteSelectedRange(target, relaySelStart, relaySelEnd)
							}
							insertStringAtCursor(target, cleanPasted)
							relaySelStart = -1
							relaySelEnd = -1
							relaySelField = -1
							if currentScreen == ScreenLobby {
								lobby.SetToast(toastMsg)
							} else {
								room.SetToast(toastMsg)
							}
						}
					}
					continue
				}
				if currentScreen == ScreenLobby && pasted != "" && !showTestModal && !showExitModal {
					cleanPasted := strings.TrimSpace(pasted)
					if lobby.ActiveInput == 0 {
						lobby.NickState.SetValue(cleanPasted)
						lobby.SetToast("Username pasted")
					} else if lobby.ActiveInput == 1 {
						cleanCode := NormalizeCode(cleanPasted)
						if cleanCode != "" {
							lobby.CodeState.SetValue(cleanCode)
							lobby.SetToast(fmt.Sprintf("Room key pasted: %s", cleanCode))
						}
					}
				} else if currentScreen == ScreenRoom && pasted != "" && !showTestModal && !showLeaveModal && !showExitModal && !showScreenShareModal {
					room.SetChatFocused(true)
					for _, r := range pasted {
						if r == '\r' {
							continue
						}
						room.ChatInputState.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: r})
					}
				}

			case driver.EventKey:
				e := ev.Key

				if showRelayModal {
					// 1. Control Shortcuts (Ctrl+C, Ctrl+V, Ctrl+X, Ctrl+A, Ctrl+U)
					if e.Ctrl {
						switch e.Ch {
						case 'v', 'V':
							relayPasteAction(relayModalActiveField)
							continue
						case 'c', 'C':
							relayCopyAction(relayModalActiveField)
							continue
						case 'x', 'X':
							var target *widgets.TextInputState
							if relayModalActiveField == 0 {
								target = relayURLInput
							} else if relayModalActiveField == 1 {
								target = relayTokenInput
							}
							if target != nil {
								if relaySelStart != -1 && relaySelStart != relaySelEnd && relaySelField == relayModalActiveField {
									s, end := relaySelStart, relaySelEnd
									if s > end {
										s, end = end, s
									}
									if s < len(target.Text) && end <= len(target.Text) {
										CopyToClipboard(string(target.Text[s:end]))
										deleteSelectedRange(target, s, end)
										relaySelStart = -1
										relaySelEnd = -1
										relaySelField = -1
									}
								} else {
									CopyToClipboard(target.Value())
									target.SetValue("")
								}
								if currentScreen == ScreenLobby {
									lobby.SetToast("Cut to clipboard")
								} else {
									room.SetToast("Cut to clipboard")
								}
							}
							continue
						case 'a', 'A':
							if relayModalActiveField == 0 {
								relaySelField = 0
								relaySelStart = 0
								relaySelEnd = len(relayURLInput.Text)
								relayURLInput.Cursor = len(relayURLInput.Text)
							} else if relayModalActiveField == 1 {
								relaySelField = 1
								relaySelStart = 0
								relaySelEnd = len(relayTokenInput.Text)
								relayTokenInput.Cursor = len(relayTokenInput.Text)
							}
							continue
						case 'u', 'U':
							relayClearAction(relayModalActiveField)
							continue
						}
					}

					// 2. Shift+Insert Paste
					if e.Type == driver.KeyInsert && (e.Shift || e.Ctrl) {
						relayPasteAction(relayModalActiveField)
						continue
					}

					switch e.Type {
					case driver.KeyEsc:
						closeRelayModal()
					case driver.KeyTab:
						relaySelStart = -1
						relaySelEnd = -1
						relaySelField = -1
						if e.Shift {
							relayModalActiveField = (relayModalActiveField + 4) % 5
						} else {
							relayModalActiveField = (relayModalActiveField + 1) % 5
						}
					case driver.KeyArrowUp:
						relaySelStart = -1
						relaySelEnd = -1
						relaySelField = -1
						if relayModalActiveField > 0 {
							relayModalActiveField--
						}
					case driver.KeyArrowDown:
						relaySelStart = -1
						relaySelEnd = -1
						relaySelField = -1
						if relayModalActiveField < 4 {
							relayModalActiveField++
						}
					case driver.KeyArrowLeft:
						if relayModalActiveField > 2 {
							relayModalActiveField--
						} else {
							var target *widgets.TextInputState
							if relayModalActiveField == 0 {
								target = relayURLInput
							} else if relayModalActiveField == 1 {
								target = relayTokenInput
							}
							if target != nil {
								if e.Shift {
									if relaySelField != relayModalActiveField || relaySelStart == -1 {
										relaySelField = relayModalActiveField
										relaySelStart = target.Cursor
										relaySelEnd = target.Cursor
									}
									if target.Cursor > 0 {
										target.Cursor--
										relaySelEnd = target.Cursor
									}
								} else {
									relaySelStart = -1
									relaySelEnd = -1
									relaySelField = -1
									target.HandleKey(e)
								}
							}
						}
					case driver.KeyArrowRight:
						if relayModalActiveField >= 2 && relayModalActiveField < 4 {
							relayModalActiveField++
						} else {
							var target *widgets.TextInputState
							if relayModalActiveField == 0 {
								target = relayURLInput
							} else if relayModalActiveField == 1 {
								target = relayTokenInput
							}
							if target != nil {
								if e.Shift {
									if relaySelField != relayModalActiveField || relaySelStart == -1 {
										relaySelField = relayModalActiveField
										relaySelStart = target.Cursor
										relaySelEnd = target.Cursor
									}
									if target.Cursor < len(target.Text) {
										target.Cursor++
										relaySelEnd = target.Cursor
									}
								} else {
									relaySelStart = -1
									relaySelEnd = -1
									relaySelField = -1
									target.HandleKey(e)
								}
							}
						}
					case driver.KeyHome, driver.KeyEnd:
						var target *widgets.TextInputState
						if relayModalActiveField == 0 {
							target = relayURLInput
						} else if relayModalActiveField == 1 {
							target = relayTokenInput
						}
						if target != nil {
							if e.Shift {
								if relaySelField != relayModalActiveField || relaySelStart == -1 {
									relaySelField = relayModalActiveField
									relaySelStart = target.Cursor
									relaySelEnd = target.Cursor
								}
								target.HandleKey(e)
								relaySelEnd = target.Cursor
							} else {
								relaySelStart = -1
								relaySelEnd = -1
								relaySelField = -1
								target.HandleKey(e)
							}
						}
					case driver.KeySpace:
						var target *widgets.TextInputState
						if relayModalActiveField == 0 {
							target = relayURLInput
						} else if relayModalActiveField == 1 {
							target = relayTokenInput
						}
						if target != nil {
							if relaySelStart != -1 && relaySelStart != relaySelEnd && relaySelField == relayModalActiveField {
								deleteSelectedRange(target, relaySelStart, relaySelEnd)
								relaySelStart = -1
								relaySelEnd = -1
								relaySelField = -1
							}
							target.HandleKey(e)
						}
					case driver.KeyBackspace, driver.KeyDelete:
						var target *widgets.TextInputState
						if relayModalActiveField == 0 {
							target = relayURLInput
						} else if relayModalActiveField == 1 {
							target = relayTokenInput
						}
						if target != nil {
							if relaySelStart != -1 && relaySelStart != relaySelEnd && relaySelField == relayModalActiveField {
								deleteSelectedRange(target, relaySelStart, relaySelEnd)
								relaySelStart = -1
								relaySelEnd = -1
								relaySelField = -1
							} else {
								target.HandleKey(e)
							}
						}
					case driver.KeyEnter:
						if relayModalActiveField == 2 || relayModalActiveField == 0 || relayModalActiveField == 1 {
							newURL := strings.TrimSpace(relayURLInput.Value())
							newToken := strings.TrimSpace(relayTokenInput.Value())
							node.UpdateRelaySettings(newURL, newToken)
							_ = SaveAppConfig(AppConfig{RelayURL: newURL, RelayToken: newToken})
							probeRelayStatus(newURL, newToken)
							if currentScreen == ScreenLobby {
								lobby.RelayURL = newURL
								lobby.SetToast("Relay server settings saved!")
							} else {
								room.SetToast("Relay server settings saved!")
							}
							closeRelayModal()
						} else if relayModalActiveField == 3 {
							node.UpdateRelaySettings(DefaultRelayURL, "")
							_ = ResetAppConfig()
							probeRelayStatus(DefaultRelayURL, "")
							if currentScreen == ScreenLobby {
								lobby.RelayURL = DefaultRelayURL
								lobby.SetToast("Reset to official default relay server!")
							} else {
								room.SetToast("Reset to official default relay server!")
							}
							closeRelayModal()
						} else if relayModalActiveField == 4 {
							closeRelayModal()
						}
					default:
						var target *widgets.TextInputState
						if relayModalActiveField == 0 {
							target = relayURLInput
						} else if relayModalActiveField == 1 {
							target = relayTokenInput
						}
						if target != nil {
							if relaySelStart != -1 && relaySelStart != relaySelEnd && relaySelField == relayModalActiveField {
								deleteSelectedRange(target, relaySelStart, relaySelEnd)
								relaySelStart = -1
								relaySelEnd = -1
								relaySelField = -1
							}
							target.HandleKey(e)
						}
					}
					continue
				}

				// Ctrl+C: If text is selected in chat, copy it! Otherwise trigger exit/leave prompt
				if e.Ctrl && (e.Ch == 'c' || e.Ch == 'C') {
					if currentScreen == ScreenRoom {
						room.mu.Lock()
						selText := room.SelectedText
						selActive := room.SelectionActive
						room.mu.Unlock()
						if selActive && selText != "" {
							CopyToClipboard(selText)
							previewStr := selText
							if len([]rune(previewStr)) > 30 {
								previewStr = string([]rune(previewStr)[:30]) + "…"
							}
							room.SetToast(fmt.Sprintf("✓ Copied: %s", previewStr))
							continue
						}
					}
					if showExitModal || showLeaveModal {
						cleanExit()
					}
					if currentScreen == ScreenRoom {
						openLeaveModal()
					} else {
						openExitModal()
					}
					continue
				}

				// Ctrl+V in active room chat
				if e.Ctrl && (e.Ch == 'v' || e.Ch == 'V') {
					if currentScreen == ScreenRoom && !showTestModal && !showLeaveModal && !showExitModal && !showScreenShareModal && !showDebugModal {
						room.SetChatFocused(true)
						clipText := GetClipboardText()
						for _, r := range clipText {
							if r == '\r' {
								continue
							}
							room.ChatInputState.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: r})
						}
						continue
					}
				}

				// Dedicated Global Debug Modal Key (F12)
				if e.Type == driver.KeyF12 {
					if showDebugModal {
						closeDebugModal()
					} else {
						openDebugModal()
					}
					continue
				}

				if showDebugModal {
					switch e.Type {
					case driver.KeyEsc:
						closeDebugModal()
					case driver.KeyRune:
						if e.Ch == 'c' || e.Ch == 'C' {
							allLogs := GetAllDebugLogsText()
							CopyToClipboard(allLogs)
							if currentScreen == ScreenLobby {
								lobby.SetToast("Copied all debug logs to clipboard")
							} else {
								room.SetToast("Copied all debug logs to clipboard")
							}
						} else if e.Ch == 'x' || e.Ch == 'X' {
							ClearDebugLogs()
						}
					case driver.KeyDelete:
						ClearDebugLogs()
					case driver.KeyArrowUp:
						debugScrollOffset++
					case driver.KeyArrowDown:
						if debugScrollOffset > 0 {
							debugScrollOffset--
						}
					case driver.KeyPageUp:
						debugScrollOffset += 10
					case driver.KeyPageDown:
						if debugScrollOffset >= 10 {
							debugScrollOffset -= 10
						} else {
							debugScrollOffset = 0
						}
					case driver.KeyHome:
						debugScrollOffset = len(GetDebugLogs())
					case driver.KeyEnd:
						debugScrollOffset = 0
					}
					continue
				}

				// --- 1. Handle Active Modals First ---
				if currentFileOffer != nil {
					switch e.Type {
					case driver.KeyEnter:
						acceptCurrentOffer(false)
					case driver.KeyEsc:
						declineCurrentOffer()
					case driver.KeyRune:
						if e.Ch == 'y' || e.Ch == 'Y' {
							acceptCurrentOffer(false)
						} else if e.Ch == 'n' || e.Ch == 'N' {
							declineCurrentOffer()
						} else if (e.Ch == 'o' || e.Ch == 'O') && currentFileOffer.IsCode {
							acceptCurrentOffer(true)
						}
					}
					continue
				}



				if showExitModal {
					focused := t.FocusManager().Focused()
					switch e.Type {
					case driver.KeyTab:
						if e.Shift {
							t.FocusManager().Prev()
						} else {
							t.FocusManager().Next()
						}
					case driver.KeyArrowLeft:
						t.FocusManager().Prev()
					case driver.KeyArrowRight:
						t.FocusManager().Next()
					case driver.KeyEnter, driver.KeySpace:
						if focused == "exit_app_dialog_btn_0" {
							cleanExit()
						} else {
							closeExitModal()
						}
					case driver.KeyEsc:
						closeExitModal()
					case driver.KeyRune:
						if e.Ch == 'e' || e.Ch == 'E' || e.Ch == 'y' || e.Ch == 'Y' {
							cleanExit()
						} else if e.Ch == 'h' || e.Ch == 'H' || e.Ch == 'n' || e.Ch == 'N' {
							closeExitModal()
						}
					}
					continue
				}

				if showLeaveModal {
					focused := t.FocusManager().Focused()
					switch e.Type {
					case driver.KeyTab:
						if e.Shift {
							t.FocusManager().Prev()
						} else {
							t.FocusManager().Next()
						}
					case driver.KeyArrowLeft:
						t.FocusManager().Prev()
					case driver.KeyArrowRight:
						t.FocusManager().Next()
					case driver.KeyEnter, driver.KeySpace:
						if focused == "leave_room_dialog_btn_0" {
							leaveRoom()
						} else {
							closeLeaveModal()
						}
					case driver.KeyEsc:
						closeLeaveModal()
					case driver.KeyRune:
						if e.Ch == 'e' || e.Ch == 'E' || e.Ch == 'y' || e.Ch == 'Y' {
							leaveRoom()
						} else if e.Ch == 'h' || e.Ch == 'H' || e.Ch == 'n' || e.Ch == 'N' {
							closeLeaveModal()
						}
					}
					continue
				}

				if showTestModal {
					if audio.PTTListeningKey {
						switch e.Type {
						case driver.KeyEsc:
							audio.mu.Lock()
							audio.PTTListeningKey = false
							audio.mu.Unlock()
						case driver.KeySpace:
							audio.SetPTTKey(' ', "Space")
						case driver.KeyEnter:
							audio.SetPTTKey('\n', "Enter")
						case driver.KeyTab:
							audio.SetPTTKey('\t', "Tab")
						case driver.KeyRune:
							ch := unicode.ToLower(e.Ch)
							audio.SetPTTKey(ch, strings.ToUpper(string(e.Ch)))
						}
						continue
					}

					switch e.Type {
					case driver.KeyEsc:
						closeTestModal()
					case driver.KeySpace:
						if audio.InputMode == InputModePushToTalk && audio.PTTKey == ' ' {
							audio.PulsePTT(350 * time.Millisecond)
						} else if audio.InputMode == InputModeVoiceActivity {
							audio.ToggleLoopback()
						}
					case driver.KeyArrowLeft:
						audio.CycleInputDevice(-1)
					case driver.KeyArrowRight:
						audio.CycleInputDevice(1)
					case driver.KeyRune:
						if audio.InputMode == InputModePushToTalk && unicode.ToLower(e.Ch) == unicode.ToLower(audio.PTTKey) {
							audio.PulsePTT(350 * time.Millisecond)
							continue
						}
						switch e.Ch {
						case 'k', 'K':
							if audio.InputMode == InputModePushToTalk {
								audio.mu.Lock()
								audio.PTTListeningKey = true
								audio.mu.Unlock()
							}
						case 'p', 'P':
							audio.CycleInputMode()
						case ' ':
							if audio.InputMode == InputModePushToTalk && audio.PTTKey == ' ' {
								audio.PulsePTT(350 * time.Millisecond)
							} else if audio.InputMode == InputModeVoiceActivity {
								audio.ToggleLoopback()
							}
						case 'l', 'L':
							audio.ToggleLoopback()
						case 'n', 'N':
							audio.CycleSuppressionMode()
						case 'm', 'M':
							isMuted := audio.ToggleMute()
							if node != nil {
								node.SendMuteState(isMuted)
							}
						case 'd', 'D':
							isDeaf := audio.ToggleDeafen()
							if node != nil {
								node.SendDeafenState(isDeaf)
								node.SendMuteState(audio.Muted)
							}
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
					continue
				}

				if showScreenShareModal {
					switch e.Type {
					case driver.KeyEsc:
						closeScreenShareModal()
					case driver.KeyArrowUp:
						if selectedScreenShareIdx > 0 {
							selectedScreenShareIdx--
						}
					case driver.KeyArrowDown:
						if selectedScreenShareIdx < len(screenShareTargets)-1 {
							selectedScreenShareIdx++
						}
					case driver.KeyEnter, driver.KeySpace:
						if selectedScreenShareIdx >= 0 && selectedScreenShareIdx < len(screenShareTargets) {
							startSelectedScreenShare(screenShareTargets[selectedScreenShareIdx])
						} else {
							closeScreenShareModal()
						}
					}
					continue
				}

				// Dedicated Global Test Modal Key (F4)
				if e.Type == driver.KeyF4 {
					openTestModal()
					continue
				}

				// Dedicated Global Relay Settings Modal Key (F5)
				if e.Type == driver.KeyF5 {
					openRelayModal()
					continue
				}

				// --- 2. Screen: Lobby Key Handling ---
				if currentScreen == ScreenLobby {
					if e.Type == driver.KeyF2 {
						CopyToClipboard(lobby.CurrentCode)
						lobby.SetToast(fmt.Sprintf("Room key copied: %s", lobby.CurrentCode))
						continue
					}
					if e.Type == driver.KeyF3 {
						lobby.CurrentCode = GenerateRoomCode()
						lobby.SetToast(fmt.Sprintf("New room key generated: %s", lobby.CurrentCode))
						continue
					}

					// Check for Ctrl+V / Paste shortcut
					if e.Ctrl && (e.Ch == 'v' || e.Ch == 'V') {
						clipText := GetClipboardText()
						if clipText != "" {
							if lobby.ActiveInput == 0 {
								lobby.NickState.SetValue(clipText)
								lobby.SetToast("Username pasted")
							} else if lobby.ActiveInput == 1 {
								cleanCode := NormalizeCode(clipText)
								lobby.CodeState.SetValue(cleanCode)
								lobby.SetToast(fmt.Sprintf("Room key pasted: %s", cleanCode))
							}
						} else {
							lobby.SetToast("Clipboard empty or unreadable")
						}
						continue
					}

					// Cancel connecting on Esc
					if lobby.IsConnecting && e.Type == driver.KeyEsc {
						cancelJoin()
						continue
					}

					// Tab Navigation
					if e.Type == driver.KeyTab {
						numInputs := 3
						if lobby.IsPinProtected {
							numInputs = 4
						}
						if e.Shift {
							lobby.ActiveInput = (lobby.ActiveInput + numInputs - 1) % numInputs
						} else {
							lobby.ActiveInput = (lobby.ActiveInput + 1) % numInputs
						}
						continue
					}

					// Enter key
					if e.Type == driver.KeyEnter {
						joinCode := NormalizeCode(lobby.CodeState.Value())
						if lobby.ActiveInput == 1 && joinCode != "" {
							joinRoom(joinCode)
						} else {
							startHost()
						}
						continue
					}

					// Input Routing based on ActiveInput Focus (Supports Left, Right, Home, End, Backspace, Delete)
					switch lobby.ActiveInput {
					case 0: // Nickname Input Focused
						if e.Type == driver.KeyEsc {
							lobby.ActiveInput = 2
						} else {
							lobby.NickState.HandleKey(e)
						}

					case 1: // Join Room Code Input Focused (Full cursor navigation & editing)
						if e.Type == driver.KeyEsc {
							lobby.ActiveInput = 2
						} else {
							lobby.CodeState.HandleKey(e)
						}

					case 2: // Host / General Section
						switch e.Type {
						case driver.KeyEsc:
							openExitModal()
						case driver.KeyRune:
							switch e.Ch {
							case '1':
								lobby.ActiveInput = 0
							case '2':
								startHost()
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
									lobby.ActiveInput = 2
									lobby.SetToast("PIN Protection Disabled")
								}
							case 'c', 'C':
								CopyToClipboard(lobby.CurrentCode)
								lobby.SetToast(fmt.Sprintf("Room key copied: %s", lobby.CurrentCode))
							case 'g', 'G':
								lobby.CurrentCode = GenerateRoomCode()
								lobby.SetToast("New room key generated!")
							case 'm', 'M':
								lobby.Cycle3DMode()
							case ' ':
								lobby.AutoRotate = !lobby.AutoRotate
								if lobby.AutoRotate {
									lobby.SetToast("3D Auto-Rotate: Enabled")
								} else {
									lobby.SetToast("3D Auto-Rotate: Paused")
								}
							case 't', 'T':
								openTestModal()
							case 'r', 'R':
								openRelayModal()
							case 'q', 'Q':
								openExitModal()
							}
						}

					case 3: // Host PIN Input Focused
						if e.Type == driver.KeyEsc {
							lobby.ActiveInput = 2
						} else if e.Type == driver.KeyEnter {
							startHost()
						} else {
							lobby.PinState.HandleKey(e)
						}
					}

				} else {
					// --- 3. Screen: Room Key Handling ---
					if room.IsChatFocused {
						switch e.Type {
						case driver.KeyEsc:
							room.SetChatFocused(false)
						case driver.KeyEnter:
							if e.Shift || e.Alt || e.Ctrl {
								room.ChatInputState.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: '\n'})
							} else {
								val := room.ChatInputState.Value()
								if strings.HasSuffix(val, `\`) && !strings.HasSuffix(val, `\\`) {
									// Trailing backslash line continuation: continue on new line!
									trimmed := strings.TrimSuffix(val, `\`)
									room.ChatInputState.SetValue(trimmed + "\n")
								} else {
									if strings.HasSuffix(val, `\\`) {
										room.ChatInputState.SetValue(strings.TrimSuffix(val, `\`))
									}
									if strings.TrimSpace(room.ChatInputState.Value()) != "" {
										room.SendCurrentChat()
									} else {
										room.SetChatFocused(false)
									}
								}
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
						continue
					}

					switch e.Type {
					case driver.KeyEnter:
						room.SetChatFocused(true)

					case driver.KeyTab:
						room.SetChatFocused(true)

					case driver.KeyPageUp, driver.KeyArrowUp:
						room.ScrollChat(1)

					case driver.KeyPageDown, driver.KeyArrowDown:
						room.ScrollChat(-1)

					case driver.KeyEsc:
						if node.IsWatchingScreen {
							_ = node.StopWatchingScreen()
							room.SetToast("Screen viewer closed")
						} else if node.IsSharingScreen {
							_ = node.StopScreenShare()
							room.SetToast("Screen share stopped")
						} else {
							openLeaveModal()
						}

					case driver.KeySpace:
						if audio.InputMode == InputModePushToTalk && audio.PTTKey == ' ' {
							audio.PulsePTT(350 * time.Millisecond)
						}

					case driver.KeyF2:
						CopyToClipboard(node.RoomCode)
						room.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))

					case driver.KeyRune:
						if e.Ch == '/' {
							room.SetChatFocused(true)
							continue
						}

						if audio.InputMode == InputModePushToTalk && unicode.ToLower(e.Ch) == unicode.ToLower(audio.PTTKey) {
							audio.PulsePTT(350 * time.Millisecond)
							continue
						}

						switch e.Ch {
						case 'p', 'P':
							m := audio.CycleInputMode()
							if m == InputModePushToTalk {
								room.SetToast(fmt.Sprintf("Mode: Push-to-Talk (Hold %s to talk)", audio.GetPTTKeyName()))
							} else {
								room.SetToast("Mode: Voice Activity (Always on / VAD)")
							}

						case ' ':
							if audio.InputMode == InputModePushToTalk && audio.PTTKey == ' ' {
								audio.PulsePTT(350 * time.Millisecond)
							}

						case 't', 'T':
							openTestModal()

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
							openScreenShareModal()

						case 'w', 'W':
							if node.IsWatchingScreen {
								_ = node.StopWatchingScreen()
								room.SetToast("Screen viewer closed")
							} else {
								peers := node.GetPeersList()
								var streamingPeers []*PeerInfo
								for _, p := range peers {
									if p.IsSharingScreen {
										streamingPeers = append(streamingPeers, p)
									}
								}
								if len(streamingPeers) > 0 {
									target := streamingPeers[0]
									port := target.VideoPort
									if port <= 0 {
										port = 50100
									}
									opts := screenshare.ReceiverOptions{
										WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (HD 60 FPS)", target.Nickname),
									}
									room.SetToast(fmt.Sprintf("🎬 Starting %s stream...", target.Nickname))
									go func() {
										err := node.StartWatchingScreen(target.ID, port, opts)
										if err != nil {
											room.SetToast(fmt.Sprintf("Error: %v", err))
										} else {
											room.SetToast(fmt.Sprintf("%s stream opened (HD 60 FPS)", target.Nickname))
										}
									}()
								} else {
									room.SetToast("No active screen share in the room")
								}
							}

						case '+', '=':
							gain := audio.AdjustGain(0.1)
							room.SetToast(fmt.Sprintf("Mic Volume: %.0f%%", gain*100))

						case '-', '_':
							gain := audio.AdjustGain(-0.1)
							room.SetToast(fmt.Sprintf("Mic Volume: %.0f%%", gain*100))

						case 'c', 'C':
							CopyToClipboard(node.RoomCode)
							room.SetToast(fmt.Sprintf("Room Code Copied: %s", node.RoomCode))

						case 'q', 'Q':
							openLeaveModal()
						}
					}
				}

			case driver.EventMouse:
				if showRelayModal {
					modalW, modalH := uint16(72), uint16(15)
					screenArea := lastScreenArea
					if screenArea.Width == 0 || screenArea.Height == 0 {
						w, h, _ := b.Size()
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
					urlInputRect := cell.NewRect(inner.X+1, inner.Y+3, inner.Width-2, 1)
					tokenInputRect := cell.NewRect(inner.X+1, inner.Y+6, inner.Width-2, 1)

					if ev.Mouse.Button == driver.MouseLeft {
						if urlInputRect.Contains(ev.Mouse.X, ev.Mouse.Y) {
							visW := int(urlInputRect.Width)
							startOffset := 0
							if relayURLInput.Cursor >= visW {
								startOffset = relayURLInput.Cursor - visW + 1
							}
							col := startOffset + int(ev.Mouse.X) - int(urlInputRect.X)
							if col < 0 {
								col = 0
							}
							if col > len(relayURLInput.Text) {
								col = len(relayURLInput.Text)
							}
							relayModalActiveField = 0
							relayURLInput.Cursor = col
							if !ev.Mouse.Drag {
								relaySelField = 0
								relaySelStart = col
								relaySelEnd = col
							} else {
								relaySelEnd = col
							}
						} else if tokenInputRect.Contains(ev.Mouse.X, ev.Mouse.Y) {
							visW := int(tokenInputRect.Width)
							startOffset := 0
							if relayTokenInput.Cursor >= visW {
								startOffset = relayTokenInput.Cursor - visW + 1
							}
							col := startOffset + int(ev.Mouse.X) - int(tokenInputRect.X)
							if col < 0 {
								col = 0
							}
							if col > len(relayTokenInput.Text) {
								col = len(relayTokenInput.Text)
							}
							relayModalActiveField = 1
							relayTokenInput.Cursor = col
							if !ev.Mouse.Drag {
								relaySelField = 1
								relaySelStart = col
								relaySelEnd = col
							} else {
								relaySelEnd = col
							}
						}
					} else if ev.Mouse.Button == driver.MouseRight {
						clip := strings.TrimSpace(GetClipboardText())
						if clip != "" {
							if urlInputRect.Contains(ev.Mouse.X, ev.Mouse.Y) {
								relayModalActiveField = 0
								if relaySelStart != -1 && relaySelStart != relaySelEnd && relaySelField == 0 {
									deleteSelectedRange(relayURLInput, relaySelStart, relaySelEnd)
								}
								insertStringAtCursor(relayURLInput, clip)
								relaySelStart = -1
								relaySelEnd = -1
								relaySelField = -1
							} else if tokenInputRect.Contains(ev.Mouse.X, ev.Mouse.Y) {
								relayModalActiveField = 1
								if relaySelStart != -1 && relaySelStart != relaySelEnd && relaySelField == 1 {
									deleteSelectedRange(relayTokenInput, relaySelStart, relaySelEnd)
								}
								insertStringAtCursor(relayTokenInput, clip)
								relaySelStart = -1
								relaySelEnd = -1
								relaySelField = -1
							}
						}
					}
				}

				if currentScreen == ScreenRoom && !showTestModal && !showLeaveModal && !showExitModal && !showScreenShareModal && !showDebugModal {
					if ev.Mouse.Button == driver.MouseLeft && !ev.Mouse.Drag {
						room.mu.Lock()
						wasChatFocused := room.IsChatFocused
						lastLog := room.LastLogArea
						room.mu.Unlock()

						if wasChatFocused && !lastLog.Contains(ev.Mouse.X, ev.Mouse.Y) {
							room.mu.Lock()
							room.IsChatFocused = false
							room.mu.Unlock()
						}
					}
				}

				handled := t.RouteMouseEvent(ev.Mouse)
				if !handled {
					if showDebugModal {
						if ev.Mouse.Button == driver.MouseScrollUp {
							debugScrollOffset++
						} else if ev.Mouse.Button == driver.MouseScrollDown {
							if debugScrollOffset > 0 {
								debugScrollOffset--
							}
						}
					} else if currentScreen == ScreenLobby && !showTestModal && !showExitModal {
						if ev.Mouse.Button == driver.MouseLeft {
							if ev.Mouse.Drag {
								if lobby.DragActive {
									dx := int(ev.Mouse.X) - lobby.LastDragX
									dy := int(ev.Mouse.Y) - lobby.LastDragY
									lobby.RotY += float64(dx) * 1.6
									lobby.RotX += float64(dy) * 1.6
								}
								lobby.LastDragX = int(ev.Mouse.X)
								lobby.LastDragY = int(ev.Mouse.Y)
								lobby.DragActive = true
							} else {
								lobby.DragActive = false
							}
						} else if ev.Mouse.Button == driver.MouseNone {
							lobby.DragActive = false
						} else if ev.Mouse.Button == driver.MouseScrollUp {
							if lobby.Scale < 12.0 {
								lobby.Scale += 0.3
							}
						} else if ev.Mouse.Button == driver.MouseScrollDown {
							if lobby.Scale > 1.5 {
								lobby.Scale -= 0.3
							}
						}
					} else if currentScreen == ScreenRoom && !showTestModal && !showLeaveModal && !showExitModal && !showScreenShareModal {
						room.mu.Lock()
						lastLog := room.LastLogArea
						isDragging := room.SelectionDragging
						room.mu.Unlock()

						inChatLog := lastLog.Contains(ev.Mouse.X, ev.Mouse.Y)
						if ev.Mouse.Button == driver.MouseLeft {
							if ev.Mouse.Drag {
								if inChatLog || isDragging {
									room.HandleMouseDrag(ev.Mouse.X, ev.Mouse.Y)
								}
							} else {
								if inChatLog {
									// Immediate one-click copy on message/copy elements!
									if !room.HandleChatClick(ev.Mouse.X, ev.Mouse.Y) {
										room.HandleMousePress(ev.Mouse.X, ev.Mouse.Y)
									}
								} else {
									room.ClearSelection()
								}
							}
						} else if ev.Mouse.Button == driver.MouseRelease || ev.Mouse.Button == driver.MouseNone {
							if isDragging {
								copied := room.HandleMouseRelease(ev.Mouse.X, ev.Mouse.Y)
								if copied != "" {
									CopyToClipboard(copied)
									previewStr := copied
									if len([]rune(previewStr)) > 30 {
										previewStr = string([]rune(previewStr)[:30]) + "…"
									}
									room.SetToast(fmt.Sprintf("✓ Copied: %s", previewStr))
								}
							}
						} else if ev.Mouse.Button == driver.MouseScrollUp {
							room.ScrollChat(1)
						} else if ev.Mouse.Button == driver.MouseScrollDown {
							room.ScrollChat(-1)
						}
					}
				}
			}

		case now := <-renderTicker.C:
			dt := float64(now.Sub(lastTime).Milliseconds())
			lastTime = now

			exitDialogAnim.Update(now)
			leaveDialogAnim.Update(now)
			screenShareDialogAnim.Update(now)
			fileOfferDialogAnim.Update(now)
			relayDialogAnim.Update(now)

			exitProg := exitDialogAnim.Value()
			if exitProg <= 0.001 && !exitDialogAnim.IsAnimating() {
				showExitModal = false
			}

			leaveProg := leaveDialogAnim.Value()
			if leaveProg <= 0.001 && !leaveDialogAnim.IsAnimating() {
				showLeaveModal = false
			}

			screenShareProg := screenShareDialogAnim.Value()
			if screenShareProg <= 0.001 && !screenShareDialogAnim.IsAnimating() {
				showScreenShareModal = false
			}

			relayProg := relayDialogAnim.Value()
			if relayProg <= 0.001 && !relayDialogAnim.IsAnimating() {
				showRelayModal = false
			}

			fileOfferProg := fileOfferDialogAnim.Value()

			if currentScreen == ScreenLobby {
				if showRelayModal {
					if relayModalActiveField == 0 {
						t.FocusManager().SetFocused("relay_url_input")
					} else if relayModalActiveField == 1 {
						t.FocusManager().SetFocused("relay_token_input")
					} else {
						t.FocusManager().SetFocused("")
					}
				} else if !showTestModal && !showExitModal {
					switch lobby.ActiveInput {
					case 0:
						t.FocusManager().SetFocused("nick_input")
					case 1:
						t.FocusManager().SetFocused("roomcode_input")
					case 2:
						t.FocusManager().SetFocused("")
					}
				}
				lobby.Update(dt)
				_ = t.Draw(func(f *terminal.Frame) {
					lastScreenArea = f.Area()
					lobby.Render(f, f.Area())

					if showDebugModal {
						DrawDebugModal(f, f.Area(), debugScrollOffset, closeDebugModal, ClearDebugLogs, func() {
							CopyToClipboard(GetAllDebugLogsText())
							lobby.SetToast("Copied debug logs to clipboard")
						})
					} else if showRelayModal || relayProg > 0.001 {
						DrawRelayModal(
							f, f.Area(), relayProg,
							node.RelayURL, node.RelayToken,
							relayURLInput, relayTokenInput,
							relayModalActiveField,
							relaySelField, relaySelStart, relaySelEnd,
							func(field int) { relayModalActiveField = field },
							func(newURL, newToken string) {
								newURL = strings.TrimSpace(newURL)
								newToken = strings.TrimSpace(newToken)
								node.UpdateRelaySettings(newURL, newToken)
								_ = SaveAppConfig(AppConfig{RelayURL: newURL, RelayToken: newToken})
								lobby.RelayURL = newURL
								probeRelayStatus(newURL, newToken)
								lobby.SetToast("Relay server settings saved!")
								closeRelayModal()
							},
							func() {
								node.UpdateRelaySettings(DefaultRelayURL, "")
								_ = ResetAppConfig()
								lobby.RelayURL = DefaultRelayURL
								probeRelayStatus(DefaultRelayURL, "")
								lobby.SetToast("Reset to default official relay!")
								closeRelayModal()
							},
							func() {
								closeRelayModal()
							},
							lobby.RelayStatus,
						)
					} else if showTestModal {
						DrawTestModal(f, f.Area(), audio, node, closeTestModal)
					} else if showExitModal || exitProg > 0.001 {
						DrawExitModal(f, f.Area(), exitProg, func() {
							cleanExit()
						}, func() {
							closeExitModal()
						})
					}

					if currentFileOffer != nil || fileOfferProg > 0.001 {
						DrawFileOfferModal(f, f.Area(), fileOfferProg, currentFileOffer, func() {
							acceptCurrentOffer(false)
						}, func() {
							declineCurrentOffer()
						}, func() {
							acceptCurrentOffer(true)
						})
					}
				})
			} else {
				if !showTestModal && !showLeaveModal && !showExitModal && !showScreenShareModal && !showDebugModal && !showRelayModal {
					if room.IsChatFocused {
						t.FocusManager().SetFocused("room_chat_input")
					} else {
						t.FocusManager().SetFocused("")
					}
				}
				room.OnLeave = func() {
					openLeaveModal()
				}
				room.OnOpenTestModal = openTestModal
				room.OnOpenScreenShareModal = openScreenShareModal
				room.OnSendChat = func(text string) {
					trimmed := strings.TrimSpace(text)
					if strings.EqualFold(trimmed, "/relay") || strings.EqualFold(trimmed, "/server") {
						openRelayModal()
						return
					}
					node.SendChatMessage(text)
					room.AddChatMessage(node.Nickname, node.LocalID, text, true, time.Now())
				}
				room.OnTriggerHop = func() {
					_ = node.RotatePort()
				}
				room.OnChangeNick = func(newNick string) {
					node.Nickname = newNick
					node.SendMuteState(audio.Muted)
				}
				room.OnTriggerMute = func() {
					isMuted := audio.ToggleMute()
					node.SendMuteState(isMuted)
					if isMuted {
						room.SetToast("Microphone Off (Muted)")
						room.AddLog("[MIC] Microphone muted")
					} else {
						room.SetToast("Microphone On")
						room.AddLog("[MIC] Microphone unmuted")
					}
				}
				room.OnTriggerDeafen = func() {
					isDeaf := audio.ToggleDeafen()
					node.SendDeafenState(isDeaf)
					node.SendMuteState(audio.Muted)
					if isDeaf {
						room.SetToast("Audio Off (Deafened)")
						room.AddLog("[AUDIO] Audio deafened (Sound & Mic off)")
					} else {
						room.SetToast("Audio On")
						room.AddLog("[AUDIO] Audio undeafened")
					}
				}
				room.OnTriggerSFX = func() {
					isMuted := audio.ToggleSFXMute()
					if isMuted {
						room.SetToast("Sound Effects Muted")
						room.AddLog("[SFX] Sound effects (join/leave/chat) muted")
					} else {
						room.SetToast("Sound Effects Enabled")
						room.AddLog("[SFX] Sound effects enabled")
					}
				}
				room.OnSetPeerVolume = func(target string, vol float64) {
					peers := node.GetPeersList()
					if len(peers) == 0 {
						room.SetToast("No other users in the room")
						return
					}
					var matched *PeerInfo
					if target == "" {
						if len(peers) == 1 {
							matched = peers[0]
						} else {
							room.SetToast("Multiple peers in room. Use: /vol <nickname> <0-200>")
							return
						}
					} else {
						tLower := strings.ToLower(target)
						for _, p := range peers {
							if strings.ToLower(p.Nickname) == tLower || strings.HasPrefix(strings.ToLower(p.Nickname), tLower) || p.ID == target {
								matched = p
								break
							}
						}
					}
					if matched == nil {
						room.SetToast(fmt.Sprintf("User '%s' not found", target))
						return
					}
					audio.SetPeerVolume(matched.ID, vol)
					pct := int(math.Round(vol * 100))
					room.SetToast(fmt.Sprintf("Volume for %s set to %d%%", matched.Nickname, pct))
					room.AddLog(fmt.Sprintf("[VOL] Volume for %s set to %d%%", matched.Nickname, pct))
				}
				room.OnSendFile = func(filePath string) {
					room.AddLog(fmt.Sprintf("[FILE] Sending %s...", filePath))
					go func() {
						err := node.SendFile(filePath)
						if err != nil {
							room.SetToast(fmt.Sprintf("Send error: %v", err))
							room.AddLog(fmt.Sprintf("[FILE] Error sending %s: %v", filePath, err))
						} else {
							room.SetToast("File transfer started")
						}
					}()
				}
				room.OnSendCode = func(title, code string) {
					room.AddLog(fmt.Sprintf("[CODE] Sharing code snippet (%d bytes)...", len(code)))
					go func() {
						err := node.SendCodeSnippet(title, code)
						if err != nil {
							room.SetToast(fmt.Sprintf("Code send error: %v", err))
						} else {
							room.SetToast("Code snippet sent to room")
						}
					}()
				}
				room.OnLockRoom = func(pin string) {
					if !node.IsHost {
						room.SetToast("Only the room host can lock the room")
						return
					}
					node.LockRoom(pin)
					if pin != "" {
						room.SetToast(fmt.Sprintf("Room locked with PIN: %s", pin))
						room.AddLog(fmt.Sprintf("[ROOM] Room locked with PIN: %s", pin))
					} else {
						room.SetToast("Room locked")
						room.AddLog("[ROOM] Room locked by host")
					}
				}
				room.OnUnlockRoom = func() {
					if !node.IsHost {
						room.SetToast("Only the room host can unlock the room")
						return
					}
					node.UnlockRoom()
					room.SetToast("Room unlocked")
					room.AddLog("[ROOM] Room unlocked by host")
				}
				room.OnOpenEditor = func(filePath string) {
					err := OpenInEditor(filePath)
					if err != nil {
						room.SetToast(fmt.Sprintf("Could not open editor: %v", err))
					} else {
						room.SetToast(fmt.Sprintf("Opened %s in editor", filepath.Base(filePath)))
					}
				}
				room.OnOpenFolder = func(dirPath string) {
					err := OpenFolder(dirPath)
					if err != nil {
						room.SetToast(fmt.Sprintf("Could not open folder: %v", err))
					} else {
						room.SetToast(fmt.Sprintf("Opened folder: %s", dirPath))
					}
				}
				room.Update()
				if showRelayModal {
					if relayModalActiveField == 0 {
						t.FocusManager().SetFocused("relay_url_input")
					} else if relayModalActiveField == 1 {
						t.FocusManager().SetFocused("relay_token_input")
					} else {
						t.FocusManager().SetFocused("")
					}
				}
				_ = t.Draw(func(f *terminal.Frame) {
					lastScreenArea = f.Area()
					room.Render(f, f.Area(), node, audio)

					if showDebugModal {
						DrawDebugModal(f, f.Area(), debugScrollOffset, closeDebugModal, ClearDebugLogs, func() {
							CopyToClipboard(GetAllDebugLogsText())
							room.SetToast("Copied debug logs to clipboard")
						})
					} else if showRelayModal || relayProg > 0.001 {
						DrawRelayModal(
							f, f.Area(), relayProg,
							node.RelayURL, node.RelayToken,
							relayURLInput, relayTokenInput,
							relayModalActiveField,
							relaySelField, relaySelStart, relaySelEnd,
							func(field int) { relayModalActiveField = field },
							func(newURL, newToken string) {
								newURL = strings.TrimSpace(newURL)
								newToken = strings.TrimSpace(newToken)
								node.UpdateRelaySettings(newURL, newToken)
								_ = SaveAppConfig(AppConfig{RelayURL: newURL, RelayToken: newToken})
								probeRelayStatus(newURL, newToken)
								room.SetToast("Relay server settings saved!")
								closeRelayModal()
							},
							func() {
								node.UpdateRelaySettings(DefaultRelayURL, "")
								_ = ResetAppConfig()
								probeRelayStatus(DefaultRelayURL, "")
								room.SetToast("Reset to default official relay!")
								closeRelayModal()
							},
							func() {
								closeRelayModal()
							},
							node.RelayStatus(),
						)
					} else if showTestModal {
						DrawTestModal(f, f.Area(), audio, node, closeTestModal)
					} else if showLeaveModal || leaveProg > 0.001 {
						DrawLeaveModal(f, f.Area(), leaveProg, func() {
							leaveRoom()
						}, func() {
							closeLeaveModal()
						})
					} else if showScreenShareModal || screenShareProg > 0.001 {
						DrawScreenShareModal(f, f.Area(), screenShareProg, selectedScreenShareIdx, screenShareTargets, func(target screenshare.WindowInfo) {
							startSelectedScreenShare(target)
						}, func() {
							closeScreenShareModal()
						})
					}

					if currentFileOffer != nil || fileOfferProg > 0.001 {
						DrawFileOfferModal(f, f.Area(), fileOfferProg, currentFileOffer, func() {
							acceptCurrentOffer(false)
						}, func() {
							declineCurrentOffer()
						}, func() {
							acceptCurrentOffer(true)
						})
					}
				})
			}
		}
	}
}
