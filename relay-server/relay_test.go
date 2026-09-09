package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestRelayServerRoomMatchingAndForwarding(t *testing.T) {
	server := NewRelayServer()
	s := httptest.NewServer(server.upgraderHandler())
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"

	// Connect Host
	hostConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect host: %v", err)
	}
	defer hostConn.Close()

	// Host creates room
	hostMsg := ControlMessage{
		Type:     "host_room",
		RoomCode: "TEST-1234",
		SenderID: "host_1",
		Nickname: "Alice",
	}
	hostData, _ := json.Marshal(hostMsg)
	if err := hostConn.WriteMessage(websocket.TextMessage, hostData); err != nil {
		t.Fatalf("Failed to send host_room: %v", err)
	}

	// Read room_created
	_, resp, err := hostConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read room_created: %v", err)
	}
	var createdMsg ControlMessage
	json.Unmarshal(resp, &createdMsg)
	if createdMsg.Type != "room_created" {
		t.Fatalf("Expected room_created, got %s", createdMsg.Type)
	}

	// Connect Joiner
	joinConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect joiner: %v", err)
	}
	defer joinConn.Close()

	// Joiner joins room
	joinMsg := ControlMessage{
		Type:     "join_room",
		RoomCode: "TEST-1234",
		SenderID: "joiner_1",
		Nickname: "Bob",
	}
	joinData, _ := json.Marshal(joinMsg)
	if err := joinConn.WriteMessage(websocket.TextMessage, joinData); err != nil {
		t.Fatalf("Failed to send join_room: %v", err)
	}

	// Read welcome on Joiner
	_, joinResp, err := joinConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read welcome: %v", err)
	}
	var welcomeMsg ControlMessage
	json.Unmarshal(joinResp, &welcomeMsg)
	if welcomeMsg.Type != "welcome" || welcomeMsg.SenderID != "host_1" {
		t.Fatalf("Expected welcome from host_1, got %+v", welcomeMsg)
	}

	// Host should receive peer_joined
	_, hostPeerResp, err := hostConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read peer_joined: %v", err)
	}
	var peerJoinedMsg ControlMessage
	json.Unmarshal(hostPeerResp, &peerJoinedMsg)
	if peerJoinedMsg.Type != "peer_joined" || peerJoinedMsg.SenderID != "joiner_1" {
		t.Fatalf("Expected peer_joined for joiner_1, got %+v", peerJoinedMsg)
	}

	// Test high-throughput video packet forwarding (Host -> Joiner)
	for i := 0; i < 50; i++ {
		testVideoChunk := make([]byte, 1316)
		testVideoChunk[0] = byte(i)
		testVideoChunk[1315] = byte(i * 2)
		if err := hostConn.WriteMessage(websocket.BinaryMessage, testVideoChunk); err != nil {
			t.Fatalf("Failed to send video chunk %d: %v", i, err)
		}
		msgType, receivedVideo, err := joinConn.ReadMessage()
		if err != nil {
			t.Fatalf("Joiner failed to read video chunk %d: %v", i, err)
		}
		if msgType != websocket.BinaryMessage || len(receivedVideo) != 1316 || receivedVideo[0] != byte(i) {
			t.Fatalf("Video chunk %d mismatch: type=%d, len=%d", i, msgType, len(receivedVideo))
		}
	}
}

func TestEmptyRoomLeaveNoDeadlock(t *testing.T) {
	server := NewRelayServer()
	s := httptest.NewServer(server.upgraderHandler())
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"

	// 1. Host creates room and disconnects
	hostConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect host: %v", err)
	}
	hostMsg := ControlMessage{
		Type:     "host_room",
		RoomCode: "EMPTY-TEST",
		SenderID: "host_empty",
		Nickname: "HostEmpty",
	}
	hostData, _ := json.Marshal(hostMsg)
	_ = hostConn.WriteMessage(websocket.TextMessage, hostData)

	// Read room_created
	_, _, _ = hostConn.ReadMessage()

	// Close host connection (making room empty)
	hostConn.Close()

	// 2. Immediately create another room with a new client to ensure server is NOT deadlocked
	newHostConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect second host: %v", err)
	}
	defer newHostConn.Close()

	newHostMsg := ControlMessage{
		Type:     "host_room",
		RoomCode: "NEW-ROOM-1",
		SenderID: "host_2",
		Nickname: "Host2",
	}
	newData, _ := json.Marshal(newHostMsg)
	if err := newHostConn.WriteMessage(websocket.TextMessage, newData); err != nil {
		t.Fatalf("Failed to send second host_room: %v", err)
	}

	_, newResp, err := newHostConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read response on second host (possible deadlock!): %v", err)
	}
	var createdMsg ControlMessage
	json.Unmarshal(newResp, &createdMsg)
	if createdMsg.Type != "room_created" {
		t.Fatalf("Expected room_created, got %s", createdMsg.Type)
	}
}

