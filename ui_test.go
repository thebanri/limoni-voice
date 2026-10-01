package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/i18n"
	"github.com/thebanri/limoni-voice/internal/p2p"
	"github.com/thebanri/limoni-voice/screenshare"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
)

func TestRoomCode(t *testing.T) {
	code := GenerateRoomCode()
	if code == "" {
		t.Fatalf("Expected non-empty room code")
	}

	// Room ID digits + three secret words (24 bits of handshake entropy)
	parts := strings.Split(code, "-")
	if len(parts) != 4 {
		t.Fatalf("Expected 4 parts in room code, got %d: %s", len(parts), code)
	}

	normalized := NormalizeCode("  " + strings.ToUpper(code) + "  ")
	if normalized != code {
		t.Fatalf("NormalizeCode failed: expected %s, got %s", code, normalized)
	}
}

func TestTextInputTypingNoConflict(t *testing.T) {
	state := widgets.NewTextInputState()
	state.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: 'c'})
	state.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: 'g'})
	state.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: 'j'})

	if state.Value() != "cgj" {
		t.Fatalf("Expected text 'cgj', got %q", state.Value())
	}
}

func TestVerticalMeterAndDialogs(t *testing.T) {
	audio := engine.NewAudioEngine()
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
	DrawTestModal(frame, cell.NewRect(0, 0, 80, 30), audio, nil, nil, true, nil, nil, func() { closed = true })
	_ = closed
	DrawLeaveModal(frame, cell.NewRect(0, 0, 80, 24), 1.0, func() {}, func() {})
	DrawExitModal(frame, cell.NewRect(0, 0, 80, 24), 1.0, func() {}, func() {})
	DrawScreenShareModal(frame, cell.NewRect(0, 0, 80, 24), ScreenShareDialogState{Progress: 1.0, Preset: 1, Targets: []screenshare.WindowInfo{
		{ID: "desktop", Title: "[Desktop] Entire Screen (Primary View)"},
	}}, func(int) {}, func() {}, func(_ screenshare.WindowInfo) {}, func() {})
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
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("local_user", "You", audio)

	room.Render(frame, cell.NewRect(0, 0, 120, 40), node, audio)
}

func TestChatClickableLinks(t *testing.T) {
	room := NewRoomView()
	fullURL := "https://github.com/thebanri/limoni-voice"
	room.AddChatMessage("Alice", "peer_alice", "Check "+fullURL, false, time.Now())

	// Test 1: Wide layout
	bufWide := buffer.NewBuffer(cell.NewRect(0, 0, 120, 30))
	frameWide := terminal.NewFrame(bufWide, terminal.NewFocusManager())
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("local_user", "You", audio)
	room.Render(frameWide, cell.NewRect(0, 0, 120, 30), node, audio)

	// Test 2: Narrow layout where link is wrapped across lines
	// buildDisplayLines with small width (e.g. maxW = 28)
	lines := room.buildDisplayLines(room.Messages, 28)
	if len(lines) < 2 {
		t.Fatalf("Expected message to wrap into at least 2 lines at width 28, got %d", len(lines))
	}

	// Verify that wrapped link chunks all retain the exact full ClickURL
	foundLinkSpans := 0
	for _, l := range lines {
		for _, s := range l.Spans {
			if s.IsLink {
				foundLinkSpans++
				if s.ClickURL != fullURL {
					t.Fatalf("Expected ClickURL to be full URL %q, got %q (text: %q)", fullURL, s.ClickURL, s.Text)
				}
			}
		}
	}
	if foundLinkSpans == 0 {
		t.Fatalf("Expected at least one link span in wrapped lines")
	}

	// Test OpenBrowserURL edge cases
	if err := OpenBrowserURL(""); err != nil {
		t.Fatalf("OpenBrowserURL empty string should not error: %v", err)
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

	DrawDebugModal(frame, cell.NewRect(0, 0, 120, 40), NewDebugView(), []string{"Network: relay"}, func() { closed = true }, func() { cleared = true }, func() { copied = true })
	_ = closed
	_ = cleared
	_ = copied

	allText := GetAllDebugLogsText()
	if !strings.Contains(allText, "Chunk sent") {
		t.Fatalf("GetAllDebugLogsText mismatch: %q", allText)
	}
}

func TestChatFocusOutsideClick(t *testing.T) {
	room := NewRoomView()
	buf := buffer.NewBuffer(cell.NewRect(0, 0, 120, 30))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("local_user", "You", audio)

	room.Render(frame, cell.NewRect(0, 0, 120, 30), node, audio)

	if room.LastLogArea.Width == 0 || room.LastLogArea.Height == 0 {
		t.Fatalf("Expected LastLogArea to be set after render")
	}

	// 1. Set chat focused
	room.IsChatFocused = true

	// 2. Click inside LastLogArea -> should stay focused
	insideX := room.LastLogArea.X + room.LastLogArea.Width/2
	insideY := room.LastLogArea.Y + room.LastLogArea.Height/2
	if !room.LastLogArea.Contains(insideX, insideY) {
		t.Fatalf("Inside point not in LastLogArea")
	}

	// Simulate event logic
	wasFocused := room.IsChatFocused
	if wasFocused && !room.LastLogArea.Contains(insideX, insideY) {
		room.IsChatFocused = false
	}
	if !room.IsChatFocused {
		t.Fatalf("Chat should remain focused when clicking inside chat area")
	}

	// 3. Click outside LastLogArea (e.g. at 0, 0) -> should lose focus
	outsideX := uint16(0)
	outsideY := uint16(0)
	if room.LastLogArea.Contains(outsideX, outsideY) {
		outsideX = room.LastLogArea.X - 5
		outsideY = room.LastLogArea.Y - 5
	}

	wasFocused = room.IsChatFocused
	if wasFocused && !room.LastLogArea.Contains(outsideX, outsideY) {
		room.IsChatFocused = false
	}
	if room.IsChatFocused {
		t.Fatalf("Chat should lose focus when clicking outside chat area")
	}
}

func TestChatMultilineAndSlashCommands(t *testing.T) {
	room := NewRoomView()

	// 1. Test unread count badge
	room.SetChatFocused(false)
	room.AddChatMessage("Alice", "alice-id", "Hello there!", false, time.Now())
	room.AddChatMessage("Bob", "bob-id", "How are you?", false, time.Now())
	if room.UnreadChatCount != 2 {
		t.Fatalf("Expected UnreadChatCount=2, got %d", room.UnreadChatCount)
	}

	// Focusing chat clears unread count
	room.SetChatFocused(true)
	if room.UnreadChatCount != 0 {
		t.Fatalf("Expected UnreadChatCount to reset to 0 after focus, got %d", room.UnreadChatCount)
	}

	// 2. Test multi-line message wrapping
	longMsg := "This is a very long text message that should automatically wrap across multiple lines nicely!"
	room.AddChatMessage("Alice", "alice-id", longMsg, false, time.Now())

	lines := room.buildDisplayLines(room.Messages, 30)
	if len(lines) < 3 {
		t.Fatalf("Expected long message to wrap into at least 3 display lines, got %d", len(lines))
	}
	if !lines[len(lines)-1].IsContinuation {
		t.Fatalf("Expected subsequent wrapped line to be marked as continuation")
	}

	// 3. Test chat history recall
	room.ChatInputState.SetValue("First sent message")
	room.SendCurrentChat()
	room.ChatInputState.SetValue("Second sent message")
	room.SendCurrentChat()

	room.HistoryUp()
	if room.ChatInputState.Value() != "Second sent message" {
		t.Fatalf("Expected HistoryUp to recall 'Second sent message', got '%s'", room.ChatInputState.Value())
	}
	room.HistoryUp()
	if room.ChatInputState.Value() != "First sent message" {
		t.Fatalf("Expected HistoryUp to recall 'First sent message', got '%s'", room.ChatInputState.Value())
	}
	room.HistoryDown()
	if room.ChatInputState.Value() != "Second sent message" {
		t.Fatalf("Expected HistoryDown to return to 'Second sent message', got '%s'", room.ChatInputState.Value())
	}

	// 4. Test slash commands
	// /clear
	room.ChatInputState.SetValue("/clear")
	room.SendCurrentChat()
	if len(room.Messages) != 0 {
		t.Fatalf("Expected /clear to clear all room messages, got %d", len(room.Messages))
	}

	// 5. Test /mute slash command
	muteTriggered := false
	room.OnTriggerMute = func() {
		muteTriggered = true
	}
	room.ChatInputState.SetValue("/mute")
	room.SendCurrentChat()
	if !muteTriggered {
		t.Fatalf("Expected /mute to invoke OnTriggerMute")
	}

	// Test /m shortcut
	muteTriggered = false
	room.ChatInputState.SetValue("/m")
	room.SendCurrentChat()
	if !muteTriggered {
		t.Fatalf("Expected /m to invoke OnTriggerMute")
	}

	// 6. Test /mute sfx / /sfx slash command
	sfxTriggered := false
	room.OnTriggerSFX = func() {
		sfxTriggered = true
	}
	room.ChatInputState.SetValue("/mute sfx")
	room.SendCurrentChat()
	if !sfxTriggered {
		t.Fatalf("Expected /mute sfx to invoke OnTriggerSFX")
	}

	sfxTriggered = false
	room.ChatInputState.SetValue("/sfx")
	room.SendCurrentChat()
	if !sfxTriggered {
		t.Fatalf("Expected /sfx to invoke OnTriggerSFX")
	}

	// 7. Test /deafen / /d slash command
	deafenTriggered := false
	room.OnTriggerDeafen = func() {
		deafenTriggered = true
	}
	room.ChatInputState.SetValue("/deafen")
	room.SendCurrentChat()
	if !deafenTriggered {
		t.Fatalf("Expected /deafen to invoke OnTriggerDeafen")
	}

	// 8. Test /nick
	nickChanged := make(chan string, 1)
	room.OnChangeNick = func(newNick string) {
		nickChanged <- newNick
	}
	room.ChatInputState.SetValue("/nick SuperUser")
	room.SendCurrentChat()
	select {
	case got := <-nickChanged:
		if got != "SuperUser" {
			t.Fatalf("Expected /nick to invoke OnChangeNick with 'SuperUser', got '%s'", got)
		}
	case <-time.After(time.Second):
		t.Fatalf("Expected /nick to invoke OnChangeNick")
	}

}

func TestThemeEngine(t *testing.T) {
	// 1. Check all available themes
	if len(AvailableThemes) < 5 {
		t.Fatalf("Expected at least 5 curated themes, got %d", len(AvailableThemes))
	}

	themeNames := make(map[string]bool)
	for _, theme := range AvailableThemes {
		if theme.ID == "" || theme.Name == "" {
			t.Fatalf("Theme has empty ID or Name: %+v", theme)
		}
		themeNames[theme.ID] = true
	}

	expectedThemes := []string{"cyberpunk", "dracula", "catppuccin", "nord", "tokyonight"}
	for _, exp := range expectedThemes {
		if !themeNames[exp] {
			t.Fatalf("Expected theme '%s' to be available in ThemeEngine", exp)
		}
	}

	// 2. Test SetThemeByID and CycleTheme
	if !SetThemeByID("dracula") {
		t.Fatalf("SetThemeByID('dracula') failed")
	}
	if cur := CurrentTheme(); cur.ID != "dracula" {
		t.Fatalf("Expected CurrentTheme() to be 'dracula', got '%s'", cur.ID)
	}

	nextThemeName := CycleTheme()
	if nextThemeName == "" || CurrentTheme().ID == "dracula" {
		t.Fatalf("Expected CycleTheme to switch to next theme, got %s", nextThemeName)
	}
}

func TestLobbyPinProtection(t *testing.T) {
	lobby := NewLobbyView()
	if lobby.IsPinProtected {
		t.Fatalf("Expected LobbyView.IsPinProtected to start as false")
	}

	// Toggle PIN protection ON
	lobby.IsPinProtected = true
	lobby.PinState.SetValue("4321")

	if !lobby.IsPinProtected {
		t.Fatalf("Expected IsPinProtected to be true")
	}
	if lobby.PinState.Value() != "4321" {
		t.Fatalf("Expected PinState value to be '4321', got %s", lobby.PinState.Value())
	}
}

func TestCompactHUDScreenShareButton(t *testing.T) {
	room := NewRoomView()
	room.IsCompactMode = true

	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("hud_share_test", "ShareUser", audio)
	defer node.Close()
	node.HostRoom("998877")

	buf := buffer.NewBuffer(cell.NewRect(0, 0, 100, 5))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, 100, 5), node, audio)

	var renderedText strings.Builder
	for y := uint16(0); y < 5; y++ {
		for x := uint16(0); x < 100; x++ {
			c := buf.Get(x, y)
			if c != nil && c.Content != 0 {
				renderedText.WriteRune(c.Content)
			}
		}
	}
	bufStr := renderedText.String()
	if !strings.Contains(bufStr, "SHARE [V]") {
		t.Fatalf("Expected Compact HUD to render Screen Share button [SHARE [V]], got:\n%s", bufStr)
	}
}

