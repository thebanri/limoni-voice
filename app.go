package main

import (
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thebanri/limoni-voice/internal/ptt"
	"github.com/thebanri/limoni-voice/screenshare"
	"github.com/thebanri/limoni/animation"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
)

// App owns the terminal UI state and routes events between the views, the audio engine and
// the network node. Behaviour is split by concern across app_*.go files.
type App struct {
	backend *driver.Backend
	term    *terminal.Terminal
	node    *P2PNode
	audio   *AudioEngine
	lobby   *LobbyView
	room    *RoomView

	currentScreen AppScreen
	appStartTime  time.Time
	lastTime      time.Time
	lastScreen    cell.Rect

	// Modals
	showTestModal        bool
	showLeaveModal       bool
	showExitModal        bool
	showScreenShareModal bool
	showDebugModal       bool
	showRelayModal       bool
	debugScrollOffset    int

	screenShareTargets     []screenshare.WindowInfo
	selectedScreenShareIdx int
	screenPreset           int
	shareSystemAudio       bool
	screenShareDeps        screenshare.DependencyStatus

	relayDialogAnim       *animation.Float
	exitDialogAnim        *animation.Float
	leaveDialogAnim       *animation.Float
	screenShareDialogAnim *animation.Float
	fileOfferDialogAnim   *animation.Float

	relayURLInput         *widgets.TextInputState
	relayTokenInput       *widgets.TextInputState
	relayModalActiveField int // 0: URL, 1: Token, 2: Save, 3: Reset, 4: Cancel
	relaySelStart         int
	relaySelEnd           int
	relaySelField         int

	// File offers
	fileOfferMu       sync.Mutex
	currentFileOffer  *FileOffer
	pendingFileOffers []*FileOffer

	// Global push-to-talk
	pttMu      sync.Mutex
	pttWatcher ptt.Watcher
	pttKey     string
}

// NewApp builds the application and wires network / audio callbacks.
func NewApp(b *driver.Backend, t *terminal.Terminal, node *P2PNode, audio *AudioEngine, cfg AppConfig) *App {
	app := &App{
		backend:               b,
		term:                  t,
		node:                  node,
		audio:                 audio,
		lobby:                 NewLobbyView(),
		room:                  NewRoomView(),
		currentScreen:         ScreenLobby,
		appStartTime:          time.Now(),
		lastTime:              time.Now(),
		screenPreset:          screenshare.DefaultPreset,
		relayDialogAnim:       animation.NewFloat(0.0),
		exitDialogAnim:        animation.NewFloat(0.0),
		leaveDialogAnim:       animation.NewFloat(0.0),
		screenShareDialogAnim: animation.NewFloat(0.0),
		fileOfferDialogAnim:   animation.NewFloat(0.0),
		relayURLInput:         widgets.NewTextInputState(),
		relayTokenInput:       widgets.NewTextInputState(),
		relaySelStart:         -1,
		relaySelEnd:           -1,
		relaySelField:         -1,
	}
	app.applySettings(cfg)
	app.wireNodeCallbacks()
	app.wireLobbyCallbacks()
	app.probeRelayStatus(node.RelayURL, node.RelayToken)

	// Background auto-updater check
	go CheckAndUpdateAsync(func(msg string) {
		app.toast(msg)
		app.room.AddLog(fmt.Sprintf("[UPDATE] %s", msg))
	})
	return app
}

// toast shows a message on the active screen.
func (a *App) toast(msg string) {
	if a.currentScreen == ScreenLobby {
		a.lobby.SetToast(msg)
	} else {
		a.room.SetToast(msg)
	}
}

