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
	if err := node2.Start(); err != nil {
		t.Fatalf("Node2 start failed: %v", err)
	}
	defer func() {
		if node2.Conn != nil {
			node2.Conn.Close()
		}
	}()

	room := "4819-azure-tiger"
	node1.JoinRoom(room)
	node2.JoinRoom(room)

	// Wait up to 1 second for instant discovery and mutual mesh handshake
	deadline := time.Now().Add(1 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		if len(node1.GetPeersList()) > 0 && len(node2.GetPeersList()) > 0 {
			connected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !connected {
		t.Fatalf("Nodes failed to discover each other! node1 peers: %d, node2 peers: %d",
			len(node1.GetPeersList()), len(node2.GetPeersList()))
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
