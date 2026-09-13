package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thebanri/limoni-voice/internal/protocol"
	"github.com/thebanri/limoni-voice/internal/sysaudio"
	"github.com/thebanri/limoni-voice/internal/video"
	"github.com/thebanri/limoni-voice/internal/voice"
	"github.com/thebanri/limoni-voice/screenshare"
)

// Screen share transport (v2):
//
//   - Video goes only to members that watch (PacketScreenWatch keepalives every 2 s).
//   - The encoder's MPEG-TS is cut into ≤1128 byte chunks, sealed once, kept in a history for
//     retransmission and paced with a token bucket so keyframes do not burst the uplink.
//   - Viewers release chunks in order through a time based reorder buffer and NACK gaps.
//   - A new viewer first receives the chunks since the last keyframe (instant picture).
//   - Viewers report loss; the sharer steps the encoder bitrate down/up (encoder restart).
//   - System audio is Opus encoded and sent to watchers alongside the video.

const (
	watchKeepalive     = 2 * time.Second
	watchExpiry        = 7 * time.Second
	screenHistorySize  = 4096
	screenAudioBitrate = 64000
	relayAllMembers    = "@relay" // pacer recipient: one room broadcast through a non-targeting relay
)

// Process launchers (replaced in tests with synthetic encoders / headless players).
var (
	startCaptureSession = screenshare.StartBroadcasting
	startPlayerSession  = screenshare.StartReceiving
	// screenSendFilter, when set, can drop outgoing screen packets (loss injection in tests).
	screenSendFilter func(data []byte, to string, class byte) bool
)

// screenTx is the sharing side.
type screenTx struct {
	n *P2PNode

	mu         sync.Mutex
	session    *screenshare.Session
	conn       *net.UDPConn
	preset     screenshare.Preset
	opts       screenshare.BroadcastOptions
	seq        uint32
	chunker    video.Chunker
	history    *video.History
	pacer      *video.Pacer
	abr        *video.Controller
	watchers   map[string]*screenWatcher
	srcPort    int
	srcSeen    time.Time
	withAudio  bool
	audioOn    bool
	sys        sysaudio.Stream
	audioEnc   *voice.Encoder
	audioSeq   uint32
	silentRun  int
	restarting atomic.Bool
	stop       chan struct{}
	stopOnce   sync.Once

	sentChunks, sentBytes, retransmits uint64
}

type screenWatcher struct {
	since    time.Time
	lastSeen time.Time
	lossPct  float64
	lossAt   time.Time
}

// screenRx is the watching side.
type screenRx struct {
	n      *P2PNode
	peerID string
	opt    screenshare.ReceiverOptions

	mu       sync.Mutex
	reorder  *video.Reorder
	session  *screenshare.Session
	playerCh chan []byte
	lastData time.Time
	received uint64
	lost     uint64

	stop     chan struct{}
	stopOnce sync.Once
}

// ---------------------------------------------------------------------------------------------
// Sharing

// ScreenShareConfig selects what and how to share.
type ScreenShareConfig struct {
	TargetID    string
	Preset      int  // index into screenshare.Presets
	SystemAudio bool // share what the computer plays
}

// StartScreenShare starts sharing with explicit broadcast options (legacy entry point: default
// preset when no options are given, system audio per ShareSystemAudio).
func (n *P2PNode) StartScreenShare(targetIP string, targetPort int, customOpts ...screenshare.BroadcastOptions) error {
	n.mu.RLock()
	cfg := ScreenShareConfig{TargetID: "portal", Preset: n.ScreenPreset, SystemAudio: n.ShareSystemAudio}
	n.mu.RUnlock()
	preset := screenshare.PresetByIndex(cfg.Preset)
	opts := preset.Options(cfg.TargetID)
	if len(customOpts) > 0 {
		opts = customOpts[0]
		if opts.BitrateKbps == 0 {
			opts.BitrateKbps = parseKbps(opts.Bitrate, preset.Kbps)
		}
		preset.Kbps = opts.BitrateKbps
		preset.FPS = opts.FPS
	}
	return n.startScreenShare(opts, preset, cfg.SystemAudio)
}

