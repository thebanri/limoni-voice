package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/thebanri/limoni-voice/internal/protocol"
	"github.com/thebanri/limoni-voice/internal/voice"
	"github.com/thebanri/limoni-voice/screenshare"
)

func (n *P2PNode) encoder() *voice.Encoder {
	n.voiceEncOnce.Do(func() {
		enc, err := voice.NewEncoder(AudioSampleRate, AudioFrameSamples)
		if err != nil {
			n.log(fmt.Sprintf("[ERROR] Opus encoder unavailable: %v", err))
			return
		}
		n.voiceEnc = enc
	})
	return n.voiceEnc
}

// SendAudio encodes one captured PCM frame with Opus and sends it to the room.
func (n *P2PNode) SendAudio(rms float64, speaking bool, pcm []byte) {
	n.mu.RLock()
	active := n.IsConnected && len(n.Peers) > 0
	n.mu.RUnlock()
	if !active {
		return
	}
	enc := n.encoder()
	if enc == nil {
		return
	}
	// Every captured frame is encoded (including silence kept for the pre-roll) so the encoder
	// state stays continuous when speech starts.
	frame, err := enc.EncodePCM16LE(pcm)
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()

	n.mu.Lock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.Unlock()
		return
	}
	room := n.roomID

	// DTX (Discontinuous Transmission) with Pre-Roll Lookback Cushion:
	// When silent, maintain a 3-frame (~60ms) ring buffer. When speech begins, flush the
	// pre-buffered frames immediately so word onsets are never clipped.
	var flushedPreRoll []audioPreRollFrame
	if speaking {
		if n.silenceHangover == 0 && len(n.audioPreRoll) > 0 {
			flushedPreRoll = n.audioPreRoll
			n.audioPreRoll = nil
		}
		n.silenceHangover = 25
	} else {
		if n.silenceHangover > 0 {
			n.silenceHangover--
		} else {
			n.audioPreRoll = append(n.audioPreRoll, audioPreRollFrame{rms: rms, frame: frame, ts: now})
			if len(n.audioPreRoll) > 3 {
				n.audioPreRoll = n.audioPreRoll[len(n.audioPreRoll)-3:]
			}
			n.mu.Unlock()
			return
		}
	}

	muted, deafened := false, false
	if n.audio != nil {
		muted, deafened = n.audio.Muted, n.audio.Deafened
	}
	pktsToSend := make([]P2PPacket, 0, len(flushedPreRoll)+1)
	for _, pre := range flushedPreRoll {
		n.audioSeq++
		pktsToSend = append(pktsToSend, P2PPacket{
			Type: PacketAudio, RoomCode: room, SenderID: n.LocalID,
			IsMuted: muted, IsDeafened: deafened, Speaking: true,
			RMS: pre.rms, Seq: n.audioSeq, Timestamp: pre.ts, Payload: pre.frame,
		})
	}
	n.audioSeq++
	pktsToSend = append(pktsToSend, P2PPacket{
		Type: PacketAudio, RoomCode: room, SenderID: n.LocalID,
		IsMuted: muted, IsDeafened: deafened, Speaking: speaking,
		RMS: rms, Seq: n.audioSeq, Timestamp: now, Payload: frame,
	})
	n.mu.Unlock()

	for i := range pktsToSend {
		n.sendAudioToPeers(&pktsToSend[i])
	}
}

// sendAudioToPeers sends a realtime packet over the relay and directly to every known peer
// address (redundant paths; receivers deduplicate by sequence number).
func (n *P2PNode) sendAudioToPeers(pkt *P2PPacket) {
	n.sendRedundant(pkt, protocol.FrameRealtime)
}