func TestGracePeriodReconnect(t *testing.T) {
	server := NewRelayServer()
	s := httptest.NewServer(server.upgraderHandler())
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"

	// 1. Host creates room
	hostConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect host: %v", err)
	}
	defer hostConn.Close()

	hostMsg := ControlMessage{
		Type:     "host_room",
		RoomCode: "GRACE-1234",
		SenderID: "host_alice",
		Nickname: "Alice",
	}
	hostData, _ := json.Marshal(hostMsg)
	_ = hostConn.WriteMessage(websocket.TextMessage, hostData)
	_, _, _ = hostConn.ReadMessage() // room_created

	// 2. Joiner connects and joins
	joinConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect joiner: %v", err)
	}

	joinMsg := ControlMessage{
		Type:     "join_room",
		RoomCode: "GRACE-1234",
		SenderID: "joiner_bob",
		Nickname: "Bob",
	}
	joinData, _ := json.Marshal(joinMsg)
	_ = joinConn.WriteMessage(websocket.TextMessage, joinData)
	_, _, _ = joinConn.ReadMessage() // welcome
	_, _, _ = hostConn.ReadMessage() // peer_joined

	// 3. Joiner suddenly loses internet connection (closes socket unexpectedly)
	joinConn.Close()

	// 4. Joiner reconnects 100ms later with new connection (within 10s grace window)
	reconnectJoinConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to reconnect joiner: %v", err)
	}
	defer reconnectJoinConn.Close()

	_ = reconnectJoinConn.WriteMessage(websocket.TextMessage, joinData)
	_, welcomeResp, err := reconnectJoinConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read welcome on reconnected joiner: %v", err)
	}
	var reWelcome ControlMessage
	json.Unmarshal(welcomeResp, &reWelcome)
	if reWelcome.Type != "welcome" {
		t.Fatalf("Expected welcome on reconnected joiner, got %s", reWelcome.Type)
	}

	// 5. Host should receive updated peer_joined and NOT peer_left
	_, updateResp, err := hostConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read host update: %v", err)
	}
	var updateMsg ControlMessage
	json.Unmarshal(updateResp, &updateMsg)
	if updateMsg.Type != "peer_joined" {
		t.Fatalf("Expected peer_joined on reconnect, got %s", updateMsg.Type)
	}
}

func (s *RelayServer) upgraderHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	return mux
}

func TestHostHijackingPrevention(t *testing.T) {
	server := NewRelayServer()
	s := httptest.NewServer(server.upgraderHandler())
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"

	// 1. Alice creates room
	aliceConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect Alice: %v", err)
	}
	defer aliceConn.Close()

	aliceMsg := ControlMessage{
		Type:     "host_room",
		RoomCode: "SECURE-101",
		SenderID: "alice_host_id",
		Nickname: "Alice",
	}
	data, _ := json.Marshal(aliceMsg)
	_ = aliceConn.WriteMessage(websocket.TextMessage, data)

	_, resp, err := aliceConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read Alice response: %v", err)
	}
	var createdMsg ControlMessage
	json.Unmarshal(resp, &createdMsg)
	if createdMsg.Type != "room_created" || createdMsg.HostToken == "" {
		t.Fatalf("Expected room_created with HostToken, got %+v", createdMsg)
	}
	aliceToken := createdMsg.HostToken

	// 2. Attacker Bob tries to hijack Alice's room with Alice's sender_id but without token
	bobConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect Bob: %v", err)
	}
	defer bobConn.Close()

	hijackMsgNoToken := ControlMessage{
		Type:     "host_room",
		RoomCode: "SECURE-101",
		SenderID: "alice_host_id", // spoofing Alice
		Nickname: "FakeAlice",
	}
	data, _ = json.Marshal(hijackMsgNoToken)
	_ = bobConn.WriteMessage(websocket.TextMessage, data)

	_, hijackResp, err := bobConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read Bob response: %v", err)
	}
	var errResp ControlMessage
	json.Unmarshal(hijackResp, &errResp)
	if errResp.Type != "error" {
		t.Fatalf("Expected error for missing token hijack, got %+v", errResp)
	}

	// 3. Attacker Bob tries with invalid token
	hijackMsgBadToken := ControlMessage{
		Type:      "host_room",
		RoomCode:  "SECURE-101",
		SenderID:  "alice_host_id",
		Nickname:  "FakeAlice",
		HostToken: "invalid_fake_token_12345678901234567890",
	}
	data, _ = json.Marshal(hijackMsgBadToken)
	_ = bobConn.WriteMessage(websocket.TextMessage, data)

	_, hijackResp2, err := bobConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read Bob response: %v", err)
	}
	json.Unmarshal(hijackResp2, &errResp)
	if errResp.Type != "error" {
		t.Fatalf("Expected error for invalid token hijack, got %+v", errResp)
	}

	// 4. Real Alice reconnects with valid HostToken
	aliceReconnectConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect Alice reconnect: %v", err)
	}
	defer aliceReconnectConn.Close()

	reconnectMsg := ControlMessage{
		Type:      "host_room",
		RoomCode:  "SECURE-101",
		SenderID:  "alice_host_id",
		Nickname:  "Alice",
		HostToken: aliceToken,
	}
	data, _ = json.Marshal(reconnectMsg)
	_ = aliceReconnectConn.WriteMessage(websocket.TextMessage, data)

	_, reconnResp, err := aliceReconnectConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read Alice reconnect: %v", err)
	}
	var reconnMsg ControlMessage
	json.Unmarshal(reconnResp, &reconnMsg)
	if reconnMsg.Type != "room_created" {
		t.Fatalf("Expected room_created for authorized host reconnect, got %+v", reconnMsg)
	}
}

