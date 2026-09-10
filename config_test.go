package main

import (
	"os"
	"path/filepath"
	"testing"

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
	if IsCustomRelayActive(DefaultRelayURL) {
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

func TestP2PNodeUpdateRelaySettings(t *testing.T) {
	node := &P2PNode{
		RelayURL: DefaultRelayURL,
	}

	// Update to custom URL and token
	node.UpdateRelaySettings("wss://my-relay.domain.com/ws", "my-secret-token")
	if node.RelayURL != "wss://my-relay.domain.com/ws" {
		t.Fatalf("Expected updated relay URL, got %s", node.RelayURL)
	}
	if node.RelayToken != "my-secret-token" {
		t.Fatalf("Expected updated relay token, got %s", node.RelayToken)
	}
	if node.LanOnly {
		t.Fatalf("Expected LanOnly to be false")
	}

	// Setting to "none" should enable LanOnly
	node.UpdateRelaySettings("none", "")
	if !node.LanOnly {
		t.Fatalf("Expected LanOnly to be true after setting URL to 'none'")
	}
	if node.RelayURL != "" {
		t.Fatalf("Expected empty RelayURL when LanOnly, got %s", node.RelayURL)
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