func (n *P2PNode) sendRedundant(pkt *P2PPacket, relayClass byte) {
	n.mu.RLock()
	keyring := n.keyring
	isRelay := n.isRelayConnected
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	if keyring == nil || (len(n.Peers) == 0 && !isRelay) {
		n.mu.RUnlock()
		return
	}
	type target struct {
		addr *net.UDPAddr
		conn *net.UDPConn
	}
	targets := make([]target, 0, len(n.Peers))
	for _, peer := range n.Peers {
		if peer.Addr != nil {
			targets = append(targets, target{peer.Addr, peer.conn})
		}
	}
	n.mu.RUnlock()

	data, err := sealPacket(pkt, keyring)
	if err != nil {
		return
	}
	if isRelay {
		n.sendRelayFrame(relayClass, data)
	}
	for _, t := range targets {
		n.writeUDP(data, t.addr, t.conn)
	}
}

// adaptEncoderToLoss raises Opus in-band FEC redundancy when receivers report packet loss.
func (n *P2PNode) adaptEncoderToLoss() {
	enc := n.voiceEnc
	if enc == nil {
		return
	}
	n.mu.RLock()
	worst := 0.0
	for _, p := range n.Peers {
		worst = max(worst, p.RemoteLossPct)
	}
	n.mu.RUnlock()
	hint := voice.DefaultLossHint
	if worst > 1 {
		hint = int(worst*1.5) + 2
	}
	enc.SetPacketLoss(hint)
}

func (n *P2PNode) SendScreenShareState(isSharing bool, videoPort int) {
	n.mu.RLock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.RUnlock()
		return
	}
	room := n.roomID
	n.mu.RUnlock()

	pktType := PacketScreenShareStop
	if isSharing {
		pktType = PacketScreenShareStart
	}
	pkt := P2PPacket{
		Type:            pktType,
		RoomCode:        room,
		SenderID:        n.LocalID,
		Nickname:        n.Nickname,
		LocalPort:       n.Port,
		IsSharingScreen: isSharing,
		VideoPort:       videoPort,
		Timestamp:       time.Now().UnixMilli(),
	}
	n.broadcastToPeers(&pkt)
}

// broadcastVideoPacket routes screen share chunks directly to reachable peers and through the
// relay only for relay-routed peers (no double streaming).
func (n *P2PNode) broadcastVideoPacket(pkt *P2PPacket) {
	n.mu.RLock()
	keyring := n.keyring
	isRelay := n.isRelayConnected
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	if keyring == nil || (len(n.Peers) == 0 && !isRelay) {
		n.mu.RUnlock()
		return
	}
	type target struct {
		addr *net.UDPAddr
		conn *net.UDPConn
	}
	var targets []target
	hasRelayPeer := false
	for _, peer := range n.Peers {
		if peer.ViaRelay || peer.Addr == nil {
			hasRelayPeer = true
		} else {
			targets = append(targets, target{peer.Addr, peer.conn})
		}
	}
	n.mu.RUnlock()

	data, err := sealPacket(pkt, keyring)
	if err != nil {
		return
	}
	for _, t := range targets {
		n.writeUDP(data, t.addr, t.conn)
	}
	if hasRelayPeer && isRelay {
		n.sendRelayFrame(protocol.FrameBulk, data)
	}
}

