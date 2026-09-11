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
)

// AppConfig stores user-customizable persistent configuration
type AppConfig struct {
	RelayURL   string `json:"relay_url,omitempty"`
	RelayToken string `json:"relay_token,omitempty"`
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
	clean := strings.TrimSpace(url)
	if clean == "" || clean == DefaultRelayURL {
		return false
	}
	return true
}

// ProbeRelayServer performs a quick network probe to check if a relay server is reachable and active.
func ProbeRelayServer(relayURL, token string, timeout time.Duration) (bool, string) {
	clean := strings.TrimSpace(relayURL)
	if clean == "" || strings.EqualFold(clean, "none") || strings.EqualFold(clean, "off") {
		return false, "LAN Mode"
	}
	targetURL := clean
	headers := http.Header{}
	if token != "" {
		headers.Set("X-Auth-Token", token)
		if !strings.Contains(targetURL, "token=") {
			sep := "?"
			if strings.Contains(targetURL, "?") {
				sep = "&"
			}
			targetURL = fmt.Sprintf("%s%stoken=%s", targetURL, sep, url.QueryEscape(token))
		}
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: timeout,
	}
	conn, resp, err := dialer.Dial(targetURL, headers)
	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusUnauthorized {
				return false, "Auth Failed (401)"
			}
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound {
				// Server is online and responding via HTTP
				return true, "Online"
			}
		}
		// Fallback: check HTTP /health or / endpoint
		httpURL := strings.Replace(targetURL, "wss://", "https://", 1)
		httpURL = strings.Replace(httpURL, "ws://", "http://", 1)
		healthURL := strings.TrimSuffix(httpURL, "/ws") + "/health"
		req, rErr := http.NewRequest("GET", healthURL, nil)
		if rErr == nil {
			if token != "" {
				req.Header.Set("X-Auth-Token", token)
			}
			client := &http.Client{Timeout: timeout}
			hResp, hErr := client.Do(req)
			if hErr == nil {
				defer hResp.Body.Close()
				if hResp.StatusCode == http.StatusOK {
					return true, "Online"
				} else if hResp.StatusCode == http.StatusUnauthorized {
					return false, "Auth Failed (401)"
				}
			}
		}
		return false, "Offline"
	}
	_ = conn.Close()
	return true, "Online"
}