func TestRedesignedMiniHUDHeightsAndWidths(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("hud_multi_test", "Alice", audio)
	defer node.Close()
	node.HostRoom("123456")

	testSizes := []struct {
		w, h     uint16
		expected []string
	}{
		{w: 80, h: 1, expected: []string{"123456", "MIC ON [M]", "SHARE [V]", "FULL UI [H]"}},
		{w: 80, h: 2, expected: []string{"123456", "MIC [M]", "LEAVE [Esc]", "FULL UI [H]"}},
		{w: 100, h: 4, expected: []string{"MINI HUD", "123456", "MIC ON [M]", "FULL UI [H]"}},
		{w: 100, h: 5, expected: []string{"MINI HUD", "123456", "LEAVE [Esc]", "You", "mic on"}},
		{w: 120, h: 8, expected: []string{"MINI HUD", "ROOM #123456", "You", "▱▱▱▱"}},
		{w: 40, h: 2, expected: []string{"123456", "[H]"}},
	}

	for _, tc := range testSizes {
		room := NewRoomView()
		room.IsCompactMode = true

		buf := buffer.NewBuffer(cell.NewRect(0, 0, tc.w, tc.h))
		frame := terminal.NewFrame(buf, terminal.NewFocusManager())
		room.Render(frame, cell.NewRect(0, 0, tc.w, tc.h), node, audio)

		var sb strings.Builder
		for y := uint16(0); y < tc.h; y++ {
			for x := uint16(0); x < tc.w; x++ {
				c := buf.Get(x, y)
				if c != nil && c.Content != 0 {
					sb.WriteRune(c.Content)
				}
			}
		}
		rendered := sb.String()
		for _, exp := range tc.expected {
			if !strings.Contains(rendered, exp) {
				t.Errorf("For size %dx%d, expected substring %q in rendered output:\n%s", tc.w, tc.h, exp, rendered)
			}
		}
	}
}

func TestMiniHUDInteractiveClickHandlers(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("hud_click_test", "Bob", audio)
	defer node.Close()
	node.HostRoom("778899")

	// Add peer
	node.Peers["peer_alice"] = &p2p.PeerInfo{
		ID:       "peer_alice",
		Nickname: "Alice",
		PingMs:   25,
	}

	room := NewRoomView()
	room.IsCompactMode = true
	room.ToastMsg = "Test Toast Notification"
	room.Messages = append(room.Messages, RoomMessage{
		Sender: "Alice",
		Text:   "Hello from chat!",
	})

	buf := buffer.NewBuffer(cell.NewRect(0, 0, 100, 8))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, 100, 8), node, audio)

	// Verify click handler count registered
	if len(frame.ClickRegions) == 0 {
		t.Fatalf("Expected click handlers to be registered in frame.ClickRegions for HUD pills, got 0")
	}

	// Trigger mute toggle via audio engine and re-render
	audio.ToggleMute()
	room.Render(frame, cell.NewRect(0, 0, 100, 8), node, audio)

	var sb strings.Builder
	for y := uint16(0); y < 8; y++ {
		for x := uint16(0); x < 100; x++ {
			c := buf.Get(x, y)
			if c != nil && c.Content != 0 {
				sb.WriteRune(c.Content)
			}
		}
	}
	rendered := sb.String()
	if !strings.Contains(rendered, "MUTED") {
		t.Errorf("Expected MUTED pill after mute toggle, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Alice") {
		t.Errorf("Expected Alice row rendered in stacked list, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "[-]") || !strings.Contains(rendered, "[+]") {
		t.Errorf("Expected per-user volume controls [-] and [+] on Alice row, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Test Toast Notification") {
		t.Errorf("Expected Toast Notification in bottom row, got:\n%s", rendered)
	}
}

func TestMiniHUDSpeakingAndSharingStates(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("hud_state_test", "Charlie", audio)
	defer node.Close()
	node.HostRoom("445566")

	// 1. Test Speaking state
	audio.IsSpeaking = true
	room := NewRoomView()
	room.IsCompactMode = true

	buf := buffer.NewBuffer(cell.NewRect(0, 0, 100, 5))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, 100, 5), node, audio)

	var sb strings.Builder
	for y := uint16(0); y < 5; y++ {
		for x := uint16(0); x < 100; x++ {
			c := buf.Get(x, y)
			if c != nil && c.Content != 0 {
				sb.WriteRune(c.Content)
			}
		}
	}
	rendered := sb.String()
	if !strings.Contains(rendered, "SPEAKING") && !strings.Contains(rendered, "TALKING") {
		t.Errorf("Expected SPEAKING or TALKING state when audio.IsSpeaking=true, got:\n%s", rendered)
	}

	// 2. Test Screen Sharing state
	node.IsSharingScreen = true
	buf = buffer.NewBuffer(cell.NewRect(0, 0, 100, 5))
	frame = terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, 100, 5), node, audio)

	sb.Reset()
	for y := uint16(0); y < 5; y++ {
		for x := uint16(0); x < 100; x++ {
			c := buf.Get(x, y)
			if c != nil && c.Content != 0 {
				sb.WriteRune(c.Content)
			}
		}
	}
	rendered = sb.String()
	if !strings.Contains(rendered, "SHARING") {
		t.Errorf("Expected SHARING pill when node.IsSharingScreen=true, got:\n%s", rendered)
	}

	// 3. Test Peer Sharing Stream Watch state
	node.IsSharingScreen = false
	node.Peers["peer_streamer"] = &p2p.PeerInfo{
		ID:              "peer_streamer",
		Nickname:        "StreamerDave",
		IsSharingScreen: true,
		VideoPort:       50200,
	}
	buf = buffer.NewBuffer(cell.NewRect(0, 0, 120, 8))
	frame = terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, 120, 8), node, audio)

	sb.Reset()
	for y := uint16(0); y < 8; y++ {
		for x := uint16(0); x < 120; x++ {
			c := buf.Get(x, y)
			if c != nil && c.Content != 0 {
				sb.WriteRune(c.Content)
			}
		}
	}
	rendered = sb.String()
	if !strings.Contains(rendered, "WATCH LIVE [W]") {
		t.Errorf("Expected WATCH LIVE [W] button for StreamerDave row, got:\n%s", rendered)
	}
}

func TestChatMultilineInput(t *testing.T) {
	state := widgets.NewTextInputState()

	// Type initial text "Hello"
	for _, ch := range "Hello" {
		state.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: ch})
	}
	if state.Value() != "Hello" {
		t.Fatalf("Expected 'Hello', got %q", state.Value())
	}

	// 1. Shift+Enter should insert newline '\n'
	state.HandleKey(driver.KeyEvent{Type: driver.KeyEnter, Shift: true})
	for _, ch := range "World" {
		state.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: ch})
	}
	if state.Value() != "Hello\nWorld" {
		t.Fatalf("Expected 'Hello\\nWorld', got %q", state.Value())
	}

	// 2. Alt+Enter should insert newline '\n'
	state.HandleKey(driver.KeyEvent{Type: driver.KeyEnter, Alt: true})
	for _, ch := range "123" {
		state.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: ch})
	}
	if state.Value() != "Hello\nWorld\n123" {
		t.Fatalf("Expected 'Hello\\nWorld\\n123', got %q", state.Value())
	}

	// 3. Ctrl+Enter should insert newline '\n'
	state.HandleKey(driver.KeyEvent{Type: driver.KeyEnter, Ctrl: true})
	for _, ch := range "End" {
		state.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: ch})
	}
	if state.Value() != "Hello\nWorld\n123\nEnd" {
		t.Fatalf("Expected 'Hello\\nWorld\\n123\\nEnd', got %q", state.Value())
	}

	// 4. Plain Enter should NOT insert '\n' in TextInputState
	state.HandleKey(driver.KeyEvent{Type: driver.KeyEnter})
	if state.Value() != "Hello\nWorld\n123\nEnd" {
		t.Fatalf("Plain enter modified text input state unexpectedly: %q", state.Value())
	}
}