// StartScreenShareWith starts sharing cfg.TargetID with a quality preset.
func (n *P2PNode) StartScreenShareWith(cfg ScreenShareConfig) error {
	preset := screenshare.PresetByIndex(cfg.Preset)
	n.mu.Lock()
	n.ScreenPreset = cfg.Preset
	n.ShareSystemAudio = cfg.SystemAudio
	n.mu.Unlock()
	return n.startScreenShare(preset.Options(cfg.TargetID), preset, cfg.SystemAudio)
}

func parseKbps(s string, fallback int) int {
	s = strings.TrimSpace(strings.ToLower(s))
	var v float64
	switch {
	case strings.HasSuffix(s, "m"):
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "m"), "%g", &v); err == nil {
			return int(v * 1000)
		}
	case strings.HasSuffix(s, "k"):
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "k"), "%g", &v); err == nil {
			return int(v)
		}
	}
	return fallback
}

func (n *P2PNode) startScreenShare(opts screenshare.BroadcastOptions, preset screenshare.Preset, withAudio bool) error {
	n.mu.Lock()
	if !n.IsConnected {
		n.mu.Unlock()
		return errors.New("cannot share screen while disconnected")
	}
	if n.IsSharingScreen && n.screenTx != nil {
		n.mu.Unlock()
		return nil
	}
	n.mu.Unlock()

	captureConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return fmt.Errorf("failed to open screen capture port: %w", err)
	}
	_ = captureConn.SetReadBuffer(4 * 1024 * 1024)
	localPort := captureConn.LocalAddr().(*net.UDPAddr).Port

	tx := &screenTx{
		n:         n,
		conn:      captureConn,
		preset:    preset,
		history:   video.NewHistory(screenHistorySize),
		abr:       video.NewController(preset.FloorKbps(), max(opts.BitrateKbps, preset.FloorKbps())),
		watchers:  map[string]*screenWatcher{},
		withAudio: withAudio,
		stop:      make(chan struct{}),
	}
	tx.pacer = video.NewPacer(pacerRate(tx.abr.Current, 1), tx.deliver)
	if withAudio {
		if enc, err := newScreenAudioEncoder(); err == nil {
			tx.audioEnc = enc
		}
		if runtime.GOOS == "darwin" {
			opts.OnAudio = tx.onSystemAudio
		}
	}
	tx.opts = opts

	session, err := startCaptureSession(context.Background(), "127.0.0.1", localPort, opts)
	if err != nil {
		_ = captureConn.Close()
		return err
	}
	tx.session = session

	if withAudio && tx.audioEnc != nil && runtime.GOOS != "darwin" {
		if n.audio != nil {
			n.audio.EnableLoopbackExclusion(true)
		}
		if s, err := sysaudio.Open(tx.onSystemAudio); err != nil {
			n.log(fmt.Sprintf("[WARN] [SHARE] System audio unavailable: %v", err))
		} else {
			tx.sys = s
			tx.audioOn = true
			n.debugLog("[SCREEN] [SHARE] System audio via " + s.Backend())
		}
	} else if withAudio && tx.audioEnc != nil {
		tx.audioOn = true // delivered by the capture helper
		if n.audio != nil {
			n.audio.EnableLoopbackExclusion(true)
		}
	}

	n.mu.Lock()
	n.screenTx = tx
	n.ScreenSharePort = localPort
	n.IsSharingScreen = true
	n.ActiveScreenShareFPS = opts.FPS
	n.mu.Unlock()

	go tx.pacer.Run()
	go tx.readLoop()
	go tx.controlLoop()
	go tx.watchSession(session)

	n.announceScreenShare(true)
	audioNote := ""
	if tx.audioOn {
		audioNote = " + system audio"
	}
	n.log(fmt.Sprintf("[SCREEN] Screen share started (%s, %s @ %d FPS, %d kbps%s)", preset.Name, opts.Resolution, opts.FPS, tx.abr.Current, audioNote))
	return nil
}

// pacerRate is the byte rate for a video bitrate: headroom for keyframes and retransmissions,
// multiplied by the number of separate recipients.
func pacerRate(kbps, recipients int) float64 {
	return float64(kbps) * 1000 / 8 * 1.6 * float64(max(recipients, 1))
}

func newScreenAudioEncoder() (*voice.Encoder, error) {
	return voice.NewMusicEncoder(AudioSampleRate, AudioFrameSamples, screenAudioBitrate)
}

