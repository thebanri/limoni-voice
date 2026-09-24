// Room view: participant cards, the members sidebar and the screen-share stage.

package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/p2p"
	"github.com/thebanri/limoni-voice/screenshare"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/layout"
	"github.com/thebanri/limoni/widgets"
)

// minCardHeight is the least height a member card needs to show more than its border.
const minCardHeight = 4

func (r *RoomView) renderClassicGrid(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine, peers []*p2p.PeerInfo) {
	if area.Height < 2*minCardHeight {
		r.renderMemberList(frame, area, node, audio, peers)
		return
	}
	rowFl := layout.NewFlexLayout(layout.Vertical, 0,
		layout.Percentage(50),
		layout.Percentage(50),
	)
	rowSplits := rowFl.Split(area)
	if len(rowSplits) < 2 {
		return
	}

	topRow := rowSplits[0]
	botRow := rowSplits[1]

	colFl := layout.NewFlexLayout(layout.Horizontal, 0,
		layout.Percentage(50),
		layout.Percentage(50),
	)
	topCols := colFl.Split(topRow)
	botCols := colFl.Split(botRow)
	if len(topCols) < 2 || len(botCols) < 2 {
		return
	}

	// Slot 0 (Top-Left): Local User (Self)
	r.renderLocalSlot(frame, topCols[0], node, audio)

	// Slot 1 (Top-Right): Peer 1
	if len(peers) > 0 {
		r.renderPeerSlot(frame, topCols[1], peers[0], node, audio, 2)
	} else {
		r.renderEmptySlot(frame, topCols[1], node.RoomCode, 2)
	}

	// Slot 2 (Bottom-Left): Peer 2
	if len(peers) > 1 {
		r.renderPeerSlot(frame, botCols[0], peers[1], node, audio, 3)
	} else {
		r.renderEmptySlot(frame, botCols[0], node.RoomCode, 3)
	}

	// Slot 3 (Bottom-Right): Peer 3
	if len(peers) > 2 {
		r.renderPeerSlot(frame, botCols[1], peers[2], node, audio, 4)
	} else {
		r.renderEmptySlot(frame, botCols[1], node.RoomCode, 4)
	}
}

