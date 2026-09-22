// Room view: the compact HUD bar and its pills.

package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/thebanri/limoni-voice/screenshare"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
)

// drawHUDPill draws a styled capsule pill and registers an optional click handler.
// It returns the advanced X position and true if drawn, or false if it couldn't fit.
func drawHUDPill(buf *buffer.Buffer, frame *terminal.Frame, x, y, maxX uint16, label string, style cell.Style, onClick func()) (uint16, bool) {
	pillRunes := []rune(label)
	pillLen := uint16(len(pillRunes))
	if pillLen == 0 || x+pillLen > maxX {
		return x, false
	}
	buf.SetString(x, y, label, style)
	if onClick != nil && frame != nil {
		frame.RegisterClickHandler(cell.NewRect(x, y, pillLen, 1), func(_ driver.MouseEvent) {
			onClick()
		})
	}
	return x + pillLen, true
}

// drawMiniVUBar renders a horizontal equalizer level meter bar into the buffer.
func drawMiniVUBar(buf *buffer.Buffer, x, y, maxX uint16, rms float64, isSpeaking, isMuted bool, width int, theme ThemePalette) uint16 {
	if x >= maxX {
		return x
	}
	buf.SetCell(x, y, cell.Cell{Content: '[', Style: cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}})
	x++

	filledBars := int(math.Round(rms * float64(width) * 2.0))
	if filledBars > width {
		filledBars = width
	}
	if isMuted {
		filledBars = 0
	}

	for i := 0; i < width; i++ {
		if x >= maxX {
			break
		}
		ch := '▱'
		barStyle := cell.Style{Fg: theme.Border, Bg: theme.SurfaceBg}
		if i < filledBars {
			ch = '▰'
			if isSpeaking {
				barStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0xFF, 0x88), Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
			} else {
				barStyle = cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg}
			}
		}
		buf.SetCell(x, y, cell.Cell{Content: ch, Style: barStyle})
		x++
	}

	if x < maxX {
		buf.SetCell(x, y, cell.Cell{Content: ']', Style: cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}})
		x++
	}
	return x
}