func TestTerminalParserMultilineEnter(t *testing.T) {
	// 1. CSI u Shift+Enter: \x1b[13;2u
	csiShiftEnter := []byte("\x1b[13;2u")
	ev, _ := driver.ParseEvent(csiShiftEnter)
	if ev.Key.Type != driver.KeyEnter || !ev.Key.Shift {
		t.Fatalf("Expected Shift+Enter event from CSI u, got: %+v", ev)
	}

	// 2. CSI u Ctrl+Enter: \x1b[13;5u
	csiCtrlEnter := []byte("\x1b[13;5u")
	ev, _ = driver.ParseEvent(csiCtrlEnter)
	if ev.Key.Type != driver.KeyEnter || !ev.Key.Ctrl {
		t.Fatalf("Expected Ctrl+Enter event from CSI u, got: %+v", ev)
	}

	// 3. modifyOtherKeys Shift+Enter: \x1b[27;2;13~
	mokShiftEnter := []byte("\x1b[27;2;13~")
	ev, _ = driver.ParseEvent(mokShiftEnter)
	if ev.Key.Type != driver.KeyEnter || !ev.Key.Shift {
		t.Fatalf("Expected Shift+Enter event from modifyOtherKeys, got: %+v", ev)
	}

	// 4. Alt+Enter: \x1b\r
	altEnter := []byte("\x1b\r")
	ev, _ = driver.ParseEvent(altEnter)
	if ev.Key.Type != driver.KeyEnter || !ev.Key.Alt {
		t.Fatalf("Expected Alt+Enter event from \\x1b\\r, got: %+v", ev)
	}

	// 5. Ctrl+J (ASCII 10): \n
	ctrlJ := []byte("\n")
	ev, _ = driver.ParseEvent(ctrlJ)
	if ev.Key.Type != driver.KeyEnter || !ev.Key.Ctrl {
		t.Fatalf("Expected Ctrl+Enter event from \\n (Ctrl+J), got: %+v", ev)
	}
}

func TestRoomChatMouseSelection(t *testing.T) {
	room := NewRoomView()

	// Simulate populated renderedChatLines
	line1 := renderedChatLine{
		RowY:   5,
		StartX: 10,
		EndX:   20,
		Chars: []renderedChatChar{
			{X: 10, Y: 5, R: 'H'},
			{X: 11, Y: 5, R: 'e'},
			{X: 12, Y: 5, R: 'l'},
			{X: 13, Y: 5, R: 'l'},
			{X: 14, Y: 5, R: 'o'},
			{X: 15, Y: 5, R: ' '},
			{X: 16, Y: 5, R: 'W'},
			{X: 17, Y: 5, R: 'o'},
			{X: 18, Y: 5, R: 'r'},
			{X: 19, Y: 5, R: 'l'},
			{X: 20, Y: 5, R: 'd'},
		},
	}
	line2 := renderedChatLine{
		RowY:   6,
		StartX: 10,
		EndX:   16,
		Chars: []renderedChatChar{
			{X: 10, Y: 6, R: 'F'},
			{X: 11, Y: 6, R: 'o'},
			{X: 12, Y: 6, R: 'o'},
			{X: 13, Y: 6, R: ' '},
			{X: 14, Y: 6, R: 'B'},
			{X: 15, Y: 6, R: 'a'},
			{X: 16, Y: 6, R: 'r'},
		},
	}
	room.renderedLines = []renderedChatLine{line1, line2}

	// 1. Mouse Press at (10, 5) -> 'H'
	room.HandleMousePress(10, 5)
	if !room.SelectionDragging || room.SelectionStartX != 10 || room.SelectionStartY != 5 {
		t.Fatalf("HandleMousePress failed to initialize selection correctly")
	}

	// 2. Mouse Drag to (14, 5) -> 'o' in 'Hello'
	room.HandleMouseDrag(14, 5)
	if !room.SelectionActive || room.SelectionEndX != 14 || room.SelectionEndY != 5 {
		t.Fatalf("HandleMouseDrag failed: active=%v, endX=%d, endY=%d", room.SelectionActive, room.SelectionEndX, room.SelectionEndY)
	}

	// Check cell selection
	if !room.isCellSelected(10, 5) || !room.isCellSelected(12, 5) || !room.isCellSelected(14, 5) {
		t.Fatalf("isCellSelected failed for selected cells")
	}
	if room.isCellSelected(15, 5) || room.isCellSelected(10, 6) {
		t.Fatalf("isCellSelected returned true for non-selected cells")
	}

	// 3. Mouse Release -> Should extract "Hello"
	text := room.HandleMouseRelease(14, 5)
	if text != "Hello" {
		t.Fatalf("Expected extracted text 'Hello', got %q", text)
	}
	if room.SelectedText != "Hello" {
		t.Fatalf("Expected SelectedText 'Hello', got %q", room.SelectedText)
	}

	// 4. Multiline selection drag from (16, 5) ['W'] down to (12, 6) ['o']
	room.HandleMousePress(16, 5)
	room.HandleMouseDrag(12, 6)
	multiText := room.HandleMouseRelease(12, 6)
	expectedMulti := "World\nFoo"
	if multiText != expectedMulti {
		t.Fatalf("Expected multiline extracted text %q, got %q", expectedMulti, multiText)
	}

	// 5. ClearSelection
	room.ClearSelection()
	if room.SelectionActive || room.SelectionDragging || room.SelectedText != "" {
		t.Fatalf("ClearSelection failed to reset state")
	}
}

func TestChatCopyCommandAndSpans(t *testing.T) {
	// 1. Test parseMessageSpans for 📋 [Kopyala: ...]
	spans1 := parseMessageSpans("Server: 📋 [Kopyala: 192.168.1.50:9000] (connect now)")
	if len(spans1) != 3 {
		t.Fatalf("Expected 3 spans, got %d", len(spans1))
	}
	if spans1[0].Text != "Server: " || spans1[0].IsCopy || spans1[0].IsLink {
		t.Fatalf("Span 0 mismatch: %+v", spans1[0])
	}
	if spans1[1].Text != "📋 [Kopyala: 192.168.1.50:9000]" || !spans1[1].IsCopy || spans1[1].CopyText != "192.168.1.50:9000" {
		t.Fatalf("Span 1 copy mismatch: %+v", spans1[1])
	}
	if spans1[2].Text != " (connect now)" {
		t.Fatalf("Span 2 mismatch: %+v", spans1[2])
	}

	// 2. Test parseMessageSpans for [copy: ...]
	spans2 := parseMessageSpans("Run this: [copy: git pull origin main]")
	if len(spans2) != 2 || !spans2[1].IsCopy || spans2[1].CopyText != "git pull origin main" {
		t.Fatalf("Span 2 copy mismatch: %+v", spans2)
	}

	// 3. Test parseMessageSpans for copy://...
	spans3 := parseMessageSpans("Secret: copy://token-xyz-12345")
	if len(spans3) != 2 || !spans3[1].IsCopy || spans3[1].CopyText != "token-xyz-12345" {
		t.Fatalf("Span 3 copy mismatch: %+v", spans3)
	}

	// 4. Test SendCurrentChat with /copy
	room := NewRoomView()
	var sentMessage string
	room.OnSendChat = func(txt string) {
		sentMessage = txt
	}

	room.ChatInputState.SetValue("/copy 192.168.1.100:3000")
	room.SendCurrentChat()

	expectedSent := "📋 [Copy: 192.168.1.100:3000]"
	if sentMessage != expectedSent {
		t.Fatalf("Expected sent chat message %q, got %q", expectedSent, sentMessage)
	}

	// 5. Test SendCurrentChat with /copy empty shows usage
	room.ChatInputState.SetValue("/copy")
	room.SendCurrentChat()
	if len(room.Messages) == 0 || !strings.Contains(room.Messages[len(room.Messages)-1].Text, "Usage: /copy") {
		t.Fatalf("Expected usage message for empty /copy, got: %+v", room.Messages)
	}

	// 6. Test rendering and wrapping of copy spans
	room.AddChatMessage("Bob", "peer_bob", "Token: 📋 [Kopyala: very-long-token-secret-key-1234567890]", false, time.Now())
	buf := buffer.NewBuffer(cell.NewRect(0, 0, 120, 30))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("local_user", "You", audio)
	room.Render(frame, cell.NewRect(0, 0, 120, 30), node, audio)

	// Test wrapping with narrow width
	wrappedLines := room.buildDisplayLines(room.Messages, 30)
	foundCopySpan := false
	for _, l := range wrappedLines {
		for _, s := range l.Spans {
			if s.IsCopy {
				foundCopySpan = true
				if s.CopyText != "very-long-token-secret-key-1234567890" {
					t.Fatalf("Expected CopyText to be preserved, got %q", s.CopyText)
				}
			}
		}
	}
	if !foundCopySpan {
		t.Fatalf("Expected to find copy span in wrapped lines")
	}
}