func (n *P2PNode) forwardVideoChunk(senderID string, payload []byte, seq uint32, nickname string) {
	if len(payload) == 0 {
		return
	}

	n.mu.RLock()
	watching := n.IsWatchingScreen
	watchingPeerID := n.WatchingPeerID
	peer, exists := n.Peers[senderID]
	needsUpdate := exists && (time.Since(peer.LastSeen) > 1*time.Second || !peer.IsSharingScreen)
	n.mu.RUnlock()

	if needsUpdate {
		n.mu.Lock()
		if p, ok := n.Peers[senderID]; ok {
			p.LastSeen = time.Now()
			p.IsSharingScreen = true
		}
		n.mu.Unlock()
	}

	// If we are watching a specific peer, ignore stream packets from other broadcasters
	if watching && watchingPeerID != "" && senderID != "" && senderID != watchingPeerID {
		return
	}

	readyChunks := n.videoReorder.Push(seq, payload)
	if len(readyChunks) == 0 {
		return
	}

	n.mu.Lock()
	// Pre-buffer up to 10 recent chunks so player gets PAT/PMT/SPS sync headers immediately on connect
	for _, chunk := range readyChunks {
		if len(chunk) > 0 {
			if len(n.videoPreBuf) < 10 {
				n.videoPreBuf = append(n.videoPreBuf, chunk)
			} else {
				copy(n.videoPreBuf, n.videoPreBuf[1:])
				n.videoPreBuf[len(n.videoPreBuf)-1] = chunk
			}
		}
	}
	watching = n.IsWatchingScreen
	playerCh := n.videoPlayerCh
	n.mu.Unlock()

	// Non-blocking dispatch to the dedicated player pump: receive loops never block on player writes.
	if watching && playerCh != nil {
		for _, chunk := range readyChunks {
			if len(chunk) == 0 {
				continue
			}
			select {
			case playerCh <- chunk:
			default:
				// Pump channel full: drain older backlog to snap back to real-time
				for i := len(playerCh) / 2; i > 0; i-- {
					select {
					case <-playerCh:
					default:
					}
				}
				select {
				case playerCh <- chunk:
				default:
				}
			}
		}
	}
}

// videoPlayerWritePump flushes video chunks to the player (mpv/ffplay) over local loopback TCP
// using batched writes, decoupled from the network receive thread.
func (n *P2PNode) videoPlayerWritePump(conn net.Conn, playerCh chan []byte, cancelCh chan struct{}) {
	pumpDone := make(chan struct{})
	defer func() {
		close(pumpDone)
		_ = conn.Close()
	}()

	if cancelCh != nil {
		go func() {
			select {
			case <-cancelCh:
				_ = conn.Close()
			case <-pumpDone:
			}
		}()
	}

	var batch net.Buffers
	for {
		select {
		case <-cancelCh:
			return
		case chunk, ok := <-playerCh:
			if !ok {
				return
			}
			if len(chunk) == 0 {
				continue
			}
			batch = append(batch[:0], chunk)

		drainLoop:
			for len(batch) < 64 {
				select {
				case nextChunk, ok := <-playerCh:
					if !ok {
						_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
						_, _ = batch.WriteTo(conn)
						return
					}
					if len(nextChunk) > 0 {
						batch = append(batch, nextChunk)
					}
				default:
					break drainLoop
				}
			}

			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := batch.WriteTo(conn); err != nil {
				n.debugLog(fmt.Sprintf("⚠️ [WATCH] Player TCP write error: %v", err))
				return
			}
		}
	}
}