func (n *P2PNode) announceScreenShare(sharing bool) {
	n.mu.RLock()
	room := n.roomID
	tx := n.screenTx
	fps := n.ActiveScreenShareFPS
	port := n.ScreenSharePort
	n.mu.RUnlock()
	pkt := P2PPacket{
		Type:            PacketScreenShareStop,
		RoomCode:        room,
		SenderID:        n.LocalID,
		Nickname:        n.Nickname,
		IsSharingScreen: sharing,
		Timestamp:       time.Now().UnixMilli(),
	}
	if sharing {
		pkt.Type = PacketScreenShareStart
		pkt.VideoFPS = fps
		pkt.VideoPort = port
		if tx != nil {
			tx.mu.Lock()
			pkt.VideoKbps = uint32(tx.abr.Current)
			pkt.HasAudio = tx.audioOn
			tx.mu.Unlock()
		}
	}
	n.broadcastToPeers(&pkt)
}

// StopScreenShare stops active broadcasting
func (n *P2PNode) StopScreenShare() error {
	n.mu.Lock()
	tx := n.screenTx
	wasSharing := n.IsSharingScreen
	n.screenTx = nil
	n.IsSharingScreen = false
	n.ActiveScreenShareFPS = 0
	n.ScreenSharePort = 0
	n.mu.Unlock()
	if tx == nil && !wasSharing {
		return nil
	}
	if tx != nil {
		tx.shutdown()
	}
	n.announceScreenShare(false)
	n.log("[SCREEN] Screen share stopped.")
	return nil
}

func (tx *screenTx) shutdown() {
	tx.stopOnce.Do(func() {
		close(tx.stop)
		tx.pacer.Stop()
		_ = tx.conn.Close()
		tx.mu.Lock()
		sys, session := tx.sys, tx.session
		tx.sys = nil
		tx.mu.Unlock()
		if sys != nil {
			_ = sys.Close()
		}
		if session != nil {
			_ = session.Stop()
		}
		if tx.n.audio != nil {
			tx.n.audio.EnableLoopbackExclusion(false)
		}
	})
}

func (tx *screenTx) watchSession(session *screenshare.Session) {
	select {
	case err := <-session.Err():
		tx.n.log(fmt.Sprintf("[WARN] Screen stream closed: %v", err))
	case <-session.Done():
		tx.n.log("[INFO] Screen stream ended.")
	case <-tx.stop:
		return
	}
	tx.n.mu.RLock()
	current := tx.n.screenTx == tx
	tx.n.mu.RUnlock()
	if current {
		_ = tx.n.StopScreenShare()
	}
}

// readLoop reads the encoder's MPEG-TS datagrams, chunks, seals and queues them.
func (tx *screenTx) readLoop() {
	buf := make([]byte, 65535)
	n := tx.n
	first := true
	for {
		size, addr, err := tx.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !tx.acceptSource(addr, buf[:size]) {
			continue
		}
		if first {
			first = false
			n.debugLog(fmt.Sprintf("[SCREEN] [SHARE] First video datagram captured (%d bytes)", size))
		}
		n.mu.RLock()
		keyring, room := n.keyring, n.roomID
		n.mu.RUnlock()
		if keyring == nil {
			continue
		}
		tx.mu.Lock()
		tx.chunker.Push(buf[:size], func(chunk []byte, keyframe bool) {
			tx.seq++
			pkt := P2PPacket{Type: PacketScreenShareData, RoomCode: room, SenderID: n.LocalID, Seq: tx.seq, Payload: chunk}
			sealed, err := sealPacket(&pkt, keyring)
			if err != nil {
				return
			}
			tx.history.Put(tx.seq, sealed, keyframe)
			if to := tx.recipientsLocked(); len(to) > 0 {
				tx.pacer.Enqueue(sealed, to, false)
				tx.sentChunks++
				tx.sentBytes += uint64(len(sealed) * len(to))
			}
		})
		tx.mu.Unlock()
	}
}

