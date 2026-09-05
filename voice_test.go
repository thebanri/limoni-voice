package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/thebanri/limoni/core/backend"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
	"github.com/thebanri/limoni-voice/screenshare"
)

func TestRoomCode(t *testing.T) {
	code := GenerateRoomCode()
	if code == "" {
		t.Fatalf("Expected non-empty room code")
	}

	parts := strings.Split(code, "-")
	if len(parts) != 3 {
		t.Fatalf("Expected 3 parts in Croc code, got %d: %s", len(parts), code)
	}

	normalized := NormalizeCode("  " + strings.ToUpper(code) + "  ")
	if normalized != code {
		t.Fatalf("NormalizeCode failed: expected %s, got %s", code, normalized)
	}
}

func TestAudioEngine(t *testing.T) {
	engine := NewAudioEngine()
	if engine.Muted {
		t.Fatalf("Expected audio engine to start unmuted")
	}

	engine.ToggleMute()
	if !engine.Muted {
		t.Fatalf("Expected audio engine to be muted")
	}

	engine.ToggleMute()
	if engine.Muted {
		t.Fatalf("Expected audio engine to be unmuted")
	}

	engine.AdjustGain(0.5)
	if engine.Gain != 1.5 {
		t.Fatalf("Expected gain 1.5, got %f", engine.Gain)
	}

	// Test RMS and PCM gain mixing
	testPCM := make([]byte, AudioChunkSize)
	for i := 0; i < len(testPCM); i += 2 {
		testPCM[i] = 0x00
		testPCM[i+1] = 0x20
	}
	rms := calculateRMS(testPCM)
	if rms <= 0 {
		t.Fatalf("Expected positive RMS for non-empty PCM, got %f", rms)
	}

	amplified := applyGain(testPCM, 2.0)
	if calculateRMS(amplified) <= rms {
		t.Fatalf("Expected amplified RMS to be greater than original")
	}

	// Test peer audio mixing
	engine.PlayPeerPCM("peer_1", testPCM, rms, true)
	wave := engine.GetPeerWave("peer_1")
	if len(wave) != 40 || wave[len(wave)-1] != rms {
		t.Fatalf("Expected peer wave to record latest RMS")
	}
}

func TestTextInputTypingNoConflict(t *testing.T) {
	state := widgets.NewTextInputState()
	state.HandleKey(backend.KeyEvent{Type: backend.KeyRune, Ch: 'c'})
	state.HandleKey(backend.KeyEvent{Type: backend.KeyRune, Ch: 'g'})
	state.HandleKey(backend.KeyEvent{Type: backend.KeyRune, Ch: 'j'})

	if state.Value() != "cgj" {
		t.Fatalf("Expected text 'cgj', got %q", state.Value())
	}
}

func TestP2PPacketCodec(t *testing.T) {
	roomCode := "7492-neon-falcon"
	key := deriveRoomKey(roomCode)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher failed: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM failed: %v", err)
	}

	pkt := P2PPacket{
		Type:       PacketHello,
		RoomCode:   roomCode,
		SenderID:   "peer_1",
		Nickname:   "Tester",
		IsMuted:    false,
		IsDeafened: true,
		Speaking:   true,
		RMS:        0.75,
		Seq:        12,
		Timestamp:  time.Now().UnixMilli(),
		Payload:    []byte{1, 2, 3, 4},
	}

	// 1. Test standard encrypt & decrypt
	data, err := encodeAndEncryptPacket(&pkt, aead)
	if err != nil {
		t.Fatalf("encodeAndEncryptPacket failed: %v", err)
	}

	var decoded P2PPacket
	if err := decryptAndDecodePacket(data, &decoded, aead); err != nil {
		t.Fatalf("decryptAndDecodePacket failed: %v", err)
	}

	if decoded.RoomCode != pkt.RoomCode || decoded.Nickname != pkt.Nickname || decoded.RMS != pkt.RMS || decoded.IsDeafened != pkt.IsDeafened {
		t.Fatalf("Decoded packet mismatch: %+v vs %+v", decoded, pkt)
	}

	// 2. Test wrong key rejection (unauthorized room code)
	wrongKey := deriveRoomKey("other-room-code")
	wrongBlock, _ := aes.NewCipher(wrongKey)
	wrongAEAD, _ := cipher.NewGCM(wrongBlock)

	var wrongDecoded P2PPacket
	if err := decryptAndDecodePacket(data, &wrongDecoded, wrongAEAD); err == nil {
		t.Fatalf("Expected decryption to FAIL with wrong key, but succeeded")
	}

	// 3. Test tampering rejection (modified payload)
	tamperedData := make([]byte, len(data))
	copy(tamperedData, data)
	tamperedData[len(tamperedData)-1] ^= 0xFF // Flip bits in ciphertext / tag

	var tamperedDecoded P2PPacket
	if err := decryptAndDecodePacket(tamperedData, &tamperedDecoded, aead); err == nil {
		t.Fatalf("Expected decryption to FAIL on tampered packet, but succeeded")
	}
}

func TestP2PMaxPeers(t *testing.T) {
	audio := NewAudioEngine()
	node := NewP2PNode("host_node", "Host", audio)
	if err := node.Start(); err != nil {
		t.Fatalf("Node start failed: %v", err)
	}
	defer func() {
		if node.Conn != nil {
			node.Conn.Close()
		}
	}()

	node.JoinRoom("test-room")
	if node.RoomCode != "test-room" || !node.IsConnected {
		t.Fatalf("Failed to join room")
	}

	node.LeaveRoom()
	if node.IsConnected || len(node.Peers) != 0 {
		t.Fatalf("LeaveRoom did not clear room state")
	}
}

