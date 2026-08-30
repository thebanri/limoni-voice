package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/thebanri/limoni/animation"
	"github.com/thebanri/limoni/core/backend"
	"github.com/thebanri/limoni/core/terminal"
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
  --relay <url>       Custom WebSocket relay URL for self-hosted servers
                      Example: --relay ws://192.168.1.100:8080/ws
                      (Set to 'none' or 'off' to disable relay)
  --lan, --lan-only   Force LAN-only offline mode (disables relay, direct P2P on local network)
  --offline           Alias for --lan
  --peer, --connect   Direct target peer IP/host for cross-subnet or VPN LAN P2P
                      Example: --peer 192.168.1.50:50000
  --version           Show version information
  --help, -h          Show this help message

Environment Variables:
  LIMONI_RELAY_URL    Override default WebSocket relay URL
  LIMONI_LAN_ONLY     Set to 1 / true to enable LAN-only mode by default
  LIMONI_OFFLINE      Set to 1 / true to enable offline mode
  LIMONI_PEER         Set direct target peer IP/host

Examples:
  # Standard launch (connects to default public relay + LAN auto-discovery):
  limoni-voice

  # Force LAN-only direct P2P communication on local Wi-Fi / Ethernet:
  limoni-voice --lan

  # Connect directly to a specific LAN peer IP:
  limoni-voice --lan --peer 192.168.1.50

  # Connect using your self-hosted Docker relay server:
  limoni-voice --relay ws://192.168.1.100:8080/ws`)
}