// acceptSource only takes whole MPEG-TS datagrams from the encoder's source port; another local
// process cannot inject data into the stream while the encoder is sending.
func (tx *screenTx) acceptSource(addr *net.UDPAddr, data []byte) bool {
	if addr == nil || !addr.IP.IsLoopback() || !video.ValidTS(data) {
		return false
	}
	now := time.Now()
	tx.mu.Lock()
	defer tx.mu.Unlock()
	switch {
	case tx.srcPort == addr.Port:
		tx.srcSeen = now
		return true
	case tx.srcPort == 0 || now.Sub(tx.srcSeen) > 250*time.Millisecond:
		// First datagram, or the encoder was restarted (new ephemeral port). Encoders write at
		// least every PCR interval (≤ 100 ms), so a live encoder never loses its lock.
		tx.srcPort, tx.srcSeen = addr.Port, now
		return true
	}
	return false
}

// recipientsLocked lists pacer recipients for the current watchers.
func (tx *screenTx) recipientsLocked() []string {
	if len(tx.watchers) == 0 {
		return nil
	}
	n := tx.n
	n.mu.RLock()
	defer n.mu.RUnlock()
	to := make([]string, 0, len(tx.watchers))
	viaRelay := false
	for id := range tx.watchers {
		peer, ok := n.Peers[id]
		if !ok {
			continue
		}
		if peer.Addr != nil && !peer.ViaRelay {
			to = append(to, id)
		} else if n.relayTargeted {
			to = append(to, id)
		} else {
			viaRelay = true
		}
	}
	if viaRelay {
		to = append(to, relayAllMembers)
	}
	return to
}

// deliver sends sealed screen data to one recipient (pacer callback).
func (tx *screenTx) deliver(data []byte, to string) {
	tx.n.sendScreenData(data, to, protocol.FrameBulk)
}

// sendScreenData routes a sealed packet to a member: direct UDP when reachable, otherwise the
// relay (targeted when supported).
func (n *P2PNode) sendScreenData(data []byte, to string, class byte) {
	if screenSendFilter != nil && !screenSendFilter(data, to, class) {
		return
	}
	if to == relayAllMembers {
		n.sendRelayFrame(class, data)
		return
	}
	n.mu.RLock()
	peer, ok := n.Peers[to]
	var addr *net.UDPAddr
	var conn *net.UDPConn
	viaRelay := true
	if ok {
		addr, conn, viaRelay = peer.Addr, peer.conn, peer.ViaRelay
	}
	targeted := n.relayTargeted
	n.mu.RUnlock()
	if !ok {
		return
	}
	if addr != nil && !viaRelay {
		n.writeUDP(data, addr, conn)
		return
	}
	if targeted {
		n.sendRelayTo(class, to, data)
	} else {
		n.sendRelayFrame(class, data)
	}
}

// sendScreenControl seals and sends a small control packet to the sharer / a viewer.
func (n *P2PNode) sendScreenControl(pkt *P2PPacket, to string) {
	n.mu.RLock()
	keyring := n.keyring
	pkt.RoomCode = n.roomID
	n.mu.RUnlock()
	pkt.SenderID = n.LocalID
	pkt.TargetID = to
	if pkt.Timestamp == 0 {
		pkt.Timestamp = time.Now().UnixMilli()
	}
	data, err := sealPacket(pkt, keyring)
	if err != nil {
		return
	}
	n.sendScreenData(data, to, protocol.FrameRealtime)
}

// controlLoop expires watchers, adapts the bitrate and keeps the pacer rate in line.
func (tx *screenTx) controlLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	n := tx.n
	tick := 0
	for {
		select {
		case <-tx.stop:
			return
		case <-ticker.C:
		}
		tick++
		now := time.Now()
		tx.mu.Lock()
		for id, w := range tx.watchers {
			if now.Sub(w.lastSeen) > watchExpiry {
				delete(tx.watchers, id)
				n.debugLog(fmt.Sprintf("[SCREEN] [SHARE] Viewer %s timed out", id))
			}
		}
		recipients := len(tx.recipientsLocked())
		var worstLoss float64
		for _, w := range tx.watchers {
			if now.Sub(w.lossAt) < 5*time.Second {
				worstLoss = math.Max(worstLoss, w.lossPct)
			}
		}
		watching := len(tx.watchers) > 0
		kbps := tx.abr.Current
		tx.mu.Unlock()

		tx.pacer.SetRate(pacerRate(kbps, recipients))
		if dropped := tx.pacer.Dropped(); dropped > 0 {
			n.debugLog(fmt.Sprintf("[SCREEN] [SHARE] Uplink congested: dropped %d stale chunks", dropped))
		}
		if tick%2 != 0 || !watching || tx.restarting.Load() {
			continue
		}
		tx.mu.Lock()
		next, change := tx.abr.Report(worstLoss, tx.pacer.QueueDelay(), now)
		tx.mu.Unlock()
		if change {
			go tx.applyBitrate(next, worstLoss)
		}
	}
}