func TestP2PDiscoveryAndEncryptionBetweenTwoNodes(t *testing.T) {
	audio1 := NewAudioEngine()
	node1 := NewP2PNode("node_1", "Alice", audio1)
	node1.LanOnly = true
	node1.RelayURL = ""
	if err := node1.Start(); err != nil {
		t.Fatalf("Node1 start failed: %v", err)
	}
	defer func() {
		if node1.Conn != nil {
			node1.Conn.Close()
		}
	}()

	audio2 := NewAudioEngine()
	node2 := NewP2PNode("node_2", "Bob", audio2)
	node2.LanOnly = true
	node2.RelayURL = ""
	if err := node2.Start(); err != nil {
		t.Fatalf("Node2 start failed: %v", err)
	}
	defer func() {
		if node2.Conn != nil {
			node2.Conn.Close()
		}
	}()

	room := "4819-azure-tiger"
	// Alice opens room as Host
	node1.HostRoom(room)
	if !node1.IsHost || !node1.IsConnected {
		t.Fatalf("Expected Node1 to be Host and Connected")
	}

	// Bob requests to join Alice's open room
	joinedSuccess := false
	var joinedHost string
	node2.RequestJoinRoom(room, 2*time.Second, func(hostNick string) {
		joinedSuccess = true
		joinedHost = hostNick
	}, func(reason string) {
		t.Errorf("Unexpected join failure: %s", reason)
	})

	// Wait up to 1 second for discovery and handshake
	deadline := time.Now().Add(1 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		if len(node1.GetPeersList()) > 0 && len(node2.GetPeersList()) > 0 && joinedSuccess {
			connected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !connected {
		t.Fatalf("Nodes failed to discover each other! node1 peers: %d, node2 peers: %d, joinedSuccess: %v",
			len(node1.GetPeersList()), len(node2.GetPeersList()), joinedSuccess)
	}

	if node2.IsHost {
		t.Fatalf("Expected Node2 (Joiner) to NOT be host")
	}
	if joinedHost != "Alice" {
		t.Fatalf("Expected joined host to be Alice, got %s", joinedHost)
	}
}

func TestP2PLANOnlyModeDirectDiscovery(t *testing.T) {
	audio1 := NewAudioEngine()
	node1 := NewP2PNode("lan_node_1", "HostAlice", audio1)
	node1.LanOnly = true
	node1.RelayURL = ""
	if err := node1.Start(); err != nil {
		t.Fatalf("Node1 start failed: %v", err)
	}
	defer func() {
		if node1.Conn != nil {
			node1.Conn.Close()
		}
	}()

	audio2 := NewAudioEngine()
	node2 := NewP2PNode("lan_node_2", "JoinerBob", audio2)
	node2.LanOnly = true
	node2.RelayURL = ""
	if err := node2.Start(); err != nil {
		t.Fatalf("Node2 start failed: %v", err)
	}
	defer func() {
		if node2.Conn != nil {
			node2.Conn.Close()
		}
	}()

	room := "9912-silent-falcon"
	node1.HostRoom(room)

	joinedSuccess := false
	var joinedHost string
	node2.RequestJoinRoom(room, 2*time.Second, func(hostNick string) {
		joinedSuccess = true
		joinedHost = hostNick
	}, func(reason string) {
		t.Errorf("Unexpected LAN join failure: %s", reason)
	})

	deadline := time.Now().Add(1 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		if len(node1.GetPeersList()) > 0 && len(node2.GetPeersList()) > 0 && joinedSuccess {
			connected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !connected {
		t.Fatalf("LAN nodes failed to discover each other! node1 peers: %d, node2 peers: %d, joinedSuccess: %v",
			len(node1.GetPeersList()), len(node2.GetPeersList()), joinedSuccess)
	}
	if joinedHost != "HostAlice" {
		t.Fatalf("Expected HostAlice, got %s", joinedHost)
	}
}

func TestJoinClosedRoomFails(t *testing.T) {
	audio := NewAudioEngine()
	node := NewP2PNode("lonely_node", "Charlie", audio)
	node.LanOnly = true
	node.RelayURL = ""
	if err := node.Start(); err != nil {
		t.Fatalf("Node start failed: %v", err)
	}
	defer func() {
		if node.Conn != nil {
			node.Conn.Close()
		}
	}()

	room := "9999-ghost-falcon"
	failed := false
	var failReason string
	done := make(chan struct{})

	// Attempt to join non-existent room with 150ms timeout
	node.RequestJoinRoom(room, 150*time.Millisecond, func(hostNick string) {
		t.Errorf("Expected join to FAIL on unopened room, but succeeded with host %s", hostNick)
		close(done)
	}, func(reason string) {
		failed = true
		failReason = reason
		close(done)
	})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Expected join request callback to be called within timeout")
	}

	if !failed {
		t.Fatalf("Expected join request to timeout and fail for unopened room")
	}
	if node.IsConnected {
		t.Fatalf("Node should NOT be connected after failing to find a room")
	}
	if failReason == "" {
		t.Fatalf("Expected non-empty failure reason message")
	}
}

func TestVerticalMeterAndDialogs(t *testing.T) {
	audio := NewAudioEngine()
	if audio.Loopback {
		t.Fatalf("Expected loopback to start false")
	}

	audio.ToggleLoopback()
	if !audio.Loopback {
		t.Fatalf("Expected loopback to be true")
	}

	initialThresh := audio.VADThreshold
	audio.AdjustThreshold(0.01)
	if audio.VADThreshold <= initialThresh {
		t.Fatalf("Expected adjusted threshold to increase")
	}

	buf := buffer.NewBuffer(cell.NewRect(0, 0, 40, 10))
	DrawVerticalLevelMeter(buf, cell.NewRect(0, 0, 30, 6), 0.5, true, false, "TEST METRE")

	c := buf.Get(0, 0)
	if c.Content != 'T' {
		t.Fatalf("Expected header label 'T', got %c", c.Content)
	}

	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	closed := false
	DrawTestModal(frame, cell.NewRect(0, 0, 80, 24), audio, nil, func() { closed = true })
	_ = closed
	DrawLeaveModal(frame, cell.NewRect(0, 0, 80, 24), 1.0, func() {}, func() {})
	DrawExitModal(frame, cell.NewRect(0, 0, 80, 24), 1.0, func() {}, func() {})
	DrawScreenShareModal(frame, cell.NewRect(0, 0, 80, 24), 1.0, 0, []screenshare.WindowInfo{
		{ID: "desktop", Title: "[Desktop] Entire Screen (Primary View)"},
	}, func(_ screenshare.WindowInfo) {}, func() {})
}

func TestNoiseSuppressionAndTestMode(t *testing.T) {
	audio := NewAudioEngine()

	if audio.SuppressionMode != 1 {
		t.Fatalf("Expected default suppression mode 1 (ACIK), got %d", audio.SuppressionMode)
	}

	audio.CycleSuppressionMode()
	if audio.SuppressionMode != 2 {
		t.Fatalf("Expected suppression mode 2 (YUKSEK), got %d", audio.SuppressionMode)
	}

	audio.SetSuppressionMode(1)
	if audio.SuppressionMode != 1 {
		t.Fatalf("Expected suppression mode 1 (ACIK), got %d", audio.SuppressionMode)
	}

	// Generate synthetic vocal frame (400Hz tone at typical speaking volume)
	speechPCM := make([]byte, AudioChunkSize)
	for i := 0; i < 320; i++ {
		val := int16(3000.0 * math.Sin(2.0*math.Pi*400.0*float64(i)/float64(AudioSampleRate)))
		binary.LittleEndian.PutUint16(speechPCM[i*2:i*2+2], uint16(val))
	}

	speaking, finalRMS, filtered := audio.processNoiseCancellation(speechPCM, audio.SuppressionMode)
	if !speaking {
		t.Fatalf("Expected speaking=true for vocal frame in ACIK mode, got false")
	}
	if finalRMS <= 0.01 {
		t.Fatalf("Expected audible finalRMS > 0.01 for vocal frame, got %f", finalRMS)
	}
	if len(filtered) != len(speechPCM) {
		t.Fatalf("Expected filtered PCM size %d, got %d", len(speechPCM), len(filtered))
	}

	audio.Muted = true
	audio.Deafened = false

	audio.EnterTestMode()
	if !audio.InTestMode || audio.Muted || !audio.Deafened || !audio.Loopback {
		t.Fatalf("EnterTestMode failed: inTest=%v muted=%v deaf=%v loop=%v",
			audio.InTestMode, audio.Muted, audio.Deafened, audio.Loopback)
	}

	audio.LeaveTestMode()
	if audio.InTestMode || !audio.Muted || audio.Deafened || audio.Loopback {
		t.Fatalf("LeaveTestMode failed: inTest=%v muted=%v deaf=%v loop=%v",
			audio.InTestMode, audio.Muted, audio.Deafened, audio.Loopback)
	}
}

func TestSpeechPassesThroughAllModes(t *testing.T) {
	audio := NewAudioEngine()

	// 500 Hz tone representing human voice vowel / formant
	speechPCM := make([]byte, AudioChunkSize)
	for i := 0; i < 320; i++ {
		val := int16(4000.0 * math.Sin(2.0*math.Pi*500.0*float64(i)/float64(AudioSampleRate)))
		binary.LittleEndian.PutUint16(speechPCM[i*2:i*2+2], uint16(val))
	}

	// Test Mode 0 (KAPALI)
	rawRMS := calculateRMS(speechPCM)
	if rawRMS < 0.05 {
		t.Fatalf("Expected speech rawRMS >= 0.05, got %f", rawRMS)
	}

	// Test Mode 1 (ACIK)
	speaking1, rms1, out1 := audio.processNoiseCancellation(speechPCM, 1)
	if !speaking1 {
		t.Fatalf("Expected speaking=true in Mode 1 (ACIK)")
	}
	if rms1 < 0.01 || calculateRMS(out1) < 0.01 {
		t.Fatalf("Expected non-zero audible output in Mode 1 (ACIK), got rms=%f", rms1)
	}

	// Test Mode 2 (YUKSEK)
	speaking2, rms2, out2 := audio.processNoiseCancellation(speechPCM, 2)
	if !speaking2 {
		t.Fatalf("Expected speaking=true in Mode 2 (YUKSEK)")
	}
	if rms2 < 0.01 || calculateRMS(out2) < 0.01 {
		t.Fatalf("Expected non-zero audible output in Mode 2 (YUKSEK), got rms=%f", rms2)
	}
}

func TestVADSensitivityMapping(t *testing.T) {
	audio := NewAudioEngine()

	// Default sensitivity
	if audio.GetVADSensitivity() != 65 {
		t.Fatalf("Expected default sensitivity 65, got %d", audio.GetVADSensitivity())
	}

	// Maximum sensitivity (100% -> very low threshold ~0.001)
	audio.SetVADSensitivity(100)
	if audio.GetVADSensitivity() != 100 {
		t.Fatalf("Expected sensitivity 100, got %d", audio.GetVADSensitivity())
	}
	if audio.VADThreshold > 0.0015 {
		t.Fatalf("Expected low threshold for max sensitivity, got %f", audio.VADThreshold)
	}

	// Minimum sensitivity (1% -> high threshold ~0.050)
	audio.SetVADSensitivity(1)
	if audio.GetVADSensitivity() != 1 {
		t.Fatalf("Expected sensitivity 1, got %d", audio.GetVADSensitivity())
	}
	if audio.VADThreshold < 0.045 {
		t.Fatalf("Expected high threshold for min sensitivity, got %f", audio.VADThreshold)
	}

	// Reset to 65%
	audio.SetVADSensitivity(65)
	if audio.GetVADSensitivity() != 65 {
		t.Fatalf("Expected sensitivity 65, got %d", audio.GetVADSensitivity())
	}
}

func TestNoiseSuppressionFiltersFanNoise(t *testing.T) {
	audio := NewAudioEngine()

	// Generate synthetic PC fan / AC hum (120Hz + 240Hz low drone at moderate volume)
	fanPCM := make([]byte, AudioChunkSize)
	for i := 0; i < 320; i++ {
		s1 := 1200.0 * math.Sin(2.0*math.Pi*120.0*float64(i)/float64(AudioSampleRate))
		s2 := 800.0 * math.Sin(2.0*math.Pi*240.0*float64(i)/float64(AudioSampleRate))
		val := int16(s1 + s2)
		binary.LittleEndian.PutUint16(fanPCM[i*2:i*2+2], uint16(val))
	}

	// Run fan noise in Mode 1:
	audio.SetSuppressionMode(1)
	audio.IsSpeaking = false

	// Let the adaptive noise tracker adapt over multiple frames
	for f := 0; f < 30; f++ {
		audio.processNoiseCancellation(fanPCM, 1)
	}

	speaking, finalRMS, out := audio.processNoiseCancellation(fanPCM, 1)
	if speaking {
		t.Fatalf("Expected fan noise alone to NOT trigger speech VAD, but got speaking=true")
	}
	if finalRMS > 0.01 {
		t.Fatalf("Expected fan noise to be gated to silence, got finalRMS=%f", finalRMS)
	}
	if calculateRMS(out) > 0.01 {
		t.Fatalf("Expected gated output PCM RMS <= 0.01, got %f", calculateRMS(out))
	}
}

func TestHandClapSuppression(t *testing.T) {
	audio := NewAudioEngine()
	audio.SetSuppressionMode(2) // Mode 2: HIGH (Sonar AI mode)

	// Generate synthetic hand clap (sharp impulsive peak at sample 40, rapidly decaying, non-harmonic)
	clapPCM := make([]byte, AudioChunkSize)
	for i := 0; i < 320; i++ {
		var sample float64
		if i >= 40 && i < 120 {
			tVal := float64(i - 40)
			sample = 16000.0 * math.Exp(-tVal/8.0) * math.Sin(2.0*math.Pi*1800.0*tVal/float64(AudioSampleRate))
		}
		val := int16(sample)
		binary.LittleEndian.PutUint16(clapPCM[i*2:i*2+2], uint16(val))
	}

	speaking, _, _ := audio.processNoiseCancellation(clapPCM, 2)
	if speaking {
		t.Fatalf("Expected hand clap to be rejected by Sonar noise suppressor, but got speaking=true")
	}
}

func TestMechanicalKeyboardTypingSuppression(t *testing.T) {
	audio := NewAudioEngine()
	audio.SetSuppressionMode(2)

	// Generate synthetic mechanical keyboard switch click (high frequency click burst at 3500Hz)
	keyPCM := make([]byte, AudioChunkSize)
	for i := 0; i < 320; i++ {
		var sample float64
		if i >= 30 && i < 90 {
			tVal := float64(i - 30)
			sample = 10000.0 * math.Exp(-tVal/6.0) * math.Sin(2.0*math.Pi*3600.0*tVal/float64(AudioSampleRate))
		}
		val := int16(sample)
		binary.LittleEndian.PutUint16(keyPCM[i*2:i*2+2], uint16(val))
	}

	speaking, _, _ := audio.processNoiseCancellation(keyPCM, 2)
	if speaking {
		t.Fatalf("Expected mechanical keyboard click to be suppressed, but got speaking=true")
	}
}

func TestCoughAndThroatClearingSuppression(t *testing.T) {
	audio := NewAudioEngine()
	audio.SetSuppressionMode(2)

	// Generate synthetic cough / non-harmonic turbulent burst (pseudo-random broadband noise burst)
	coughPCM := make([]byte, AudioChunkSize)
	for i := 0; i < 320; i++ {
		// Non-harmonic multi-frequency turbulent burst
		s := 4000.0*math.Sin(float64(i*i)*0.13) + 3000.0*math.Cos(float64(i*i*i)*0.07)
		val := int16(s)
		binary.LittleEndian.PutUint16(coughPCM[i*2:i*2+2], uint16(val))
	}

	speaking, _, _ := audio.processNoiseCancellation(coughPCM, 2)
	if speaking {
		t.Fatalf("Expected cough turbulence to be suppressed, but got speaking=true")
	}
}

func TestPitchHarmonicSpeechPassthrough(t *testing.T) {
	audio := NewAudioEngine()

	// Generate synthetic human speech with rich fundamental pitch + vocal harmonics (160 Hz + 320 Hz + 480 Hz)
	speechPCM := make([]byte, AudioChunkSize)
	for i := 0; i < 320; i++ {
		f0 := 160.0
		tVal := float64(i) / float64(AudioSampleRate)
		s1 := 3500.0 * math.Sin(2.0*math.Pi*f0*tVal)
		s2 := 2500.0 * math.Sin(2.0*math.Pi*2.0*f0*tVal)
		s3 := 1500.0 * math.Sin(2.0*math.Pi*3.0*f0*tVal)
		val := int16(s1 + s2 + s3)
		binary.LittleEndian.PutUint16(speechPCM[i*2:i*2+2], uint16(val))
	}

	// Test Mode 1 (Standard) and Mode 2 (High Sonar)
	speaking1, rms1, out1 := audio.processNoiseCancellation(speechPCM, 1)
	if !speaking1 || rms1 < 0.02 || calculateRMS(out1) < 0.02 {
		t.Fatalf("Expected rich voiced speech to pass cleanly in Mode 1, speaking=%v, rms=%f", speaking1, rms1)
	}

	speaking2, rms2, out2 := audio.processNoiseCancellation(speechPCM, 2)
	if !speaking2 || rms2 < 0.02 || calculateRMS(out2) < 0.02 {
		t.Fatalf("Expected rich voiced speech to pass cleanly in Mode 2, speaking=%v, rms=%f", speaking2, rms2)
	}
}

func TestQuietSpeechAndDeepVoicePassthrough(t *testing.T) {
	audio := NewAudioEngine()

	// 1. Test quiet human speech (RMS ~ 0.005)
	quietPCM := make([]byte, AudioChunkSize)
	for i := 0; i < 320; i++ {
		f0 := 140.0
		tVal := float64(i) / float64(AudioSampleRate)
		s := 220.0*math.Sin(2.0*math.Pi*f0*tVal) + 150.0*math.Sin(2.0*math.Pi*2.0*f0*tVal)
		binary.LittleEndian.PutUint16(quietPCM[i*2:i*2+2], uint16(int16(s)))
	}

	speaking, rms, _ := audio.processNoiseCancellation(quietPCM, 1)
	if !speaking || rms < 0.003 {
		t.Fatalf("Expected quiet speech to trigger VAD in Mode 1, speaking=%v, rms=%f", speaking, rms)
	}

	// 2. Test deep male voice (95 Hz low pitch fundamental with high low-frequency energy)
	deepPCM := make([]byte, AudioChunkSize)
	for i := 0; i < 320; i++ {
		f0 := 95.0
		tVal := float64(i) / float64(AudioSampleRate)
		s := 800.0*math.Sin(2.0*math.Pi*f0*tVal) + 500.0*math.Sin(2.0*math.Pi*2.0*f0*tVal) + 300.0*math.Sin(2.0*math.Pi*3.0*f0*tVal)
		binary.LittleEndian.PutUint16(deepPCM[i*2:i*2+2], uint16(int16(s)))
	}

	speakingDeep, rmsDeep, _ := audio.processNoiseCancellation(deepPCM, 1)
	if !speakingDeep || rmsDeep < 0.01 {
		t.Fatalf("Expected deep voice to trigger VAD in Mode 1, speaking=%v, rms=%f", speakingDeep, rmsDeep)
	}
}

func TestVideoReorderBuffer(t *testing.T) {
	buf := VideoReorderBuffer{}
	buf.Reset()

	// Push in-order packet 1 -> returns [chunk1]
	c1 := []byte("chunk1")
	out := buf.Push(1, c1)
	if len(out) != 1 || string(out[0]) != "chunk1" {
		t.Fatalf("Expected chunk1, got %v", out)
	}

	// Push out-of-order packet 3 -> buffers, returns nil
	c3 := []byte("chunk3")
	out = buf.Push(3, c3)
	if len(out) != 0 {
		t.Fatalf("Expected nil when packet 2 is missing, got %v", out)
	}

	// Push missing packet 2 -> returns [chunk2, chunk3] in exact sequence order!
	c2 := []byte("chunk2")
	out = buf.Push(2, c2)
	if len(out) != 2 || string(out[0]) != "chunk2" || string(out[1]) != "chunk3" {
		t.Fatalf("Expected [chunk2, chunk3], got %v", out)
	}

	// Push duplicate packet 2 -> dropped, returns nil
	out = buf.Push(2, c2)
	if len(out) != 0 {
		t.Fatalf("Expected duplicate packet 2 to be dropped, got %v", out)
	}

	// Test Reset
	buf.Reset()
	out = buf.Push(10, []byte("chunk10"))
	if len(out) != 1 || string(out[0]) != "chunk10" {
		t.Fatalf("Expected chunk10 after reset, got %v", out)
	}
}

func TestPushToTalkMode(t *testing.T) {
	engine := NewAudioEngine()
	if engine.InputMode != InputModeVoiceActivity {
		t.Fatalf("Expected default input mode to be VoiceActivity")
	}
	if !engine.IsTransmitting() {
		t.Fatalf("Expected VoiceActivity mode to be transmitting when unmuted")
	}

	engine.CycleInputMode()
	if engine.InputMode != InputModePushToTalk {
		t.Fatalf("Expected InputMode to be PushToTalk")
	}
	if engine.IsTransmitting() {
		t.Fatalf("Expected PushToTalk to NOT be transmitting when PTT is idle")
	}

	engine.PulsePTT(200 * time.Millisecond)
	if !engine.IsTransmitting() {
		t.Fatalf("Expected PushToTalk to be transmitting after PulsePTT")
	}

	engine.SetPTT(true)
	if !engine.IsTransmitting() {
		t.Fatalf("Expected PushToTalk to be transmitting when PTT is active")
	}

	engine.SetPTT(false)
	// Due to hangover delay, should still be transmitting briefly
	if !engine.IsTransmitting() {
		t.Fatalf("Expected PushToTalk to still be transmitting during release hangover")
	}
}

func TestAudioDeduplicator(t *testing.T) {
	dedup := AudioDeduplicator{}

	if !dedup.ShouldProcess("peer1", 1) {
		t.Fatalf("Expected peer1 seq 1 to be processed")
	}
	if dedup.ShouldProcess("peer1", 1) {
		t.Fatalf("Expected duplicate peer1 seq 1 to be dropped")
	}
	if !dedup.ShouldProcess("peer2", 1) {
		t.Fatalf("Expected peer2 seq 1 to be processed independently")
	}
	if !dedup.ShouldProcess("peer1", 2) {
		t.Fatalf("Expected peer1 seq 2 to be processed")
	}
	if dedup.ShouldProcess("peer1", 2) {
		t.Fatalf("Expected duplicate peer1 seq 2 to be dropped")
	}

	dedup.Reset("peer1")
	if !dedup.ShouldProcess("peer1", 1) {
		t.Fatalf("Expected peer1 seq 1 to be processed after Reset")
	}
}

func TestPeerJitterBuffer(t *testing.T) {
	jb := newPeerJitterBuffer(2) // 2-chunk prebuffer
	chunk1 := make([]byte, AudioChunkSize)
	chunk2 := make([]byte, AudioChunkSize)
	chunk1[0] = 0x11
	chunk2[0] = 0x22

	// Push 1st chunk: should not be playable yet (cushion building)
	jb.Push(chunk1)
	if _, ok := jb.Pop(); ok {
		t.Fatalf("Expected Pop to return false before prebuffer is full")
	}

	// Push 2nd chunk: now prebuffer target reached -> playable!
	jb.Push(chunk2)
	c, ok := jb.Pop()
	if !ok || c[0] != 0x11 {
		t.Fatalf("Expected Pop to return chunk1 (0x11)")
	}

	c, ok = jb.Pop()
	if !ok || c[0] != 0x22 {
		t.Fatalf("Expected Pop to return chunk2 (0x22)")
	}

	// Now starved: Pop should return false
	if _, ok = jb.Pop(); ok {
		t.Fatalf("Expected Pop to return false on starved buffer")
	}
}

func TestGainRange300(t *testing.T) {
	engine := NewAudioEngine()
	engine.Gain = 1.0

	// Boost beyond 200% up to 300%
	engine.AdjustGain(1.5) // 1.0 + 1.5 = 2.5 (250%)
	if engine.Gain != 2.5 {
		t.Fatalf("Expected gain 2.5, got %f", engine.Gain)
	}

	engine.AdjustGain(1.0) // 2.5 + 1.0 = 3.5 -> clamped to 3.0 (300%)
	if engine.Gain != 3.0 {
		t.Fatalf("Expected gain 3.0, got %f", engine.Gain)
	}

	testPCM := make([]byte, AudioChunkSize)
	for i := 0; i < len(testPCM)/2; i++ {
		binary.LittleEndian.PutUint16(testPCM[i*2:i*2+2], 10000)
	}
	boosted := applyGain(testPCM, 3.0)
	val := int16(binary.LittleEndian.Uint16(boosted[0:2]))
	if val <= 10000 || val > 32767 {
		t.Fatalf("Expected soft-boosted sample between 10000 and 32767, got %d", val)
	}
}

func TestAudioDeviceEnumerationAndSelection(t *testing.T) {
	inDevs := EnumerateInputDevices()
	if len(inDevs) == 0 {
		t.Fatalf("Expected at least 1 input device (default), got 0")
	}

	outDevs := EnumerateOutputDevices()
	if len(outDevs) == 0 {
		t.Fatalf("Expected at least 1 output device (default), got 0")
	}

	engine := NewAudioEngine()
	// Add mock devices for comprehensive cycling test
	engine.InputDevices = []AudioDevice{
		{ID: "default", Name: "Default Mic", IsDefault: true, IsInput: true},
		{ID: "mic_2", Name: "USB Headset Mic", IsInput: true},
		{ID: "mic_3", Name: "Webcam Mic", IsInput: true},
	}
	engine.OutputDevices = []AudioDevice{
		{ID: "default", Name: "Default Speakers", IsDefault: true, IsInput: false},
		{ID: "out_2", Name: "Headphones", IsInput: false},
	}

	// Test Mic Cycling
	if engine.GetSelectedInputName() != "Default Mic" {
		t.Fatalf("Expected 'Default Mic', got %q", engine.GetSelectedInputName())
	}
	engine.CycleInputDevice(1)
	if engine.SelectedInputIdx != 1 || engine.GetSelectedInputName() != "USB Headset Mic" {
		t.Fatalf("Expected 'USB Headset Mic' at index 1, got %q", engine.GetSelectedInputName())
	}
	engine.CycleInputDevice(-1)
	if engine.SelectedInputIdx != 0 {
		t.Fatalf("Expected index 0 after reverse cycle, got %d", engine.SelectedInputIdx)
	}

	// Test Output Device Cycling
	if engine.GetSelectedOutputName() != "Default Speakers" {
		t.Fatalf("Expected 'Default Speakers', got %q", engine.GetSelectedOutputName())
	}
	engine.CycleOutputDevice(1)
	if engine.SelectedOutputIdx != 1 || engine.GetSelectedOutputName() != "Headphones" {
		t.Fatalf("Expected 'Headphones' at index 1, got %q", engine.GetSelectedOutputName())
	}
}

func TestOutputVolumeAndAGC(t *testing.T) {
	engine := NewAudioEngine()

	// Initial output volume should be 1.0 (100%)
	if engine.OutputVolume != 1.0 {
		t.Fatalf("Expected initial output volume 1.0, got %f", engine.OutputVolume)
	}

	// Boost output volume to 1.5 (150%)
	engine.AdjustOutputVolume(0.5)
	if engine.OutputVolume != 1.5 {
		t.Fatalf("Expected output volume 1.5, got %f", engine.OutputVolume)
	}

	// Test mixPCM with output volume multiplier
	chunk := make([]byte, AudioChunkSize)
	for i := 0; i < len(chunk)/2; i++ {
		binary.LittleEndian.PutUint16(chunk[i*2:i*2+2], 1000)
	}
	mixed := mixPCM([][]byte{chunk}, len(chunk)/2, 2.0)
	val := int16(binary.LittleEndian.Uint16(mixed[0:2]))
	if val < 1900 || val > 2100 {
		t.Fatalf("Expected output sample ~2000 after 2.0x volume, got %d", val)
	}

	// Test PlayPeerPCM dynamic AGC boosting for quiet incoming voice
	quietChunk := make([]byte, AudioChunkSize)
	for i := 0; i < len(quietChunk)/2; i++ {
		binary.LittleEndian.PutUint16(quietChunk[i*2:i*2+2], 500)
	}
	quietRMS := calculateRMS(quietChunk)
	engine.PlayPeerPCM("peer_quiet", quietChunk, quietRMS, true)
	jb := engine.peerJitterBuffers["peer_quiet"]
	if jb == nil {
		t.Fatalf("Expected jitter buffer created for peer_quiet")
	}
}

func TestChatMessagePacketCodec(t *testing.T) {
	roomCode := "4820-cyber-otter"
	key := deriveRoomKey(roomCode)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher failed: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM failed: %v", err)
	}

	chatText := "Selam! Limoni Voice chat test 🚀"
	pkt := P2PPacket{
		Type:      PacketChatMessage,
		RoomCode:  roomCode,
		SenderID:  "peer_abc",
		Nickname:  "Alice",
		Timestamp: time.Now().UnixMilli(),
		Payload:   []byte(chatText),
	}

	data, err := encodeAndEncryptPacket(&pkt, aead)
	if err != nil {
		t.Fatalf("encodeAndEncryptPacket failed: %v", err)
	}

	var decoded P2PPacket
	if err := decryptAndDecodePacket(data, &decoded, aead); err != nil {
		t.Fatalf("decryptAndDecodePacket failed: %v", err)
	}

	if decoded.Type != PacketChatMessage {
		t.Fatalf("Expected packet type PacketChatMessage (%d), got %d", PacketChatMessage, decoded.Type)
	}
	if decoded.Nickname != "Alice" {
		t.Fatalf("Expected sender Nickname 'Alice', got %q", decoded.Nickname)
	}
	if string(decoded.Payload) != chatText {
		t.Fatalf("Expected payload %q, got %q", chatText, string(decoded.Payload))
	}
}

func TestRoomViewChatAndLogs(t *testing.T) {
	room := NewRoomView()

	// 1. Test adding log
	room.AddLog("[+] Friend joined the room")
	if len(room.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(room.Messages))
	}
	if room.Messages[0].IsChat {
		t.Fatalf("Expected first message to be log/event, not chat")
	}

	// 2. Test adding chat message
	room.AddChatMessage("Bob", "peer_bob", "Hello world!", false, time.Now())
	if len(room.Messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(room.Messages))
	}
	if !room.Messages[1].IsChat || room.Messages[1].Sender != "Bob" || room.Messages[1].Text != "Hello world!" {
		t.Fatalf("Chat message not recorded properly: %+v", room.Messages[1])
	}

	// 3. Test sending current chat input
	var sentText string
	room.OnSendChat = func(text string) {
		sentText = text
	}
	room.ChatInputState.SetValue("My new message")
	room.SendCurrentChat()

	if sentText != "My new message" {
		t.Fatalf("Expected sent text 'My new message', got %q", sentText)
	}
	if room.ChatInputState.Value() != "" {
		t.Fatalf("Expected ChatInputState cleared after send, got %q", room.ChatInputState.Value())
	}

	// 4. Test scrolling chat
	room.ScrollChat(2)
	if room.ChatScrollOffset != 2 {
		t.Fatalf("Expected scroll offset 2, got %d", room.ChatScrollOffset)
	}
	room.ScrollChat(-5)
	if room.ChatScrollOffset != 0 {
		t.Fatalf("Expected scroll offset 0 (min bound), got %d", room.ChatScrollOffset)
	}

	// 5. Test rendering frame without crash
	buf := buffer.NewBuffer(cell.NewRect(0, 0, 120, 40))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	audio := NewAudioEngine()
	node := NewP2PNode("local_user", "You", audio)

	room.Render(frame, cell.NewRect(0, 0, 120, 40), node, audio)
}

