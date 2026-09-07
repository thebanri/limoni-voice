package main

import (
	"sync"

	"github.com/thebanri/limoni/core/cell"
)

// ThemePalette defines colors used across all Limoni Voice UI widgets.
type ThemePalette struct {
	ID            string
	Name          string
	Bg            cell.Color
	SurfaceBg     cell.Color
	CardBg        cell.Color
	HeaderBg      cell.Color
	InputBg       cell.Color
	Border        cell.Color
	BorderFocused cell.Color
	Accent        cell.Color // Primary brand / active color
	Secondary     cell.Color // Secondary highlight
	Text          cell.Color
	TextMuted     cell.Color
	Success       cell.Color
	Warning       cell.Color
	Danger        cell.Color
	WaveColor     cell.Color
}

var (
	ThemeCyberpunk = ThemePalette{
		ID:            "cyberpunk",
		Name:          "Cyberpunk Neon",
		Bg:            cell.NewColorRGB(0x0A, 0x0E, 0x17),
		SurfaceBg:     cell.NewColorRGB(0x0F, 0x14, 0x22),
		CardBg:        cell.NewColorRGB(0x10, 0x18, 0x27),
		HeaderBg:      cell.NewColorRGB(0x0B, 0x10, 0x1D),
		InputBg:       cell.NewColorRGB(0x18, 0x20, 0x33),
		Border:        cell.NewColorRGB(0x23, 0x2E, 0x42),
		BorderFocused: cell.NewColorRGB(0x00, 0xD2, 0xD3),
		Accent:        cell.NewColorRGB(0x00, 0xFF, 0x88),
		Secondary:     cell.NewColorRGB(0x00, 0xD2, 0xD3),
		Text:          cell.NewColorRGB(0xF1, 0xF2, 0xF6),
		TextMuted:     cell.NewColorRGB(0x63, 0x6E, 0x72),
		Success:       cell.NewColorRGB(0x55, 0xEF, 0xC4),
		Warning:       cell.NewColorRGB(0xFF, 0xE6, 0x6D),
		Danger:        cell.NewColorRGB(0xFF, 0x76, 0x75),
		WaveColor:     cell.NewColorRGB(0x00, 0xFF, 0x88),
	}

	ThemeDracula = ThemePalette{
		ID:            "dracula",
		Name:          "Dracula Dark",
		Bg:            cell.NewColorRGB(0x1E, 0x1F, 0x29),
		SurfaceBg:     cell.NewColorRGB(0x28, 0x2A, 0x36),
		CardBg:        cell.NewColorRGB(0x2F, 0x31, 0x40),
		HeaderBg:      cell.NewColorRGB(0x21, 0x22, 0x2C),
		InputBg:       cell.NewColorRGB(0x34, 0x37, 0x46),
		Border:        cell.NewColorRGB(0x44, 0x47, 0x5A),
		BorderFocused: cell.NewColorRGB(0xBD, 0x93, 0xF9),
		Accent:        cell.NewColorRGB(0xFF, 0x79, 0xC6),
		Secondary:     cell.NewColorRGB(0xBD, 0x93, 0xF9),
		Text:          cell.NewColorRGB(0xF8, 0xF8, 0xF2),
		TextMuted:     cell.NewColorRGB(0x62, 0x72, 0xA4),
		Success:       cell.NewColorRGB(0x50, 0xFA, 0x7B),
		Warning:       cell.NewColorRGB(0xF1, 0xFA, 0x8C),
		Danger:        cell.NewColorRGB(0xFF, 0x55, 0x55),
		WaveColor:     cell.NewColorRGB(0x8B, 0xE9, 0xFD),
	}

	ThemeCatppuccin = ThemePalette{
		ID:            "catppuccin",
		Name:          "Catppuccin Mocha",
		Bg:            cell.NewColorRGB(0x18, 0x18, 0x25),
		SurfaceBg:     cell.NewColorRGB(0x1E, 0x1E, 0x2E),
		CardBg:        cell.NewColorRGB(0x24, 0x24, 0x38),
		HeaderBg:      cell.NewColorRGB(0x11, 0x11, 0x1B),
		InputBg:       cell.NewColorRGB(0x31, 0x32, 0x44),
		Border:        cell.NewColorRGB(0x45, 0x47, 0x5A),
		BorderFocused: cell.NewColorRGB(0xCB, 0xA6, 0xF7),
		Accent:        cell.NewColorRGB(0xCB, 0xA6, 0xF7),
		Secondary:     cell.NewColorRGB(0x89, 0xDC, 0xEB),
		Text:          cell.NewColorRGB(0xCD, 0xD6, 0xF4),
		TextMuted:     cell.NewColorRGB(0x7F, 0x84, 0x9C),
		Success:       cell.NewColorRGB(0xA6, 0xE3, 0xA1),
		Warning:       cell.NewColorRGB(0xF9, 0xE2, 0xAF),
		Danger:        cell.NewColorRGB(0xF3, 0x8B, 0xA8),
		WaveColor:     cell.NewColorRGB(0x94, 0xE2, 0xD5),
	}

	ThemeNord = ThemePalette{
		ID:            "nord",
		Name:          "Nord Arctic",
		Bg:            cell.NewColorRGB(0x24, 0x29, 0x33),
		SurfaceBg:     cell.NewColorRGB(0x2E, 0x34, 0x40),
		CardBg:        cell.NewColorRGB(0x3B, 0x42, 0x52),
		HeaderBg:      cell.NewColorRGB(0x20, 0x24, 0x2C),
		InputBg:       cell.NewColorRGB(0x43, 0x4C, 0x5E),
		Border:        cell.NewColorRGB(0x4C, 0x56, 0x6A),
		BorderFocused: cell.NewColorRGB(0x88, 0xC0, 0xD0),
		Accent:        cell.NewColorRGB(0x88, 0xC0, 0xD0),
		Secondary:     cell.NewColorRGB(0x81, 0xA1, 0xC1),
		Text:          cell.NewColorRGB(0xEC, 0xEF, 0xF4),
		TextMuted:     cell.NewColorRGB(0x7B, 0x88, 0xA1),
		Success:       cell.NewColorRGB(0xA3, 0xBE, 0x8C),
		Warning:       cell.NewColorRGB(0xEB, 0xCB, 0x8B),
		Danger:        cell.NewColorRGB(0xBF, 0x61, 0x6A),
		WaveColor:     cell.NewColorRGB(0x8F, 0xBC, 0xBB),
	}

	ThemeTokyoNight = ThemePalette{
		ID:            "tokyonight",
		Name:          "Tokyo Night",
		Bg:            cell.NewColorRGB(0x16, 0x16, 0x1E),
		SurfaceBg:     cell.NewColorRGB(0x1A, 0x1B, 0x26),
		CardBg:        cell.NewColorRGB(0x24, 0x28, 0x3B),
		HeaderBg:      cell.NewColorRGB(0x13, 0x13, 0x1A),
		InputBg:       cell.NewColorRGB(0x29, 0x2E, 0x42),
		Border:        cell.NewColorRGB(0x41, 0x48, 0x68),
		BorderFocused: cell.NewColorRGB(0x7A, 0xA2, 0xF7),
		Accent:        cell.NewColorRGB(0x7A, 0xA2, 0xF7),
		Secondary:     cell.NewColorRGB(0xBB, 0x9A, 0xF7),
		Text:          cell.NewColorRGB(0xC0, 0xCA, 0xF5),
		TextMuted:     cell.NewColorRGB(0x56, 0x5F, 0x89),
		Success:       cell.NewColorRGB(0x9E, 0xCE, 0x6A),
		Warning:       cell.NewColorRGB(0xE0, 0xAF, 0x68),
		Danger:        cell.NewColorRGB(0xF7, 0x76, 0x8E),
		WaveColor:     cell.NewColorRGB(0x7D, 0xC5, 0xF7),
	}

	AvailableThemes = []ThemePalette{
		ThemeCyberpunk,
		ThemeDracula,
		ThemeCatppuccin,
		ThemeNord,
		ThemeTokyoNight,
	}

	themeMu          sync.RWMutex
	currentThemeIdx  = 0
	activeThemeState = ThemeCyberpunk

	hudMu            sync.RWMutex
	globalCompactHUD bool
)

