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

// NormalizeRelayURL converts user-entered URL, web link, or raw domain into a valid WebSocket relay URL.
// Examples:
//   - "voice.thebanri.dpdns.org" -> "wss://voice.thebanri.dpdns.org/ws"
//   - "https://voice.thebanri.dpdns.org" -> "wss://voice.thebanri.dpdns.org/ws"
//   - "https://voice.thebanri.dpdns.org/" -> "wss://voice.thebanri.dpdns.org/ws"
//   - "http://192.168.1.3:27850" -> "ws://192.168.1.3:27850/ws"
//   - "192.168.1.3:27850" -> "ws://192.168.1.3:27850/ws"
//   - "localhost:27850" -> "ws://localhost:27850/ws"
//   - "wss://myrelay.com/ws" -> "wss://myrelay.com/ws"
func NormalizeRelayURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" || strings.EqualFold(u, "none") || strings.EqualFold(u, "off") || strings.EqualFold(u, "lan") {
		return ""
	}

	hasWss := strings.HasPrefix(strings.ToLower(u), "wss://")
	hasWs := strings.HasPrefix(strings.ToLower(u), "ws://")
	hasHttps := strings.HasPrefix(strings.ToLower(u), "https://")
	hasHttp := strings.HasPrefix(strings.ToLower(u), "http://")

	var scheme string
	var rest string

	if hasWss {
		scheme = "wss://"
		rest = u[6:]
	} else if hasWs {
		scheme = "ws://"
		rest = u[5:]
	} else if hasHttps {
		scheme = "wss://"
		rest = u[8:]
	} else if hasHttp {
		scheme = "ws://"
		rest = u[7:]
	} else {
		// No protocol scheme provided.
		// Determine whether it's local network (ws://) or public domain with SSL (wss://).
		cleanHost := u
		if slashIdx := strings.Index(cleanHost, "/"); slashIdx != -1 {
			cleanHost = cleanHost[:slashIdx]
		}
		hostOnly := cleanHost
		if colonIdx := strings.Index(hostOnly, ":"); colonIdx != -1 {
			hostOnly = hostOnly[:colonIdx]
		}

		isLocal := hostOnly == "localhost" ||
			hostOnly == "127.0.0.1" ||
			strings.HasPrefix(hostOnly, "192.168.") ||
			strings.HasPrefix(hostOnly, "10.") ||
			strings.HasPrefix(hostOnly, "172.")

		if isLocal {
			scheme = "ws://"
		} else {
			scheme = "wss://"
		}
		rest = u
	}

	// Preserve query parameters if present (e.g. ?token=abc)
	query := ""
	if qIdx := strings.Index(rest, "?"); qIdx != -1 {
		query = rest[qIdx:]
		rest = rest[:qIdx]
	}

	// Trim trailing slashes
	rest = strings.TrimRight(rest, "/")

	// If no path is provided or ends without /ws, append /ws
	slashIdx := strings.Index(rest, "/")
	if slashIdx == -1 {
		rest = rest + "/ws"
	} else {
		pathPart := rest[slashIdx:]
		if pathPart == "" {
			rest = rest + "/ws"
		}
	}

	return scheme + rest + query
}

// IsCustomRelayActive returns whether the configured URL is different from default public relay
func IsCustomRelayActive(url string) bool {
	clean := NormalizeRelayURL(url)
	if clean == "" || clean == DefaultRelayURL {
		return false
	}
	return true
}

// ProbeRelayServer performs a quick network probe to check if a relay server is reachable and active.
func ProbeRelayServer(relayURL, token string, timeout time.Duration) (bool, string) {
	targetURL := NormalizeRelayURL(relayURL)
	if targetURL == "" {
		return false, "LAN Mode"
	}
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