func (a *App) wireNodeCallbacks() {
	node, audio := a.node, a.audio

	screenshare.LogCallback = func(msg string) {
		AddDebugLog("[SCREEN] " + msg)
	}

	node.OnLog = func(msg string) {
		AddDebugLog("[ROOM] " + msg)
		isUserEvent := strings.HasPrefix(msg, "[+]") || strings.HasPrefix(msg, "[-]") ||
			strings.HasPrefix(msg, "[HOST]") || strings.HasPrefix(msg, "[ERROR]") ||
			strings.HasPrefix(msg, "[E2EE]") || strings.Contains(msg, "joined") || strings.Contains(msg, "left")
		if isUserEvent {
			a.room.AddLog(msg)
		}
		if a.currentScreen == ScreenLobby {
			a.lobby.SetToast(msg)
		} else if strings.HasPrefix(msg, "[WARN]") || strings.HasPrefix(msg, "[ERROR]") || strings.HasPrefix(msg, "⚠️") || strings.HasPrefix(msg, "❌") || strings.Contains(msg, "⚠️") {
			a.room.SetToast(msg)
		}
	}

	node.OnDebugLog = func(msg string) {
		AddDebugLog("[NET] " + msg)
	}

	node.OnPeerEvent = func(event string, peer *PeerInfo) {
		if event == "join" {
			audio.PlaySound(SoundJoin)
			a.room.AddLog(fmt.Sprintf("[+] %s joined the room.", peer.Nickname))
		} else if event == "leave" {
			audio.PlaySound(SoundLeave)
			a.room.AddLog(fmt.Sprintf("[-] %s left the room.", peer.Nickname))
		}
	}

	node.OnRoomCodeChanged = func(newCode string) {
		a.lobby.CurrentCode = newCode
		a.room.AddLog(fmt.Sprintf("[ROOM] Room key changed to %s (share the new key)", newCode))
		a.room.SetToast(fmt.Sprintf("Room key is now %s", newCode))
	}

	node.OnChatMessage = func(senderID string, nickname string, text string, ts time.Time) {
		audio.PlaySound(SoundChat)
		a.room.AddChatMessage(nickname, senderID, text, false, ts)
	}

	node.OnFileTransferProgress = func(transferID string, fileName string, transferred int64, total int64, speed float64, isUpload bool, done bool, err error) {
		if err != nil {
			a.room.SetToast(fmt.Sprintf("File error (%s): %v", fileName, err))
			a.room.AddLog(fmt.Sprintf("[FILE] Transfer error for %s: %v", fileName, err))
			return
		}
		pct := 0
		if total > 0 {
			pct = int(transferred * 100 / total)
		}
		barLen := 10
		filled := min(pct*barLen/100, barLen)
		bar := strings.Repeat("=", filled) + strings.Repeat(" ", barLen-filled)
		direction := "Downloading"
		if isUpload {
			direction = "Uploading"
		}
		if done {
			a.room.SetToast(fmt.Sprintf("✓ %s completed: %s", direction, fileName))
			a.room.AddLog(fmt.Sprintf("[FILE] %s '%s' completed (%s)", direction, fileName, formatBytes(total)))
		} else {
			a.room.SetToast(fmt.Sprintf("%s %s [%s] %d%% (%.1f KB/s)", direction, fileName, bar, pct, speed/1024.0))
		}
	}

	node.OnFileOfferReceived = a.enqueueFileOffer

	node.OnFileReceived = func(transferID string, fileName string, filePath string, isCode bool, content string) {
		audio.PlaySound(SoundChat)
		var size int64
		if fi, err := os.Stat(filePath); err == nil {
			size = fi.Size()
		}
		if isCode {
			a.room.AddLog(fmt.Sprintf("[CODE] Received code snippet '%s' (%s) saved to %s", fileName, formatBytes(size), filePath))
			a.room.SetToast(fmt.Sprintf("Code snippet received: %s", fileName))
		} else {
			a.room.AddLog(fmt.Sprintf("[FILE] Received file '%s' (%s) saved to %s", fileName, formatBytes(size), filePath))
			a.room.SetToast(fmt.Sprintf("File received: %s", fileName))
		}
	}

	node.OnRoomLocked = func(isLocked bool, pin string) {
		if a.currentScreen == ScreenRoom {
			switch {
			case isLocked && pin != "" && node.IsHost:
				a.room.SetToast(fmt.Sprintf("Room locked with PIN: %s", pin))
				a.room.AddLog(fmt.Sprintf("[ROOM] Room locked with PIN: %s (Host only)", pin))
			case isLocked && pin != "":
				a.room.SetToast("Room is locked with PIN")
				a.room.AddLog("[ROOM] Room is locked with PIN by host")
			case isLocked:
				a.room.SetToast("Room locked")
				a.room.AddLog("[ROOM] Room locked by host")
			default:
				a.room.SetToast("Room unlocked")
				a.room.AddLog("[ROOM] Room unlocked by host")
			}
		}
		a.term.ForceFullRedraw()
	}
}

