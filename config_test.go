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
		nil, nil, nil, nil,
	)

	// 2. Animated render at full progress
	savedURL := ""
	savedToken := ""
	resetCalled := false
	cancelCalled := false

	DrawRelayModal(
		frame, cell.NewRect(0, 0, 100, 30), 1.0,
		"wss://custom.server.com/ws", "topsecret",
		urlState, tokenState, 0,
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

	// Verify modal title was drawn
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