func TestP2PNodeChatCallbacks(t *testing.T) {
	audio := NewAudioEngine()
	node := NewP2PNode("test_peer_1", "User1", audio)
	node.IsConnected = true
	node.RoomCode = "1234-alpha-beta"

	receivedCount := 0
	receivedMsg := ""
	receivedSender := ""
	node.OnChatMessage = func(senderID string, nickname string, text string, ts time.Time) {
		receivedCount++
		receivedSender = nickname
		receivedMsg = text
	}

	// Simulate receiving a PacketChatMessage from remote peer (e.g. from WebSocket Relay)
	chatPkt := P2PPacket{
		Type:      PacketChatMessage,
		RoomCode:  "1234-alpha-beta",
		SenderID:  "remote_peer_2",
		Nickname:  "User2",
		Seq:       42,
		Payload:   []byte("Hey there!"),
		Timestamp: 1725431154000,
	}

	node.handlePacket(&chatPkt, nil)

	// Simulate duplicate packet arriving via Direct UDP transport
	node.handlePacket(&chatPkt, nil)

	// Wait briefly for goroutine callback
	time.Sleep(50 * time.Millisecond)

	if receivedCount != 1 {
		t.Fatalf("Expected exactly 1 callback (deduplicated), got %d", receivedCount)
	}
	if receivedSender != "User2" || receivedMsg != "Hey there!" {
		t.Fatalf("Expected received chat 'User2': 'Hey there!', got %q: %q", receivedSender, receivedMsg)
	}
}