func (r *RoomView) renderCompactHUD(frame *terminal.Frame, area cell.Rect, node *P2PNode, audio *AudioEngine) {
	if area.Height < 1 || area.Width < 8 {
		return
	}

	theme := CurrentTheme()
	buf := frame.Buffer

	peers := node.GetPeersList()
	totalMembers := len(peers) + 1

	// Gather audio & streaming state
	var streamingPeers []*PeerInfo
	for _, p := range peers {
		if p.IsSharingScreen {
			streamingPeers = append(streamingPeers, p)
		}
	}

	var speakingPeers []string
	if audio != nil && audio.IsSpeaking && !audio.Muted {
		speakingPeers = append(speakingPeers, "You")
	}
	for _, p := range peers {
		if p.Speaking && !p.IsMuted {
			speakingPeers = append(speakingPeers, p.Nickname)
		}
	}

	// Calculate latency indicator (average of all active peers with measured ping)
	peerPing := 0
	allRelayed := false
	anyRelayed := false
	hasLANPeer := false
	hasP2PPeer := false
	if len(peers) > 0 {
		var totalPing int64
		count := 0
		relayedCount := 0
		for _, p := range peers {
			if p.PingMs > 0 {
				totalPing += p.PingMs
				count++
			}
			if p.ViaRelay {
				relayedCount++
				anyRelayed = true
			} else if p.Addr != nil && (p.Addr.IP.IsLoopback() || p.Addr.IP.IsPrivate()) {
				hasLANPeer = true
			} else {
				hasP2PPeer = true
			}
		}
		if count > 0 {
			peerPing = int(totalPing / int64(count))
		}
		if relayedCount == len(peers) {
			allRelayed = true
		}
	}
	pingColor := theme.Success
	if peerPing > 120 {
		pingColor = theme.Danger
	} else if peerPing > 50 {
		pingColor = theme.Warning
	}

	// Determine container inner area
	var inner cell.Rect
	if area.Height >= 4 && area.Width >= 32 {
		hudTitle := fmt.Sprintf(" 🍋 LIMONI VOICE • MINI HUD [%s] ", formatDuration(time.Since(r.StartTime)))
		if area.Width < 50 {
			hudTitle = " 🍋 LIMONI MINI HUD "
		}
		block := widgets.Block{
			Title:         hudTitle,
			Borders:       widgets.BorderAll,
			BorderSymbols: widgets.SymbolsRounded,
			BorderStyle:   cell.Style{Fg: theme.BorderFocused},
			Style:         cell.Style{Bg: theme.SurfaceBg},
		}
		inner = block.Inner(area)
		frame.RenderWidget(block, area)
	} else {
		inner = area
	}

	// Fill background
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	if inner.Height < 1 || inner.Width < 6 {
		return
	}

	maxX := inner.X + inner.Width

	// =========================================================================
	// CASE A: ULTRA-COMPACT 1-LINE RIBBON (inner.Height == 1)
	// =========================================================================
	if inner.Height == 1 {
		rowY := inner.Y
		curX := inner.X

		// 1. Brand / Room pill
		roomPill := fmt.Sprintf(" 🍋 #%s ", node.RoomCode)
		curX, _ = drawHUDPill(buf, frame, curX, rowY, maxX, roomPill, cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}, func() {
			CopyToClipboard(node.RoomCode)
			r.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))
		})
		curX++

		// 2. Mic pill
		var micLabel string
		var micStyle cell.Style
		if audio != nil && audio.Muted {
			micLabel = " 🔴 MUTED [M] "
			micStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0xFF, 0xFF), Bg: theme.Danger, Modifier: cell.ModifierBold}
		} else if audio != nil && audio.IsSpeaking {
			micLabel = " 🟢 TALK [M] "
			micStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: cell.NewColorRGB(0x00, 0xFF, 0x88), Modifier: cell.ModifierBold}
		} else {
			micLabel = " 🎙️ MIC [M] "
			micStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Success, Modifier: cell.ModifierBold}
		}
		curX, _ = drawHUDPill(buf, frame, curX, rowY, maxX, micLabel, micStyle, func() {
			if audio != nil {
				isMuted := audio.ToggleMute()
				node.SendMuteState(isMuted)
				if isMuted {
					r.SetToast("Microphone Muted")
				} else {
					r.SetToast("Microphone Active")
				}
			}
		})
		curX++

		// 3. Deafen pill
		var deafLabel string
		var deafStyle cell.Style
		if audio != nil && audio.Deafened {
			deafLabel = " 🔇 DEAF [D] "
			deafStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Warning, Modifier: cell.ModifierBold}
		} else {
			deafLabel = " 🔊 SPK [D] "
			deafStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Secondary, Modifier: cell.ModifierBold}
		}
		curX, _ = drawHUDPill(buf, frame, curX, rowY, maxX, deafLabel, deafStyle, func() {
			if audio != nil {
				isDeaf := audio.ToggleDeafen()
				node.SendDeafenState(isDeaf)
				node.SendMuteState(audio.Muted)
			}
		})
		curX++

		// 4. Screen Share pill
		var shareLabel string
		var shareStyle cell.Style
		var shareAction func()
		if node.IsSharingScreen {
			shareLabel = " 📺 SHARING [V] "
			shareStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold}
			shareAction = func() {
				_ = node.StopScreenShare()
				r.SetToast("Screen share stopped")
			}
		} else if node.IsWatchingScreen {
			shareLabel = " 📺 WATCHING [W] "
			shareStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Secondary, Modifier: cell.ModifierBold}
			shareAction = func() {
				_ = node.StopWatchingScreen()
				r.SetToast("Stream viewer closed")
			}
		} else if len(streamingPeers) > 0 {
			target := streamingPeers[0]
			shareLabel = fmt.Sprintf(" 🔴 WATCH %s [W] ", target.Nickname)
			shareStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0xFF, 0xFF), Bg: theme.Danger, Modifier: cell.ModifierBold}
			shareAction = func() {
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
				r.SetToast(fmt.Sprintf("Opening %s stream...", target.Nickname))
				go func() {
					_ = node.StartWatchingScreen(target.ID, port, opts)
				}()
			}
		} else {
			shareLabel = " 📺 SHARE [V] "
			shareStyle = cell.Style{Fg: theme.Text, Bg: theme.CardBg, Modifier: cell.ModifierBold}
			shareAction = func() {
				if r.OnOpenScreenShareModal != nil {
					r.OnOpenScreenShareModal()
				}
			}
		}
		curX, _ = drawHUDPill(buf, frame, curX, rowY, maxX, shareLabel, shareStyle, shareAction)
		curX++

		// 5. Speaker or Member count
		if len(speakingPeers) > 0 {
			spkPill := fmt.Sprintf(" 🔊 %s ", strings.Join(speakingPeers, ", "))
			curX, _ = drawHUDPill(buf, frame, curX, rowY, maxX, spkPill, cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Accent,
				Modifier: cell.ModifierBold,
			}, nil)
			curX++
		} else {
			memPill := fmt.Sprintf(" 👥 %d/4 ", totalMembers)
			curX, _ = drawHUDPill(buf, frame, curX, rowY, maxX, memPill, cell.Style{
				Fg: theme.Success,
				Bg: theme.CardBg,
			}, nil)
			curX++
		}

		// 6. Ping pill
		pingPill := " ⚡ -- "
		if peerPing > 0 {
			modeTag := "P2P"
			if allRelayed {
				modeTag = "Relay"
			} else if anyRelayed {
				modeTag = "Mesh"
			} else if hasLANPeer && !hasP2PPeer {
				modeTag = "LAN"
			}
			pingPill = fmt.Sprintf(" ⚡ %dms (%s) ", peerPing, modeTag)
		}
		drawHUDPill(buf, frame, curX, rowY, maxX, pingPill, cell.Style{
			Fg: pingColor,
			Bg: theme.CardBg,
		}, nil)

		// 7. Full UI expand button pinned on right
		expandLabel := " [▲ FULL UI [H]] "
		expandLen := uint16(len([]rune(expandLabel)))
		if maxX > expandLen {
			expandX := maxX - expandLen
			drawHUDPill(buf, frame, expandX, rowY, maxX, expandLabel, cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Accent,
				Modifier: cell.ModifierBold,
			}, func() {
				ToggleCompactHUD()
				r.IsCompactMode = false
			})
		}
		return
	}

	// =========================================================================
	// CASE B: 2-LINE COMPACT HUD (inner.Height == 2)
	// =========================================================================
	if inner.Height == 2 {
		row1Y := inner.Y
		row2Y := inner.Y + 1

		// Row 1: Header / Connectivity Strip
		curX := inner.X

		// Brand
		curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, " 🍋 LIMONI ", cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Accent,
			Modifier: cell.ModifierBold,
		}, nil)
		curX++

		// Room Code
		roomPill := fmt.Sprintf(" 🔑 #%s ", node.RoomCode)
		curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, roomPill, cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}, func() {
			CopyToClipboard(node.RoomCode)
			r.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))
		})
		curX++

		// Lock pill
		if node.IsLocked {
			lockPill := " 🔒 LOCKED "
			if node.RoomPIN != "" && node.IsHost {
				lockPill = fmt.Sprintf(" 🔒 PIN: %s ", node.RoomPIN)
			}
			curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, lockPill, cell.Style{
				Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
				Bg:       theme.Danger,
				Modifier: cell.ModifierBold,
			}, nil)
			curX++
		}

		// Members
		memPill := fmt.Sprintf(" 👥 %d/4 ", totalMembers)
		curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, memPill, cell.Style{
			Fg: theme.Success,
			Bg: theme.CardBg,
		}, nil)
		curX++

		// Role
		if node.IsHost {
			curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, " 👑 HOST ", cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Warning,
				Modifier: cell.ModifierBold,
			}, nil)
			curX++
		}

		// Latency
		pingPill := " ⚡ -- "
		if peerPing > 0 {
			modeTag := "P2P"
			if allRelayed {
				modeTag = "Relay"
			} else if anyRelayed {
				modeTag = "Mesh"
			} else if hasLANPeer && !hasP2PPeer {
				modeTag = "LAN"
			}
			pingPill = fmt.Sprintf(" ⚡ %dms (%s) ", peerPing, modeTag)
		}
		drawHUDPill(buf, frame, curX, row1Y, maxX, pingPill, cell.Style{
			Fg: pingColor,
			Bg: theme.CardBg,
		}, nil)

		// Expand button right aligned
		expandLabel := " [▲ FULL UI [H]] "
		expandLen := uint16(len([]rune(expandLabel)))
		if maxX > expandLen {
			drawHUDPill(buf, frame, maxX-expandLen, row1Y, maxX, expandLabel, cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Accent,
				Modifier: cell.ModifierBold,
			}, func() {
				ToggleCompactHUD()
				r.IsCompactMode = false
			})
		}

		// Row 2: Controls & Speaker Strip
		curX = inner.X

		// Mic button
		var micLabel string
		var micStyle cell.Style
		if audio != nil && audio.Muted {
			micLabel = " 🔴 🎙️ MUTED [M] "
			micStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0xFF, 0xFF), Bg: theme.Danger, Modifier: cell.ModifierBold}
		} else if audio != nil && audio.IsSpeaking {
			micLabel = " 🟢 🎙️ SPEAKING [M] "
			micStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: cell.NewColorRGB(0x00, 0xFF, 0x88), Modifier: cell.ModifierBold}
		} else {
			micLabel = " 🎙️ MIC ON [M] "
			micStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Success, Modifier: cell.ModifierBold}
		}
		curX, _ = drawHUDPill(buf, frame, curX, row2Y, maxX, micLabel, micStyle, func() {
			if audio != nil {
				isMuted := audio.ToggleMute()
				node.SendMuteState(isMuted)
			}
		})
		curX++

		// Deafen button
		var deafLabel string
		var deafStyle cell.Style
		if audio != nil && audio.Deafened {
			deafLabel = " 🔇 DEAFENED [D] "
			deafStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Warning, Modifier: cell.ModifierBold}
		} else {
			deafLabel = " 🔊 AUDIO ON [D] "
			deafStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Secondary, Modifier: cell.ModifierBold}
		}
		curX, _ = drawHUDPill(buf, frame, curX, row2Y, maxX, deafLabel, deafStyle, func() {
			if audio != nil {
				isDeaf := audio.ToggleDeafen()
				node.SendDeafenState(isDeaf)
				node.SendMuteState(audio.Muted)
			}
		})
		curX++

		// Screen Share button
		var shareLabel string
		var shareStyle cell.Style
		var shareAction func()
		if node.IsSharingScreen {
			shareLabel = " 📺 SHARING [V] "
			shareStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold}
			shareAction = func() {
				_ = node.StopScreenShare()
				r.SetToast("Screen share stopped")
			}
		} else if node.IsWatchingScreen {
			shareLabel = " 📺 WATCHING [W] "
			shareStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Secondary, Modifier: cell.ModifierBold}
			shareAction = func() {
				_ = node.StopWatchingScreen()
				r.SetToast("Stream viewer closed")
			}
		} else if len(streamingPeers) > 0 {
			target := streamingPeers[0]
			shareLabel = fmt.Sprintf(" 🔴 WATCH %s [W] ", target.Nickname)
			shareStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0xFF, 0xFF), Bg: theme.Danger, Modifier: cell.ModifierBold}
			shareAction = func() {
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
				r.SetToast(fmt.Sprintf("Opening %s stream...", target.Nickname))
				go func() {
					_ = node.StartWatchingScreen(target.ID, port, opts)
				}()
			}
		} else {
			shareLabel = " 📺 SHARE [V] "
			shareStyle = cell.Style{Fg: theme.Text, Bg: theme.CardBg, Modifier: cell.ModifierBold}
			shareAction = func() {
				if r.OnOpenScreenShareModal != nil {
					r.OnOpenScreenShareModal()
				}
			}
		}
		curX, _ = drawHUDPill(buf, frame, curX, row2Y, maxX, shareLabel, shareStyle, shareAction)
		curX++

		// Live Voice Status / Speakers
		if curX < maxX {
			if len(speakingPeers) > 0 {
				spkText := fmt.Sprintf("🔊 Talking: ● %s", strings.Join(speakingPeers, ", "))
				buf.SetString(curX, row2Y, spkText, cell.Style{
					Fg:       theme.Accent,
					Bg:       theme.SurfaceBg,
					Modifier: cell.ModifierBold,
				})
			} else {
				buf.SetString(curX, row2Y, "💤 Voice: Idle", cell.Style{
					Fg: theme.TextMuted,
					Bg: theme.SurfaceBg,
				})
			}
		}
		return
	}

	// =========================================================================
	// CASE C: MULTI-ROW FULL MINI HUD (inner.Height >= 3)
	// =========================================================================
	row1Y := inner.Y
	row2Y := inner.Y + 1

	// --- ROW 1: Header / Connectivity Strip ---
	curX := inner.X

	// Brand
	curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, " 🍋 LIMONI VOICE ", cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Accent,
		Modifier: cell.ModifierBold,
	}, nil)
	curX++

	// Room Code Pill
	roomPill := fmt.Sprintf(" 🔑 ROOM #%s ", node.RoomCode)
	curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, roomPill, cell.Style{
		Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
		Bg:       theme.Warning,
		Modifier: cell.ModifierBold,
	}, func() {
		CopyToClipboard(node.RoomCode)
		r.SetToast(fmt.Sprintf("Room code copied: %s", node.RoomCode))
	})
	curX++

	// Lock Status Pill
	if node.IsLocked {
		lockPill := " 🔒 LOCKED "
		if node.RoomPIN != "" && node.IsHost {
			lockPill = fmt.Sprintf(" 🔒 PIN: %s ", node.RoomPIN)
		}
		curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, lockPill, cell.Style{
			Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
			Bg:       theme.Danger,
			Modifier: cell.ModifierBold,
		}, nil)
		curX++
	}

	// Member Count
	memPill := fmt.Sprintf(" 👥 %d/4 Members ", totalMembers)
	curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, memPill, cell.Style{
		Fg:       theme.Success,
		Bg:       theme.CardBg,
		Modifier: cell.ModifierBold,
	}, nil)
	curX++

	// Role
	if node.IsHost {
		curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, " 👑 Host (You) ", cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Warning,
			Modifier: cell.ModifierBold,
		}, nil)
		curX++
	} else {
		hostNick := node.HostNick
		if hostNick == "" {
			hostNick = "Host"
		}
		curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, fmt.Sprintf(" 👤 Host: %s ", hostNick), cell.Style{
			Fg: theme.Secondary,
			Bg: theme.CardBg,
		}, nil)
		curX++
	}

	// Latency
	pingPill := " ⚡ -- "
	if peerPing > 0 {
		modeTag := "P2P"
		if allRelayed {
			modeTag = "Relay"
		} else if anyRelayed {
			modeTag = "Mesh"
		} else if hasLANPeer && !hasP2PPeer {
			modeTag = "LAN"
		}
		pingPill = fmt.Sprintf(" ⚡ %dms (%s) ", peerPing, modeTag)
	}
	curX, _ = drawHUDPill(buf, frame, curX, row1Y, maxX, pingPill, cell.Style{
		Fg:       pingColor,
		Bg:       theme.CardBg,
		Modifier: cell.ModifierBold,
	}, nil)
	curX++

	// Port Hopping
	remHop := node.NextHopRemaining()
	var hopMin int
	if remHop > 0 {
		hopMin = int(remHop.Minutes())
	}
	portPill := fmt.Sprintf(" 🛡️ :%d (%dm) ", node.Port, hopMin)
	drawHUDPill(buf, frame, curX, row1Y, maxX, portPill, cell.Style{
		Fg: theme.Accent,
		Bg: theme.CardBg,
	}, func() {
		r.SetToast(fmt.Sprintf("Port Hopping: :%d (Next in %dm, Epoch %d)", node.Port, hopMin, node.currentEpoch))
	})

	// Right-aligned Full UI expand button
	expandLabel := " [▲ FULL UI [H]] "
	expandLen := uint16(len([]rune(expandLabel)))
	if maxX > expandLen {
		drawHUDPill(buf, frame, maxX-expandLen, row1Y, maxX, expandLabel, cell.Style{
			Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
			Bg:       theme.Accent,
			Modifier: cell.ModifierBold,
		}, func() {
			ToggleCompactHUD()
			r.IsCompactMode = false
		})
	}

	// --- ROW 2: Audio & Quick Action Capsules ---
	curX = inner.X

	// Mic button
	var micLabel string
	var micStyle cell.Style
	if audio != nil && audio.Muted {
		micLabel = " 🔴 🎙️ MIC MUTED [M] "
		micStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0xFF, 0xFF), Bg: theme.Danger, Modifier: cell.ModifierBold}
	} else if audio != nil && audio.IsSpeaking {
		micLabel = " 🟢 🎙️ SPEAKING [M] "
		micStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: cell.NewColorRGB(0x00, 0xFF, 0x88), Modifier: cell.ModifierBold}
	} else if audio != nil && audio.InputMode == InputModePushToTalk {
		micLabel = " 🎙️ PTT READY [SPACE] "
		micStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Warning, Modifier: cell.ModifierBold}
	} else {
		micLabel = " 🎙️ MIC ON [M] "
		micStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Success, Modifier: cell.ModifierBold}
	}
	curX, _ = drawHUDPill(buf, frame, curX, row2Y, maxX, micLabel, micStyle, func() {
		if audio != nil {
			isMuted := audio.ToggleMute()
			node.SendMuteState(isMuted)
			if isMuted {
				r.SetToast("Microphone Off (Muted)")
			} else {
				r.SetToast("Microphone Active")
			}
		}
	})
	curX++

	// Deafen button
	var deafLabel string
	var deafStyle cell.Style
	if audio != nil && audio.Deafened {
		deafLabel = " 🔇 DEAFENED [D] "
		deafStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Warning, Modifier: cell.ModifierBold}
	} else {
		deafLabel = " 🔊 AUDIO ON [D] "
		deafStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Secondary, Modifier: cell.ModifierBold}
	}
	curX, _ = drawHUDPill(buf, frame, curX, row2Y, maxX, deafLabel, deafStyle, func() {
		if audio != nil {
			isDeaf := audio.ToggleDeafen()
			node.SendDeafenState(isDeaf)
			node.SendMuteState(audio.Muted)
			if isDeaf {
				r.SetToast("Audio Deafened (All Sounds Muted)")
			} else {
				r.SetToast("Audio Restored")
			}
		}
	})
	curX++

	// Screen Share / Stream button
	var shareLabel string
	var shareStyle cell.Style
	var shareAction func()
	if node.IsSharingScreen {
		shareLabel = " 📺 SHARING [V] "
		shareStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold}
		shareAction = func() {
			_ = node.StopScreenShare()
			r.SetToast("Screen share stopped")
		}
	} else if node.IsWatchingScreen {
		shareLabel = " 📺 WATCHING [W] "
		shareStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Secondary, Modifier: cell.ModifierBold}
		shareAction = func() {
			_ = node.StopWatchingScreen()
			r.SetToast("Stream viewer closed")
		}
	} else if len(streamingPeers) > 0 {
		target := streamingPeers[0]
		shareLabel = fmt.Sprintf(" 🔴 WATCH %s [W] ", target.Nickname)
		shareStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0xFF, 0xFF), Bg: theme.Danger, Modifier: cell.ModifierBold}
		shareAction = func() {
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
			r.SetToast(fmt.Sprintf("Opening %s stream...", target.Nickname))
			go func() {
				_ = node.StartWatchingScreen(target.ID, port, opts)
			}()
		}
	} else {
		shareLabel = " 📺 SHARE [V] "
		shareStyle = cell.Style{Fg: theme.Text, Bg: theme.CardBg, Modifier: cell.ModifierBold}
		shareAction = func() {
			if r.OnOpenScreenShareModal != nil {
				r.OnOpenScreenShareModal()
			}
		}
	}
	curX, _ = drawHUDPill(buf, frame, curX, row2Y, maxX, shareLabel, shareStyle, shareAction)
	curX++

	// Noise Filter Pill
	if audio != nil {
		noisePill := fmt.Sprintf(" 🪄 %s [N] ", audio.SuppressionModeString())
		curX, _ = drawHUDPill(buf, frame, curX, row2Y, maxX, noisePill, cell.Style{
			Fg: theme.Accent,
			Bg: theme.CardBg,
		}, func() {
			audio.CycleSuppressionMode()
			r.SetToast(fmt.Sprintf("Noise Filter: %s", audio.SuppressionModeString()))
		})
		curX++
	}

	// Soundboard / SFX Pill
	curX, _ = drawHUDPill(buf, frame, curX, row2Y, maxX, " 🔔 SFX [F] ", cell.Style{
		Fg: theme.Secondary,
		Bg: theme.CardBg,
	}, func() {
		if r.OnTriggerSFX != nil {
			r.OnTriggerSFX()
		} else {
			r.SetToast("Soundboard: Press F to play sound effects")
		}
	})
	curX++

	// Leave Button
	drawHUDPill(buf, frame, curX, row2Y, maxX, " 🚪 LEAVE ", cell.Style{
		Fg: theme.Danger,
		Bg: theme.CardBg,
	}, func() {
		if r.OnLeave != nil {
			r.OnLeave()
		}
	})

	// =========================================================================
	// CASE C1: COMPACT 3-LINE SUMMARY (inner.Height == 3)
	// =========================================================================
	if inner.Height == 3 {
		row3Y := inner.Y + 2
		curX = inner.X

		var rms float64
		if audio != nil {
			rms = audio.LocalRMS
		}
		selfSpeaking := audio != nil && audio.IsSpeaking && !audio.Muted
		selfMuted := audio != nil && audio.Muted

		curX, _ = drawHUDPill(buf, frame, curX, row3Y, maxX, " 🎙️ VU: ", cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}, nil)
		curX = drawMiniVUBar(buf, curX, row3Y, maxX, rms, selfSpeaking, selfMuted, 10, theme)
		curX += 2

		if len(speakingPeers) > 0 {
			spkHeader := " 🔊 TALKING: "
			buf.SetString(curX, row3Y, spkHeader, cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Success,
				Modifier: cell.ModifierBold,
			})
			curX += uint16(len([]rune(spkHeader))) + 1

			for _, spk := range speakingPeers {
				spkTag := fmt.Sprintf(" ● %s ", spk)
				curX, _ = drawHUDPill(buf, frame, curX, row3Y, maxX, spkTag, cell.Style{
					Fg:       theme.Accent,
					Bg:       theme.CardBg,
					Modifier: cell.ModifierBold,
				}, nil)
				curX++
			}
		} else {
			idleText := " 💤 Voice: Idle "
			buf.SetString(curX, row3Y, idleText, cell.Style{
				Fg: theme.TextMuted,
				Bg: theme.SurfaceBg,
			})
		}
		return
	}

	// =========================================================================
	// CASE C2: STACKED PARTICIPANT ROWS & LIVE VU METERS (inner.Height >= 4)
	// =========================================================================
	curRowY := inner.Y + 2

	// --- ROW A: Local User (You) ---
	if curRowY < inner.Y+inner.Height {
		curX = inner.X

		// Avatar & Nickname
		var selfAvatar string
		var selfAvatarStyle cell.Style
		if audio != nil && audio.IsSpeaking && !audio.Muted {
			selfAvatar = " 🟢 ● You "
			selfAvatarStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: cell.NewColorRGB(0x00, 0xFF, 0x88), Modifier: cell.ModifierBold}
		} else if audio != nil && audio.Muted {
			selfAvatar = " 🔴 ● You "
			selfAvatarStyle = cell.Style{Fg: cell.NewColorRGB(0xFF, 0xFF, 0xFF), Bg: theme.Danger, Modifier: cell.ModifierBold}
		} else {
			selfAvatar = " ⚪ ● You "
			selfAvatarStyle = cell.Style{Fg: theme.Accent, Bg: theme.CardBg, Modifier: cell.ModifierBold}
		}
		curX, _ = drawHUDPill(buf, frame, curX, curRowY, maxX, selfAvatar, selfAvatarStyle, nil)
		curX++

		// Role badge
		roleTag := " [YOU] "
		if node.IsHost {
			roleTag = " [HOST] "
		}
		curX, _ = drawHUDPill(buf, frame, curX, curRowY, maxX, roleTag, cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}, nil)
		curX++

		// Self Live VU Equalizer Bar
		var selfRMS float64
		if audio != nil {
			selfRMS = audio.LocalRMS
		}
		selfSpeaking := audio != nil && audio.IsSpeaking && !audio.Muted
		selfMuted := audio != nil && audio.Muted

		curX, _ = drawHUDPill(buf, frame, curX, curRowY, maxX, "VU: ", cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}, nil)
		curX = drawMiniVUBar(buf, curX, curRowY, maxX, selfRMS, selfSpeaking, selfMuted, 8, theme)
		curX++

		// Status Badge
		var selfStatus string
		var selfStatusStyle cell.Style
		if selfMuted {
			selfStatus = " [🔇 MUTED] "
			selfStatusStyle = cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
		} else if selfSpeaking {
			selfStatus = " [🎙️ SPEAKING] "
			selfStatusStyle = cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}
		} else {
			selfStatus = " [🎙️ MIC ON] "
			selfStatusStyle = cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
		}
		curX, _ = drawHUDPill(buf, frame, curX, curRowY, maxX, selfStatus, selfStatusStyle, nil)
		curX++

		// Self Screen Share Badge
		if node.IsSharingScreen {
			drawHUDPill(buf, frame, curX, curRowY, maxX, " [📺 SHARING SCREEN [V]] ", cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Accent,
				Modifier: cell.ModifierBold,
			}, func() {
				_ = node.StopScreenShare()
				r.SetToast("Screen share stopped")
			})
		} else {
			drawHUDPill(buf, frame, curX, curRowY, maxX, " [📺 SHARE [V]] ", cell.Style{
				Fg: theme.Text,
				Bg: theme.CardBg,
			}, func() {
				if r.OnOpenScreenShareModal != nil {
					r.OnOpenScreenShareModal()
				}
			})
		}

		curRowY++
	}

	// --- ROW B+: Connected Peers (Stacked Lines with VU, Individual Volume Controls & Stream Watch) ---
	for _, p := range peers {
		if curRowY >= inner.Y+inner.Height {
			break
		}
		curX = inner.X
		targetPeer := p

		// 1. Peer Avatar & Speaking Dot
		var pAvatar string
		var pAvatarStyle cell.Style
		if p.Speaking && !p.IsMuted {
			pAvatar = fmt.Sprintf(" 🟢 ● %s ", p.Nickname)
			pAvatarStyle = cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: cell.NewColorRGB(0x00, 0xFF, 0x88), Modifier: cell.ModifierBold}
		} else if p.IsMuted {
			pAvatar = fmt.Sprintf(" 🔴 ● %s ", p.Nickname)
			pAvatarStyle = cell.Style{Fg: theme.Danger, Bg: theme.CardBg}
		} else {
			pAvatar = fmt.Sprintf(" ⚪ ● %s ", p.Nickname)
			pAvatarStyle = cell.Style{Fg: theme.Text, Bg: theme.CardBg}
		}
		curX, _ = drawHUDPill(buf, frame, curX, curRowY, maxX, pAvatar, pAvatarStyle, nil)
		curX++

		// 2. Peer Live VU Equalizer Bar
		curX, _ = drawHUDPill(buf, frame, curX, curRowY, maxX, "VU: ", cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}, nil)
		curX = drawMiniVUBar(buf, curX, curRowY, maxX, p.RMS, p.Speaking, p.IsMuted, 8, theme)
		curX++

		// 3. Individual Volume Controls ([-] [VOL%] [+])
		volVal := 1.0
		if audio != nil {
			volVal = audio.GetPeerVolume(targetPeer.ID)
		}
		volPct := int(math.Round(volVal * 100))

		// Volume [-] Button
		curX, _ = drawHUDPill(buf, frame, curX, curRowY, maxX, " [-] ", cell.Style{
			Fg:       theme.Secondary,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}, func() {
			if audio != nil {
				curV := audio.GetPeerVolume(targetPeer.ID)
				nextV := curV - 0.25
				if nextV < 0.0 {
					nextV = 0.0
				}
				audio.SetPeerVolume(targetPeer.ID, nextV)
				r.SetToast(fmt.Sprintf("Volume for %s: %d%%", targetPeer.Nickname, int(math.Round(nextV*100))))
			}
		})
		curX++

		// Volume Level / Mute Pill
		volLabel := fmt.Sprintf(" %d%% ", volPct)
		volStyle := cell.Style{Fg: theme.Text, Bg: theme.CardBg}
		if targetPeer.IsMuted || volPct == 0 {
			volLabel = " 🔇 MUTED "
			volStyle = cell.Style{Fg: theme.Danger, Bg: theme.CardBg, Modifier: cell.ModifierBold}
		} else if volPct > 100 {
			volStyle = cell.Style{Fg: theme.Accent, Bg: theme.CardBg, Modifier: cell.ModifierBold}
		}
		curX, _ = drawHUDPill(buf, frame, curX, curRowY, maxX, volLabel, volStyle, func() {
			if audio != nil {
				curV := int(math.Round(audio.GetPeerVolume(targetPeer.ID) * 100))
				var nextV float64
				switch {
				case curV >= 200:
					nextV = 0.0
				case curV == 0:
					nextV = 0.50
				case curV < 100:
					nextV = float64(curV+25) / 100.0
				default:
					nextV = float64(curV+25) / 100.0
					if nextV > 2.0 {
						nextV = 2.0
					}
				}
				audio.SetPeerVolume(targetPeer.ID, nextV)
				r.SetToast(fmt.Sprintf("Volume for %s set to %d%%", targetPeer.Nickname, int(math.Round(nextV*100))))
			}
		})
		curX++

		// Volume [+] Button
		curX, _ = drawHUDPill(buf, frame, curX, curRowY, maxX, " [+] ", cell.Style{
			Fg:       theme.Success,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}, func() {
			if audio != nil {
				curV := audio.GetPeerVolume(targetPeer.ID)
				nextV := curV + 0.25
				if nextV > 2.0 {
					nextV = 2.0
				}
				audio.SetPeerVolume(targetPeer.ID, nextV)
				r.SetToast(fmt.Sprintf("Volume for %s: %d%%", targetPeer.Nickname, int(math.Round(nextV*100))))
			}
		})
		curX++

		// 4. Stream Watch Button or Latency Indicator
		if targetPeer.IsSharingScreen {
			if node.IsWatchingScreen && node.WatchingPeerID == targetPeer.ID {
				drawHUDPill(buf, frame, curX, curRowY, maxX, " [📺 WATCHING [W]] ", cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Secondary,
					Modifier: cell.ModifierBold,
				}, func() {
					_ = node.StopWatchingScreen()
					r.SetToast("Stream viewer closed")
				})
			} else {
				drawHUDPill(buf, frame, curX, curRowY, maxX, " [🔴 WATCH LIVE [W]] ", cell.Style{
					Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
					Bg:       theme.Danger,
					Modifier: cell.ModifierBold,
				}, func() {
					port := targetPeer.VideoPort
					if port <= 0 {
						port = 50100
					}
					fps := targetPeer.VideoFPS
					if fps <= 0 {
						fps = 60
					}
					opts := screenshare.DefaultReceiverOptions(fps)
					opts.WindowTitle = fmt.Sprintf("Limoni Voice - %s Live Stream (%d FPS)", targetPeer.Nickname, fps)
					r.SetToast(fmt.Sprintf("Opening %s stream...", targetPeer.Nickname))
					go func() {
						_ = node.StartWatchingScreen(targetPeer.ID, port, opts)
					}()
				})
			}
		} else {
			pPing := int(targetPeer.PingMs)
			pPill := " ⚡ -- "
			if pPing > 0 {
				trans := "P2P"
				if targetPeer.ViaRelay {
					trans = "Relay"
				} else if targetPeer.Addr != nil && (targetPeer.Addr.IP.IsLoopback() || targetPeer.Addr.IP.IsPrivate()) {
					trans = "LAN"
				}
				pPill = fmt.Sprintf(" ⚡ %dms (%s) ", pPing, trans)
			}
			drawHUDPill(buf, frame, curX, curRowY, maxX, pPill, cell.Style{
				Fg: theme.TextMuted,
				Bg: theme.SurfaceBg,
			}, nil)
		}

		curRowY++
	}

	// --- ROW C: Toast Banner or Recent Chat message (if remaining vertical space) ---
	if curRowY < inner.Y+inner.Height {
		if r.ToastMsg != "" {
			toastText := fmt.Sprintf(" 🔔 %s ", r.ToastMsg)
			if uint16(len([]rune(toastText))) <= inner.Width {
				buf.SetString(inner.X+1, curRowY, toastText, cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Warning,
					Modifier: cell.ModifierBold,
				})
			}
		} else if len(r.Messages) > 0 {
			lastMsg := r.Messages[len(r.Messages)-1]
			chatText := fmt.Sprintf(" 💬 %s: %s", lastMsg.Sender, lastMsg.Text)
			if uint16(len([]rune(chatText))) > inner.Width-2 {
				chatRunes := []rune(chatText)
				if int(inner.Width) > 5 {
					chatText = string(chatRunes[:inner.Width-5]) + "..."
				}
			}
			buf.SetString(inner.X+1, curRowY, chatText, cell.Style{
				Fg: theme.TextMuted,
				Bg: theme.SurfaceBg,
			})
		}
	}
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// peerTransport labels the path audio to a peer takes: LAN, P2P, P2P-v6, Relay-UDP or Relay.
func peerTransport(node *P2PNode, peer *PeerInfo) string {
	if node == nil {
		return peer.Path(false)
	}
	return node.PeerPath(peer)
}