// StartScreenShare starts capturing the screen and streams MPEG-TS chunks E2EE to the room.
func (n *P2PNode) StartScreenShare(targetIP string, targetPort int, customOpts ...screenshare.BroadcastOptions) error {
	n.mu.Lock()
	if !n.IsConnected {
		n.mu.Unlock()
		return errors.New("cannot share screen while disconnected")
	}
	if n.IsSharingScreen && n.screenSession != nil {
		n.mu.Unlock()
		return nil
	}
	n.mu.Unlock()

	// Listen on a dynamic free local UDP port for the capture process output
	captureConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return fmt.Errorf("failed to open screen capture port: %w", err)
	}
	_ = captureConn.SetReadBuffer(4 * 1024 * 1024)
	_ = captureConn.SetWriteBuffer(4 * 1024 * 1024)
	localAssignedPort := captureConn.LocalAddr().(*net.UDPAddr).Port

	opts := screenshare.DefaultBroadcastOptions()
	if len(customOpts) > 0 {
		opts = customOpts[0]
	}
	session, err := screenshare.StartBroadcasting(context.Background(), "127.0.0.1", localAssignedPort, opts)
	if err != nil {
		_ = captureConn.Close()
		return err
	}

	n.mu.Lock()
	n.ScreenSharePort = localAssignedPort
	n.screenSession = session
	n.videoCaptureConn = captureConn
	n.IsSharingScreen = true
	n.ActiveScreenShareFPS = opts.FPS
	roomID := n.roomID
	localID := n.LocalID
	nickname := n.Nickname
	n.mu.Unlock()

	startPkt := P2PPacket{
		Type:            PacketScreenShareStart,
		RoomCode:        roomID,
		SenderID:        localID,
		Nickname:        nickname,
		IsSharingScreen: true,
		VideoPort:       localAssignedPort,
		VideoFPS:        opts.FPS,
		Timestamp:       time.Now().UnixMilli(),
	}
	n.broadcastToPeers(&startPkt)
	n.log(fmt.Sprintf("[SCREEN] Screen share started (%s %d FPS - Internet)", opts.Resolution, opts.FPS))

	go func() {
		buf := make([]byte, 65535)
		var seq uint32
		var totalPackets int
		var totalBytes int64

		for {
			nBytes, _, err := captureConn.ReadFromUDP(buf)
			if err != nil || nBytes <= 0 {
				n.debugLog(fmt.Sprintf("[WARN] [SHARE] UDP capture read ended: %v", err))
				break
			}

			n.mu.RLock()
			sharing := n.IsSharingScreen
			currentRoom := n.roomID
			n.mu.RUnlock()
			if !sharing {
				break
			}

			seq++
			if seq == 0 {
				seq = 1
			}
			totalPackets++
			totalBytes += int64(nBytes)
			if totalPackets == 1 {
				n.debugLog(fmt.Sprintf("[SCREEN] [SHARE] First video chunk captured (%d bytes)! Broadcasting...", nBytes))
			} else if totalPackets%120 == 0 {
				n.debugLog(fmt.Sprintf("[SCREEN] [SHARE] Stream active: %d chunks (%d KB) sent", totalPackets, totalBytes/1024))
			}

			chunk := make([]byte, nBytes)
			copy(chunk, buf[:nBytes])
			vidPkt := P2PPacket{
				Type:     PacketScreenShareData,
				RoomCode: currentRoom,
				SenderID: localID,
				Seq:      seq,
				Payload:  chunk,
			}
			n.broadcastVideoPacket(&vidPkt)
		}
	}()

	go func() {
		select {
		case err := <-session.Err():
			n.log(fmt.Sprintf("[WARN] Screen stream closed: %v", err))
		case <-session.Done():
			n.log("[INFO] Screen stream ended.")
		}

		_ = captureConn.Close()

		n.mu.Lock()
		wasCurrent := n.screenSession == session
		if wasCurrent {
			n.IsSharingScreen = false
			n.ActiveScreenShareFPS = 0
			n.screenSession = nil
			n.videoCaptureConn = nil
		}
		room := n.roomID
		n.mu.Unlock()

		if wasCurrent {
			stopPkt := P2PPacket{
				Type:      PacketScreenShareStop,
				RoomCode:  room,
				SenderID:  localID,
				Nickname:  nickname,
				Timestamp: time.Now().UnixMilli(),
			}
			n.broadcastToPeers(&stopPkt)
		}
	}()

	return nil
}

// StopScreenShare stops active broadcasting
func (n *P2PNode) StopScreenShare() error {
	n.mu.Lock()
	if !n.IsSharingScreen || n.screenSession == nil {
		n.mu.Unlock()
		return nil
	}

	session := n.screenSession
	conn := n.videoCaptureConn
	n.screenSession = nil
	n.videoCaptureConn = nil
	n.IsSharingScreen = false
	n.ActiveScreenShareFPS = 0
	roomID := n.roomID
	n.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	if session != nil {
		_ = session.Stop()
	}

	stopPkt := P2PPacket{
		Type:      PacketScreenShareStop,
		RoomCode:  roomID,
		SenderID:  n.LocalID,
		Nickname:  n.Nickname,
		Timestamp: time.Now().UnixMilli(),
	}
	n.broadcastToPeers(&stopPkt)
	n.log("[SCREEN] Screen share stopped.")
	return nil
}