func (r *RoomView) renderSidebarMembers(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine, peers []*p2p.PeerInfo) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         T(" VOICE CHANNEL MEMBERS "),
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.BorderFocused},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	// Keep rows laid out for more height inside the border.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer
		_ = buf
		// Calculate height per slot
		totalSlots := len(peers) + 1
		slotHeight := int(inner.Height) / totalSlots
		if slotHeight < 3 {
			slotHeight = 3
		}

		currY := inner.Y

		// 1. Self Slot
		selfCard := cell.Rect{X: inner.X, Y: currY, Width: inner.Width, Height: uint16(slotHeight)}
		r.renderMemberMiniCard(frame, selfCard, node.Nickname+T(" (YOU)"), audio.LocalRMS, audio.IsSpeaking, audio.Muted, audio.Deafened, node.IsSharingScreen, false, 0, false, true)
		currY += uint16(slotHeight)

		// 2. Peers Slots
		for _, peer := range peers {
			if currY+uint16(slotHeight) > inner.Y+inner.Height {
				break
			}
			peerCard := cell.Rect{X: inner.X, Y: currY, Width: inner.Width, Height: uint16(slotHeight)}
			isReconnecting := time.Since(peer.LastSeen) > 8000*time.Millisecond
			isBeingWatched := node.IsWatchingScreen && node.WatchingPeerID == peer.ID
			trans := peerTransport(node, peer)
			r.renderMemberMiniCard(frame, peerCard, peer.Nickname, peer.RMS, peer.Speaking, peer.IsMuted, peer.IsDeafened, peer.IsSharingScreen, isBeingWatched, peer.PingMs, isReconnecting, false, trans)

			if peer.IsSharingScreen {
				targetPeer := peer
				clickable(frame, peerCard, func(_ driver.MouseEvent) {
					port := targetPeer.VideoPort
					if port <= 0 {
						port = 50100
					}
					fps := targetPeer.VideoFPS
					if fps <= 0 {
						fps = 60
					}
					opts := screenshare.DefaultReceiverOptions(fps)
					opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", targetPeer.Nickname, fps)
					r.SetToast(fmt.Sprintf("Starting %s stream (%d FPS)...", targetPeer.Nickname, fps))
					go func() {
						err := node.StartWatchingScreen(targetPeer.ID, port, opts)
						if err != nil {
							r.SetToast(fmt.Sprintf("Error: %v", err))
						} else {
							r.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", targetPeer.Nickname, fps))
						}
					}()
				})
			}
			currY += uint16(slotHeight)
		}
	})
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalSec := int(d.Seconds())
	hours := totalSec / 3600
	minutes := (totalSec % 3600) / 60
	seconds := totalSec % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func (r *RoomView) renderMemberMiniCard(frame *terminal.Frame, area cell.Rect, name string, rms float64, isSpeaking, isMuted, isDeafened, isSharing, isBeingWatched bool, pingMs int64, isReconnecting bool, isSelf bool, trans ...string) {
	theme := CurrentTheme()
	buf := frame.Buffer

	// Icon & Color
	var icon string
	nameStyle := cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold}

	if isSelf {
		nameStyle.Fg = theme.Accent
	}

	if isReconnecting {
		icon = "[!]"
		nameStyle.Fg = theme.Warning
	} else if isBeingWatched {
		icon = "[W]"
		nameStyle.Fg = theme.Secondary
	} else if isSharing {
		icon = "[*]"
		nameStyle.Fg = theme.Accent
	} else if isSpeaking {
		icon = "●"
		nameStyle.Fg = theme.Success
	} else if isDeafened {
		icon = "[D]"
		nameStyle.Fg = theme.Warning
	} else if isMuted {
		icon = "[M]"
		nameStyle.Fg = theme.Danger
	} else {
		icon = "○"
	}

	titleText := fmt.Sprintf("%s %s", icon, name)
	if isBeingWatched {
		titleText += T(" [WATCHING]")
	} else if isSharing {
		titleText += T(" [LIVE]")
	}
	buf.SetString(area.X+1, area.Y, clipToWidth(titleText, int(area.Width)-1), nameStyle)

	// Status Line / Ping
	statusStr := ""
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg}
	if isReconnecting {
		statusStr = T("[Reconnecting...]")
		statusStyle.Fg = theme.Warning
	} else if isDeafened {
		statusStr = T("[Deafened]")
		statusStyle.Fg = theme.Warning
	} else if isMuted {
		statusStr = T("[Muted]")
		statusStyle.Fg = theme.Danger
	} else if isSpeaking {
		statusStr = T("[Speaking]")
		statusStyle.Fg = theme.Success
	} else {
		statusStr = T("[Connected]")
	}

	if pingMs > 0 {
		tTag := ""
		if len(trans) > 0 && trans[0] != "" {
			tTag = fmt.Sprintf(" (%s)", trans[0])
		}
		statusStr += fmt.Sprintf(" • %dms%s", pingMs, tTag)
	}

	if area.Height >= 2 {
		buf.SetString(area.X+2, area.Y+1, clipToWidth(statusStr, int(area.Width)-2), statusStyle)
	}

	// Audio Level bar at bottom of mini-card
	if area.Height >= 3 && area.Width > 6 {
		meterRect := cell.Rect{X: area.X + 2, Y: area.Y + 2, Width: area.Width - 4, Height: 1}
		DrawHorizontalLevelMeter(buf, meterRect, rms, isSpeaking, isMuted)
	}
}