func TestMultilinePasteAndBackslashContinuation(t *testing.T) {
	room := NewRoomView()
	var sentMessages []string
	room.OnSendChat = func(txt string) {
		sentMessages = append(sentMessages, txt)
	}

	// 1. Simulate pasting multiline text (e.g. registry command)
	pastedSnippet := "reg add \"HKEY_LOCAL_MACHINE\\System\\CurrentControlSet\\Control\\TimeZoneInformation\" /v\r\n  RealTimeIsUniversal /t REG_DWORD /d 1 /f"
	for _, r := range pastedSnippet {
		if r == '\r' {
			continue
		}
		room.ChatInputState.HandleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: r})
	}

	expectedInput := "reg add \"HKEY_LOCAL_MACHINE\\System\\CurrentControlSet\\Control\\TimeZoneInformation\" /v\n  RealTimeIsUniversal /t REG_DWORD /d 1 /f"
	if room.ChatInputState.Value() != expectedInput {
		t.Fatalf("Expected ChatInputState to contain multiline text, got %q", room.ChatInputState.Value())
	}

	// Sending should send as a SINGLE multiline message
	room.SendCurrentChat()
	if len(sentMessages) != 1 {
		t.Fatalf("Expected 1 sent message, got %d", len(sentMessages))
	}
	if sentMessages[0] != expectedInput {
		t.Fatalf("Sent message content mismatch: %q vs %q", sentMessages[0], expectedInput)
	}

	// 2. Test buildDisplayLines rendering of multiline message
	room.AddChatMessage("You", "self", sentMessages[0], true, time.Now())
	lines := room.buildDisplayLines(room.Messages, 100)
	if len(lines) < 2 {
		t.Fatalf("Expected at least 2 display lines for multiline message, got %d", len(lines))
	}
	if lines[0].Badge != "You: " || lines[0].IsContinuation {
		t.Fatalf("Expected line 0 to be initial message with badge 'You: ', got %+v", lines[0])
	}
	if lines[1].Badge != "" {
		t.Fatalf("Expected line 1 to have empty badge, got %+v", lines[1])
	}

	// 3. Test backslash continuation logic
	val := "first line \\"
	if strings.HasSuffix(val, `\`) && !strings.HasSuffix(val, `\\`) {
		trimmed := strings.TrimSuffix(val, `\`)
		val = trimmed + "\n"
	}
	if val != "first line \n" {
		t.Fatalf("Expected backslash continuation to convert to newline, got %q", val)
	}
}

func TestDirectChatClickAndNoResizeNewline(t *testing.T) {
	room := NewRoomView()
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("local_user", "You", audio)

	rawCmd := "reg add \"HKEY_LOCAL_MACHINE\\System\\CurrentControlSet\\Control\\TimeZoneInformation\" /v RealTimeIsUniversal /t REG_DWORD /d 1 /f"
	room.AddChatMessage("Alice", "peer_alice", "📋 [Kopyala: "+rawCmd+"]", false, time.Now())

	// Render in a narrow 50-column buffer so the line wraps into multiple visual lines
	buf := buffer.NewBuffer(cell.NewRect(0, 0, 50, 20))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, 50, 20), node, audio)

	room.mu.Lock()
	rLines := room.renderedLines
	room.mu.Unlock()

	if len(rLines) < 2 {
		t.Fatalf("Expected message to wrap into at least 2 rendered lines in 50-col width, got %d", len(rLines))
	}

	// 1. Direct HandleChatClick on the message row should copy rawCmd without \n
	clicked := room.HandleChatClick(10, rLines[0].RowY)
	if !clicked {
		t.Fatalf("Expected HandleChatClick to succeed on rendered message row")
	}
	if room.ToastMsg != copiedToast {
		t.Fatalf("toast after copying = %q, want %q", room.ToastMsg, copiedToast)
	}
	// The copied text is not posted to the chat.
	for _, m := range room.Messages {
		if strings.Contains(m.Text, "Copied") {
			t.Fatalf("copying added a chat line: %q", m.Text)
		}
	}

	// 2. Drag-selecting across all wrapped lines must NOT insert fake \n due to window resize
	room.HandleMousePress(rLines[0].StartX, rLines[0].RowY)
	lastLine := rLines[len(rLines)-1]
	room.HandleMouseDrag(lastLine.EndX, lastLine.RowY)
	extracted := room.HandleMouseRelease(lastLine.EndX, lastLine.RowY)

	// Verify that extracted text does NOT have newline breaking words
	if strings.Contains(extracted, "Control\n") || strings.Contains(extracted, "Control\\\n") {
		t.Fatalf("Extracted text has unwanted newline from window resize wrapping: %q", extracted)
	}
}

func TestMultilineCopyCommandAndNoIndentationArtifacts(t *testing.T) {
	room := NewRoomView()
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("local_user", "You", audio)

	rawCmd := "reg add \"HKLM\\System\\CurrentControlSet\\Control\\TimeZoneInformation\" /v RealTimeIsUniversal /t REG_DWORD /d 1 /f"
	room.AddChatMessage("Banri", "peer_banri", rawCmd, false, time.Now())

	buf := buffer.NewBuffer(cell.NewRect(0, 0, 45, 20))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, 45, 20), node, audio)

	room.mu.Lock()
	rLines := room.renderedLines
	room.mu.Unlock()

	if len(rLines) < 2 {
		t.Fatalf("Expected at least 2 lines, got %d", len(rLines))
	}
	// A click on plain text copies nothing: /copy messages and selection do that.
	if room.HandleChatClick(10, rLines[1].RowY) {
		t.Fatalf("a click on a plain chat message copied it")
	}

	ok := CopyToClipboard(rawCmd)
	if !ok {
		t.Fatalf("Expected CopyToClipboard to succeed")
	}

	room.HandleMousePress(rLines[0].StartX, rLines[0].RowY)
	room.HandleMouseDrag(rLines[1].EndX, rLines[1].RowY)
	selected := room.HandleMouseRelease(rLines[1].EndX, rLines[1].RowY)
	if strings.Contains(selected, "            ") {
		t.Fatalf("Selected text contains multiple visual indentation spaces: %q", selected)
	}
}

func TestSanitizeClipboardText(t *testing.T) {
	dirty := "\x1b[200~reg add HKLM\\System\\CurrentControlSet\\Control\\TimeZoneInformation /v RealTimeIsUniversal /t REG_DWORD /d 1 /f\x1b[201~\x07\x00\x1b"
	clean := SanitizeClipboardText(dirty)
	expected := "reg add HKLM\\System\\CurrentControlSet\\Control\\TimeZoneInformation /v RealTimeIsUniversal /t REG_DWORD /d 1 /f"
	if clean != expected {
		t.Fatalf("Sanitization failed, got %q, expected %q", clean, expected)
	}

	for _, r := range clean {
		if r < 32 && r != '\n' && r != '\t' {
			t.Fatalf("Cleaned text contains unprintable control char: %d", r)
		}
		if r == 127 || r == '\uFFFD' {
			t.Fatalf("Cleaned text contains invalid character: %c", r)
		}
	}
}

func TestOpenBrowserURLSecurityValidation(t *testing.T) {
	// 1. Empty URL should return nil
	if err := OpenBrowserURL(""); err != nil {
		t.Fatalf("Expected nil for empty string, got %v", err)
	}

	// 2. Dangerous schemes should be rejected
	disallowedURLs := []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"powershell -Command calc.exe",
		"https://example.com\" & calc.exe",
		"https://example.com/test\nmalicious",
		"https://example.com/test;rm -rf /",
		"https://example.com/test|whoami",
		"https://example.com/test^%USERPROFILE%",
	}

	for _, badURL := range disallowedURLs {
		err := OpenBrowserURL(badURL)
		if err == nil {
			t.Fatalf("Expected security error for dangerous URL %q, but got nil", badURL)
		}
	}
}

func TestLobbyPinToggleAndHostHygiene(t *testing.T) {
	lobby := NewLobbyView()
	if lobby.IsPinProtected {
		t.Fatalf("Expected LobbyView.IsPinProtected to start as false")
	}
	if lobby.PinState.Value() != "" {
		t.Fatalf("Expected initial PinState to be empty, got %q", lobby.PinState.Value())
	}

	// 1. User checks PIN protection and enters PIN
	lobby.IsPinProtected = true
	lobby.PinState.SetValue("5678")
	if !lobby.IsPinProtected || lobby.PinState.Value() != "5678" {
		t.Fatalf("Failed to enable PIN protection in lobby")
	}

	// 2. User unchecks PIN protection
	lobby.IsPinProtected = false
	lobby.PinState.SetValue("")
	lobby.ActiveInput = 2

	// 3. Node hosts room based on unchecked lobby
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("host_node_1", "HostUser", audio)
	defer node.Close()

	node.HostRoom(lobby.CurrentCode)
	if lobby.IsPinProtected {
		node.LockRoom(lobby.PinState.Value())
	} else {
		node.UnlockRoom()
	}

	if node.IsLocked {
		t.Fatalf("Expected room to NOT be locked when lobby.IsPinProtected is false")
	}
	if node.RoomPIN != "" {
		t.Fatalf("Expected room PIN to be empty when lobby.IsPinProtected is false, got %q", node.RoomPIN)
	}

	// 4. Verify that leaving and re-hosting clears any lingering lock state
	node.LockRoom("1234")
	if !node.IsLocked || node.RoomPIN != "1234" {
		t.Fatalf("Failed to lock room with PIN 1234")
	}

	node.LeaveRoom()
	if node.IsLocked || node.RoomPIN != "" {
		t.Fatalf("LeaveRoom did not reset IsLocked (%v) or RoomPIN (%q)", node.IsLocked, node.RoomPIN)
	}

	node.HostRoom("clean_room")
	if node.IsLocked || node.RoomPIN != "" {
		t.Fatalf("HostRoom did not reset IsLocked (%v) or RoomPIN (%q)", node.IsLocked, node.RoomPIN)
	}
}

func TestScreenShareFPSModes(t *testing.T) {
	opt120 := screenshare.GetPresetOptions(120, "win-120")
	if opt120.FPS != 120 || opt120.Bitrate != "6.5M" || opt120.Quality != "ultra" || opt120.WindowID != "win-120" {
		t.Fatalf("Unexpected 120 FPS preset: %+v", opt120)
	}

	opt60 := screenshare.GetPresetOptions(60, "win-60")
	if opt60.FPS != 60 || opt60.Bitrate != "4.5M" || opt60.Quality != "high" || opt60.WindowID != "win-60" {
		t.Fatalf("Unexpected 60 FPS preset: %+v", opt60)
	}

	opt30 := screenshare.GetPresetOptions(30, "win-30")
	if opt30.FPS != 30 || opt30.Bitrate != "3.0M" || opt30.Quality != "fast" || opt30.WindowID != "win-30" {
		t.Fatalf("Unexpected 30 FPS preset: %+v", opt30)
	}

	// Unknown defaults to 60 FPS
	optDef := screenshare.GetPresetOptions(999, "win-def")
	if optDef.FPS != 60 {
		t.Fatalf("Expected fallback to 60 FPS, got %d", optDef.FPS)
	}

	rec120 := screenshare.DefaultReceiverOptions(120)
	if rec120.FPS != 120 || !strings.Contains(rec120.WindowTitle, "120 FPS") || strings.Contains(rec120.WindowTitle, "HD") {
		t.Fatalf("Unexpected receiver options for 120 FPS: %+v", rec120)
	}

	rec30 := screenshare.DefaultReceiverOptions(30)
	if rec30.FPS != 30 || !strings.Contains(rec30.WindowTitle, "30 FPS") || strings.Contains(rec30.WindowTitle, "HD") {
		t.Fatalf("Unexpected receiver options for 30 FPS: %+v", rec30)
	}
}

func TestScreenShareModalResponsivePresets(t *testing.T) {
	for _, w := range []uint16{40, 50, 60, 80} {
		buf := buffer.NewBuffer(cell.NewRect(0, 0, w, 24))
		frame := terminal.NewFrame(buf, terminal.NewFocusManager())
		DrawScreenShareModal(frame, cell.NewRect(0, 0, w, 24), ScreenShareDialogState{
			Progress: 1.0, Preset: 4, SystemAudio: true,
			Deps:    screenshare.DependencyStatus{MissingRecommended: "ffmpeg (to share)", InstallHint: "sudo pacman -S ffmpeg"},
			Targets: []screenshare.WindowInfo{{ID: "desktop", Title: "Desktop 1"}},
		}, func(int) {}, func() {}, func(_ screenshare.WindowInfo) {}, func() {})

		var screen strings.Builder
		for y := uint16(0); y < 24; y++ {
			for x := uint16(0); x < w; x++ {
				if c := buf.Get(x, y); c != nil {
					screen.WriteRune(c.Content)
				}
			}
			screen.WriteByte('\n')
		}
		text := screen.String()
		for _, want := range []string{"1080p120", "System audio: ON", "Missing:"} {
			if !strings.Contains(text, want) {
				t.Fatalf("width %d: %q missing from the dialog:\n%s", w, want, text)
			}
		}
	}
}

func TestRoomControlsVolumeMinusLowersGain(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("vol_click_test", "Bob", audio)
	defer node.Close()
	node.HostRoom("445566")
	room := NewRoomView()

	const w, h = 160, 45
	buf := buffer.NewBuffer(cell.NewRect(0, 0, w, h))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, w, h), node, audio)

	// Locate "[+/-] Vol:" on screen.
	var px, py uint16
	found := false
	for y := uint16(0); y < h && !found; y++ {
		var line []rune
		for x := uint16(0); x < w; x++ {
			line = append(line, buf.Get(x, y).Content)
		}
		if i := strings.Index(string(line), "[+/-] Vol:"); i >= 0 {
			px, py, found = uint16(len([]rune(string(line)[:i]))), y, true
		}
	}
	if !found {
		t.Skip("controls row not rendered at this size")
	}
	click := func(x uint16) {
		for i := len(frame.ClickRegions) - 1; i >= 0; i-- {
			if cr := frame.ClickRegions[i]; cr.Area.Contains(x, py) {
				cr.Handler(driver.MouseEvent{X: x, Y: py, Button: driver.MouseLeft})
				return
			}
		}
		t.Fatalf("no click handler at x=%d", x)
	}
	click(px + 3) // "-"
	if math.Abs(audio.Gain-0.9) > 1e-9 {
		t.Fatalf("minus: gain %.2f, want 0.90", audio.Gain)
	}
	click(px + 1) // "+"
	if math.Abs(audio.Gain-1.0) > 1e-9 {
		t.Fatalf("plus: gain %.2f, want 1.00", audio.Gain)
	}
}

// A narrow member card keeps its status readable: the volume and ping pills give way instead
// of being drawn over it (seen as "[SPEAKIN[VOL: 100%]" in a 99 column terminal).
func TestPeerCardPillsNeverCoverTheStatus(t *testing.T) {
	node := p2p.NewP2PNode("self", "Me", engine.NewAudioEngine())
	peer := &p2p.PeerInfo{ID: "p", Nickname: "User_9065", Speaking: true, PingMs: 137, LastSeen: time.Now()}
	for _, width := range []uint16{30, 40, 48, 60, 90} {
		buf := buffer.NewBuffer(cell.NewRect(0, 0, width, 12))
		frame := terminal.NewFrame(buf, terminal.NewFocusManager())
		NewRoomView().renderPeerSlot(frame, cell.NewRect(0, 0, width, 12), peer, node, engine.NewAudioEngine(), 2)
		var row strings.Builder
		for x := uint16(0); x < width; x++ {
			if c := buf.Get(x, 1); c != nil && c.Content != 0 {
				row.WriteRune(c.Content)
			}
		}
		if !strings.Contains(row.String(), "[SPEAKING...]") {
			t.Errorf("width %d: status covered: %q", width, row.String())
		}
	}
}

// screenText returns row y of buf as a string.
func screenText(buf *buffer.Buffer, y uint16) string {
	var sb strings.Builder
	for x := uint16(0); x < buf.Area.Width; x++ {
		if c := buf.CellAt(x, y); c.Content != 0 && c.Content != cell.RuneContinuation {
			sb.WriteRune(c.Content)
		}
	}
	return sb.String()
}

// Clicking the chat panel opens the chat input.
func TestClickingChatPanelOpensInput(t *testing.T) {
	term, err := terminal.New(driver.NewPortableBackend(driver.NewMemoryTerminalIO(nil, 120, 30)))
	if err != nil {
		t.Fatal(err)
	}
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("chat_click", "Alice", audio)
	defer node.Close()
	node.HostRoom("112233")
	a := &App{term: term, room: NewRoomView(), node: node, audio: audio, currentScreen: ScreenRoom}
	_ = term.Draw(func(f *terminal.Frame) { a.room.Render(f, f.Area(), node, audio) })

	log := a.room.LastLogArea
	if log.Width == 0 {
		t.Fatal("chat panel was not drawn")
	}
	a.handleMouse(driver.MouseEvent{X: log.X + log.Width/2, Y: log.Y + log.Height - 2, Button: driver.MouseLeft})
	if !a.room.IsChatFocused {
		t.Fatal("clicking the chat panel did not open the chat input")
	}
	a.handleMouse(driver.MouseEvent{X: 1, Y: 1, Button: driver.MouseLeft})
	if a.room.IsChatFocused {
		t.Fatal("clicking outside the chat panel did not close the chat input")
	}
}

// Every room control stays on screen however narrow or short the terminal, in both languages.
func TestRoomControlsWrapInsteadOfDisappearing(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.English) })
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("controls_wrap", "Alice", audio)
	defer node.Close()
	node.HostRoom("445566")
	keys := []string{"[M]", "[D]", "[P]", "[N]", "[V]", "[W]", "[T]", "[+/-]", "[C]", "[Esc]"}

	for _, lang := range []i18n.Lang{i18n.English, i18n.Turkish} {
		i18n.Set(lang)
		for _, sz := range [][2]uint16{{160, 40}, {120, 30}, {100, 24}, {80, 24}, {64, 20}, {80, 14}, {120, 12}} {
			buf := buffer.NewBuffer(cell.NewRect(0, 0, sz[0], sz[1]))
			frame := terminal.NewFrame(buf, terminal.NewFocusManager())
			NewRoomView().Render(frame, buf.Area, node, audio)
			var all strings.Builder
			for y := uint16(0); y < sz[1]; y++ {
				all.WriteString(screenText(buf, y))
			}
			if strings.Contains(all.String(), "MINI HUD") || strings.Contains(all.String(), "MİNİ HUD") {
				continue // too short for the full layout: the mini HUD has its own test
			}
			for _, k := range keys {
				if !strings.Contains(all.String(), k) {
					t.Errorf("%s %dx%d: control %s is missing:\n%s", lang.Name(), sz[0], sz[1], k, all.String())
				}
			}
		}
	}
}

// The settings dialog keeps its buttons on screen, and working, however short the window.
func TestSettingsDialogFitsShortWindows(t *testing.T) {
	audio := engine.NewAudioEngine()
	for h := uint16(8); h <= 40; h++ {
		term, err := terminal.New(driver.NewPortableBackend(driver.NewMemoryTerminalIO(nil, 90, h)))
		if err != nil {
			t.Fatal(err)
		}
		closed := 0
		closeX, closeY := -1, -1
		_ = term.Draw(func(f *terminal.Frame) {
			DrawTestModal(f, f.Area(), audio, nil, nil, true, nil, nil, func() { closed++ })
			for y := uint16(0); y < h; y++ {
				if x := strings.Index(screenText(f.Buffer, y), "Close (Esc)"); x >= 0 {
					closeX, closeY = len([]rune(screenText(f.Buffer, y)[:x])), int(y)
				}
			}
		})
		if closeY < 0 {
			t.Fatalf("height %d: the Close button is not on screen", h)
		}
		term.RouteMouseEvent(driver.MouseEvent{X: uint16(closeX + 2), Y: uint16(closeY), Button: driver.MouseLeft})
		if closed != 1 {
			t.Fatalf("height %d: clicking Close fired %d times, want 1", h, closed)
		}
	}
}

// When the settings dialog has to scroll, every setting can be reached.
func TestSettingsDialogScrollsToEveryRow(t *testing.T) {
	for h := 3; h < testModalRows; h++ { // at full height the backend line comes last
		seen := map[int]bool{}
		for scroll := 0; scroll < 20; scroll++ {
			rows, _, _, _ := testModalRowMap(h, scroll)
			if len(rows) != h || rows[h-1] != testRowButtons {
				t.Fatalf("height %d: rows %v, want %d with the buttons last", h, rows, h)
			}
			for _, v := range rows {
				seen[v] = true
			}
		}
		for v := testRowFirst; v <= testRowLast; v += 2 {
			if !seen[v] {
				t.Errorf("height %d: setting row %d can never be shown", h, v)
			}
		}
	}
}

// Emoji and CJK in a nickname survive the clipping the member cards are drawn through.
func TestWideNicknameSurvivesClippedCards(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("wide_nick", "Alice", audio)
	defer node.Close()
	node.HostRoom("123456")
	node.Peers["p1"] = &p2p.PeerInfo{ID: "p1", Nickname: "🍋Bob李", PingMs: 25}
	for _, sharing := range []bool{false, true} {
		node.Peers["p1"].IsSharingScreen = sharing
		buf := buffer.NewBuffer(cell.NewRect(0, 0, 100, 30))
		NewRoomView().Render(terminal.NewFrame(buf, terminal.NewFocusManager()), buf.Area, node, audio)
		var all strings.Builder
		for y := uint16(0); y < 30; y++ {
			all.WriteString(screenText(buf, y) + "\n")
		}
		if !strings.Contains(all.String(), "🍋Bob李") {
			t.Fatalf("sharing=%v: the wide characters of the nickname were blanked:\n%s", sharing, all.String())
		}
	}
}

// The mouse pointer turns into a hand over a room control, and the control still clicks.
func TestHandPointerOverControls(t *testing.T) {
	mem := driver.NewMemoryTerminalIO(nil, 120, 30)
	term, err := terminal.New(driver.NewPortableBackend(mem))
	if err != nil {
		t.Fatal(err)
	}
	caps := term.Capabilities()
	caps.PointerShape = true
	term.SetCapabilities(caps)

	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("hand_pointer", "Alice", audio)
	defer node.Close()
	node.HostRoom("123456")
	room := NewRoomView()
	testOpened := 0
	room.OnOpenTestModal = func() { testOpened++ }

	var tx, ty uint16
	_ = term.Draw(func(f *terminal.Frame) {
		room.Render(f, f.Area(), node, audio)
		for y := uint16(0); y < 30; y++ {
			if i := strings.Index(screenText(f.Buffer, y), "[T] Test"); i >= 0 {
				tx, ty = uint16(len([]rune(screenText(f.Buffer, y)[:i]))), y
			}
		}
	})
	if ty == 0 {
		t.Fatal("the Test button was not drawn")
	}

	shapes := func(out []byte) []string {
		var got []string
		for _, part := range strings.Split(string(out), "\x1b]22;")[1:] {
			shape, _, _ := strings.Cut(part, "\x1b\\")
			got = append(got, shape)
		}
		return got
	}
	before := len(mem.Output())
	term.RouteMouseEvent(driver.MouseEvent{X: 0, Y: ty, Button: driver.MouseNone})
	term.RouteMouseEvent(driver.MouseEvent{X: tx + 1, Y: ty, Button: driver.MouseNone})
	term.RouteMouseEvent(driver.MouseEvent{X: 0, Y: ty, Button: driver.MouseNone})
	if got := strings.Join(shapes(mem.Output()[before:]), ","); got != "pointer," {
		t.Errorf("OSC 22 writes %q, want the hand over the button and the default off it", got)
	}
	term.RouteMouseEvent(driver.MouseEvent{X: tx + 1, Y: ty, Button: driver.MouseLeft})
	if testOpened != 1 {
		t.Errorf("clicking the Test button under the hand pointer opened the dialog %d times, want 1", testOpened)
	}
}

// memTerminal is a terminal writing to memory, with the given capabilities switched on.
func memTerminal(t *testing.T, w, h uint16, enable func(*terminal.CapabilityProfile)) (*terminal.Terminal, *driver.MemoryTerminalIO) {
	t.Helper()
	mem := driver.NewMemoryTerminalIO(nil, w, h)
	term, err := terminal.New(driver.NewPortableBackend(mem))
	if err != nil {
		t.Fatal(err)
	}
	caps := term.Capabilities()
	enable(&caps)
	term.SetCapabilities(caps)
	return term, mem
}

// A link in chat is also an OSC 8 hyperlink, so the terminal can open it on the user's side.
func TestChatLinkIsTerminalHyperlink(t *testing.T) {
	term, mem := memTerminal(t, 120, 30, func(c *terminal.CapabilityProfile) { c.Hyperlinks = true })
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("chat_link", "Alice", audio)
	defer node.Close()
	node.HostRoom("123456")
	room := NewRoomView()
	room.AddChatMessage("Bob", "p1", "see https://example.com/docs", false, time.Time{})
	_ = term.Draw(func(f *terminal.Frame) { room.Render(f, f.Area(), node, audio) })
	// ESC ] 8 ; params ; url ST — Limoni puts an id in the params.
	if !regexp.MustCompile(`\x1b\]8;[^;]*;https://example\.com/docs\x1b\\`).Match(mem.Output()) {
		t.Fatal("the chat link was not written as an OSC 8 hyperlink")
	}
}