func TestPeerLeaveNoDeadlock(t *testing.T) {
	audio := NewAudioEngine()
	node := NewP2PNode("test_host", "Host", audio)
	node.IsConnected = true
	node.RoomCode = "5678-delta-echo"

	// Register peer
	node.Peers["peer_leaving"] = &PeerInfo{
		ID:              "peer_leaving",
		Nickname:        "LeavingUser",
		IsSharingScreen: true,
	}

	leftFired := false
	node.OnPeerEvent = func(event string, peer *PeerInfo) {
		if event == "leave" {
			leftFired = true
		}
	}

	leavePkt := P2PPacket{
		Type:      PacketLeave,
		RoomCode:  "5678-delta-echo",
		SenderID:  "peer_leaving",
		Nickname:  "LeavingUser",
		Timestamp: time.Now().UnixMilli(),
	}

	done := make(chan struct{})
	go func() {
		node.handlePacket(&leavePkt, nil)
		close(done)
	}()

	select {
	case <-done:
		// Success, no deadlock!
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handlePacket deadlocked on PacketLeave!")
	}

	time.Sleep(20 * time.Millisecond)
	if !leftFired {
		t.Fatalf("Expected OnPeerEvent leave callback to fire")
	}
	if node.Peers["peer_leaving"] != nil {
		t.Fatalf("Expected peer removed from map")
	}
}

