package p2p

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/e2ee"
	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/protocol"
	"github.com/thebanri/limoni-voice/internal/video"
)

func TestP2PPacketCodec(t *testing.T) {
	roomCode := "7492-neon-falcon"
	aead := testKeyring(roomCode)

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
	data, err := sealPacket(&pkt, aead)
	if err != nil {
		t.Fatalf("sealPacket failed: %v", err)
	}
	if bytes.Contains(data, []byte("LVS1")) || bytes.Contains(data, []byte(pkt.Nickname)) {
		t.Fatalf("sealed packet leaks a fixed magic prefix or plaintext")
	}

	var decoded P2PPacket
	if err := openPacket(data, &decoded, aead); err != nil {
		t.Fatalf("openPacket failed: %v", err)
	}

	if decoded.RoomCode != pkt.RoomCode || decoded.Nickname != pkt.Nickname || decoded.RMS != pkt.RMS || decoded.IsDeafened != pkt.IsDeafened {
		t.Fatalf("Decoded packet mismatch: %+v vs %+v", decoded, pkt)
	}

	// 2. Test wrong key rejection (unauthorized room)
	var wrongDecoded P2PPacket
	if err := openPacket(data, &wrongDecoded, testKeyring("other-room-code")); err == nil {
		t.Fatalf("Expected decryption to FAIL with wrong key, but succeeded")
	}

	// 3. Test tampering rejection (modified payload)
	tamperedData := make([]byte, len(data))
	copy(tamperedData, data)
	tamperedData[len(tamperedData)-1] ^= 0xFF // Flip bits in ciphertext / tag

	var tamperedDecoded P2PPacket
	if err := openPacket(tamperedData, &tamperedDecoded, aead); err == nil {
		t.Fatalf("Expected decryption to FAIL on tampered packet, but succeeded")
	}
}