func (r *RoomView) renderStreamStage(frame *terminal.Frame, area cell.Rect, streamingPeers []*p2p.PeerInfo, node *p2p.P2PNode) {
	theme := CurrentTheme()
	stageTitle := T(" LIVE STREAM STAGE ")
	borderCol := theme.Accent
	if !node.IsWatchingScreen && !node.IsSharingScreen {
		borderCol = theme.BorderFocused
	}

	block := widgets.Block{
		Title:         stageTitle,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: borderCol, Modifier: cell.ModifierBold},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)
	buf := frame.Buffer

	r.LastStageArea = inner

	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}

	// Keep rows laid out for more height inside the border.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer
		_ = buf
		// 1. Case: We are watching a peer's stream (Active in native HD player window)
		if node.IsWatchingScreen {
			watchedNick := node.WatchingPeerNick
			if watchedNick == "" {
				if p := node.GetPeer(node.WatchingPeerID); p != nil {
					watchedNick = p.Nickname
				} else if len(streamingPeers) > 0 {
					watchedNick = streamingPeers[0].Nickname
				} else {
					watchedNick = T("Stream")
				}
			}

			streamFPS := 60
			if p, ok := node.Peers[node.WatchingPeerID]; ok && p.VideoFPS > 0 {
				streamFPS = p.VideoFPS
			}
			topBarText := Tf(" %s'S LIVE STREAM ACTIVE (%d FPS) ", strings.ToUpper(watchedNick), streamFPS)
			buf.SetString(inner.X+3, inner.Y+1, clipToWidth(topBarText, int(inner.Width)-4), cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})

			msg1 := T("Playing in high-performance hardware-accelerated video window.")
			msg2 := T("Press [W] / [Esc] to close viewer, or click the stop button below.")
			buf.SetString(inner.X+3, inner.Y+3, clipToWidth(msg1, int(inner.Width)-4), cell.Style{Fg: theme.Success, Bg: theme.SurfaceBg})
			buf.SetString(inner.X+3, inner.Y+4, clipToWidth(msg2, int(inner.Width)-4), cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg})

			btnText := T("   [W] STOP WATCHING (Click)   ")
			btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Danger, Modifier: cell.ModifierBold}
			buf.SetString(inner.X+3, inner.Y+6, clipToWidth(btnText, int(inner.Width)-4), btnStyle)

			clickable(frame, cell.NewRect(inner.X+3, inner.Y+6, uint16(len([]rune(btnText))), 1), func(_ driver.MouseEvent) {
				_ = node.StopWatchingScreen()
				r.SetToast("Screen viewer closed")
			})

			// Show other streams in room to switch easily
			otherPeers := make([]*p2p.PeerInfo, 0)
			for _, p := range streamingPeers {
				if p.ID != node.WatchingPeerID {
					otherPeers = append(otherPeers, p)
				}
			}

			if len(otherPeers) > 0 {
				switchY := inner.Y + 8
				buf.SetString(inner.X+3, switchY, clipToWidth(T("Switch to another live stream:"), int(inner.Width)-4), cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
				switchY += 1
				for idx, p := range otherPeers {
					btnRowY := switchY + uint16(idx*2)
					if btnRowY >= inner.Y+inner.Height {
						break
					}
					targetPeer := p
					fps := targetPeer.VideoFPS
					if fps <= 0 {
						fps = 60
					}
					swBtnText := Tf("   ► Switch to %s's Stream (%d FPS)   ", p.Nickname, fps)
					swBtnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Secondary, Modifier: cell.ModifierBold}
					buf.SetString(inner.X+3, btnRowY, clipToWidth(swBtnText, int(inner.Width)-4), swBtnStyle)

					clickable(frame, cell.NewRect(inner.X+3, btnRowY, uint16(len([]rune(swBtnText))), 1), func(_ driver.MouseEvent) {
						port := targetPeer.VideoPort
						if port <= 0 {
							port = 50100
						}
						opts := screenshare.DefaultReceiverOptions(fps)
						opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", targetPeer.Nickname, fps)
						r.SetToast(fmt.Sprintf("Switching to %s...", targetPeer.Nickname))
						go func() {
							err := node.StartWatchingScreen(targetPeer.ID, port, opts)
							if err != nil {
								r.SetToast(fmt.Sprintf("Error: %v", err))
							} else {
								r.SetToast(fmt.Sprintf("Switched to %s (%d FPS)", targetPeer.Nickname, fps))
							}
						}()
					})
				}
			}
			return
		}

		// 2. Case: Local User is Broadcasting
		if node.IsSharingScreen {
			localFPS := node.ActiveScreenShareFPS
			if localFPS <= 0 {
				localFPS = 60
			}
			stats := node.ScreenStats()
			msg1 := Tf("YOUR SCREEN IS LIVE (%d FPS)", localFPS)
			msg2 := T("Nobody is watching yet: no video is uploaded until someone opens your stream.")
			if stats.Watchers > 0 {
				msg2 = Tf("%d viewer(s) · %s · %.1f Mbps (adapts to their connection)", stats.Watchers, stats.Preset, float64(stats.Kbps)/1000)
			}
			if stats.Audio {
				msg1 += T(" + SYSTEM AUDIO")
			}
			btnText := T("   [V] STOP BROADCAST (Click)   ")

			buf.SetString(inner.X+3, inner.Y+2, clipToWidth(msg1, int(inner.Width)-4), cell.Style{Fg: theme.Danger, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
			buf.SetString(inner.X+3, inner.Y+4, clipToWidth(msg2, int(inner.Width)-4), cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg})

			btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Danger, Modifier: cell.ModifierBold}
			buf.SetString(inner.X+3, inner.Y+6, clipToWidth(btnText, int(inner.Width)-4), btnStyle)

			clickable(frame, cell.NewRect(inner.X+3, inner.Y+6, uint16(len([]rune(btnText))), 1), func(_ driver.MouseEvent) {
				_ = node.StopScreenShare()
				r.SetToast("Screen share stopped")
			})

			// If other peers are ALSO broadcasting, allow watching them too
			if len(streamingPeers) > 0 {
				switchY := inner.Y + 9
				buf.SetString(inner.X+3, switchY, clipToWidth(T("Other Members Streaming in Room (Click to watch):"), int(inner.Width)-4), cell.Style{Fg: theme.Warning, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
				switchY += 1
				for idx, p := range streamingPeers {
					if switchY+uint16(idx*2) >= inner.Y+inner.Height {
						break
					}
					btnRowY := switchY + uint16(idx*2)
					swBtnText := Tf("   ► Watch %s's Stream   ", p.Nickname)
					swBtnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold}
					buf.SetString(inner.X+3, btnRowY, clipToWidth(swBtnText, int(inner.Width)-4), swBtnStyle)

					targetPeer := p
					clickable(frame, cell.NewRect(inner.X+3, btnRowY, uint16(len([]rune(swBtnText))), 1), func(_ driver.MouseEvent) {
						port := targetPeer.VideoPort
						if port <= 0 {
							port = 50100
						}
						fps := targetPeer.VideoFPS
						if fps <= 0 {
							fps = 60
						}
						opts := screenshare.DefaultReceiverOptions(fps)
						opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", targetPeer.Nickname, fps)
						r.SetToast(fmt.Sprintf("Starting %s stream...", targetPeer.Nickname))
						go func() {
							err := node.StartWatchingScreen(targetPeer.ID, port, opts)
							if err != nil {
								r.SetToast(fmt.Sprintf("Error: %v", err))
							} else {
								r.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", targetPeer.Nickname, fps))
							}
						}()
					})
				}
			}
			return
		}

		// 3. Case: One or More Peers are Broadcasting (Idle watcher)
		if len(streamingPeers) == 1 {
			p := streamingPeers[0]
			streamFPS := p.VideoFPS
			if streamFPS <= 0 {
				streamFPS = 60
			}
			msg1 := Tf("%s IS SHARING SCREEN (%d FPS)", strings.ToUpper(p.Nickname), streamFPS)
			msg2 := T("Click the button below to watch with 20ms ultra-low latency:")
			btnText := Tf("   ► [W] WATCH %s STREAM (Click)   ", strings.ToUpper(p.Nickname))

			buf.SetString(inner.X+3, inner.Y+2, clipToWidth(msg1, int(inner.Width)-4), cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
			buf.SetString(inner.X+3, inner.Y+4, clipToWidth(msg2, int(inner.Width)-4), cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg})

			btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold}
			buf.SetString(inner.X+3, inner.Y+6, clipToWidth(btnText, int(inner.Width)-4), btnStyle)

			clickable(frame, cell.NewRect(inner.X+3, inner.Y+6, uint16(len([]rune(btnText))), 1), func(_ driver.MouseEvent) {
				port := p.VideoPort
				if port <= 0 {
					port = 50100
				}
				opts := screenshare.DefaultReceiverOptions(streamFPS)
				opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", p.Nickname, streamFPS)
				r.SetToast("Starting stream viewer...")
				go func() {
					err := node.StartWatchingScreen(p.ID, port, opts)
					if err != nil {
						r.SetToast(fmt.Sprintf("Error: %v", err))
					} else {
						r.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", p.Nickname, streamFPS))
					}
				}()
			})
			return
		} else if len(streamingPeers) > 1 {
			msg1 := Tf("%d MEMBERS ARE SHARING SCREEN IN THIS ROOM", len(streamingPeers))
			msg2 := T("Select which member's live stream you want to watch:")

			buf.SetString(inner.X+3, inner.Y+2, clipToWidth(msg1, int(inner.Width)-4), cell.Style{Fg: theme.Accent, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
			buf.SetString(inner.X+3, inner.Y+3, clipToWidth(msg2, int(inner.Width)-4), cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg})

			listY := inner.Y + 5
			for idx, p := range streamingPeers {
				if listY+uint16(idx*2) >= inner.Y+inner.Height {
					break
				}
				btnRowY := listY + uint16(idx*2)
				targetPeer := p
				fps := targetPeer.VideoFPS
				if fps <= 0 {
					fps = 60
				}
				btnText := Tf("   ► WATCH %s'S LIVE STREAM (%d FPS)   ", strings.ToUpper(p.Nickname), fps)
				btnStyle := cell.Style{Fg: cell.NewColorRGB(0x00, 0x00, 0x00), Bg: theme.Accent, Modifier: cell.ModifierBold}
				buf.SetString(inner.X+3, btnRowY, clipToWidth(btnText, int(inner.Width)-4), btnStyle)

				clickable(frame, cell.NewRect(inner.X+3, btnRowY, uint16(len([]rune(btnText))), 1), func(_ driver.MouseEvent) {
					port := targetPeer.VideoPort
					if port <= 0 {
						port = 50100
					}
					opts := screenshare.DefaultReceiverOptions(fps)
					opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", targetPeer.Nickname, fps)
					r.SetToast(fmt.Sprintf("Starting %s stream...", targetPeer.Nickname))
					go func() {
						err := node.StartWatchingScreen(targetPeer.ID, port, opts)
						if err != nil {
							r.SetToast(fmt.Sprintf("Error: %v", err))
						} else {
							r.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", targetPeer.Nickname, fps))
						}
					}()
				})
			}
			return
		}
	})
}