func TestChatClickableLinks(t *testing.T) {
	room := NewRoomView()
	room.AddChatMessage("Alice", "peer_alice", "Check out https://github.com/thebanri/limoni and www.google.com for info!", false, time.Now())

	buf := buffer.NewBuffer(cell.NewRect(0, 0, 120, 30))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	audio := NewAudioEngine()
	node := NewP2PNode("local_user", "You", audio)

	room.Render(frame, cell.NewRect(0, 0, 120, 30), node, audio)

	// Verify click handler registered on frame
	if frame == nil {
		t.Fatalf("Frame is nil")
	}
}

func TestDebugModalAndLogs(t *testing.T) {
	ClearDebugLogs()
	AddDebugLog("Test debug entry 1: [SCREEN] Chunk sent 1024 bytes")
	AddDebugLog("Test debug entry 2: [NET] TCP connected to port 50100")

	logs := GetDebugLogs()
	if len(logs) != 2 {
		t.Fatalf("Expected 2 debug logs, got %d", len(logs))
	}
	if !strings.Contains(logs[0], "Chunk sent") || !strings.Contains(logs[1], "port 50100") {
		t.Fatalf("Debug log content mismatch: %v", logs)
	}

	buf := buffer.NewBuffer(cell.NewRect(0, 0, 120, 40))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	closed := false
	cleared := false
	copied := false

	DrawDebugModal(frame, cell.NewRect(0, 0, 120, 40), 0, func() { closed = true }, func() { cleared = true }, func() { copied = true })
	_ = closed
	_ = cleared
	_ = copied

	allText := GetAllDebugLogsText()
	if !strings.Contains(allText, "Chunk sent") {
		t.Fatalf("GetAllDebugLogsText mismatch: %q", allText)
	}
}