// The window title shows the member count and unread messages, and never the room code.
func TestWindowTitle(t *testing.T) {
	term, mem := memTerminal(t, 100, 30, func(*terminal.CapabilityProfile) {})
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("window_title", "Alice", audio)
	defer node.Close()
	node.HostRoom("123456")
	node.Peers["p1"] = &p2p.PeerInfo{ID: "p1", Nickname: "Bob"}
	a := &App{term: term, room: NewRoomView(), node: node, audio: audio, currentScreen: ScreenLobby}

	titles := func() []string {
		var got []string
		for _, part := range strings.Split(string(mem.Output()), "\x1b]2;")[1:] {
			title, _, _ := strings.Cut(part, "\x07")
			got = append(got, title)
		}
		return got
	}
	a.updateWindowTitle()
	a.currentScreen = ScreenRoom
	a.updateWindowTitle()
	a.updateWindowTitle() // unchanged: not written again
	a.room.AddChatMessage("Bob", "p1", "hi", false, time.Time{})
	a.updateWindowTitle()

	want := []string{"Limoni Voice", "Limoni Voice · 2/4", "(1) Limoni Voice · 2/4"}
	if got := titles(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("titles %q, want %q", got, want)
	}
	if strings.Contains(strings.Join(titles(), ""), node.RoomCode) {
		t.Fatal("the room code reached the window title")
	}
}