// applyBitrate restarts the encoder at kbps.
func (tx *screenTx) applyBitrate(kbps int, loss float64) {
	if !tx.restarting.CompareAndSwap(false, true) {
		return
	}
	defer tx.restarting.Store(false)
	tx.mu.Lock()
	opts := tx.opts
	session := tx.session
	tx.mu.Unlock()
	if opts.BitrateKbps == kbps || session == nil {
		return
	}
	direction := "down"
	if kbps > opts.BitrateKbps {
		direction = "up"
	}
	opts.BitrateKbps = kbps
	tx.n.log(fmt.Sprintf("[SCREEN] Adapting stream %s to %d kbps (viewer loss %.1f%%)", direction, kbps, loss))
	if err := session.Restart(opts); err != nil {
		tx.n.log(fmt.Sprintf("[WARN] Screen encoder restart failed: %v", err))
		return
	}
	tx.mu.Lock()
	tx.opts = opts
	tx.srcPort = 0 // the new encoder sends from a new port
	tx.mu.Unlock()
	tx.n.announceScreenShare(true)
}

// onWatch registers / refreshes a viewer; a new viewer gets the current GOP first.
func (tx *screenTx) onWatch(viewer string, lossPct float64) {
	now := time.Now()
	tx.mu.Lock()
	w, existed := tx.watchers[viewer]
	if !existed {
		w = &screenWatcher{since: now}
		tx.watchers[viewer] = w
	}
	w.lastSeen = now
	if existed {
		w.lossPct, w.lossAt = lossPct, now
	}
	var replay [][]byte
	var to []string
	if !existed {
		_, replay = tx.history.SinceKeyframe()
		for _, r := range tx.recipientsLocked() {
			if r == viewer || r == relayAllMembers {
				to = append(to, r)
			}
		}
	}
	tx.mu.Unlock()
	if existed {
		return
	}
	tx.n.log(fmt.Sprintf("[SCREEN] %s started watching your screen", tx.n.peerNick(viewer)))
	for _, chunk := range replay {
		tx.pacer.Enqueue(chunk, to, false)
	}
}

func (tx *screenTx) onUnwatch(viewer string) {
	tx.mu.Lock()
	_, ok := tx.watchers[viewer]
	delete(tx.watchers, viewer)
	tx.mu.Unlock()
	if ok {
		tx.n.log(fmt.Sprintf("[SCREEN] %s stopped watching your screen", tx.n.peerNick(viewer)))
	}
}

func (tx *screenTx) onNack(viewer string, seqs []uint32) {
	tx.mu.Lock()
	_, watching := tx.watchers[viewer]
	var to []string
	if watching {
		for _, r := range tx.recipientsLocked() {
			if r == viewer || r == relayAllMembers {
				to = append(to, r)
			}
		}
	}
	tx.mu.Unlock()
	if !watching || len(to) == 0 {
		return
	}
	for _, s := range seqs {
		if data := tx.history.Get(s); data != nil {
			tx.pacer.Enqueue(data, to, true)
			tx.mu.Lock()
			tx.retransmits++
			tx.mu.Unlock()
		}
	}
}