// StartWatchingScreen launches the native video receiver (mpv/ffplay) and feeds it decrypted
// stream chunks over a local TCP socket.
func (n *P2PNode) StartWatchingScreen(peerID string, port int, opts ...screenshare.ReceiverOptions) error {
	n.mu.Lock()
	if n.receiverSession != nil {
		prevSession := n.receiverSession
		prevLn := n.videoTCPListener
		prevConn := n.videoTCPConn
		prevCancel := n.videoPlayerCancel
		n.receiverSession = nil
		n.videoTCPListener = nil
		n.videoTCPConn = nil
		n.videoPlayerCh = nil
		n.videoPlayerCancel = nil
		n.mu.Unlock()
		if prevCancel != nil {
			select {
			case <-prevCancel:
			default:
				close(prevCancel)
			}
		}
		if prevConn != nil {
			_ = prevConn.Close()
		}
		if prevLn != nil {
			_ = prevLn.Close()
		}
		_ = prevSession.Stop()
		n.mu.Lock()
	}

	n.WatchingPeerID = peerID
	if p, ok := n.Peers[peerID]; ok {
		n.WatchingPeerNick = p.Nickname
	} else {
		n.WatchingPeerNick = ""
	}

	var opt screenshare.ReceiverOptions
	if len(opts) > 0 {
		opt = opts[0]
	} else {
		fps := 60
		if p, ok := n.Peers[peerID]; ok && p.VideoFPS > 0 {
			fps = p.VideoFPS
		}
		opt = screenshare.DefaultReceiverOptions(fps)
	}
	n.videoReorder.Reset()
	n.videoPreBuf = nil
	// Capacity 192 absorbs full 1080p 120 FPS keyframe bursts without dropping slices.
	playerCh := make(chan []byte, 192)
	cancelCh := make(chan struct{})
	n.videoPlayerCh = playerCh
	n.videoPlayerCancel = cancelCh
	n.mu.Unlock()

	tcpLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to open local TCP player socket: %w", err)
	}
	assignedTCPPort := tcpLn.Addr().(*net.TCPAddr).Port

	n.mu.Lock()
	n.IsWatchingScreen = true
	n.videoTCPListener = tcpLn
	n.lastVideoChunkTime = time.Now()
	n.mu.Unlock()

	go func() {
		conn, err := tcpLn.Accept()
		if err != nil {
			n.debugLog(fmt.Sprintf("[WARN] [WATCH] Player TCP accept error: %v", err))
			return
		}
		n.debugLog(fmt.Sprintf("[VIEWER] [WATCH] Player connected to internal TCP port %d", assignedTCPPort))
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetNoDelay(true)
			_ = tcp.SetWriteBuffer(64 * 1024)
			_ = tcp.SetReadBuffer(64 * 1024)
		}
		n.mu.Lock()
		if n.IsWatchingScreen {
			n.videoTCPConn = conn
			preBuf := append([][]byte(nil), n.videoPreBuf...)
			activePlayerCh := n.videoPlayerCh
			activeCancelCh := n.videoPlayerCancel
			n.mu.Unlock()

			// Flush pre-buffered chunks so the player receives sync headers instantly
			for _, chunk := range preBuf {
				if len(chunk) > 0 {
					_, _ = conn.Write(chunk)
				}
			}
			go n.videoPlayerWritePump(conn, activePlayerCh, activeCancelCh)
		} else {
			n.mu.Unlock()
			_ = conn.Close()
		}
	}()

	session, err := screenshare.StartReceiving(context.Background(), assignedTCPPort, opt)
	if err != nil {
		n.mu.Lock()
		n.IsWatchingScreen = false
		n.WatchingPeerID = ""
		n.WatchingPeerNick = ""
		n.videoTCPListener = nil
		cancelToClose := n.videoPlayerCancel
		n.videoPlayerCh = nil
		n.videoPlayerCancel = nil
		n.mu.Unlock()
		if cancelToClose != nil {
			select {
			case <-cancelToClose:
			default:
				close(cancelToClose)
			}
		}
		_ = tcpLn.Close()
		return err
	}

	n.mu.Lock()
	n.receiverSession = session
	n.mu.Unlock()

	n.log(fmt.Sprintf("[VIEWER] Live screen stream viewer window opened (%d FPS).", opt.FPS))
	go n.monitorViewerSession(session, assignedTCPPort, opt)
	return nil
}