func main() {
	var (
		flagRelay   = flag.String("relay", "", "Custom WebSocket relay URL (e.g. ws://192.168.1.100:8080/ws, or 'none' for LAN only)")
		flagLAN     = flag.Bool("lan", false, "Force LAN-only offline mode (disables relay connection)")
		flagLANOnly = flag.Bool("lan-only", false, "Alias for -lan")
		flagOffline = flag.Bool("offline", false, "Alias for -lan")
		flagPeer    = flag.String("peer", "", "Direct target peer IP / host for LAN / VPN P2P (e.g. 192.168.1.50)")
		flagConnect = flag.String("connect", "", "Alias for -peer")
		flagHelp    = flag.Bool("help", false, "Show help and usage instructions")
		flagVersion = flag.Bool("version", false, "Show version information")
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
		fmt.Println("Limoni Voice v1.0.0 (Go 1.24+ | E2EE AES-256-GCM | P2P Full-Mesh)")
		os.Exit(0)
	}

	b := backend.NewBackend(os.Stdin, os.Stdout)
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

	// Modal States & Animations
	showTestModal := false
	showLeaveModal := false
	showExitModal := false
	showScreenShareModal := false
	var screenShareTargets []screenshare.WindowInfo
	selectedScreenShareIdx := 0

	exitDialogAnim := animation.NewFloat(0.0)
	leaveDialogAnim := animation.NewFloat(0.0)
	screenShareDialogAnim := animation.NewFloat(0.0)

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

	closeLeaveModal := func() {
		showLeaveModal = false
		leaveDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
		t.FocusManager().SetFocused("")
		t.ForceFullRedraw()
	}

	closeScreenShareModal := func() {
		showScreenShareModal = false
		screenShareDialogAnim.AnimateTo(0.0, 200*time.Millisecond, animation.EaseInCubic)
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
				{ID: "desktop", Title: "🖥️  Entire Screen (Primary View)"},
			}
		}
		selectedScreenShareIdx = 0
		showScreenShareModal = true
		screenShareDialogAnim.AnimateTo(1.0, 250*time.Millisecond, animation.EaseOutCubic)
	}

	node.OnLog = func(msg string) {
		room.AddLog(msg)
		if currentScreen == ScreenLobby {
			lobby.SetToast(msg)
		} else {
			if (strings.HasPrefix(msg, "⚠️") || strings.HasPrefix(msg, "❌") || strings.HasPrefix(msg, "📺") || strings.HasPrefix(msg, "⏹️") || strings.HasPrefix(msg, "🎬")) &&
				!strings.Contains(msg, "[FFMPEG") && !strings.Contains(msg, "[MPV") && !strings.Contains(msg, "[WATCH]") && !strings.Contains(msg, "[SHARE]") && !strings.Contains(msg, "[BROADCAST]") && !strings.Contains(msg, "[DARWIN]") {
				room.SetToast(msg)
			}
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
		room = NewRoomView()
		currentScreen = ScreenRoom
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

		node.RequestJoinRoom(cleanCode, 8*time.Second,
			func(hostNick string) {
				lobby.IsConnecting = false
				room = NewRoomView()
				currentScreen = ScreenRoom
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

	// Audio frame capture and sender loop
	audio.Start(func(rms float64, speaking bool, pcm []byte) {
		if currentScreen == ScreenRoom && !audio.InTestMode && !audio.Muted {
			node.SendAudio(rms, speaking, pcm)
		}
	})
	defer audio.Stop()

	renderTicker := time.NewTicker(33 * time.Millisecond) // ~30 FPS
	defer renderTicker.Stop()

	lastTime := time.Now()

	for {
		select {
		case ev := <-b.Events():
			switch ev.Type {
			case backend.EventPaste:
				// Terminal bracketed paste event
				pasted := strings.TrimSpace(ev.Paste.Text)
				if currentScreen == ScreenLobby && pasted != "" && !showTestModal && !showExitModal {
					if lobby.ActiveInput == 0 {
						lobby.NickState.SetValue(pasted)
						lobby.SetToast("Username pasted")
					} else if lobby.ActiveInput == 1 {
						lobby.CodeState.SetValue(NormalizeCode(pasted))
						lobby.SetToast(fmt.Sprintf("Room key pasted: %s", NormalizeCode(pasted)))
					}
				}

			case backend.EventKey:
				e := ev.Key

				// Ctrl+C Always triggers exit confirmation
				if e.Ctrl && (e.Ch == 'c' || e.Ch == 'C') {
					if showExitModal {
						b.Close()
						os.Exit(0)
					}
					if currentScreen == ScreenRoom {
						openLeaveModal()
					} else {
						openExitModal()
					}
					continue
				}

				// --- 1. Handle Active Modals First ---
				if showExitModal {
					focused := t.FocusManager().Focused()
					switch e.Type {
					case backend.KeyTab:
						if e.Shift {
							t.FocusManager().Prev()
						} else {
							t.FocusManager().Next()
						}
					case backend.KeyArrowLeft:
						t.FocusManager().Prev()
					case backend.KeyArrowRight:
						t.FocusManager().Next()
					case backend.KeyEnter, backend.KeySpace:
						if focused == "exit_app_dialog_btn_0" {
							b.Close()
							os.Exit(0)
						} else {
							closeExitModal()
						}
					case backend.KeyEsc:
						closeExitModal()
					case backend.KeyRune:
						if e.Ch == 'e' || e.Ch == 'E' || e.Ch == 'y' || e.Ch == 'Y' {
							b.Close()
							os.Exit(0)
						} else if e.Ch == 'h' || e.Ch == 'H' || e.Ch == 'n' || e.Ch == 'N' {
							closeExitModal()
						}
					}
					continue
				}

				if showLeaveModal {
					focused := t.FocusManager().Focused()
					switch e.Type {
					case backend.KeyTab:
						if e.Shift {
							t.FocusManager().Prev()
						} else {
							t.FocusManager().Next()
						}
					case backend.KeyArrowLeft:
						t.FocusManager().Prev()
					case backend.KeyArrowRight:
						t.FocusManager().Next()
					case backend.KeyEnter, backend.KeySpace:
						if focused == "leave_room_dialog_btn_0" {
							leaveRoom()
						} else {
							closeLeaveModal()
						}
					case backend.KeyEsc:
						closeLeaveModal()
					case backend.KeyRune:
						if e.Ch == 'e' || e.Ch == 'E' || e.Ch == 'y' || e.Ch == 'Y' {
							leaveRoom()
						} else if e.Ch == 'h' || e.Ch == 'H' || e.Ch == 'n' || e.Ch == 'N' {
							closeLeaveModal()
						}
					}
					continue
				}

				if showTestModal {
					switch e.Type {
					case backend.KeyEsc:
						closeTestModal()
					case backend.KeySpace:
						audio.ToggleLoopback()
					case backend.KeyArrowLeft, backend.KeyArrowRight:
						audio.CycleSuppressionMode()
					case backend.KeyRune:
						switch e.Ch {
						case 'l', 'L':
							audio.ToggleLoopback()
						case 'n', 'N':
							audio.CycleSuppressionMode()
						case 'm', 'M':
							isMuted := audio.ToggleMute()
							node.SendMuteState(isMuted)
						case '0':
							audio.SetSuppressionMode(0)
						case '1':
							audio.AdjustGain(-0.1)
						case '2':
							audio.AdjustGain(0.1)
						case '-', '_':
							audio.AdjustGain(-0.1)
						case '+', '=':
							audio.AdjustGain(0.1)
						}
					}
					continue
				}

				if showScreenShareModal {
					switch e.Type {
					case backend.KeyEsc:
						closeScreenShareModal()
					case backend.KeyArrowUp:
						if selectedScreenShareIdx > 0 {
							selectedScreenShareIdx--
						}
					case backend.KeyArrowDown:
						if selectedScreenShareIdx < len(screenShareTargets)-1 {
							selectedScreenShareIdx++
						}
					case backend.KeyEnter, backend.KeySpace:
						if selectedScreenShareIdx >= 0 && selectedScreenShareIdx < len(screenShareTargets) {
							startSelectedScreenShare(screenShareTargets[selectedScreenShareIdx])
						} else {
							closeScreenShareModal()
						}
					}
					continue
				}

				// Dedicated Global Test Modal Key (F4)
				if e.Type == backend.KeyF4 {
					openTestModal()
					continue
				}

				// --- 2. Screen: Lobby Key Handling ---
				if currentScreen == ScreenLobby {
					if e.Type == backend.KeyF2 {
						CopyToClipboard(lobby.CurrentCode)
						lobby.SetToast(fmt.Sprintf("Room key copied: %s", lobby.CurrentCode))
						continue
					}
					if e.Type == backend.KeyF3 {
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
					if lobby.IsConnecting && e.Type == backend.KeyEsc {
						cancelJoin()
						continue
					}

					// Tab Navigation
					if e.Type == backend.KeyTab {
						if e.Shift {
							lobby.ActiveInput = (lobby.ActiveInput + 2) % 3
						} else {
							lobby.ActiveInput = (lobby.ActiveInput + 1) % 3
						}
						continue
					}

					// Enter key
					if e.Type == backend.KeyEnter {
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
						if e.Type == backend.KeyEsc {
							lobby.ActiveInput = 2
						} else {
							lobby.NickState.HandleKey(e)
						}

					case 1: // Join Room Code Input Focused (Full cursor navigation & editing)
						if e.Type == backend.KeyEsc {
							lobby.ActiveInput = 2
						} else {
							lobby.CodeState.HandleKey(e)
						}

					case 2: // Host / General Section
						switch e.Type {
						case backend.KeyEsc:
							openExitModal()
						case backend.KeyRune:
							switch e.Ch {
							case '1':
								lobby.ActiveInput = 0
							case '2':
								startHost()
							case '3':
								lobby.ActiveInput = 1
							case 'c', 'C':
								CopyToClipboard(lobby.CurrentCode)
								lobby.SetToast(fmt.Sprintf("Room key copied: %s", lobby.CurrentCode))
							case 'g', 'G':
								lobby.CurrentCode = GenerateRoomCode()
								lobby.SetToast("New room key generated!")
							case 't', 'T':
								openTestModal()
							case 'q', 'Q':
								openExitModal()
							}
						}
					}

				} else {
					// --- 3. Screen: Room Key Handling ---
					switch e.Type {
					case backend.KeyEsc:
						if node.IsWatchingScreen {
							_ = node.StopWatchingScreen()
							room.SetToast("Screen viewer closed")
						} else if node.IsSharingScreen {
							_ = node.StopScreenShare()
							room.SetToast("Screen share stopped")
						} else {
							openLeaveModal()
						}

					case backend.KeyF2:
						CopyToClipboard(node.RoomCode)
						room.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))

					case backend.KeyRune:
						switch e.Ch {
						case 't', 'T':
							openTestModal()

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
								go func() {
									_ = node.StopWatchingScreen()
									room.SetToast("Stream viewer closed")
								}()
							} else {
								var streamingPeer *PeerInfo
								for _, p := range node.Peers {
									if p.IsSharingScreen {
										streamingPeer = p
										break
									}
								}
								if streamingPeer != nil {
									port := streamingPeer.VideoPort
									if port <= 0 {
										port = 50100
									}
									opts := screenshare.ReceiverOptions{
										WindowTitle: fmt.Sprintf("Limoni Voice - %s Live Stream (60 FPS)", streamingPeer.Nickname),
									}
									room.SetToast("🎬 Starting stream viewer...")
									go func() {
										err := node.StartWatchingScreen(port, opts)
										if err != nil {
											room.SetToast(fmt.Sprintf("Error: %v", err))
										} else {
											room.SetToast(fmt.Sprintf("%s stream opened (HD 60 FPS)", streamingPeer.Nickname))
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

			case backend.EventMouse:
				handled := t.RouteMouseEvent(ev.Mouse)
				if !handled && currentScreen == ScreenLobby && !showTestModal && !showExitModal {
					if ev.Mouse.Button == backend.MouseLeft {
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
					} else if ev.Mouse.Button == backend.MouseNone {
						lobby.DragActive = false
					} else if ev.Mouse.Button == backend.MouseScrollUp {
						if lobby.Scale < 12.0 {
							lobby.Scale += 0.3
						}
					} else if ev.Mouse.Button == backend.MouseScrollDown {
						if lobby.Scale > 1.5 {
							lobby.Scale -= 0.3
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

			if currentScreen == ScreenLobby {
				if !showTestModal && !showExitModal {
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
					lobby.Render(f, f.Area())

					if showTestModal {
						DrawTestModal(f, f.Area(), audio, node, closeTestModal)
					} else if showExitModal || exitProg > 0.001 {
						DrawExitModal(f, f.Area(), exitProg, func() {
							b.Close()
							os.Exit(0)
						}, func() {
							closeExitModal()
						})
					}
				})
			} else {
				room.OnLeave = func() {
					openLeaveModal()
				}
				room.OnOpenTestModal = openTestModal
				room.OnOpenScreenShareModal = openScreenShareModal
				room.Update()
				_ = t.Draw(func(f *terminal.Frame) {
					room.Render(f, f.Area(), node, audio)

					if showTestModal {
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
				})
			}
		}
	}
}