func DrawHorizontalLevelMeter(buf *buffer.Buffer, area cell.Rect, rms float64, isSpeaking, isMuted bool) {
	if area.Width == 0 || area.Height == 0 {
		return
	}

	theme := CurrentTheme()
	filled := int(rms * float64(area.Width))
	if filled > int(area.Width) {
		filled = int(area.Width)
	}

	meterStyle := cell.Style{Fg: theme.WaveColor, Bg: theme.SurfaceBg}
	if isMuted {
		meterStyle.Fg = theme.Border
	} else if isSpeaking {
		meterStyle.Fg = theme.Success
	}

	for x := 0; x < int(area.Width); x++ {
		ch := ' '
		st := meterStyle
		if x < filled {
			ch = '━'
			st.Modifier = cell.ModifierBold
		}
		buf.SetCell(area.X+uint16(x), area.Y, cell.Cell{Content: ch, Style: st})
	}
}

func (r *RoomView) renderLocalSlot(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine) {
	theme := CurrentTheme()
	borderStyle := cell.Style{Fg: theme.BorderFocused}
	statusText := T("[LISTENING]")
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.CardBg}

	if audio.Deafened {
		borderStyle = cell.Style{Fg: theme.Warning}
		statusText = T("[DEAFENED]")
		statusStyle = cell.Style{Fg: theme.Warning, Bg: theme.CardBg}
	} else if audio.Muted {
		borderStyle = cell.Style{Fg: theme.Danger}
		statusText = T("[MIC OFF]")
		statusStyle = cell.Style{Fg: theme.Danger, Bg: theme.CardBg}
	} else if audio.InputMode == engine.InputModePushToTalk {
		if audio.IsTransmitting() {
			borderStyle = cell.Style{
				Fg:       theme.Success,
				Modifier: cell.ModifierBold,
			}
			statusText = T("[PTT TALKING...]")
			statusStyle = cell.Style{
				Fg:       theme.Success,
				Bg:       theme.CardBg,
				Modifier: cell.ModifierBold,
			}
		} else {
			borderStyle = cell.Style{Fg: theme.BorderFocused}
			statusText = T("[PTT IDLE (SPACE)]")
			statusStyle = cell.Style{
				Fg: theme.Warning,
				Bg: theme.CardBg,
			}
		}
	} else if audio.IsSpeaking {
		borderStyle = cell.Style{
			Fg:       theme.Success,
			Modifier: cell.ModifierBold,
		}
		statusText = T("[SPEAKING...]")
		statusStyle = cell.Style{
			Fg:       theme.Success,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}
	}

	title := Tf(" [1] %s (YOU) ", node.Nickname)
	block := widgets.Block{
		Title:         title,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   borderStyle,
		Style:         cell.Style{Bg: theme.CardBg},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.CardBg}})
		}
	}

	// Keep rows laid out for more height inside the border.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer
		_ = buf
		statusLabel := T("Status: ")
		buf.SetString(inner.X+1, inner.Y, statusLabel, cell.Style{Fg: theme.Text, Bg: theme.CardBg})
		buf.SetString(inner.X+1+uint16(len([]rune(statusLabel))), inner.Y, statusText, statusStyle)

		gainStr := Tf("Vol: %.0f%%", audio.Gain*100)
		statusEnd := 1 + len([]rune(statusLabel)) + len([]rune(statusText)) + 1
		if int(inner.Width) >= statusEnd+len([]rune(gainStr))+1 {
			buf.SetString(inner.X+inner.Width-uint16(len([]rune(gainStr)))-1, inner.Y, gainStr, cell.Style{Fg: theme.Secondary, Bg: theme.CardBg})
		}

		// If self is sharing screen, show live broadcasting banner inside the card
		if node.IsSharingScreen && inner.Height >= 5 {
			meterHeight := inner.Height - 4
			if meterHeight < 1 {
				meterHeight = 1
			}
			meterRect := cell.Rect{
				X:      inner.X + 1,
				Y:      inner.Y + 1,
				Width:  inner.Width - 2,
				Height: meterHeight,
			}
			DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, T("AUDIO LEVEL"))

			// Broadcast Banner
			bannerY := inner.Y + meterHeight + 1
			bannerW := inner.Width - 2
			bannerStyle := cell.Style{
				Fg:       theme.Danger,
				Bg:       theme.SurfaceBg,
				Modifier: cell.ModifierBold,
			}
			for bx := uint16(0); bx < bannerW; bx++ {
				buf.SetCell(inner.X+1+bx, bannerY, cell.Cell{Content: ' ', Style: bannerStyle})
				buf.SetCell(inner.X+1+bx, bannerY+1, cell.Cell{Content: ' ', Style: bannerStyle})
			}

			localFPS := node.ActiveScreenShareFPS
			if localFPS <= 0 {
				localFPS = 60
			}
			bTitle := Tf(" LIVE: Sharing Your Screen (%d FPS) ", localFPS)
			if uint16(len([]rune(bTitle))) > bannerW {
				bTitle = T(" LIVE STREAMING ")
			}
			buf.SetString(inner.X+2, bannerY, bTitle, bannerStyle)

			bAction := T("   [V] Stop Broadcast (Click)   ")
			bActionStyle := cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Danger,
				Modifier: cell.ModifierBold,
			}
			buf.SetString(inner.X+2, bannerY+1, bAction, bActionStyle)

			clickable(frame, cell.NewRect(inner.X+1, bannerY, bannerW, 2), func(_ driver.MouseEvent) {
				_ = node.StopScreenShare()
				r.SetToast("Screen share stopped")
			})

		} else if inner.Height >= 2 && inner.Width >= 4 {
			meterRect := cell.Rect{
				X:      inner.X + 1,
				Y:      inner.Y + 1,
				Width:  inner.Width - 2,
				Height: inner.Height - 1,
			}
			DrawVerticalLevelMeter(buf, meterRect, audio.LocalRMS, audio.IsSpeaking, audio.Muted, T("AUDIO LEVEL"))
		}
	})
}

