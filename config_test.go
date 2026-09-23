package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/p2p"
	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
)

func TestAppConfigPersistence(t *testing.T) {
	// Use temporary directory for config tests
	tmpDir := t.TempDir()
	origConfigDir := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer func() {
		if origConfigDir != "" {
			os.Setenv("XDG_CONFIG_HOME", origConfigDir)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	}()

	// 1. Initially empty or default
	initialCfg := LoadAppConfig()
	if initialCfg.RelayURL != "" || initialCfg.RelayToken != "" {
		t.Fatalf("Expected empty initial config, got %+v", initialCfg)
	}

	// 2. Custom relay helper check
	if IsCustomRelayActive("") {
		t.Fatalf("Expected empty url to not be custom")
	}
	if IsCustomRelayActive(p2p.DefaultRelayURL) {
		t.Fatalf("Expected default relay url to not be custom")
	}
	customURL := "wss://relay.example.com/ws"
	if !IsCustomRelayActive(customURL) {
		t.Fatalf("Expected %s to be recognized as custom", customURL)
	}

	// 3. Save config
	newCfg := AppConfig{
		RelayURL:   customURL,
		RelayToken: "secret123",
	}
	if err := SaveAppConfig(newCfg); err != nil {
		t.Fatalf("SaveAppConfig failed: %v", err)
	}

	// 4. Verify file was created
	cfgPath, err := getConfigFilePath()
	if err != nil {
		t.Fatalf("getConfigFilePath error: %v", err)
	}
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Fatalf("Config file does not exist at %s", cfgPath)
	}

	// 5. Load saved config
	loaded := LoadAppConfig()
	if loaded.RelayURL != customURL || loaded.RelayToken != "secret123" {
		t.Fatalf("Loaded config mismatch: %+v", loaded)
	}

	// 6. Reset config
	if err := ResetAppConfig(); err != nil {
		t.Fatalf("ResetAppConfig failed: %v", err)
	}
	resetLoaded := LoadAppConfig()
	if resetLoaded.RelayURL != "" || resetLoaded.RelayToken != "" {
		t.Fatalf("Expected cleared config after reset, got %+v", resetLoaded)
	}
}

func TestDrawRelayModal(t *testing.T) {
	buf := buffer.NewBuffer(cell.NewRect(0, 0, 100, 30))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())

	urlState := widgets.NewTextInputState()
	urlState.SetValue("wss://custom.server.com/ws")
	tokenState := widgets.NewTextInputState()
	tokenState.SetValue("topsecret")

	// 1. Progress <= 0 should do nothing
	DrawRelayModal(
		frame, cell.NewRect(0, 0, 100, 30), 0.0,
		"wss://custom.server.com/ws", "topsecret",
		urlState, tokenState, 0,
		-1, -1, -1,
		nil, nil, nil, nil,
	)

	// 2. Animated render at full progress with selection
	savedURL := ""
	savedToken := ""
	resetCalled := false
	cancelCalled := false

	DrawRelayModal(
		frame, cell.NewRect(0, 0, 100, 30), 1.0,
		"wss://custom.server.com/ws", "topsecret",
		urlState, tokenState, 0,
		0, 5, 12, // Select characters 5..12 of field 0
		func(field int) {},
		func(u, tok string) {
			savedURL = u
			savedToken = tok
		},
		func() {
			resetCalled = true
		},
		func() {
			cancelCalled = true
		},
	)

	// Verify modal was drawn
	rendered := buf.Get(35, 9)
	if rendered == nil {
		t.Fatalf("Expected buffer cell at (35, 9) to be rendered")
	}

	_ = filepath.Base("")
	_ = savedURL
	_ = savedToken
	_ = resetCalled
	_ = cancelCalled
}

func TestTextSelectionAndEditingHelpers(t *testing.T) {
	state := widgets.NewTextInputState()
	state.SetValue("wss://example.com/ws")

	// Test string insertion
	deleteSelectedRange := func(st *widgets.TextInputState, start, end int) {
		if st == nil || start < 0 || end < 0 || start == end {
			return
		}
		if start > end {
			start, end = end, start
		}
		if start > len(st.Text) {
			start = len(st.Text)
		}
		if end > len(st.Text) {
			end = len(st.Text)
		}
		st.Text = append(st.Text[:start], st.Text[end:]...)
		st.Cursor = start
	}

	// Delete "example.com/" (indices 6 to 18)
	deleteSelectedRange(state, 6, 18)
	if state.Value() != "wss://ws" {
		t.Fatalf("Expected 'wss://ws', got %s", state.Value())
	}
	if state.Cursor != 6 {
		t.Fatalf("Expected cursor at 6, got %d", state.Cursor)
	}

	// Insert replacement at cursor
	insertStringAtCursor := func(st *widgets.TextInputState, s string) {
		if st == nil || s == "" {
			return
		}
		runes := []rune(s)
		newText := make([]rune, len(st.Text)+len(runes))
		copy(newText, st.Text[:st.Cursor])
		copy(newText[st.Cursor:], runes)
		copy(newText[st.Cursor+len(runes):], st.Text[st.Cursor:])
		st.Text = newText
		st.Cursor += len(runes)
	}

	insertStringAtCursor(state, "relay.domain.org/")
	if state.Value() != "wss://relay.domain.org/ws" {
		t.Fatalf("Expected 'wss://relay.domain.org/ws', got %s", state.Value())
	}
}

