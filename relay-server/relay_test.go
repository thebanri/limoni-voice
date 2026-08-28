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

	// Test binary audio forwarding (Host -> Joiner)
	testAudioData := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x42}
	if err := hostConn.WriteMessage(websocket.BinaryMessage, testAudioData); err != nil {
		t.Fatalf("Failed to send binary audio: %v", err)
	}

	// Joiner should receive the exact binary payload
	msgType, receivedAudio, err := joinConn.ReadMessage()
	if err != nil {
		t.Fatalf("Joiner failed to read binary audio: %v", err)
	}
	if msgType != websocket.BinaryMessage || len(receivedAudio) != len(testAudioData) {
		t.Fatalf("Binary audio mismatch: type=%d, len=%d", msgType, len(receivedAudio))
	}
}

func (s *RelayServer) upgraderHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	return mux
}
