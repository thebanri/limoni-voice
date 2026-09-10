package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