func TestPINBruteForceLockout(t *testing.T) {
	server := NewRelayServer()
	s := httptest.NewServer(server.upgraderHandler())
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"

	// 1. Host creates PIN protected room
	hostConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect host: %v", err)
	}
	defer hostConn.Close()

	hostMsg := ControlMessage{
		Type:     "host_room",
		RoomCode: "PIN-TEST-99",
		SenderID: "host_id",
		Nickname: "PinHost",
		PIN:      "5432",
		IsLocked: true,
	}
	data, _ := json.Marshal(hostMsg)
	_ = hostConn.WriteMessage(websocket.TextMessage, data)
	_, _, _ = hostConn.ReadMessage()

	// 2. Attacker makes 10 wrong PIN attempts
	attackerConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect attacker: %v", err)
	}
	defer attackerConn.Close()

	for i := 1; i <= 9; i++ {
		guessMsg := ControlMessage{
			Type:     "join_room",
			RoomCode: "PIN-TEST-99",
			SenderID: "attacker_id",
			Nickname: "Attacker",
			PIN:      "0000",
		}
		data, _ = json.Marshal(guessMsg)
		_ = attackerConn.WriteMessage(websocket.TextMessage, data)
		_, resp, err := attackerConn.ReadMessage()
		if err != nil {
			t.Fatalf("Premature disconnect on attempt %d: %v", i, err)
		}
		var ctrl ControlMessage
		json.Unmarshal(resp, &ctrl)
		if ctrl.Type != "room_locked" {
			t.Fatalf("Expected room_locked, got %+v", ctrl)
		}
	}

	// 10th attempt should trigger lockout / connection close
	guessMsg10 := ControlMessage{
		Type:     "join_room",
		RoomCode: "PIN-TEST-99",
		SenderID: "attacker_id",
		Nickname: "Attacker",
		PIN:      "0000",
	}
	data, _ = json.Marshal(guessMsg10)
	_ = attackerConn.WriteMessage(websocket.TextMessage, data)

	// Server sends error message and closes with PolicyViolation
	_, resp, _ := attackerConn.ReadMessage()
	var finalCtrl ControlMessage
	json.Unmarshal(resp, &finalCtrl)
	if finalCtrl.Type != "error" {
		t.Fatalf("Expected error on 10th attempt, got %+v", finalCtrl)
	}

	// Next read must be EOF / closed connection
	_, _, err = attackerConn.ReadMessage()
	if err == nil {
		t.Fatalf("Expected socket to be closed after 10 failed attempts!")
	}
}

func TestNoPINLeakInWelcome(t *testing.T) {
	server := NewRelayServer()
	s := httptest.NewServer(server.upgraderHandler())
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"

	// 1. Host creates room with PIN
	hostConn, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer hostConn.Close()

	hostMsg := ControlMessage{
		Type:     "host_room",
		RoomCode: "SECRET-ROOM",
		SenderID: "host_pin",
		Nickname: "Host",
		PIN:      "ultra_secret_pin_999",
		IsLocked: true,
	}
	data, _ := json.Marshal(hostMsg)
	_ = hostConn.WriteMessage(websocket.TextMessage, data)
	_, _, _ = hostConn.ReadMessage()

	// 2. Guest joins with correct PIN
	guestConn, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer guestConn.Close()

	joinMsg := ControlMessage{
		Type:     "join_room",
		RoomCode: "SECRET-ROOM",
		SenderID: "guest_id",
		Nickname: "Guest",
		PIN:      "ultra_secret_pin_999",
	}
	data, _ = json.Marshal(joinMsg)
	_ = guestConn.WriteMessage(websocket.TextMessage, data)

	_, resp, err := guestConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read welcome: %v", err)
	}
	var welcomeMsg ControlMessage
	json.Unmarshal(resp, &welcomeMsg)

	if welcomeMsg.Type != "welcome" {
		t.Fatalf("Expected welcome, got %+v", welcomeMsg)
	}
	if welcomeMsg.PIN != "" {
		t.Fatalf("SECURITY FLAW: Server leaked secret PIN in welcome message: %s", welcomeMsg.PIN)
	}
	if !welcomeMsg.IsLocked {
		t.Fatalf("Expected IsLocked to be true")
	}
}