func TestP2PMaxPeers(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("host_node", "Host", audio)
	if err := node.Start(); err != nil {
		t.Fatalf("Node start failed: %v", err)
	}
	defer node.Close()

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
	audio1 := engine.NewAudioEngine()
	node1 := NewP2PNode("node_1", "Alice", audio1)
	node1.LanOnly = true
	node1.RelayURL = ""
	if err := node1.Start(); err != nil {
		t.Fatalf("Node1 start failed: %v", err)
	}
	defer node1.Close()

	audio2 := engine.NewAudioEngine()
	node2 := NewP2PNode("node_2", "Bob", audio2)
	node2.LanOnly = true
	node2.RelayURL = ""
	if err := node2.Start(); err != nil {
		t.Fatalf("Node2 start failed: %v", err)
	}
	defer node2.Close()

	room := "4819-azure-tiger"
	// Alice opens room as Host
	node1.HostRoom(room)
	if !node1.IsHost || !node1.IsConnected {
		t.Fatalf("Expected Node1 to be Host and Connected")
	}

	// Bob requests to join Alice's open room
	var joinMu sync.Mutex
	joinedSuccess := false
	var joinedHost string
	joined := func() (bool, string) {
		joinMu.Lock()
		defer joinMu.Unlock()
		return joinedSuccess, joinedHost
	}
	node2.RequestJoinRoom(room, 2*time.Second, func(hostNick string) {
		joinMu.Lock()
		joinedSuccess = true
		joinedHost = hostNick
		joinMu.Unlock()
	}, func(reason string) {
		t.Errorf("Unexpected join failure: %s", reason)
	})

	// Wait up to 1 second for discovery and handshake
	deadline := time.Now().Add(1 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		if ok, _ := joined(); len(node1.GetPeersList()) > 0 && len(node2.GetPeersList()) > 0 && ok {
			connected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	joinedSuccess, joinedHost = joined()
	if !connected {
		t.Fatalf("Nodes failed to discover each other! node1 peers: %d, node2 peers: %d, joinedSuccess: %v",
			len(node1.GetPeersList()), len(node2.GetPeersList()), joinedSuccess)
	}

	node2.mu.RLock()
	joinerIsHost := node2.IsHost
	node2.mu.RUnlock()
	if joinerIsHost {
		t.Fatalf("Expected Node2 (Joiner) to NOT be host")
	}
	if joinedHost != "Alice" {
		t.Fatalf("Expected joined host to be Alice, got %s", joinedHost)
	}
}

func TestP2PLANOnlyModeDirectDiscovery(t *testing.T) {
	audio1 := engine.NewAudioEngine()
	node1 := NewP2PNode("lan_node_1", "HostAlice", audio1)
	node1.LanOnly = true
	node1.RelayURL = ""
	if err := node1.Start(); err != nil {
		t.Fatalf("Node1 start failed: %v", err)
	}
	defer node1.Close()

	audio2 := engine.NewAudioEngine()
	node2 := NewP2PNode("lan_node_2", "JoinerBob", audio2)
	node2.LanOnly = true
	node2.RelayURL = ""
	if err := node2.Start(); err != nil {
		t.Fatalf("Node2 start failed: %v", err)
	}
	defer node2.Close()

	room := "9912-silent-falcon"
	node1.HostRoom(room)

	var joinMu sync.Mutex
	joinedSuccess := false
	var joinedHost string
	joined := func() (bool, string) {
		joinMu.Lock()
		defer joinMu.Unlock()
		return joinedSuccess, joinedHost
	}
	node2.RequestJoinRoom(room, 2*time.Second, func(hostNick string) {
		joinMu.Lock()
		joinedSuccess = true
		joinedHost = hostNick
		joinMu.Unlock()
	}, func(reason string) {
		t.Errorf("Unexpected LAN join failure: %s", reason)
	})

	deadline := time.Now().Add(1 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		if ok, _ := joined(); len(node1.GetPeersList()) > 0 && len(node2.GetPeersList()) > 0 && ok {
			connected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	joinedSuccess, joinedHost = joined()
	if !connected {
		t.Fatalf("LAN nodes failed to discover each other! node1 peers: %d, node2 peers: %d, joinedSuccess: %v",
			len(node1.GetPeersList()), len(node2.GetPeersList()), joinedSuccess)
	}
	if joinedHost != "HostAlice" {
		t.Fatalf("Expected HostAlice, got %s", joinedHost)
	}
}

func TestJoinClosedRoomFails(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("lonely_node", "Charlie", audio)
	node.LanOnly = true
	node.RelayURL = ""
	if err := node.Start(); err != nil {
		t.Fatalf("Node start failed: %v", err)
	}
	defer node.Close()

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

func TestScreenRxDispatchNeverBlocks(t *testing.T) {
	rx := &screenRx{n: NewP2PNode("rx", "rx", nil), reorder: video.NewReorder(0), playerCh: make(chan playerChunk, 10), stop: make(chan struct{})}
	start := time.Now()
	for i := uint32(1); i <= 200; i++ {
		rx.onData(i, []byte("video-slice"))
	}
	if dur := time.Since(start); dur > 50*time.Millisecond {
		t.Fatalf("onData blocked on a full player queue (%v)", dur)
	}
	// The queue overflowed: the backlog is dropped and the player resyncs on a keyframe.
	if rx.catchUps == 0 || !rx.skipToKey || len(rx.playerCh) != 0 {
		t.Fatalf("overflow not resynchronised: catchUps=%d skip=%v queued=%d", rx.catchUps, rx.skipToKey, len(rx.playerCh))
	}
	key := bytes.Repeat([]byte{0xFF}, video.TSPacketSize)
	key[0], key[3], key[4], key[5] = 0x47, 0x30, 7, 0x40 // random access indicator
	rx.onData(201, []byte("p-frame"))
	rx.onData(202, key)
	rx.onData(203, []byte("next"))
	if len(rx.playerCh) != 2 || rx.skipToKey {
		t.Fatalf("expected keyframe + following chunk after resync, queued %d", len(rx.playerCh))
	}

	// The pump writes everything queued in one flush.
	var sink bytes.Buffer
	w := bufio.NewWriter(&sink)
	queue := make(chan playerChunk, 4)
	queue <- playerChunk{data: []byte("b")}
	queue <- playerChunk{data: []byte("c")}
	if !writeChunks(w, []byte("a"), queue) || sink.String() != "abc" {
		t.Fatalf("batched write got %q", sink.String())
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

func TestChatMessagePacketCodec(t *testing.T) {
	roomCode := "4820-cyber-otter"
	aead := testKeyring(roomCode)

	chatText := "Selam! Limoni Voice chat test 🚀"
	pkt := P2PPacket{
		Type:      PacketChatMessage,
		RoomCode:  roomCode,
		SenderID:  "peer_abc",
		Nickname:  "Alice",
		Timestamp: time.Now().UnixMilli(),
		Payload:   []byte(chatText),
	}

	data, err := sealPacket(&pkt, aead)
	if err != nil {
		t.Fatalf("encodeAndEncryptPacket failed: %v", err)
	}

	var decoded P2PPacket
	if err := openPacket(data, &decoded, aead); err != nil {
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

func TestP2PNodeChatCallbacks(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("test_peer_1", "User1", audio)
	node.IsConnected = true
	node.RoomCode = "1234-alpha-beta"
	node.roomID = node.RoomCode

	var cbMu sync.Mutex
	receivedCount := 0
	receivedMsg := ""
	receivedSender := ""
	node.OnChatMessage = func(senderID string, nickname string, text string, ts time.Time) {
		cbMu.Lock()
		defer cbMu.Unlock()
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

	cbMu.Lock()
	defer cbMu.Unlock()
	if receivedCount != 1 {
		t.Fatalf("Expected exactly 1 callback (deduplicated), got %d", receivedCount)
	}
	if receivedSender != "User2" || receivedMsg != "Hey there!" {
		t.Fatalf("Expected received chat 'User2': 'Hey there!', got %q: %q", receivedSender, receivedMsg)
	}
}

func TestPeerLeaveNoDeadlock(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("test_host", "Host", audio)
	node.IsConnected = true
	node.RoomCode = "5678-delta-echo"
	node.roomID = node.RoomCode

	// Register peer
	node.Peers["peer_leaving"] = &PeerInfo{
		ID:              "peer_leaving",
		Nickname:        "LeavingUser",
		IsSharingScreen: true,
	}

	var leftFired atomic.Bool
	node.OnPeerEvent = func(event string, peer *PeerInfo) {
		if event == "leave" {
			leftFired.Store(true)
		}
	}

	leavePkt := signedLeave(node, "peer_leaving", "LeavingUser")

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

	waitFor(t, "the leave callback", 2*time.Second, leftFired.Load)
	if node.GetPeer("peer_leaving") != nil {
		t.Fatalf("Expected peer removed from map")
	}
}

func TestDynamicPortHopping(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("test-hop-node", "Hopper", audio)
	err := node.Start()
	if err != nil {
		t.Fatalf("Failed to start P2PNode: %v", err)
	}
	defer node.Close()

	node.HostRoom("1111-jump-test")
	initialPort := node.Port
	if initialPort <= 0 {
		t.Fatalf("Expected valid initial port, got %d", initialPort)
	}

	// Verify NextHopRemaining is valid
	rem := node.NextHopRemaining()
	if rem <= 0 || rem > 31*time.Minute {
		t.Fatalf("Expected remaining hop time ~30m, got %v", rem)
	}

	// Setup peer node
	peerEngine := engine.NewAudioEngine()
	peerNode := NewP2PNode("test-peer-node", "Peer", peerEngine)
	err = peerNode.Start()
	if err != nil {
		t.Fatalf("Failed to start peerNode: %v", err)
	}
	defer peerNode.Close()
	peerNode.HostRoom("1111-jump-test")
	// Both nodes opened the room independently; share the group key as a completed handshake would.
	peerNode.mu.Lock()
	peerNode.keyring = node.keyring
	peerNode.mu.Unlock()

	// Interconnect nodes over local loopback
	hostAddr, _ := net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.1:%d", initialPort))
	peerAddr, _ := net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.1:%d", peerNode.Port))

	node.mu.Lock()
	node.Peers[peerNode.LocalID] = &PeerInfo{
		ID:        peerNode.LocalID,
		Nickname:  peerNode.Nickname,
		Addr:      peerAddr,
		LocalPort: peerNode.Port,
		LastSeen:  time.Now(),
	}
	node.mu.Unlock()

	peerNode.mu.Lock()
	peerNode.Peers[node.LocalID] = &PeerInfo{
		ID:        node.LocalID,
		Nickname:  node.Nickname,
		Addr:      hostAddr,
		LocalPort: initialPort,
		LastSeen:  time.Now(),
	}
	peerNode.mu.Unlock()

	// Trigger RotatePort on Host
	var hoppedPort int
	var hoppedEpoch uint32
	node.OnPortHopped = func(newPort int, epoch uint32) {
		hoppedPort = newPort
		hoppedEpoch = epoch
	}

	err = node.RotatePort()
	if err != nil {
		t.Fatalf("RotatePort failed: %v", err)
	}

	node.mu.RLock()
	newPort, epoch := node.Port, node.currentEpoch
	node.mu.RUnlock()
	if newPort == initialPort {
		t.Fatalf("Expected port to rotate to new value, got %d", newPort)
	}
	if epoch != 1 {
		t.Fatalf("Expected epoch to increment to 1, got %d", epoch)
	}
	if hoppedEpoch != 1 || hoppedPort != newPort {
		t.Fatalf("Expected OnPortHopped callback with port %d and epoch 1, got port %d epoch %d", newPort, hoppedPort, hoppedEpoch)
	}

	// Give UDP packets a brief moment to be transmitted and received on loopback
	time.Sleep(150 * time.Millisecond)

	// Verify that peerNode dynamically updated Host's address to the new hopped port
	updatedHostPeer := peerNode.GetPeer(node.LocalID)

	if updatedHostPeer == nil {
		t.Fatalf("Host peer not found in peerNode")
	}
	if updatedHostPeer.LocalPort != node.Port {
		t.Fatalf("Expected peer's stored Host LocalPort to update to %d, got %d", node.Port, updatedHostPeer.LocalPort)
	}
	if updatedHostPeer.Addr.Port != node.Port {
		t.Fatalf("Expected peer's stored Host Addr.Port to update to %d, got %d", node.Port, updatedHostPeer.Addr.Port)
	}
	if time.Since(updatedHostPeer.LastSeen) > 2*time.Second {
		t.Fatalf("Expected peer LastSeen to be updated, but was %v ago", time.Since(updatedHostPeer.LastSeen))
	}

	// Test sending chat message from peerNode to node on the new port
	var receivedChat string
	var chatReceivedChan = make(chan string, 1)
	node.mu.Lock()
	node.OnChatMessage = func(senderID, nickname, message string, timestamp time.Time) {
		select {
		case chatReceivedChan <- message:
		default:
		}
	}
	node.mu.Unlock()

	peerNode.SendChatMessage("Hello after port hop!")

	select {
	case receivedChat = <-chatReceivedChan:
	case <-time.After(500 * time.Millisecond):
	}

	if receivedChat != "Hello after port hop!" {
		t.Fatalf("Expected chat 'Hello after port hop!' after port rotation, got '%s'", receivedChat)
	}
}

func TestE2EEFileTransfer(t *testing.T) {
	// 1. Create a dummy file
	tmpFile, err := os.CreateTemp("", "limoni_test_file_*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	testContent := "Hello from Limoni Voice P2P Direct File Transfer! Testing Chunking and Checksum validation."
	if _, err := tmpFile.WriteString(testContent); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	// 2. Test FileMetadata and chunk hashing
	fileBytes := []byte(testContent)
	sum := sha256.Sum256(fileBytes)
	checksumHex := hex.EncodeToString(sum[:])

	meta := &FileMetadata{
		TransferID:  "tx_123",
		FileName:    "test.txt",
		FileSize:    int64(len(fileBytes)),
		TotalChunks: 4,
		ChunkIndex:  0,
		Checksum:    checksumHex,
		IsCode:      false,
	}

	if meta.TotalChunks != 4 {
		t.Fatalf("Expected 4 chunks for 89 bytes, got %d", meta.TotalChunks)
	}

	// 3. Test AEAD packet serialization and deserialization
	aead := testKeyring("test-room-key-file")

	headerPacket := &P2PPacket{
		Type:     PacketFileHeader,
		FileMeta: meta,
	}
	enc, err := sealPacket(headerPacket, aead)
	if err != nil {
		t.Fatalf("encodeAndEncryptPacket failed: %v", err)
	}

	var dec P2PPacket
	if err := openPacket(enc, &dec, aead); err != nil {
		t.Fatalf("decryptAndDecodePacket failed: %v", err)
	}
	if dec.Type != PacketFileHeader || dec.FileMeta == nil || dec.FileMeta.TransferID != "tx_123" {
		t.Fatalf("Decoded file header does not match original: %+v", dec.FileMeta)
	}

	// 4. Test code snippet packet
	codeSnippet := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello World!\")\n}"
	snippetMeta := &FileMetadata{
		TransferID:  "tx_code_456",
		FileName:    "snippet.go",
		FileSize:    int64(len(codeSnippet)),
		TotalChunks: 1,
		ChunkIndex:  0,
		IsCode:      true,
	}
	codePacket := &P2PPacket{
		Type:     PacketFileChunk,
		FileMeta: snippetMeta,
		Payload:  []byte(codeSnippet),
	}
	codeEnc, err := sealPacket(codePacket, aead)
	if err != nil {
		t.Fatalf("Code packet marshal failed: %v", err)
	}
	var codeDec P2PPacket
	if err := openPacket(codeEnc, &codeDec, aead); err != nil {
		t.Fatalf("Code packet unmarshal failed: %v", err)
	}
	if string(codeDec.Payload) != codeSnippet || !codeDec.FileMeta.IsCode {
		t.Fatalf("Decoded code snippet does not match original: %s", string(codeDec.Payload))
	}
}

func TestRoomLockAndPIN(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("host_lock_test", "LockHost", audio)
	defer node.Close()

	// 1. Host room
	roomCode := "987654"
	node.HostRoom(roomCode)
	if node.IsLocked || node.RoomPIN != "" {
		t.Fatalf("Expected new room to be unlocked by default")
	}

	// 2. Lock room with 4-digit PIN
	node.LockRoom("4321")
	if !node.IsLocked || node.RoomPIN != "4321" {
		t.Fatalf("Expected room to be locked with PIN 4321, got isLocked=%v, pin=%s", node.IsLocked, node.RoomPIN)
	}

	// 3. Unlock room
	node.UnlockRoom()
	if node.IsLocked || node.RoomPIN != "" {
		t.Fatalf("Expected room to be unlocked after UnlockRoom()")
	}

	// 4. Lock room without PIN (rejects all new participants)
	node.LockRoom("")
	if !node.IsLocked || node.RoomPIN != "" {
		t.Fatalf("Expected room to be locked without PIN")
	}
}

func TestHostOnlyRoomLocking(t *testing.T) {
	audio := engine.NewAudioEngine()

	// 1. Peer / Non-Host Node
	peerNode := NewP2PNode("peer_id_1", "RegularPeer", audio)
	defer peerNode.Close()
	peerNode.IsHost = false

	// Attempt to lock room as peer (should be rejected/ignored)
	peerNode.LockRoom("1234")
	if peerNode.IsLocked {
		t.Fatalf("Non-host peer should not be able to lock room")
	}

	// 2. Host Node
	hostNode := NewP2PNode("host_id_1", "HostUser", audio)
	defer hostNode.Close()
	hostNode.HostRoom("test-host-code")
	if !hostNode.IsHost {
		t.Fatalf("Expected hostNode to have IsHost = true")
	}

	// Lock room as host
	hostNode.LockRoom("9876")
	if !hostNode.IsLocked {
		t.Fatalf("Expected room to be locked by host")
	}
	if hostNode.RoomPIN != "9876" {
		t.Fatalf("Expected RoomPIN to be '9876', got '%s'", hostNode.RoomPIN)
	}

	// Unlock room as host
	hostNode.UnlockRoom()
	if hostNode.IsLocked {
		t.Fatalf("Expected room to be unlocked by host")
	}
	if hostNode.RoomPIN != "" {
		t.Fatalf("Expected RoomPIN to be cleared after unlock")
	}
}

func TestDangerousFileQuarantine(t *testing.T) {
	dangerousList := []string{
		"virus.exe", "script.sh", "setup.bat", "run.cmd", "macro.vbs",
		"installer.msi", "app.jar", "binary.bin", "trojan.scr", "payload.ps1",
		"lib.so", "driver.dll", "package.apk", "game.appimage",
	}

	for _, filename := range dangerousList {
		ext := strings.ToLower(filename[strings.LastIndex(filename, "."):])
		if !DangerousFileExtensions[ext] {
			t.Fatalf("Expected DangerousFileExtensions to contain %s", ext)
		}
	}

	safeList := []string{
		"document.pdf", "image.png", "photo.jpg", "notes.txt", "music.mp3",
		"video.mp4", "archive.zip", "data.json", "source.go",
	}

	for _, filename := range safeList {
		ext := strings.ToLower(filename[strings.LastIndex(filename, "."):])
		if DangerousFileExtensions[ext] {
			t.Fatalf("Expected safe file %s to NOT be in DangerousFileExtensions", ext)
		}
	}
}

func TestFileOfferApproval(t *testing.T) {
	// 1. Test SaveAcceptedFile for normal file
	testData := []byte("Limoni Voice Binary File Test Data")
	offer := &FileOffer{
		TransferID: "offer_test_1",
		SenderID:   "peer_99",
		SenderNick: "Alice",
		FileName:   "test_document.txt",
		FileSize:   int64(len(testData)),
		IsCode:     false,
		Checksum:   "abc123456",
		Data:       testData,
	}

	savedPath, err := SaveAcceptedFile(offer)
	if err != nil {
		t.Fatalf("SaveAcceptedFile failed: %v", err)
	}
	defer os.Remove(savedPath)

	readBack, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("Failed to read back saved file: %v", err)
	}
	if string(readBack) != string(testData) {
		t.Fatalf("Read back data does not match original: %s", string(readBack))
	}

	// 2. Test SaveAcceptedFile for code snippet
	codeData := []byte("package main\n\nfunc main() {\n\tprintln(\"Hello Limoni!\")\n}")
	codeOffer := &FileOffer{
		TransferID: "offer_test_2",
		SenderID:   "peer_99",
		SenderNick: "Alice",
		FileName:   "snippet.go",
		FileSize:   int64(len(codeData)),
		IsCode:     true,
		Checksum:   "code123456",
		Data:       codeData,
	}

	codePath, err := SaveAcceptedFile(codeOffer)
	if err != nil {
		t.Fatalf("SaveAcceptedFile for code failed: %v", err)
	}
	defer os.Remove(codePath)

	codeReadBack, err := os.ReadFile(codePath)
	if err != nil {
		t.Fatalf("Failed to read back code snippet: %v", err)
	}
	if string(codeReadBack) != string(codeData) {
		t.Fatalf("Code snippet read back does not match original: %s", string(codeReadBack))
	}
}

func TestCodeSnippetTransferNoDeadlock(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("sender_node", "Sender", audio)
	defer node.Close()

	// Setting up local room
	node.HostRoom("code-deadlock-test")

	// Verify calling SendCodeSnippet or SendFileBytes when no peers are connected returns error cleanly without deadlock
	err := node.SendCodeSnippet("test.go", "package main")
	if err == nil {
		t.Fatalf("Expected error when sending code with 0 peers connected")
	}

	// Verify node mutex is still usable and not deadlocked
	if !node.mu.TryLock() {
		t.Fatal("node mutex left locked after the failed send")
	}
	node.mu.Unlock()
}

func TestCrossPlatformTransfersDir(t *testing.T) {
	dlDir := GetLimoniTransfersDir()
	if dlDir == "" {
		t.Fatalf("Expected non-empty LimoniTransfersDir")
	}
	if !strings.Contains(dlDir, "LimoniTransfers") {
		t.Fatalf("Expected path to contain 'LimoniTransfers', got: %s", dlDir)
	}
}

func TestPINProtectionEnforcement(t *testing.T) {
	audio := engine.NewAudioEngine()

	// 1. Setup Host with PIN 4829
	hostNode := NewP2PNode("host_node_pin", "HostUser", audio)
	defer hostNode.Close()
	roomCode := "pin-protect-test-room"
	hostNode.HostRoom(roomCode)
	hostNode.LockRoom("4829")

	if !hostNode.IsLocked || hostNode.RoomPIN != "4829" {
		t.Fatalf("Expected host to be locked with PIN 4829")
	}

	// 2. Client joins without PIN or with wrong PIN -> Should be rejected
	wrongJoinPkt := P2PPacket{
		Type:      PacketJoinRequest,
		RoomCode:  hostNode.roomID,
		SenderID:  "intruder_1",
		Nickname:  "Intruder",
		PIN:       "0000",
		Timestamp: time.Now().UnixMilli(),
	}
	hostNode.handlePacket(&wrongJoinPkt, nil)

	hostNode.mu.RLock()
	_, intruderAccepted := hostNode.Peers["intruder_1"]
	hostNode.mu.RUnlock()
	if intruderAccepted {
		t.Fatalf("Intruder with wrong PIN was incorrectly accepted into room!")
	}

	// 3. Client sends random hello packet while host is locked -> Must NOT auto-register
	helloPkt := P2PPacket{
		Type:      PacketHello,
		RoomCode:  hostNode.roomID,
		SenderID:  "intruder_2",
		Nickname:  "Intruder2",
		Timestamp: time.Now().UnixMilli(),
	}
	hostNode.handlePacket(&helloPkt, nil)

	hostNode.mu.RLock()
	_, intruder2Accepted := hostNode.Peers["intruder_2"]
	hostNode.mu.RUnlock()
	if intruder2Accepted {
		t.Fatalf("Intruder sending PacketHello was incorrectly auto-registered into locked room!")
	}

	// 4. Valid client joins with correct PIN 4829 -> Should be admitted
	validJoinPkt := P2PPacket{
		Type:      PacketJoinRequest,
		RoomCode:  hostNode.roomID,
		SenderID:  "friend_1",
		Nickname:  "FriendAlice",
		PIN:       "4829",
		Timestamp: time.Now().UnixMilli(),
	}
	hostNode.handlePacket(&validJoinPkt, nil)

	hostNode.mu.RLock()
	friendPeer, friendAccepted := hostNode.Peers["friend_1"]
	hostNode.mu.RUnlock()
	if !friendAccepted || friendPeer == nil {
		t.Fatalf("Valid friend with correct PIN 4829 was not admitted into room")
	}
}

func TestHostMigrationPINPreservation(t *testing.T) {
	audio := engine.NewAudioEngine()

	// 1. Original Host & Peer
	peerNode := NewP2PNode("peer_alice", "Alice", audio)
	defer peerNode.Close()

	roomCode := "migration-test-room"
	peerNode.RoomCode = roomCode
	peerNode.roomID = peerNode.RoomCode
	peerNode.IsConnected = true
	peerNode.IsHost = false
	peerNode.HostID = "host_bob"
	peerNode.HostNick = "Bob"
	peerNode.RoomPIN = "7788"
	peerNode.IsLocked = true

	// Add host Bob to Alice's peers
	peerNode.Peers["host_bob"] = &PeerInfo{
		ID:       "host_bob",
		Nickname: "Bob",
		LastSeen: time.Now(),
	}

	// 2. Bob leaves the room (PacketLeave from Host)
	leavePkt := signedLeave(peerNode, "host_bob", "Bob")
	peerNode.handlePacket(&leavePkt, nil)

	// 3. Verify Alice is now the elected host and retains RoomPIN and IsLocked!
	peerNode.mu.RLock()
	isHost := peerNode.IsHost
	roomPIN := peerNode.RoomPIN
	isLocked := peerNode.IsLocked
	peerNode.mu.RUnlock()

	if !isHost {
		t.Fatalf("Expected Alice to become new Host after Bob left")
	}
	if !isLocked {
		t.Fatalf("Expected Alice's room to remain locked after host migration")
	}
	if roomPIN != "7788" {
		t.Fatalf("Expected Alice's RoomPIN to be '7788', got '%s'", roomPIN)
	}
}

func TestSanitizeFilenameSecurity(t *testing.T) {
	cases := []struct {
		input    string
		isCode   bool
		expected string
	}{
		{"../../../../etc/shadow", false, "shadow"},
		{"report.txt&calc.exe", false, "report.txtcalc.exe"},
		{"malicious;rm -rf /", false, "maliciousrm -rf"},
		{"pipe|injection.sh", false, "pipeinjection.sh"},
		{"$(reboot).png", false, "reboot.png"},
		{"`whoami`.txt", false, "whoami.txt"},
		{"CON.txt", false, "file_CON.txt"},
		{"prn.pdf", false, "file_prn.pdf"},
		{"aux", false, "file_aux"},
		{"NUL.dat", false, "file_NUL.dat"},
		{"", true, "snippet.txt"},
		{"", false, "received_file.bin"},
		{"....", false, "received_file.bin"},
		{"normal_document.pdf", false, "normal_document.pdf"},
	}

	for _, c := range cases {
		got := sanitizeFilename(c.input, c.isCode)
		if got != c.expected {
			t.Errorf("sanitizeFilename(%q, %v) = %q; want %q", c.input, c.isCode, got, c.expected)
		}
	}
}

func TestFileTransferSizeAndChunkLimits(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("test_receiver", "Receiver", audio)
	node.IsConnected = true
	node.RoomCode = e2ee.NormalizeCode("LIMIT-TEST")
	node.roomID = node.RoomCode

	// 1. PacketFileHeader exceeding 50 MB should be rejected
	oversizedPkt := P2PPacket{
		Type:      PacketFileHeader,
		RoomCode:  "LIMIT-TEST",
		SenderID:  "attacker",
		Nickname:  "Attacker",
		Timestamp: time.Now().UnixMilli(),
		FileMeta: &FileMetadata{
			TransferID:  "oversized_tx",
			FileName:    "huge.iso",
			FileSize:    60 * 1024 * 1024, // 60 MB > 50 MB limit
			TotalChunks: 100,
		},
	}
	node.handlePacket(&oversizedPkt, nil)

	node.mu.RLock()
	_, exists := node.incomingTransfers["oversized_tx"]
	node.mu.RUnlock()
	if exists {
		t.Fatalf("Expected oversized transfer to be rejected, but was accepted into incomingTransfers")
	}

	// 2. PacketFileHeader exceeding MaxFileChunks should be rejected
	tooManyChunksPkt := P2PPacket{
		Type:      PacketFileHeader,
		RoomCode:  "LIMIT-TEST",
		SenderID:  "attacker",
		Nickname:  "Attacker",
		Timestamp: time.Now().UnixMilli(),
		FileMeta: &FileMetadata{
			TransferID:  "chunks_tx",
			FileName:    "split.bin",
			FileSize:    10 * 1024 * 1024,
			TotalChunks: 5000, // > MaxFileChunks (50 MB / 16 KB chunks = 3200)
		},
	}
	node.handlePacket(&tooManyChunksPkt, nil)

	node.mu.RLock()
	_, exists = node.incomingTransfers["chunks_tx"]
	node.mu.RUnlock()
	if exists {
		t.Fatalf("Expected transfer with 5000 chunks to be rejected, but was accepted")
	}

	// 3. Valid file header should be accepted
	validPkt := P2PPacket{
		Type:      PacketFileHeader,
		RoomCode:  "LIMIT-TEST",
		SenderID:  "good_peer",
		Nickname:  "GoodGuy",
		Timestamp: time.Now().UnixMilli(),
		FileMeta: &FileMetadata{
			TransferID:  "valid_tx",
			FileName:    "data.json",
			FileSize:    1024,
			TotalChunks: 2,
		},
	}
	node.handlePacket(&validPkt, nil)

	node.mu.RLock()
	_, exists = node.incomingTransfers["valid_tx"]
	node.mu.RUnlock()
	if !exists {
		t.Fatalf("Expected valid transfer to be accepted into incomingTransfers")
	}

	// 4. PacketFileChunk with negative or out-of-range chunk index should be ignored
	badChunkPkt := P2PPacket{
		Type:      PacketFileChunk,
		RoomCode:  "LIMIT-TEST",
		SenderID:  "good_peer",
		Nickname:  "GoodGuy",
		Timestamp: time.Now().UnixMilli(),
		Payload:   []byte("chunk_data"),
		FileMeta: &FileMetadata{
			TransferID: "valid_tx",
			ChunkIndex: 99, // TotalChunks is 2! Out of bounds
		},
	}
	node.handlePacket(&badChunkPkt, nil)

	node.mu.RLock()
	tr := node.incomingTransfers["valid_tx"]
	_, hasBadChunk := tr.Chunks[99]
	node.mu.RUnlock()
	if hasBadChunk {
		t.Fatalf("Expected out-of-bounds chunk 99 to be rejected, but was stored")
	}
}

func TestControlPacketReplayProtection(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("test_receiver_2", "Receiver2", audio)
	node.IsConnected = true
	node.RoomCode = e2ee.NormalizeCode("REPLAY-TEST")
	node.roomID = node.RoomCode

	// Register a peer
	node.Peers["peer_alice"] = &PeerInfo{
		ID:       "peer_alice",
		Nickname: "Alice",
	}

	// 1. A stale PacketLeave from 60 seconds ago should be dropped by freshness check
	staleLeave := signedLeaveAt(node, "peer_alice", "Alice", time.Now().UnixMilli()-60000) // 60s in the past
	node.handlePacket(&staleLeave, nil)

	node.mu.RLock()
	_, peerStillExists := node.Peers["peer_alice"]
	node.mu.RUnlock()
	if !peerStillExists {
		t.Fatalf("Stale PacketLeave should have been dropped, but peer was deleted")
	}

	// 2. A fresh PacketLeave with valid current timestamp should be processed
	freshLeave := signedLeave(node, "peer_alice", "Alice")
	node.handlePacket(&freshLeave, nil)

	node.mu.RLock()
	_, peerStillExists = node.Peers["peer_alice"]
	node.mu.RUnlock()
	if peerStillExists {
		t.Fatalf("Fresh PacketLeave should have been processed, but peer still exists")
	}

	// 3. Replaying the EXACT same fresh packet immediately should be dropped by deduplicator
	node.Peers["peer_alice"] = &PeerInfo{
		ID:       "peer_alice",
		Nickname: "Alice",
	}
	node.handlePacket(&freshLeave, nil) // Replay!

	node.mu.RLock()
	_, peerStillExists = node.Peers["peer_alice"]
	node.mu.RUnlock()
	if !peerStillExists {
		t.Fatalf("Replayed PacketLeave should have been dropped by deduplicator, but was processed")
	}
}

func TestRelayHostTokenLifecycle(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("user_alice", "Alice", audio)
	defer node.Close()

	// 1. Initially hostToken should be empty
	node.mu.RLock()
	if node.hostToken != "" {
		t.Fatalf("Expected empty hostToken initially, got %s", node.hostToken)
	}
	node.mu.RUnlock()

	// 2. Receive room_created with HostToken
	node.handleRelaySignal(protocol.Signal{
		Type:      protocol.SigRoomCreated,
		Proto:     protocol.SignalVersion,
		RoomCode:  "TEST-ROOM",
		HostToken: "secret_token_1234567890abcdef",
	})

	node.mu.RLock()
	if node.hostToken != "secret_token_1234567890abcdef" {
		t.Fatalf("Expected hostToken 'secret_token_1234567890abcdef', got %s", node.hostToken)
	}
	node.mu.RUnlock()

	// 3. LeaveRoom should clear hostToken
	node.LeaveRoom()

	node.mu.RLock()
	if node.hostToken != "" {
		t.Fatalf("Expected empty hostToken after LeaveRoom, got %s", node.hostToken)
	}
	node.mu.RUnlock()

	// 4. Test new_host promotion with HostToken. The PIN is never carried by the relay:
	// a promoted host keeps enforcing the PIN it already knows locally.
	node.mu.Lock()
	node.RoomPIN = "7777"
	node.mu.Unlock()
	node.handleRelaySignal(protocol.Signal{
		Type:      protocol.SigNewHost,
		RoomCode:  "TEST-ROOM-2",
		SenderID:  node.LocalID,
		Nickname:  node.Nickname,
		IsLocked:  true,
		HostToken: "migrated_token_99999",
	})

	node.mu.RLock()
	if !node.IsHost {
		t.Fatalf("Expected IsHost to be true after promotion")
	}
	if node.hostToken != "migrated_token_99999" {
		t.Fatalf("Expected hostToken 'migrated_token_99999', got %s", node.hostToken)
	}
	if node.RoomPIN != "7777" || !node.IsLocked {
		t.Fatalf("Expected locked room with local PIN '7777', got locked=%v pin=%s", node.IsLocked, node.RoomPIN)
	}
	node.mu.RUnlock()
}

func TestChatMessageLengthCap(t *testing.T) {
	node := NewP2PNode("user_alice", "Alice", nil)
	defer node.Close()

	node.mu.Lock()
	node.IsConnected = true
	node.RoomCode = "CHAT-ROOM"
	node.roomID = node.RoomCode
	node.mu.Unlock()

	ch := make(chan string, 1)
	node.OnChatMessage = func(senderID, nickname, text string, ts time.Time) {
		ch <- text
	}

	// Simulate incoming chat packet exceeding 16KB limit (e.g. 32KB payload)
	oversizedPayload := strings.Repeat("A", 32000)
	pkt := P2PPacket{
		Type:      PacketChatMessage,
		RoomCode:  "CHAT-ROOM",
		SenderID:  "peer_bob",
		Nickname:  "Bob",
		Payload:   []byte(oversizedPayload),
		Timestamp: time.Now().UnixMilli(),
		Seq:       1,
	}

	node.handlePacket(&pkt, nil)

	select {
	case receivedText := <-ch:
		if len(receivedText) != 16384 {
			t.Fatalf("Expected chat message payload to be capped to 16384 bytes, got %d bytes", len(receivedText))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Timed out waiting for OnChatMessage callback")
	}
}

func TestSendAudioPreRollLookback(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := NewP2PNode("sender_preroll", "Sender", audio)
	defer node.Close()

	aead := testKeyring("preroll-test-room")

	priorityCh := make(chan []byte, 10)

	node.mu.Lock()
	node.IsConnected = true
	node.RoomCode = "preroll-test-room"
	node.roomID = "preroll-test-room"
	node.keyring = aead
	node.Peers["peer_1"] = &PeerInfo{ID: "peer_1", Nickname: "Bob"}
	node.isRelayConnected = true
	node.wsPriorityCh = priorityCh
	node.mu.Unlock()

	frame := func(amplitude float64) []byte {
		pcm := make([]byte, engine.AudioChunkSize)
		for i := 0; i < engine.AudioFrameSamples; i++ {
			v := int16(amplitude * math.Sin(2*math.Pi*300*float64(i)/engine.AudioSampleRate))
			binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
		}
		return pcm
	}

	// 1. Silent frames: should be buffered in audioPreRoll, not sent across network
	node.SendAudio(0.001, false, frame(30))
	node.SendAudio(0.002, false, frame(60))

	node.mu.RLock()
	preRollCount := len(node.audioPreRoll)
	hangover := node.silenceHangover
	node.mu.RUnlock()

	if preRollCount != 2 {
		t.Fatalf("Expected 2 pre-roll frames buffered during silence, got %d", preRollCount)
	}
	if hangover != 0 {
		t.Fatalf("Expected silenceHangover=0 during silence, got %d", hangover)
	}
	if len(priorityCh) != 0 {
		t.Fatalf("Expected 0 packets transmitted during silence, got %d", len(priorityCh))
	}

	// 2. Speech onset frame: should flush 2 pre-roll frames + send current speech frame (3 packets total)
	node.SendAudio(0.020, true, frame(6000))

	node.mu.RLock()
	preRollAfter := len(node.audioPreRoll)
	hangoverAfter := node.silenceHangover
	node.mu.RUnlock()

	if preRollAfter != 0 {
		t.Fatalf("Expected pre-roll buffer to be cleared after speech onset, got %d", preRollAfter)
	}
	if hangoverAfter != 25 {
		t.Fatalf("Expected silenceHangover=25 after speech onset, got %d", hangoverAfter)
	}
	if len(priorityCh) != 3 {
		t.Fatalf("Expected 3 packets transmitted (2 pre-roll + 1 speech), got %d", len(priorityCh))
	}

	// Verify the 3 packets in order (consecutive audio sequence numbers, Opus payloads, RMS kept)
	var lastSeq uint32
	sizes := map[int]bool{}
	for i, expectedRMS := range []float64{0.001, 0.002, 0.020} {
		frameData := <-priorityCh
		if frameData[0] != protocol.FrameRealtime {
			t.Fatalf("Expected realtime relay frame class, got %d", frameData[0])
		}
		var pkt P2PPacket
		if err := openPacket(frameData[1:], &pkt, aead); err != nil {
			t.Fatalf("Failed to decrypt packet %d: %v", i, err)
		}
		if pkt.Type != PacketAudio || len(pkt.Payload) == 0 || len(pkt.Payload) > 200 {
			t.Fatalf("Packet %d is not a compact Opus audio frame (%d bytes)", i, len(pkt.Payload))
		}
		sizes[len(pkt.Payload)] = true
		if math.Abs(pkt.RMS-expectedRMS) > 1e-6 {
			t.Fatalf("Packet %d RMS mismatch: expected %v, got %v", i, expectedRMS, pkt.RMS)
		}
		if i > 0 && pkt.Seq != lastSeq+1 {
			t.Fatalf("Packet %d sequence %d does not follow %d", i, pkt.Seq, lastSeq)
		}
		lastSeq = pkt.Seq
		if !pkt.Speaking {
			t.Fatalf("Expected packet %d Speaking=true, got false", i)
		}
	}
	if len(sizes) != 1 {
		t.Fatalf("Opus packets must be constant bitrate, got sizes %v", sizes)
	}
}

func TestRelayTokenConfig(t *testing.T) {
	os.Setenv("LIMONI_RELAY_TOKEN", "test-token-xyz")
	defer os.Unsetenv("LIMONI_RELAY_TOKEN")

	audio := engine.NewAudioEngine()
	node := NewP2PNode("user_alice", "Alice", audio)
	defer node.Close()

	if node.RelayToken != "test-token-xyz" {
		t.Fatalf("Expected node.RelayToken to be 'test-token-xyz', got %q", node.RelayToken)
	}
}

func TestP2PScreenShareFPSPacket(t *testing.T) {
	node := NewP2PNode("fps-test-id", "Tester", nil)
	node.HostRoom("fps-room")
	defer node.LeaveRoom()

	peerID := "peer-fps"
	node.Peers[peerID] = &PeerInfo{
		ID:       peerID,
		Nickname: "GamerPeer",
		LastSeen: time.Now(),
	}

	// Simulate PacketScreenShareStart with 120 FPS
	startPkt := P2PPacket{
		Type:            PacketScreenShareStart,
		RoomCode:        node.roomID,
		SenderID:        peerID,
		Nickname:        "GamerPeer",
		IsSharingScreen: true,
		VideoPort:       50100,
		VideoFPS:        120,
	}

	node.handlePacket(&startPkt, nil)

	peer := node.Peers[peerID]
	if !peer.IsSharingScreen || peer.VideoFPS != 120 {
		t.Fatalf("Expected peer to be sharing at 120 FPS, got sharing=%v fps=%d", peer.IsSharingScreen, peer.VideoFPS)
	}

	// Simulate PacketScreenShareStop
	stopPkt := P2PPacket{
		Type:            PacketScreenShareStop,
		RoomCode:        node.roomID,
		SenderID:        peerID,
		Nickname:        "GamerPeer",
		IsSharingScreen: false,
	}

	node.handlePacket(&stopPkt, nil)
	if peer.IsSharingScreen || peer.VideoFPS != 0 {
		t.Fatalf("Expected peer sharing to stop and fps reset to 0, got sharing=%v fps=%d", peer.IsSharingScreen, peer.VideoFPS)
	}
}

func TestVideo120FPSPrefixAndQueues(t *testing.T) {
	aead := testKeyring("video-queues")

	// 1. Video packets carry no plaintext marker (scheduling class lives in the relay frame header)
	vidPkt := P2PPacket{
		Type:     PacketScreenShareData,
		RoomCode: "TEST-120",
		SenderID: "streamer",
		Seq:      100,
		Payload:  make([]byte, 1128),
	}
	encVid, err := sealPacket(&vidPkt, aead)
	if err != nil {
		t.Fatalf("encode video packet failed: %v", err)
	}
	if bytes.HasPrefix(encVid, []byte("LVV1")) || bytes.HasPrefix(encVid, []byte("LVS1")) {
		t.Fatalf("Sealed video packet must not carry a fixed magic prefix")
	}

	// 2. Control/Ping packets are equally unmarked
	pingPkt := P2PPacket{
		Type:      PacketPing,
		RoomCode:  "TEST-120",
		SenderID:  "streamer",
		Seq:       100,
		Timestamp: time.Now().UnixMilli(),
	}
	encPing, err := sealPacket(&pingPkt, aead)
	if err != nil {
		t.Fatalf("encode ping packet failed: %v", err)
	}
	if bytes.Equal(encPing[:4], encVid[:4]) {
		t.Fatalf("Sealed packets should start with random nonces")
	}

	// 3. Decrypt must accept both packet kinds cleanly
	var decVid P2PPacket
	if err := openPacket(encVid, &decVid, aead); err != nil {
		t.Fatalf("failed to decrypt LVV1 packet: %v", err)
	}
	if decVid.Type != PacketScreenShareData || len(decVid.Payload) != 1128 {
		t.Fatalf("corrupted decrypted video packet: %+v", decVid)
	}

	var decPing P2PPacket
	if err := openPacket(encPing, &decPing, aead); err != nil {
		t.Fatalf("failed to decrypt LVS1 packet: %v", err)
	}
	if decPing.Type != PacketPing {
		t.Fatalf("corrupted decrypted ping packet: %+v", decPing)
	}

	// 4. The reorder buffer releases a gap after its wait time instead of stalling.
	reorder := video.NewReorder(50 * time.Millisecond)
	now := time.Now()
	for i := uint32(1); i <= 30; i++ {
		if chunks := reorder.Push(i, []byte(fmt.Sprintf("frame-%d", i)), now); len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
	}
	for i := uint32(32); i <= 80; i++ { // 31 lost
		reorder.Push(i, []byte(fmt.Sprintf("frame-%d", i)), now)
	}
	if flushed := reorder.Tick(now.Add(60 * time.Millisecond)); len(flushed) != 49 {
		t.Fatalf("Reorder buffer failed to advance past dropped packet: %d", len(flushed))
	}
}

func TestVideo120FPSKeyframeBurstAndJitter(t *testing.T) {
	// 1. An 80-chunk keyframe burst passes to the player without loss.
	rx := &screenRx{n: NewP2PNode("rx", "rx", nil), reorder: video.NewReorder(0), playerCh: make(chan playerChunk, 192), stop: make(chan struct{})}
	for i := uint32(1); i <= 80; i++ {
		rx.onData(i, []byte(fmt.Sprintf("iframe-slice-%d", i)))
	}
	if len(rx.playerCh) != 80 {
		t.Fatalf("Expected all 80 keyframe chunks in playerCh without loss, got %d", len(rx.playerCh))
	}

	// 2. Jitter tolerance: packet 2 arrives after 3..28 within the wait time, nothing is lost.
	reorder := video.NewReorder(100 * time.Millisecond)
	now := time.Now()
	if p1 := reorder.Push(1, []byte("data-1"), now); len(p1) != 1 {
		t.Fatalf("Expected packet 1, got %v", p1)
	}
	for i := uint32(3); i <= 28; i++ {
		if chunks := reorder.Push(i, []byte(fmt.Sprintf("data-%d", i)), now); len(chunks) != 0 {
			t.Fatalf("Expected 0 chunks while packet 2 is delayed, got %d for seq %d", len(chunks), i)
		}
	}
	if early := reorder.Tick(now.Add(40 * time.Millisecond)); len(early) != 0 {
		t.Fatal("gap released before its wait time")
	}
	out := reorder.Push(2, []byte("data-2"), now.Add(50*time.Millisecond))
	if len(out) != 27 {
		t.Fatalf("Expected all 27 chunks (2..28) recovered in exact order, got %d", len(out))
	}
	if string(out[0]) != "data-2" || string(out[26]) != "data-28" {
		t.Fatalf("Unexpected ordering: first=%s, last=%s", string(out[0]), string(out[26]))
	}
}