// onSystemAudio encodes one 20 ms system audio frame and sends it to the watchers.
func (tx *screenTx) onSystemAudio(frame []int16) {
	n := tx.n
	select {
	case <-tx.stop:
		return
	default:
	}
	if n.audio != nil {
		frame = n.audio.CancelOwnPlayback(frame)
	}
	var energy float64
	for _, s := range frame {
		energy += float64(s) * float64(s)
	}
	silent := math.Sqrt(energy/float64(len(frame))) < 8 // about −72 dBFS
	tx.mu.Lock()
	if silent {
		tx.silentRun++
	} else {
		tx.silentRun = 0
	}
	// Keep sending briefly after sound stops so the decoder fades out, then pause (DTX).
	skip := tx.silentRun > 10 || len(tx.watchers) == 0 || tx.audioEnc == nil
	to := tx.recipientsLocked()
	enc := tx.audioEnc
	tx.audioSeq++
	seq := tx.audioSeq
	tx.mu.Unlock()
	if skip || len(to) == 0 {
		return
	}
	payload, err := enc.EncodeInt16(frame)
	if err != nil {
		return
	}
	n.mu.RLock()
	keyring, room := n.keyring, n.roomID
	n.mu.RUnlock()
	pkt := P2PPacket{Type: PacketScreenAudio, RoomCode: room, SenderID: n.LocalID, Seq: seq, Timestamp: time.Now().UnixMilli(), Payload: payload}
	sealed, err := sealPacket(&pkt, keyring)
	if err != nil {
		return
	}
	for _, r := range to {
		n.sendScreenData(sealed, r, protocol.FrameRealtime)
	}
}

func (n *P2PNode) peerNick(id string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if p, ok := n.Peers[id]; ok && p.Nickname != "" {
		return p.Nickname
	}
	return id
}

// ---------------------------------------------------------------------------------------------
// Watching

// StartWatchingScreen opens the native player for peerID's screen share and subscribes to it.
// port is unused (kept for API compatibility).
func (n *P2PNode) StartWatchingScreen(peerID string, port int, opts ...screenshare.ReceiverOptions) error {
	_ = n.StopWatchingScreen()

	n.mu.RLock()
	peer, ok := n.Peers[peerID]
	fps := 60
	nick := peerID
	hasAudio := false
	if ok {
		if peer.VideoFPS > 0 {
			fps = peer.VideoFPS
		}
		nick = peer.Nickname
		hasAudio = peer.ScreenAudio
	}
	n.mu.RUnlock()
	if !ok {
		return fmt.Errorf("peer %s is not in the room", peerID)
	}

	opt := screenshare.DefaultReceiverOptions(fps)
	if len(opts) > 0 {
		opt = opts[0]
	}
	rx := &screenRx{
		n:        n,
		peerID:   peerID,
		opt:      opt,
		reorder:  video.NewReorder(0),
		playerCh: make(chan []byte, 1024),
		stop:     make(chan struct{}),
	}
	session, err := startPlayerSession(context.Background(), opt)
	if err != nil {
		return err
	}
	rx.session = session

	n.mu.Lock()
	n.screenRx = rx
	n.IsWatchingScreen = true
	n.WatchingPeerID = peerID
	n.WatchingPeerNick = nick
	n.mu.Unlock()
	if n.audio != nil {
		n.audio.SetScreenAudioSource(peerID)
	}

	go rx.playerPump(session)
	go rx.loop()
	go rx.watchPlayer(session, time.Now())

	n.sendScreenControl(&P2PPacket{Type: PacketScreenWatch}, peerID)
	audioNote := ""
	if hasAudio {
		audioNote = " with system audio"
	}
	n.log(fmt.Sprintf("[VIEWER] Watching %s's screen%s (%d FPS).", nick, audioNote, opt.FPS))
	return nil
}

// StopWatchingScreen stops the active player
func (n *P2PNode) StopWatchingScreen() error {
	n.mu.Lock()
	rx := n.screenRx
	n.screenRx = nil
	wasWatching := n.IsWatchingScreen
	n.IsWatchingScreen = false
	n.WatchingPeerID = ""
	n.WatchingPeerNick = ""
	n.mu.Unlock()
	if rx == nil {
		return nil
	}
	rx.shutdown()
	n.sendScreenControl(&P2PPacket{Type: PacketScreenUnwatch}, rx.peerID)
	if n.audio != nil {
		n.audio.SetScreenAudioSource("")
	}
	if wasWatching {
		n.log("[VIEWER] Screen viewer closed.")
	}
	return nil
}

func (rx *screenRx) shutdown() {
	rx.stopOnce.Do(func() {
		close(rx.stop)
		rx.mu.Lock()
		session := rx.session
		rx.mu.Unlock()
		if session == nil {
			return
		}
		// End of stream first so the player can exit cleanly, then make sure it is gone.
		if stdin := session.Stdin(); stdin != nil {
			_ = stdin.Close()
		}
		select {
		case <-session.Done():
		case <-time.After(400 * time.Millisecond):
		}
		_ = session.Stop()
	})
}