func (r *RoomView) renderPeerSlot(frame *terminal.Frame, area cell.Rect, peer *p2p.PeerInfo, node *p2p.P2PNode, audio *engine.AudioEngine, slotNum int) {
	theme := CurrentTheme()
	borderStyle := cell.Style{Fg: theme.Border}
	statusText := T("[LISTENING]")
	statusStyle := cell.Style{Fg: theme.TextMuted, Bg: theme.CardBg}

	if peer.IsSharingScreen {
		borderStyle = cell.Style{
			Fg:       theme.Accent,
			Modifier: cell.ModifierBold,
		}
	}

	if time.Since(peer.LastSeen) > 3500*time.Millisecond {
		borderStyle = cell.Style{Fg: theme.Warning}
		statusText = T("[RECONNECTING...]")
		statusStyle = cell.Style{
			Fg:       theme.Warning,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}
	} else if peer.IsDeafened {
		borderStyle = cell.Style{Fg: theme.Warning}
		statusText = T("[DEAFENED]")
		statusStyle = cell.Style{Fg: theme.Warning, Bg: theme.CardBg}
	} else if peer.IsMuted {
		borderStyle = cell.Style{Fg: theme.Danger}
		statusText = T("[MIC OFF]")
		statusStyle = cell.Style{Fg: theme.Danger, Bg: theme.CardBg}
	} else if peer.Speaking {
		borderStyle = cell.Style{
			Fg:       theme.Success,
			Modifier: cell.ModifierBold,
		}
		statusText = T("[SPEAKING...]")
		statusStyle = cell.Style{
			Fg:       theme.Success,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		}
	}

	title := fmt.Sprintf(" [%d] %s ", slotNum, peer.Nickname)
	if peer.IsSharingScreen {
		title = Tf(" [%d] %s 🔴 [LIVE STREAMING] ", slotNum, peer.Nickname)
	}

	block := widgets.Block{
		Title:         title,
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   borderStyle,
		Style:         cell.Style{Bg: theme.CardBg},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.CardBg}})
		}
	}

	// Keep rows laid out for more height inside the border.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer
		_ = buf
		statusLabel := T("Status: ")
		buf.SetString(inner.X+1, inner.Y, statusLabel, cell.Style{Fg: theme.Text, Bg: theme.CardBg})
		buf.SetString(inner.X+1+uint16(len([]rune(statusLabel))), inner.Y, statusText, statusStyle)

		pingStr := T("PING: --")
		if peer.PingMs > 0 {
			pingStr = Tf("PING: %dms (%s)", peer.PingMs, peerTransport(node, peer))
			if peer.LossPct >= 1 {
				pingStr = Tf("PING: %dms %d%% loss (%s)", peer.PingMs, int(peer.LossPct), peerTransport(node, peer))
			}
		}
		volVal := 1.0
		if audio != nil {
			volVal = audio.GetPeerVolume(peer.ID)
		}
		volPct := int(math.Round(volVal * 100))
		volStr := Tf("[VOL: %d%%]", volPct)
		volLen := uint16(len([]rune(volStr)))
		pingLen := uint16(len([]rune(pingStr)))
		// Right-aligned pills never cover the status text: a narrow card drops the volume
		// pill first, then the ping.
		statusEnd := inner.X + 1 + uint16(len([]rune(statusLabel))) + uint16(len([]rune(statusText))) + 1
		pingX := int(inner.X) + int(inner.Width) - int(pingLen) - 1
		volX := pingX - int(volLen) - 1

		if volX >= int(statusEnd) {
			volX := uint16(volX)
			volStyle := cell.Style{
				Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
				Bg:       theme.Secondary,
				Modifier: cell.ModifierBold,
			}
			if volPct == 0 {
				volStyle = cell.Style{
					Fg:       cell.NewColorRGB(0xFF, 0xFF, 0xFF),
					Bg:       theme.Danger,
					Modifier: cell.ModifierBold,
				}
			} else if volPct > 100 {
				volStyle = cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Accent,
					Modifier: cell.ModifierBold,
				}
			}
			buf.SetString(volX, inner.Y, volStr, volStyle)
			clickable(frame, cell.NewRect(volX, inner.Y, volLen, 1), func(_ driver.MouseEvent) {
				if audio != nil {
					curV := int(math.Round(audio.GetPeerVolume(peer.ID) * 100))
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
					audio.SetPeerVolume(peer.ID, nextV)
					r.SetToast(fmt.Sprintf("Volume for %s set to %d%%", peer.Nickname, int(math.Round(nextV*100))))
				}
			})

			buf.SetString(uint16(pingX), inner.Y, pingStr, cell.Style{Fg: theme.Success, Bg: theme.CardBg})
		} else if pingX >= int(statusEnd) {
			buf.SetString(uint16(pingX), inner.Y, pingStr, cell.Style{Fg: theme.Success, Bg: theme.CardBg})
		}

		// Discord-style Stream Preview Banner if peer is sharing screen
		if peer.IsSharingScreen && inner.Height >= 5 {
			meterHeight := inner.Height - 4
			if meterHeight < 1 {
				meterHeight = 1
			}
			meterRect := cell.Rect{
				X:      inner.X + 1,
				Y:      inner.Y + 1,
				Width:  inner.Width - 2,
				Height: meterHeight,
			}
			DrawVerticalLevelMeter(buf, meterRect, peer.RMS, peer.Speaking, peer.IsMuted, T("AUDIO LEVEL"))

			// Stream Preview Card Box
			bannerY := inner.Y + meterHeight + 1
			bannerW := inner.Width - 2

			bannerBg := cell.Style{
				Fg:       theme.Accent,
				Bg:       theme.SurfaceBg,
				Modifier: cell.ModifierBold,
			}

			for bx := uint16(0); bx < bannerW; bx++ {
				buf.SetCell(inner.X+1+bx, bannerY, cell.Cell{Content: ' ', Style: bannerBg})
				buf.SetCell(inner.X+1+bx, bannerY+1, cell.Cell{Content: ' ', Style: bannerBg})
			}

			peerFPS := peer.VideoFPS
			if peerFPS <= 0 {
				peerFPS = 60
			}
			bTitle := Tf(" %s Sharing Screen (%d FPS)", peer.Nickname, peerFPS)
			if uint16(len([]rune(bTitle))) > bannerW {
				bTitle = Tf(" %s LIVE STREAM", peer.Nickname)
			}
			buf.SetString(inner.X+2, bannerY, bTitle, bannerBg)

			if node.IsWatchingScreen && node.WatchingPeerID == peer.ID {
				bBtnText := T("   [W] Stop Watching (Click)   ")
				bBtnStyle := cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Danger,
					Modifier: cell.ModifierBold,
				}
				buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
			} else if node.IsWatchingScreen {
				bBtnText := T("   ► Switch to Stream (Click)   ")
				bBtnStyle := cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Secondary,
					Modifier: cell.ModifierBold,
				}
				buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
			} else {
				bBtnText := T("   ► [W] WATCH STREAM (Click)   ")
				bBtnStyle := cell.Style{
					Fg:       cell.NewColorRGB(0x00, 0x00, 0x00),
					Bg:       theme.Accent,
					Modifier: cell.ModifierBold,
				}
				buf.SetString(inner.X+2, bannerY+1, bBtnText, bBtnStyle)
			}

			// Click on preview banner to watch / stop watching
			clickable(frame, cell.NewRect(inner.X+1, bannerY, bannerW, 2), func(_ driver.MouseEvent) {
				if node.IsWatchingScreen && node.WatchingPeerID == peer.ID {
					go func() {
						_ = node.StopWatchingScreen()
						r.SetToast("Screen viewer closed")
					}()
				} else {
					port := peer.VideoPort
					if port <= 0 {
						port = 50100
					}
					opts := screenshare.DefaultReceiverOptions(peerFPS)
					opts.WindowTitle = Tf("Limoni Voice - %s Live Stream (%d FPS)", peer.Nickname, peerFPS)
					r.SetToast("🎬 Starting stream viewer...")
					go func() {
						err := node.StartWatchingScreen(peer.ID, port, opts)
						if err != nil {
							r.SetToast(fmt.Sprintf("Error: %v", err))
						} else {
							r.SetToast(fmt.Sprintf("%s stream opened (%d FPS)", peer.Nickname, peerFPS))
						}
					}()
				}
			})

		} else if inner.Height >= 2 && inner.Width >= 4 {
			meterRect := cell.Rect{
				X:      inner.X + 1,
				Y:      inner.Y + 1,
				Width:  inner.Width - 2,
				Height: inner.Height - 1,
			}
			DrawVerticalLevelMeter(buf, meterRect, peer.RMS, peer.Speaking, peer.IsMuted, T("AUDIO LEVEL"))
		}
	})
}