func (a *App) wireLobbyCallbacks() {
	a.lobby.OnStartHost = a.startHost
	a.lobby.OnJoinRoom = a.joinRoom
	a.lobby.OnCancelJoin = a.cancelJoin
	a.lobby.OnCopyCode = func(code string) {
		CopyToClipboard(code)
		a.lobby.SetToast(fmt.Sprintf("Room key copied: %s", code))
	}
	a.lobby.OnNewCode = func() {
		a.lobby.CurrentCode = GenerateRoomCode()
		a.lobby.SetToast("New room key generated!")
	}
	a.lobby.OnOpenTestModal = a.openTestModal
	a.lobby.OnOpenRelayModal = a.openRelayModal
	a.lobby.RelayURL = a.node.RelayURL
}

// wireRoomCallbacks binds the room view actions (the room view is recreated per session).
func (a *App) wireRoomCallbacks() {
	node, audio, room := a.node, a.audio, a.room
	room.OnLeave = a.openLeaveModal
	room.OnOpenTestModal = a.openTestModal
	room.OnOpenScreenShareModal = a.openScreenShareModal
	room.OnSendChat = func(text string) {
		trimmed := strings.TrimSpace(text)
		if strings.EqualFold(trimmed, "/relay") || strings.EqualFold(trimmed, "/server") {
			a.openRelayModal()
			return
		}
		if strings.EqualFold(trimmed, "/net") || strings.EqualFold(trimmed, "/stats") {
			for _, line := range node.Diagnostics().Lines() {
				room.AddLog("[NET] " + line)
			}
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
		_ = UpdateAppConfig(func(c *AppConfig) { c.Nickname = newNick })
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
		if audio.ToggleSFXMute() {
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
			if len(peers) != 1 {
				room.SetToast("Multiple peers in room. Use: /vol <nickname> <0-200>")
				return
			}
			matched = peers[0]
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
			if err := node.SendFile(filePath); err != nil {
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
			if err := node.SendCodeSnippet(title, code); err != nil {
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
		if err := OpenInEditor(filePath); err != nil {
			room.SetToast(fmt.Sprintf("Could not open editor: %v", err))
		} else {
			room.SetToast(fmt.Sprintf("Opened %s in editor", filepath.Base(filePath)))
		}
	}
	room.OnOpenFolder = func(dirPath string) {
		if err := OpenFolder(dirPath); err != nil {
			room.SetToast(fmt.Sprintf("Could not open folder: %v", err))
		} else {
			room.SetToast(fmt.Sprintf("Opened folder: %s", dirPath))
		}
	}
}

func (a *App) nickname(fallback string) string {
	nick := strings.TrimSpace(a.lobby.NickState.Value())
	if nick == "" {
		nick = fallback
	} else {
		_ = UpdateAppConfig(func(c *AppConfig) { c.Nickname = nick })
	}
	return nick
}

func (a *App) startHost() {
	fallback := "User_Host"
	if len(a.lobby.CurrentCode) >= 4 {
		fallback = "User_" + a.lobby.CurrentCode[:4]
	}
	a.node.Nickname = a.nickname(fallback)
	a.node.HostRoom(a.lobby.CurrentCode)
	if a.lobby.IsPinProtected {
		pinVal := strings.TrimSpace(a.lobby.PinState.Value())
		if pinVal == "" {
			pinVal = "1234"
		}
		a.node.LockRoom(pinVal)
	} else {
		a.node.UnlockRoom()
	}
	a.room = NewRoomView()
	a.currentScreen = ScreenRoom
	a.audio.PlaySound(SoundJoin)
}

func (a *App) joinRoom(code string) {
	cleanCode := NormalizeCode(code)
	if cleanCode == "" {
		a.lobby.SetToast("Please enter a valid room key")
		return
	}
	a.node.Nickname = a.nickname("User_" + a.lobby.CurrentCode[:4])
	a.lobby.IsConnecting = true
	a.lobby.ConnectingTarget = cleanCode
	a.lobby.SetToast(fmt.Sprintf("Searching for room '%s' and verifying host...", cleanCode))

	a.node.RequestJoinRoom(cleanCode, 15*time.Second,
		func(hostNick string) {
			a.lobby.IsConnecting = false
			a.room = NewRoomView()
			a.currentScreen = ScreenRoom
			a.audio.PlaySound(SoundJoin)
			a.room.AddLog(fmt.Sprintf("[+] Successfully joined room %s! (Host: %s)", cleanCode, hostNick))
			a.room.SetToast(fmt.Sprintf("Joined Room! Host: %s", hostNick))
		},
		func(reason string) {
			a.lobby.IsConnecting = false
			a.lobby.SetToast(fmt.Sprintf("❌ %s", reason))
		},
	)
}

func (a *App) cancelJoin() {
	a.node.CancelJoin()
	a.lobby.IsConnecting = false
	a.lobby.SetToast("Room search cancelled.")
}

func (a *App) leaveRoom() {
	a.audio.PlaySound(SoundLeave)
	a.node.LeaveRoom()
	a.lobby.CurrentCode = GenerateRoomCode()
	a.lobby.IsPinProtected = false
	a.lobby.PinState.SetValue("")
	a.lobby.ActiveInput = 2
	a.fileOfferMu.Lock()
	a.currentFileOffer = nil
	a.pendingFileOffers = nil
	a.fileOfferMu.Unlock()
	a.closeScreenShareModal()
	a.closeDebugModal()
	a.closeTestModal()
	a.currentScreen = ScreenLobby
	a.closeLeaveModal()
}

func (a *App) cleanExit() {
	a.stopGlobalPTT()
	a.node.Close()
	a.audio.Stop()
	a.backend.Close()
	restoreConsole()
	os.Exit(0)
}

// Run starts audio and processes events until the application exits.
func (a *App) Run() {
	a.audio.Start(func(rms float64, speaking bool, pcm []byte) {
		if a.currentScreen == ScreenRoom && !a.audio.InTestMode && !a.audio.Muted {
			if a.audio.InputMode == InputModeVoiceActivity || a.audio.IsTransmitting() {
				a.node.SendAudio(rms, speaking, pcm)
			}
		}
	})
	a.syncGlobalPTT()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	renderTicker := time.NewTicker(33 * time.Millisecond) // ~30 FPS
	defer renderTicker.Stop()

	for {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP || sig == syscall.SIGTERM {
				a.cleanExit()
			} else if sig == os.Interrupt {
				switch {
				case a.showExitModal || a.showLeaveModal:
					a.cleanExit()
				case a.currentScreen == ScreenRoom:
					a.openLeaveModal()
				default:
					a.openExitModal()
				}
			}
		case ev := <-a.backend.Events():
			switch ev.Type {
			case driver.EventPaste:
				a.handlePaste(ev.Paste.Text)
			case driver.EventKey:
				a.handleKey(ev.Key)
			case driver.EventMouse:
				a.handleMouse(ev.Mouse)
			}
		case now := <-renderTicker.C:
			a.render(now)
		}
	}
}