// onData handles a video chunk from the watched sharer.
func (rx *screenRx) onData(seq uint32, payload []byte) {
	now := time.Now()
	rx.mu.Lock()
	rx.lastData = now
	ready := rx.reorder.Push(seq, payload, now)
	rx.mu.Unlock()
	rx.dispatch(ready)
}

func (rx *screenRx) dispatch(chunks [][]byte) {
	for _, c := range chunks {
		select {
		case rx.playerCh <- c:
		default:
			// Player cannot keep up: drop the backlog to get back to real time.
			for i := len(rx.playerCh) / 2; i > 0; i-- {
				select {
				case <-rx.playerCh:
				default:
				}
			}
			select {
			case rx.playerCh <- c:
			default:
			}
		}
	}
}

// loop drives gap timeouts, NACKs and the watch keepalive / loss report.
func (rx *screenRx) loop() {
	n := rx.n
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	lastReport := time.Now()
	for {
		select {
		case <-rx.stop:
			return
		case <-ticker.C:
		}
		now := time.Now()
		n.mu.RLock()
		rtt := time.Duration(0)
		if p, ok := n.Peers[rx.peerID]; ok {
			rtt = time.Duration(p.PingMs) * time.Millisecond
		}
		n.mu.RUnlock()

		rx.mu.Lock()
		rx.reorder.MaxWait = min(max(rtt*5/2+40*time.Millisecond, 80*time.Millisecond), 300*time.Millisecond)
		ready := rx.reorder.Tick(now)
		due := rx.reorder.NackDue(now, max(rtt*3/2, 30*time.Millisecond))
		var lossPct float64
		report := now.Sub(lastReport) >= watchKeepalive
		if report {
			// The sharer adapts to raw network loss (before retransmission): NACK recovery hides
			// loss from the picture, not from the congested link.
			recv, lost, recovered := rx.reorder.Stats()
			rx.received += recv
			rx.lost += lost
			if recv+lost > 0 {
				lossPct = 100 * float64(lost+recovered) / float64(recv+lost)
			}
			lastReport = now
		}
		rx.mu.Unlock()

		rx.dispatch(ready)
		for len(due) > 0 {
			k := min(len(due), protocol.MaxSeqList)
			n.sendScreenControl(&P2PPacket{Type: PacketScreenNack, Payload: protocol.AppendSeqList(nil, due[:k])}, rx.peerID)
			due = due[k:]
		}
		if report {
			n.sendScreenControl(&P2PPacket{Type: PacketScreenWatch, LossPct: uint8(math.Min(100, math.Round(lossPct)))}, rx.peerID)
		}
	}
}

// playerPump writes ordered chunks into the player's stdin, batching bursts.
func (rx *screenRx) playerPump(session *screenshare.Session) {
	stdin := session.Stdin()
	if stdin == nil {
		return
	}
	w := bufio.NewWriterSize(stdin, 256*1024)
	for {
		var chunk []byte
		select {
		case <-rx.stop:
			_ = stdin.Close()
			return
		case <-session.Done():
			return
		case chunk = <-rx.playerCh:
		}
		if !writeChunks(w, chunk, rx.playerCh) {
			rx.n.debugLog("[WARN] [WATCH] Player pipe closed")
			return
		}
	}
}

// writeChunks writes first and everything already queued, then flushes once.
func writeChunks(w *bufio.Writer, first []byte, queue chan []byte) bool {
	if _, err := w.Write(first); err != nil {
		return false
	}
	for i := 0; i < 256; i++ {
		select {
		case c := <-queue:
			if _, err := w.Write(c); err != nil {
				return false
			}
			continue
		default:
		}
		break
	}
	return w.Flush() == nil
}