func (n *P2PNode) monitorViewerSession(curSession *screenshare.Session, assignedPort int, curOpt screenshare.ReceiverOptions) {
	startTime := time.Now()
	select {
	case err := <-curSession.Err():
		n.log(fmt.Sprintf("[WARN] Screen viewer closed/error: %v", err))

		// Auto-fallback: if the player crashed on startup, try the alternate player
		if time.Since(startTime) < 3*time.Second {
			n.mu.Lock()
			stillWatching := n.IsWatchingScreen && n.receiverSession == curSession
			n.mu.Unlock()
			if stillWatching {
				altPlayer := "ffplay"
				if strings.Contains(curSession.BinPath(), "ffplay") {
					altPlayer = "mpv"
				}
				if _, altErr := screenshare.FindExecutable(altPlayer); altErr == nil {
					n.log(fmt.Sprintf("[VIEWER] Player exited unexpectedly. Automatically falling back to %s...", altPlayer))
					curOpt.PreferredPlayer = altPlayer
					newSession, sErr := screenshare.StartReceiving(context.Background(), assignedPort, curOpt)
					if sErr == nil {
						n.mu.Lock()
						if n.IsWatchingScreen {
							n.receiverSession = newSession
							n.mu.Unlock()
							go n.monitorViewerSession(newSession, assignedPort, curOpt)
							return
						}
						n.mu.Unlock()
						_ = newSession.Stop()
					}
				}
			}
		}
	case <-curSession.Done():
		n.log("[INFO] Screen viewer window closed.")
	}

	n.mu.Lock()
	if n.receiverSession == curSession {
		n.IsWatchingScreen = false
		n.WatchingPeerID = ""
		n.WatchingPeerNick = ""
		n.receiverSession = nil
		cancelCh := n.videoPlayerCancel
		n.videoPlayerCh = nil
		n.videoPlayerCancel = nil
		if cancelCh != nil {
			select {
			case <-cancelCh:
			default:
				close(cancelCh)
			}
		}
		if n.videoTCPConn != nil {
			_ = n.videoTCPConn.Close()
			n.videoTCPConn = nil
		}
		if n.videoTCPListener != nil {
			_ = n.videoTCPListener.Close()
			n.videoTCPListener = nil
		}
	}
	n.mu.Unlock()
}

// StopWatchingScreen stops the active player
func (n *P2PNode) StopWatchingScreen() error {
	n.mu.Lock()
	session := n.receiverSession
	conn := n.videoTCPConn
	ln := n.videoTCPListener
	cancelCh := n.videoPlayerCancel
	wasWatching := n.IsWatchingScreen
	n.receiverSession = nil
	n.videoTCPConn = nil
	n.videoTCPListener = nil
	n.videoPlayerCh = nil
	n.videoPlayerCancel = nil
	n.IsWatchingScreen = false
	n.WatchingPeerID = ""
	n.WatchingPeerNick = ""
	n.lastVideoChunkTime = time.Time{}
	n.videoPreBuf = nil
	n.videoReorder.Reset()
	n.mu.Unlock()

	if cancelCh != nil {
		select {
		case <-cancelCh:
		default:
			close(cancelCh)
		}
	}
	if conn != nil {
		_ = conn.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if session != nil {
		_ = session.Stop()
	}

	if wasWatching || session != nil {
		n.log("[VIEWER] Screen viewer closed.")
	}
	return nil
}