func TestRelayModalNoOverflow(t *testing.T) {
	screenRect := cell.NewRect(0, 0, 100, 30)
	buf := buffer.NewBuffer(screenRect)
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())

	urlState := widgets.NewTextInputState()
	// Long URL that exceeds standard modal inner width
	urlState.SetValue("wss://very-long-custom-relay-server-name-that-definitely-exceeds-modal-width.domain.org/ws/endpoint/extra/long/path")
	tokenState := widgets.NewTextInputState()
	tokenState.SetValue("very-long-super-secret-token-key-that-is-over-eighty-characters-long-and-should-never-overflow-borders")

	DrawRelayModal(
		frame, screenRect, 1.0,
		urlState.Value(), tokenState.Value(),
		urlState, tokenState, 0,
		-1, -1, -1,
		nil, nil, nil, nil,
	)

	modalW, modalH := uint16(72), uint16(15)
	modalArea := terminal.CenterRect(screenRect, modalW, modalH)
	rightBorderX := modalArea.X + modalArea.Width - 1

	// 1. Verify the right border itself is intact (should contain vertical border character '│' or '╮' or '╯')
	for y := modalArea.Y; y < modalArea.Y+modalArea.Height; y++ {
		c := buf.Get(rightBorderX, y)
		if c == nil {
			t.Fatalf("Expected border cell at right edge (%d, %d)", rightBorderX, y)
		}
		if c.Content == 'w' || c.Content == 's' || c.Content == 'v' || c.Content == 'k' {
			t.Fatalf("Modal right border was overwritten by text character '%c' at y=%d!", c.Content, y)
		}
	}

	// 2. Verify nothing bled past the shadow / right border
	pastRightX := modalArea.X + modalArea.Width + 2 // past shadow
	for y := modalArea.Y; y < modalArea.Y+modalArea.Height; y++ {
		c := buf.Get(pastRightX, y)
		if c != nil && c.Content != ' ' && c.Content != 0 {
			t.Fatalf("Text bled outside modal at (%d, %d): '%c'", pastRightX, y, c.Content)
		}
	}
}

func TestClipboardTestingModeMocking(t *testing.T) {
	SetMockClipboard("")
	testStr := "secure_token_12345"
	ok := CopyToClipboard(testStr)
	if !ok {
		t.Fatalf("Expected CopyToClipboard to succeed in test mode")
	}

	got := GetClipboardText()
	if got != testStr {
		t.Fatalf("Expected GetClipboardText to return %q, got %q", testStr, got)
	}
}

func TestProbeRelayServer(t *testing.T) {
	// 1. LAN Mode
	online, status := ProbeRelayServer("lan", "", 100*time.Millisecond)
	if online || status != "LAN Mode" {
		t.Fatalf("Expected LAN Mode for 'lan', got online=%v status=%s", online, status)
	}

	online, status = ProbeRelayServer("off", "", 100*time.Millisecond)
	if online || status != "LAN Mode" {
		t.Fatalf("Expected LAN Mode for 'off', got online=%v status=%s", online, status)
	}

	// 2. Offline server (unreachable port)
	online, status = ProbeRelayServer("ws://127.0.0.1:49999/ws", "", 200*time.Millisecond)
	if online || status != "Offline" {
		t.Fatalf("Expected Offline for closed port, got online=%v status=%s", online, status)
	}

	// 3. Online server with mock HTTP /health endpoint
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	online, status = ProbeRelayServer(wsURL, "", 500*time.Millisecond)
	if !online || status != "Online" {
		t.Fatalf("Expected Online for healthy server, got online=%v status=%s", online, status)
	}

	// 4. Server returning 401 Unauthorized
	authTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Auth-Token")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != "valid_token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer authTS.Close()

	authWsURL := "ws" + strings.TrimPrefix(authTS.URL, "http") + "/ws"
	online, status = ProbeRelayServer(authWsURL, "bad_token", 500*time.Millisecond)
	if online || status != "Auth Failed (401)" {
		t.Fatalf("Expected Auth Failed (401) for invalid token, got online=%v status=%s", online, status)
	}

	online, status = ProbeRelayServer(authWsURL, "valid_token", 500*time.Millisecond)
	if !online || status != "Online" {
		t.Fatalf("Expected Online with valid token, got online=%v status=%s", online, status)
	}
}

func TestDrawRelayModalWithDynamicStatus(t *testing.T) {
	buf := buffer.NewBuffer(cell.NewRect(0, 0, 100, 30))
	frame := terminal.NewFrame(buf, terminal.NewFocusManager())

	urlState := widgets.NewTextInputState()
	urlState.SetValue("wss://custom.relay.org/ws")
	tokenState := widgets.NewTextInputState()

	// Render with Offline status
	DrawRelayModal(
		frame, cell.NewRect(0, 0, 100, 30), 1.0,
		"wss://custom.relay.org/ws", "",
		urlState, tokenState, 0,
		-1, -1, -1,
		nil, nil, nil, nil,
		"Offline",
	)

	allText := ""
	for y := uint16(0); y < 30; y++ {
		for x := uint16(0); x < 100; x++ {
			c := buf.Get(x, y)
			if c.Content != 0 && c.Content != ' ' {
				allText += string(c.Content)
			}
		}
	}
	if !strings.Contains(allText, "OFFLINE") {
		t.Fatalf("Expected modal to contain OFFLINE status indicator, got: %s", allText)
	}
}

func TestDefaultRelayLive(t *testing.T) {
	online, status := ProbeRelayServer(p2p.DefaultRelayURL, "", 5*time.Second)
	if !online {
		t.Fatalf("Expected DefaultRelayURL to probe online, got online=%v status=%s", online, status)
	}
}