// watchPlayer ends watching when the player window closes; a player that crashes right away is
// replaced by the alternative player.
func (rx *screenRx) watchPlayer(session *screenshare.Session, started time.Time) {
	n := rx.n
	select {
	case <-rx.stop:
		return
	case err := <-session.Err():
		n.log(fmt.Sprintf("[WARN] Screen viewer closed/error: %v", err))
		if time.Since(started) < 3*time.Second {
			alt := "ffplay"
			if strings.Contains(session.BinPath(), "ffplay") {
				alt = "mpv"
			}
			if _, altErr := screenshare.FindExecutable(alt); altErr == nil {
				n.log(fmt.Sprintf("[VIEWER] Player exited unexpectedly. Falling back to %s...", alt))
				opt := rx.opt
				opt.PreferredPlayer = alt
				if next, err := startPlayerSession(context.Background(), opt); err == nil {
					rx.mu.Lock()
					rx.session = next
					rx.reorder.Reset() // restart from the next keyframe
					rx.mu.Unlock()
					go rx.playerPump(next)
					go rx.watchPlayer(next, time.Now())
					return
				}
			}
		}
	case <-session.Done():
		n.log("[INFO] Screen viewer window closed.")
	}
	n.mu.RLock()
	current := n.screenRx == rx
	n.mu.RUnlock()
	if current {
		_ = n.StopWatchingScreen()
	}
}

// ---------------------------------------------------------------------------------------------
// Packet dispatch (called outside n.mu)

// handleScreenPacket processes screen share transport packets; it reports whether pkt was one.
func (n *P2PNode) handleScreenPacket(pkt *P2PPacket) bool {
	switch pkt.Type {
	case PacketScreenShareData, PacketScreenAudio, PacketScreenWatch, PacketScreenUnwatch, PacketScreenNack:
	default:
		return false
	}
	if pkt.SenderID == n.LocalID {
		return true
	}
	n.mu.RLock()
	validRoom := n.roomID != "" && strings.EqualFold(pkt.RoomCode, n.roomID) && n.IsConnected
	peer, known := n.Peers[pkt.SenderID]
	tx, rx := n.screenTx, n.screenRx
	var stale bool
	if known {
		stale = time.Since(peer.LastSeen) > time.Second
	}
	n.mu.RUnlock()
	if !validRoom || !known {
		return true
	}
	if stale {
		n.mu.Lock()
		if p, ok := n.Peers[pkt.SenderID]; ok {
			p.LastSeen = time.Now()
		}
		n.mu.Unlock()
	}

	switch pkt.Type {
	case PacketScreenShareData:
		if rx != nil && rx.peerID == pkt.SenderID {
			rx.onData(pkt.Seq, pkt.Payload)
		}
	case PacketScreenAudio:
		if rx != nil && rx.peerID == pkt.SenderID && n.audio != nil {
			n.audio.PlayScreenAudio(pkt.SenderID, pkt.Seq, pkt.Timestamp, pkt.Payload)
		}
	case PacketScreenWatch:
		if tx != nil && pkt.TargetID == n.LocalID {
			tx.onWatch(pkt.SenderID, float64(pkt.LossPct))
		}
	case PacketScreenUnwatch:
		if tx != nil && pkt.TargetID == n.LocalID {
			tx.onUnwatch(pkt.SenderID)
		}
	case PacketScreenNack:
		if tx != nil && pkt.TargetID == n.LocalID {
			if seqs, err := protocol.ParseSeqList(pkt.Payload); err == nil {
				tx.onNack(pkt.SenderID, seqs)
			}
		}
	}
	return true
}

// ScreenStats describes the local screen share / watch session for diagnostics.
type ScreenStats struct {
	Sharing     bool
	Preset      string
	Kbps        int
	Watchers    int
	QueueDelay  time.Duration
	Retransmits uint64
	Audio       bool

	Watching bool
	LossPct  float64
	MaxWait  time.Duration
}

// ScreenStats returns a snapshot of screen share statistics.
func (n *P2PNode) ScreenStats() ScreenStats {
	n.mu.RLock()
	tx, rx := n.screenTx, n.screenRx
	n.mu.RUnlock()
	var s ScreenStats
	if tx != nil {
		tx.mu.Lock()
		s.Sharing = true
		s.Preset = tx.preset.Name
		s.Kbps = tx.abr.Current
		s.Watchers = len(tx.watchers)
		s.Retransmits = tx.retransmits
		s.Audio = tx.audioOn
		tx.mu.Unlock()
		s.QueueDelay = tx.pacer.QueueDelay()
	}
	if rx != nil {
		rx.mu.Lock()
		s.Watching = true
		if rx.received+rx.lost > 0 {
			s.LossPct = 100 * float64(rx.lost) / float64(rx.received+rx.lost)
		}
		s.MaxWait = rx.reorder.MaxWait
		rx.mu.Unlock()
	}
	return s
}