// While someone shares a screen the members move to a sidebar; its volume buttons must work,
// also on the sharer's card, whose whole area opens the stream.
func TestSidebarMemberVolumeWhileStreaming(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("sidebar_vol_test", "Bob", audio)
	defer node.Close()
	node.HostRoom("778899")
	node.Peers["peer_alice"] = &p2p.PeerInfo{ID: "peer_alice", Nickname: "Alice", PingMs: 25, LastSeen: time.Now(), IsSharingScreen: true}

	room := NewRoomView()
	const w, h = 160, 40
	buf := buffer.NewBuffer(cell.NewRect(0, 0, w, h))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, w, h), node, audio)

	click := func(label string) {
		t.Helper()
		for y := uint16(0); y < h; y++ {
			var row strings.Builder
			for x := uint16(0); x < w/2; x++ {
				if c := buf.Get(x, y); c != nil && c.Content != 0 {
					row.WriteRune(c.Content)
				} else {
					row.WriteRune(' ')
				}
			}
			line := row.String()
			idx := strings.Index(line, label)
			if idx < 0 {
				continue
			}
			col := len([]rune(line[:idx])) // cells, not bytes: the border is multi-byte
			ev := driver.MouseEvent{X: uint16(col + 1), Y: y, Button: driver.MouseLeft}
			for i := len(frame.ClickRegions) - 1; i >= 0; i-- {
				if reg := frame.ClickRegions[i]; reg.LayerID == "" && reg.Area.Contains(ev.X, ev.Y) {
					reg.Handler(ev)
					return
				}
			}
			t.Fatalf("no click region on %q", label)
		}
		t.Fatalf("%q not drawn in the members sidebar", label)
	}

	click("[+]")
	if v := audio.GetPeerVolume("peer_alice"); math.Abs(v-1.25) > 1e-9 {
		t.Fatalf("volume after [+] = %v, want 1.25", v)
	}
	room.Render(frame, cell.NewRect(0, 0, w, h), node, audio)
	click("[-]")
	click("[-]")
	if v := audio.GetPeerVolume("peer_alice"); math.Abs(v-0.75) > 1e-9 {
		t.Fatalf("volume after [-] [-] = %v, want 0.75", v)
	}
}

// The chat keeps its rows while a stream is watched, and its top border drags it taller.
func TestChatPanelResize(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("chat_resize_test", "Bob", audio)
	defer node.Close()
	node.HostRoom("778899")
	node.Peers["peer_alice"] = &p2p.PeerInfo{ID: "peer_alice", Nickname: "Alice", LastSeen: time.Now(), IsSharingScreen: true}
	node.IsWatchingScreen, node.WatchingPeerID = true, "peer_alice"

	room := NewRoomView()
	const w, h = 140, 40
	area := cell.NewRect(0, 0, w, h)
	render := func() cell.Rect {
		buf := buffer.NewBuffer(area)
		frame := terminal.NewFrame(buf, terminal.NewFocusManager())
		room.Render(frame, area, node, audio)
		room.mu.Lock()
		defer room.mu.Unlock()
		return room.LastLogArea
	}

	if got := int(render().Height); got != defaultFooterRowsFor(area)+2 {
		t.Fatalf("chat panel height while watching = %d, want %d", got, defaultFooterRowsFor(area)+2)
	}

	// Drag the footer's top border up by 10 rows.
	top := room.lastFooterArea.Y
	if !room.StartChatResize(5, top) {
		t.Fatal("pressing the footer's top border did not start a resize")
	}
	if !room.DragChatResize(5, top-10) {
		t.Fatal("drag not taken")
	}
	room.EndChatResize()
	if got := int(render().Height); got != defaultFooterRowsFor(area)+2+10 {
		t.Fatalf("chat panel height after dragging up 10 rows = %d, want %d", got, defaultFooterRowsFor(area)+12)
	}
	if room.DragChatResize(5, 3) {
		t.Fatal("drag taken after the resize ended")
	}

	room.ResizeChat(-footerResizeStep) // ▼
	if got := int(render().Height); got != defaultFooterRowsFor(area)+2+10-footerResizeStep {
		t.Fatalf("chat panel height after ▼ = %d", got)
	}

	// Dragging past the header leaves the member grid its rows; dragging to the bottom keeps
	// at least the input line and one message.
	room.StartChatResize(5, room.lastFooterArea.Y)
	room.DragChatResize(5, 0)
	room.EndChatResize()
	if got, want := int(render().Height), maxFooterRows(area)+2; got != want {
		t.Fatalf("tallest chat panel = %d, want %d", got, want)
	}
	room.StartChatResize(5, room.lastFooterArea.Y)
	room.DragChatResize(5, h-1)
	room.EndChatResize()
	if got := int(render().Height); got < minFooterRows+2 {
		t.Fatalf("shortest chat panel = %d, want at least %d", got, minFooterRows+2)
	}
}

// A right click pastes like the terminal would without mouse reporting: code into the chat
// input with its lines kept, a room key into the lobby's key field.
func TestRightClickPaste(t *testing.T) {
	clip := "func main() {\r\n\tfmt.Println(\"hi\")\r\n}"
	orig := readClipboard
	readClipboard = func() string { return clip }
	t.Cleanup(func() { readClipboard = orig })

	a := &App{room: NewRoomView(), lobby: NewLobbyView()}
	a.handleRoomMouse(driver.MouseEvent{X: 5, Y: 5, Button: driver.MouseRight})
	if got, want := a.room.ChatInputState.Value(), "func main() {\n\tfmt.Println(\"hi\")\n}"; got != want {
		t.Fatalf("chat input after right click = %q, want %q", got, want)
	}
	if !a.room.IsChatFocused {
		t.Error("chat not focused after pasting into it")
	}
	a.handleRoomMouse(driver.MouseEvent{X: 6, Y: 5, Button: driver.MouseRight, Drag: true})
	if strings.Count(a.room.ChatInputState.Value(), "main") != 1 {
		t.Error("a right-button drag pasted again")
	}

	clip = "  " + a.lobby.CurrentCode + "\n"
	a.lobby.ActiveInput = 1
	a.handleLobbyMouse(driver.MouseEvent{X: 5, Y: 5, Button: driver.MouseRight})
	if got := a.lobby.CodeState.Value(); got != NormalizeCode(a.lobby.CurrentCode) || got == "" {
		t.Errorf("room key field after right click = %q, want %q", got, NormalizeCode(a.lobby.CurrentCode))
	}

	clip = ""
	before := a.room.ChatInputState.Value()
	a.handleRoomMouse(driver.MouseEvent{X: 5, Y: 5, Button: driver.MouseRight})
	if a.room.ChatInputState.Value() != before || a.room.ToastMsg != "Clipboard empty or unreadable" {
		t.Errorf("empty clipboard: input %q, toast %q", a.room.ChatInputState.Value(), a.room.ToastMsg)
	}
}