// CurrentTheme returns the active ThemePalette.
func CurrentTheme() ThemePalette {
	themeMu.RLock()
	defer themeMu.RUnlock()
	return activeThemeState
}

// CycleTheme switches to the next available theme and returns its name.
func CycleTheme() string {
	themeMu.Lock()
	defer themeMu.Unlock()
	currentThemeIdx = (currentThemeIdx + 1) % len(AvailableThemes)
	activeThemeState = AvailableThemes[currentThemeIdx]
	return activeThemeState.Name
}

// SetThemeByID sets the theme by its unique string identifier.
func SetThemeByID(id string) bool {
	themeMu.Lock()
	defer themeMu.Unlock()
	for i, t := range AvailableThemes {
		if t.ID == id {
			currentThemeIdx = i
			activeThemeState = t
			return true
		}
	}
	return false
}

// GetThemeIndex returns the current theme's index.
func GetThemeIndex() int {
	themeMu.RLock()
	defer themeMu.RUnlock()
	return currentThemeIdx
}

// GetCompactHUD returns whether compact HUD mode is enabled.
func GetCompactHUD() bool {
	hudMu.RLock()
	defer hudMu.RUnlock()
	return globalCompactHUD
}

// SetCompactHUD sets compact HUD mode state.
func SetCompactHUD(enabled bool) {
	hudMu.Lock()
	defer hudMu.Unlock()
	globalCompactHUD = enabled
}

// ToggleCompactHUD toggles compact HUD mode state and returns the new value.
func ToggleCompactHUD() bool {
	hudMu.Lock()
	defer hudMu.Unlock()
	globalCompactHUD = !globalCompactHUD
	return globalCompactHUD
}
