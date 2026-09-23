package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thebanri/limoni-voice/internal/p2p"
)

// AppConfig stores user-customizable persistent configuration
type AppConfig struct {
	RelayURL   string          `json:"relay_url,omitempty"`
	RelayToken string          `json:"relay_token,omitempty"`
	Nickname   string          `json:"nickname,omitempty"`
	Audio      *AudioSettings  `json:"audio,omitempty"`
	Screen     *ScreenSettings `json:"screen,omitempty"`
	// Notifications turns desktop notifications for room events on or off (nil = on).
	Notifications *bool `json:"notifications,omitempty"`
	// Language is the user interface language ("en", "tr"); empty means English.
	Language string `json:"language,omitempty"`
}

// ScreenSettings are the persisted screen share preferences.
type ScreenSettings struct {
	Preset      int  `json:"preset"`
	SystemAudio bool `json:"system_audio"`
}

// AudioSettings are the persisted microphone / playback preferences.
type AudioSettings struct {
	SuppressionMode  int     `json:"suppression_mode"`
	EchoCancellation bool    `json:"echo_cancellation"`
	PushToTalk       bool    `json:"push_to_talk"`
	PTTKey           string  `json:"ptt_key,omitempty"`
	GlobalPTT        bool    `json:"global_ptt"`
	Gain             float64 `json:"gain"`
	OutputVolume     float64 `json:"output_volume"`
	VADSensitivity   int     `json:"vad_sensitivity"`
	VoiceSmoothing   *bool   `json:"voice_smoothing,omitempty"` // nil = default (on)
}

// UpdateAppConfig loads the settings file, applies mutate and saves it, preserving fields the
// caller does not touch.
func UpdateAppConfig(mutate func(*AppConfig)) error {
	cfg := LoadAppConfig()
	mutate(&cfg)
	return SaveAppConfig(cfg)
}

func getConfigFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, errHome := os.UserHomeDir()
		if errHome != nil {
			return "limoni_settings.json", nil
		}
		dir = filepath.Join(home, ".config")
	}
	appDir := filepath.Join(dir, "limoni-voice")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(appDir, "settings.json"), nil
}

// LoadAppConfig reads persisted settings from disk, returning empty config on error or if not found
func LoadAppConfig() AppConfig {
	var cfg AppConfig
	path, err := getConfigFilePath()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

// SaveAppConfig writes updated configuration to disk
func SaveAppConfig(cfg AppConfig) error {
	path, err := getConfigFilePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ResetAppConfig clears custom relay settings from disk
func ResetAppConfig() error {
	path, err := getConfigFilePath()
	if err != nil {
		return err
	}
	cfg := AppConfig{}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// IsCustomRelayActive returns whether the configured URL is different from default public relay
func IsCustomRelayActive(url string) bool {
	clean := p2p.NormalizeRelayURL(url)
	if clean == "" || clean == p2p.DefaultRelayURL {
		return false
	}
	return true
}

// ProbeRelayServer performs a fast concurrent network probe to check if a relay server is reachable and active.
func ProbeRelayServer(relayURL, token string, timeout time.Duration) (bool, string) {
	u := strings.TrimSpace(relayURL)
	if strings.EqualFold(u, "none") || strings.EqualFold(u, "off") || strings.EqualFold(u, "lan") || strings.EqualFold(u, "local") {
		return false, "LAN Mode"
	}
	targetURL := p2p.NormalizeRelayURL(relayURL)
	if targetURL == "" {
		return false, "LAN Mode"
	}

	type probeResult struct {
		online bool
		status string
	}

	resCh := make(chan probeResult, 2)

	// 1. Fast HTTP /health probe
	go func() {
		httpURL := strings.Replace(targetURL, "wss://", "https://", 1)
		httpURL = strings.Replace(httpURL, "ws://", "http://", 1)
		healthURL := strings.TrimSuffix(httpURL, "/ws") + "/health"
		req, err := http.NewRequest("GET", healthURL, nil)
		if err != nil {
			resCh <- probeResult{online: false, status: "Offline"}
			return
		}
		if token != "" {
			req.Header.Set("X-Auth-Token", token)
		}
		client := &http.Client{Timeout: timeout}
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				resCh <- probeResult{online: true, status: "Online"}
				return
			} else if resp.StatusCode == http.StatusUnauthorized {
				resCh <- probeResult{online: false, status: "Auth Failed (401)"}
				return
			}
		}
		resCh <- probeResult{online: false, status: "Offline"}
	}()

	// 2. WebSocket upgrade probe
	go func() {
		headers := http.Header{}
		wsURL := targetURL
		if token != "" {
			headers.Set("X-Auth-Token", token)
			if !strings.Contains(wsURL, "token=") {
				sep := "?"
				if strings.Contains(wsURL, "?") {
					sep = "&"
				}
				wsURL = fmt.Sprintf("%s%stoken=%s", wsURL, sep, url.QueryEscape(token))
			}
		}

		dialer := websocket.Dialer{
			HandshakeTimeout: timeout,
		}
		conn, resp, err := dialer.Dial(wsURL, headers)
		if err == nil {
			_ = conn.Close()
			resCh <- probeResult{online: true, status: "Online"}
			return
		}
		if resp != nil {
			if resp.StatusCode == http.StatusUnauthorized {
				resCh <- probeResult{online: false, status: "Auth Failed (401)"}
				return
			}
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound {
				resCh <- probeResult{online: true, status: "Online"}
				return
			}
		}
		resCh <- probeResult{online: false, status: "Offline"}
	}()

	select {
	case res1 := <-resCh:
		if res1.online {
			return res1.online, res1.status
		}
		// First was not positive, wait briefly for second
		select {
		case res2 := <-resCh:
			if res2.online {
				return res2.online, res2.status
			}
			if res1.status == "Auth Failed (401)" || res2.status == "Auth Failed (401)" {
				return false, "Auth Failed (401)"
			}
			return false, "Offline"
		case <-time.After(timeout):
			return res1.online, res1.status
		}
	case <-time.After(timeout):
		return false, "Offline"
	}
}