func (r *RoomView) renderEmptySlot(frame *terminal.Frame, area cell.Rect, roomCode string, slotNum int) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         Tf(" [%d] EMPTY SLOT (WAITING) ", slotNum),
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.Border},
		Style:         cell.Style{Bg: theme.CardBg},
	}

	inner := block.Inner(area)
	frame.RenderWidget(block, area)

	buf := frame.Buffer
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.CardBg}})
		}
	}

	// Keep rows laid out for more height inside the border.
	drawClipped(frame, inner, func() {
		buf := frame.Buffer
		_ = buf
		txt1 := T("Invite Your Friend:")
		txt2 := Tf("Room Code: %s", roomCode)
		txt3 := T("Press [C] to copy the code")

		clickable(frame, area, func(_ driver.MouseEvent) {
			CopyToClipboard(roomCode)
			r.SetToast(fmt.Sprintf("Room Code Copied: %s", roomCode))
		})

		yCenter := inner.Y + inner.Height/2
		if yCenter > inner.Y+1 {
			yCenter--
		}

		buf.SetString(inner.X+2, yCenter, txt1, cell.Style{Fg: theme.TextMuted, Bg: theme.CardBg})
		buf.SetString(inner.X+2, yCenter+1, txt2, cell.Style{
			Fg:       theme.Warning,
			Bg:       theme.CardBg,
			Modifier: cell.ModifierBold,
		})
		buf.SetString(inner.X+2, yCenter+2, txt3, cell.Style{Fg: theme.Secondary, Bg: theme.CardBg})
	})
}

// renderMemberList is the member grid for a window too short for cards: one line a member.
func (r *RoomView) renderMemberList(frame *terminal.Frame, area cell.Rect, node *p2p.P2PNode, audio *engine.AudioEngine, peers []*p2p.PeerInfo) {
	theme := CurrentTheme()
	block := widgets.Block{
		Title:         Tf(" VOICE CHANNEL MEMBERS (%d/4) ", len(peers)+1),
		Borders:       widgets.BorderAll,
		BorderSymbols: widgets.SymbolsRounded,
		BorderStyle:   cell.Style{Fg: theme.BorderFocused},
		Style:         cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)
	if inner.Width < 6 || inner.Height == 0 {
		return
	}
	for y := inner.Y; y < inner.Y+inner.Height; y++ {
		for x := inner.X; x < inner.X+inner.Width; x++ {
			frame.Buffer.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}
	inner = cell.NewRect(inner.X+1, inner.Y, inner.Width-2, inner.Height)
	r.drawHUDMembers(frame, inner, inner.Y, int(inner.Height), node, audio, peers)
}