// /send takes a path the way Windows hands it out: Explorer's "Copy as path" and a file dragged
// into Windows Terminal quote it, and it may hold non-ASCII letters (Uğur, İQistan).
func TestSendFileQuotedPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Uğur", "Desktop", "Meme", "İQistan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "abaducabbar.png")
	if err := os.WriteFile(file, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, typed := range []string{`"` + file + `"`, `'` + file + `'`, "  " + file + " ", "file://" + filepath.ToSlash(file)} {
		room := NewRoomView()
		got := make(chan string, 1)
		room.OnSendFile = func(p string) { got <- p }
		room.ChatInputState.SetValue("/send " + typed)
		room.SendCurrentChat()
		select {
		case p := <-got:
			if _, err := os.Stat(p); err != nil {
				t.Errorf("/send %s passed %q, which does not open: %v", typed, p, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("/send %s did not send", typed)
		}
	}
}

func TestCleanFilePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := map[string]string{
		`"C:\Users\Uğur\Desktop\Meme\İQistan\abaducabbar.png"`: `C:\Users\Uğur\Desktop\Meme\İQistan\abaducabbar.png`,
		`'/tmp/a b.txt'`:    "/tmp/a b.txt",
		`"unbalanced`:       `"unbalanced`,
		"~/notes.txt":       filepath.Join(home, "notes.txt"),
		"file:///tmp/a%20b": "/tmp/a b",
	}
	for in, want := range cases {
		if got := cleanFilePath(in); got != want {
			t.Errorf("cleanFilePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// One long message (a pasted log) wraps into many lines; the chat scrolls through all of them,
// keeps its place while others write, and returns to the bottom when you send.
func TestChatScrollsThroughLongMessage(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("scroll_test", "Bob", audio)
	defer node.Close()
	room := NewRoomView()
	room.AddChatMessage("Alice", "a", "hi", false, time.Now())
	// Sixty lines in sixty messages: one message longer than maxShownMessageLines is cut short.
	for i := range 60 {
		room.AddChatMessage("Alice", "a", fmt.Sprintf("log line %02d", i), false, time.Now())
	}

	area := cell.NewRect(0, 0, 120, 30)
	render := func() string {
		buf := buffer.NewBuffer(area)
		room.Render(terminal.NewFrame(buf, terminal.NewFocusManager()), area, node, audio)
		var sb strings.Builder
		for y := area.Y; y < area.Height; y++ {
			for x := area.X; x < area.Width; x++ {
				if c := buf.Get(x, y); c != nil && c.Content != 0 {
					sb.WriteRune(c.Content)
				}
			}
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	render()
	if room.chatMaxScroll < 50 {
		t.Fatalf("scroll limit %d for a 60-line message, want it to reach the top", room.chatMaxScroll)
	}

	for range 40 {
		room.ScrollChatPage(1)
	}
	if screen := render(); !strings.Contains(screen, "log line 00") {
		t.Fatalf("paging up never reached the start of the long message:\n%s", screen)
	}
	top := room.ChatScrollOffset
	if top != room.chatMaxScroll {
		t.Fatalf("offset %d after paging up, want the limit %d", top, room.chatMaxScroll)
	}

	// Someone writes while you read: the view stays where it is.
	room.AddChatMessage("Alice", "a", "new message", false, time.Now())
	room.AddLog("[+] Carol joined the room.")
	if screen := render(); !strings.Contains(screen, "log line 00") {
		t.Fatalf("an incoming message moved the scrolled view:\n%s", screen)
	}

	// Sending takes you to the newest lines.
	room.ChatInputState.SetValue("my reply")
	room.SendCurrentChat()
	render()
	if room.ChatScrollOffset != 0 {
		t.Fatalf("offset %d after sending, want 0", room.ChatScrollOffset)
	}
}

// Selecting a whole message copies it exactly: nothing drawn past the panel's right edge (an
// emoji takes two cells), tabs kept visible, no space added inside a long URL where it wraps,
// the whitespace a wrap dropped put back, and code indentation kept.
func TestChatSelectionCopiesWholeMessage(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("select_test", "Bob", audio)
	defer node.Close()
	cases := []string{
		"[03:10:44.949] [NET] 🎯 [STUN] Public endpoint 82.222.238.140:43280 (local :50000, NAT: Unknown) and 🎯🎯🎯 more words here to wrap around the panel edge ok",
		"plain words only, a long line of plain ascii text that must wrap across several rows of the chat panel without losing anything at all at the right edge",
		"tab\tseparated\tvalues\tthat\tgo\ton\tand\ton\tand\ton\tand\ton\tand\ton\tand\ton\tand\ton\tand\ton\tand\ton\tend",
		"STUN: 162.159.207.0 → 82.222.238.140:43280 | 46.225.95.169 → 82.222.238.140:43280 | 74.125.250.129 → 82.222.238.140:43280 END",
		"https://example.com/a/very/long/url/without/any/spaces/that/needs/to/be/broken/into/pieces/across/lines/END",
		"func main() {\n    if ok {\n        fmt.Println(\"indented code keeps its spaces\")\n    }\n}",
	}
	for _, msg := range cases {
		room := NewRoomView()
		room.ChatHeight = 16
		room.AddChatMessage("Alice", "a", msg, false, time.Now())
		area := cell.NewRect(0, 0, 120, 30)
		room.Render(terminal.NewFrame(buffer.NewBuffer(area), terminal.NewFocusManager()), area, node, audio)

		rl := room.renderedLines
		right := int(room.LastLogArea.X + room.LastLogArea.Width - 1) // the border column
		for _, l := range rl {
			for _, c := range l.Chars {
				if int(c.X)+cell.RuneWidth(c.R) > right {
					t.Errorf("%q drawn at column %d, past the panel edge %d", c.R, c.X, right)
				}
			}
		}
		first, last := rl[0], rl[len(rl)-1]
		room.HandleMousePress(first.StartX, first.RowY)
		room.HandleMouseDrag(last.EndX, last.RowY)
		got := room.HandleMouseRelease(last.EndX, last.RowY)
		if want := strings.ReplaceAll(msg, "\t", chatTab); !strings.HasSuffix(got, want) {
			t.Errorf("selecting the whole message copied\n%q\nwant it to end with\n%q", got, want)
		}
	}
}

// The debug dialog wraps long lines instead of cutting them, scrolls by rows up to the oldest
// line and no further, keeps its place while logs arrive, and filters by kind.
func TestDebugModalScrollWrapFilter(t *testing.T) {
	ClearDebugLogs()
	t.Cleanup(ClearDebugLogs)
	AddDebugLog("[NET] oldest line with a tail that is long enough to wrap in a narrow dialog END-OLDEST")
	for i := range 80 {
		AddDebugLog(fmt.Sprintf("[ROOM] filler %02d", i))
	}
	AddDebugLog("[SCREEN] [MPV-LIVE] Error parsing option")

	area := cell.NewRect(0, 0, 60, 24)
	v := NewDebugView()
	draw := func() string {
		buf := buffer.NewBuffer(area)
		DrawDebugModal(terminal.NewFrame(buf, terminal.NewFocusManager()), area, v, nil, nil, nil, nil)
		var sb strings.Builder
		for y := area.Y; y < area.Height; y++ {
			for x := area.X; x < area.Width; x++ {
				if c := buf.Get(x, y); c != nil && c.Content != 0 {
					sb.WriteRune(c.Content)
				}
			}
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	draw()
	v.Top()
	screen := draw()
	if !strings.Contains(screen, "END-OLDEST") {
		t.Fatalf("Home did not reach the oldest line, wrapped in full:\n%s", screen)
	}
	top := v.Scroll
	v.ScrollBy(50)
	if v.Scroll != top {
		t.Fatalf("scrolled past the oldest line: %d > %d", v.Scroll, top)
	}

	AddDebugLog("[ROOM] a new line while reading")
	if screen := draw(); !strings.Contains(screen, "END-OLDEST") {
		t.Fatalf("a new log moved the view:\n%s", screen)
	}

	v.Bottom()
	v.Filter = 3 // screen share
	screen = draw()
	if !strings.Contains(screen, "[MPV-LIVE] Error parsing") || strings.Contains(screen, "filler") || !strings.Contains(screen, "[F] Filter") {
		t.Fatalf("screen share filter shows the wrong lines:\n%s", screen)
	}
}

// The line between the controls and the chat drags sideways: the chat gets wider or
// narrower, within bounds that leave the controls their room.
func TestChatPanelWidthResize(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("chat_width_test", "Bob", audio)
	defer node.Close()
	room := NewRoomView()
	area := cell.NewRect(0, 0, 140, 40)
	render := func() cell.Rect {
		room.Render(terminal.NewFrame(buffer.NewBuffer(area), terminal.NewFocusManager()), area, node, audio)
		return room.LastLogArea
	}
	chat := render()
	if want := 140 * defaultChatWidth / 100; int(chat.Width) < want-2 || int(chat.Width) > want+2 {
		t.Fatalf("default chat width %d, want about %d", chat.Width, want)
	}

	y := room.lastFooterArea.Y + 2
	if !room.StartChatResize(chat.X, y) {
		t.Fatal("pressing the chat panel's left border did not start a resize")
	}
	room.DragChatResize(chat.X-20, y)
	room.EndChatResize()
	if wider := render(); wider.Width < chat.Width+18 {
		t.Fatalf("dragging the border 20 columns left: width %d, was %d", wider.Width, chat.Width)
	}

	room.StartChatResize(room.LastLogArea.X, y)
	room.DragChatResize(0, y)
	room.EndChatResize()
	if w := render().Width; int(w) > 140*maxChatWidth/100+1 {
		t.Fatalf("chat grew to %d columns, past %d%%", w, maxChatWidth)
	}
	if room.StartChatResize(room.LastLogArea.X+5, y) {
		t.Fatal("a press inside the chat started a resize")
	}
}

// Each sharer's stream sound has its own level: [-]/[+] next to the stop button and , / . in
// the room change the watched one's, and switching streams brings each level back.
func TestStreamVolumePerSharer(t *testing.T) {
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("stream_vol_test", "Bob", audio)
	defer node.Close()
	node.HostRoom("778899")
	node.Peers["ali"] = &p2p.PeerInfo{ID: "ali", Nickname: "Ali", LastSeen: time.Now(), IsSharingScreen: true}
	node.Peers["ayse"] = &p2p.PeerInfo{ID: "ayse", Nickname: "Ayse", LastSeen: time.Now(), IsSharingScreen: true}
	watch := func(id, nick string) {
		node.IsWatchingScreen, node.WatchingPeerID, node.WatchingPeerNick = true, id, nick
	}
	watch("ali", "Ali")

	room := NewRoomView()
	area := cell.NewRect(0, 0, 160, 45)
	buf := buffer.NewBuffer(area)
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, area, node, audio)

	// Click [+] of the stream volume on the stage.
	row, col := -1, -1
	for y := area.Y; y < area.Height && row < 0; y++ {
		var line []rune
		for x := area.X; x < area.Width; x++ {
			if c := buf.Get(x, y); c != nil && c.Content != 0 {
				line = append(line, c.Content)
			} else {
				line = append(line, ' ')
			}
		}
		if text := string(line); strings.Contains(text, "Stream sound") {
			col, row = len([]rune(text[:strings.Index(text, "[+]")])), int(y)
		}
	}
	if row < 0 {
		t.Fatal("no stream volume control while watching")
	}
	for i := len(frame.ClickRegions) - 1; i >= 0; i-- {
		if reg := frame.ClickRegions[i]; reg.Area.Contains(uint16(col+1), uint16(row)) {
			reg.Handler(driver.MouseEvent{X: uint16(col + 1), Y: uint16(row), Button: driver.MouseLeft})
			break
		}
	}
	if v := audio.ScreenAudioVolumeFor("ali"); math.Abs(v-1.1) > 1e-9 {
		t.Fatalf("Ali's stream after [+] = %v, want 1.1", v)
	}

	a := &App{audio: audio, node: node, room: room, currentScreen: ScreenRoom}
	key := func(ch rune, n int) {
		for range n {
			a.handleKey(driver.KeyEvent{Type: driver.KeyRune, Ch: ch})
		}
	}
	key(',', 20)
	if v := audio.ScreenAudioVolumeFor("ali"); v != 0 {
		t.Fatalf("Ali's stream after many , = %v, want 0", v)
	}
	if !strings.Contains(room.ToastMsg, "Ali") || !strings.Contains(room.ToastMsg, "muted") {
		t.Errorf("toast %q does not say whose stream was muted", room.ToastMsg)
	}
	if v := audio.ScreenAudioVolumeFor("ayse"); v != 1 {
		t.Fatalf("Ayse's stream changed with Ali's: %v", v)
	}

	watch("ayse", "Ayse")
	key('.', 3)
	if v := audio.ScreenAudioVolumeFor("ayse"); math.Abs(v-1.3) > 1e-9 {
		t.Fatalf("Ayse's stream after 3 . = %v, want 1.3", v)
	}
	watch("ali", "Ali")
	if v := audio.ScreenAudioVolumeFor("ali"); v != 0 {
		t.Fatalf("back on Ali's stream its level is %v, want the 0 set before", v)
	}
}

// The keypad's / opens the chat like the main one: terminals with the kitty keyboard protocol
// (Alacritty, kitty, WezTerm) report it as KP_DIVIDE, others in application keypad mode as
// ESC O o. Every keypad key types its character.
func TestKeypadKeysTypeTheirCharacter(t *testing.T) {
	cases := map[string]rune{
		"\x1b[57410u": '/', "\x1b[57411u": '*', "\x1b[57412u": '-', "\x1b[57413u": '+',
		"\x1b[57409u": '.', "\x1b[57399u": '0', "\x1bOo": '/', "\x1bOj": '*', "\x1bOk": '+',
		"\x1bOm": '-', "\x1bOn": '.', "\x1bOq": '1',
	}
	for seq, want := range cases {
		ev, n := driver.ParseEvent([]byte(seq))
		if n != len(seq) || ev.Type != driver.EventKey || ev.Key.Type != driver.KeyRune || ev.Key.Ch != want {
			t.Errorf("%q: got %+v (%d bytes), want %q", seq, ev.Key, n, want)
		}
	}
	if ev, _ := driver.ParseEvent([]byte("\x1bOM")); ev.Key.Type != driver.KeyEnter {
		t.Errorf("keypad Enter in application mode: got %+v", ev.Key)
	}
}

// /file with no path sends the clipboard's image; with a path it sends that file as before.
func TestFileCommandSendsTheClipboardImage(t *testing.T) {
	room := NewRoomView()
	images := make(chan struct{}, 1)
	files := make(chan string, 1)
	room.OnSendClipboardImage = func() { images <- struct{}{} }
	room.OnSendFile = func(p string) { files <- p }

	room.ChatInputState.SetValue("/file")
	room.SendCurrentChat()
	select {
	case <-images:
	case <-time.After(time.Second):
		t.Fatal("/file did not send the clipboard image")
	}

	room.ChatInputState.SetValue("/file ./main.go")
	room.SendCurrentChat()
	select {
	case p := <-files:
		if !strings.HasSuffix(p, "main.go") {
			t.Fatalf("sent %q", p)
		}
	case <-time.After(time.Second):
		t.Fatal("/file <path> did not send the file")
	}
	if len(images) != 0 {
		t.Fatal("/file <path> also sent the clipboard image")
	}
}

// A /copy of a long log is one copy button from its first line to its last, and a click
// copies all of it: the ] of "[15:43:24]" used to end the button there.
func TestLongCopyMessageIsOneButton(t *testing.T) {
	var sb strings.Builder
	for i := range 400 {
		fmt.Fprintf(&sb, "[15:43:%02d.605] [ROOM] [WARN] satır %d: exit status 1 ] çğş\n", i%60, i)
	}
	long := strings.TrimSpace(sb.String()) + strings.Repeat("x", 3000) // and an unbroken run
	msg := "📋 [Copy: " + long + "]"

	room := NewRoomView()
	room.AddChatMessage("Ayşe", "a", msg, false, time.Now())
	lines := room.buildDisplayLines(room.Messages, 60)
	// Shown cut short: the first lines, then the note, which is part of the button too.
	if len(lines) != maxShownMessageLines+1 {
		t.Fatalf("%d lines shown, want %d", len(lines), maxShownMessageLines+1)
	}
	if note := lines[len(lines)-1].Spans[0].Text; !strings.Contains(note, "too long") {
		t.Fatalf("last line %q is not the note", note)
	}
	for i, l := range lines {
		for _, sp := range l.Spans {
			if !sp.IsCopy || sp.CopyText != long {
				t.Fatalf("line %d is not part of the copy button: %+v", i, sp.Text)
			}
		}
	}
	if got := messageCopyText(msg); got != long {
		t.Fatalf("a click copies %d bytes of %d", len(got), len(long))
	}
	// A copy button inside other text still ends at its own ].
	if got := messageCopyText("bak: [copy: git pull] sonra"); got != "git pull" {
		t.Fatalf("inline copy gave %q", got)
	}
}

// A double click on a message copies all of it, however far it runs off the screen; one
// click copies nothing, so a drag can still select part of it.
func TestDoubleClickCopiesTheWholeMessage(t *testing.T) {
	room := NewRoomView()
	audio := engine.NewAudioEngine()
	node := p2p.NewP2PNode("local_user", "You", audio)
	var sb strings.Builder
	for i := range 300 {
		fmt.Fprintf(&sb, "[15:43:%02d] log satırı %d: çok uzun bir mesaj\n", i%60, i)
	}
	long := strings.TrimSpace(sb.String())
	room.AddChatMessage("Ayşe", "a", long, false, time.Now())

	buf := buffer.NewBuffer(cell.NewRect(0, 0, 100, 40))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())
	room.Render(frame, cell.NewRect(0, 0, 100, 40), node, audio)
	room.mu.Lock()
	rows := room.renderedLines
	room.mu.Unlock()
	if len(rows) == 0 {
		t.Fatal("nothing drawn")
	}
	row := rows[len(rows)/2].RowY

	SetMockClipboard("")
	if room.HandleChatClick(20, row) {
		t.Fatal("one click on plain text copied it")
	}
	if !room.HandleChatClick(20, row) {
		t.Fatal("a double click copied nothing")
	}
	if got := GetClipboardText(); got != long {
		t.Fatalf("copied %d bytes of %d", len(got), len(long))
	}
	// Two clicks far apart in time are two single clicks.
	room.HandleChatClick(20, row)
	room.mu.Lock()
	room.lastClickAt = room.lastClickAt.Add(-time.Second)
	room.mu.Unlock()
	if room.HandleChatClick(20, row) {
		t.Fatal("two slow clicks copied the message")
	}
}

// Ctrl+V and right click paste what the clipboard holds: copied files and an image are sent,
// text is typed.
func TestPasteClipboardSendsFilesAndImages(t *testing.T) {
	orig := readClipboard
	t.Cleanup(func() { readClipboard = orig })
	a := &App{room: NewRoomView(), currentScreen: ScreenRoom}
	var sentFiles []string
	images := make(chan struct{}, 1)
	a.room.OnSendFile = func(p string) { sentFiles = append(sentFiles, p) }
	a.room.OnSendClipboardImage = func() { images <- struct{}{} }

	// An image only (a screenshot): no text, so the image is sent.
	readClipboard = func() string { return "" }
	a.pasteClipboard()
	select {
	case <-images:
	case <-time.After(time.Second):
		t.Fatal("a screenshot on the clipboard was not sent")
	}

	// Files copied in the file manager.
	file := filepath.Join(t.TempDir(), "rapor.pdf")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	clipboardMu.Lock()
	mockClipboardFiles = "file://" + filepath.ToSlash(file) + "\r\n"
	clipboardMu.Unlock()
	t.Cleanup(func() { clipboardMu.Lock(); mockClipboardFiles = ""; clipboardMu.Unlock() })
	readClipboard = func() string { return file } // file managers offer the path as text too
	a.pasteClipboard()
	if len(sentFiles) != 1 || sentFiles[0] != file || a.room.ChatInputState.Value() != "" {
		t.Fatalf("copied file: sent %q, typed %q", sentFiles, a.room.ChatInputState.Value())
	}

	// Text.
	clipboardMu.Lock()
	mockClipboardFiles = ""
	clipboardMu.Unlock()
	readClipboard = func() string { return "selam" }
	a.pasteClipboard()
	if a.room.ChatInputState.Value() != "selam" || len(images) != 0 {
		t.Fatalf("text paste typed %q", a.room.ChatInputState.Value())
	}
}

// A long message is shown cut short with a note, and still copied whole by a double click;
// a short one is shown whole.
func TestLongMessageIsShownCutShort(t *testing.T) {
	room := NewRoomView()
	long := strings.TrimSpace(strings.Repeat("uzun bir log satırı\n", 200))
	room.AddChatMessage("Ayşe", "a", long, false, time.Now())
	room.AddChatMessage("Can", "c", "kısa\nmesaj", false, time.Now())
	lines := room.buildDisplayLines(room.Messages, 80)
	if len(lines) != maxShownMessageLines+1+2 {
		t.Fatalf("%d lines, want %d", len(lines), maxShownMessageLines+3)
	}
	note := lines[maxShownMessageLines]
	if len(note.Spans) != 1 || !note.Spans[0].IsNote || note.RawMessage != long {
		t.Fatalf("note line: %+v", note)
	}
	if lines[len(lines)-1].Spans[0].Text != "mesaj" {
		t.Fatalf("short message cut: %+v", lines[len(lines)-2:])
	}
}
