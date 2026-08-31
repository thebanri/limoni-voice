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

func (s *RelayServer) upgraderHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	return mux
}

