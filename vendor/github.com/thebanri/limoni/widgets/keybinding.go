package widgets

import (
	"unicode"

	"github.com/thebanri/limoni/core/driver"
)

// Keybinding is a single keyboard shortcut and the action it triggers.
type Keybinding struct {
	// Key is the key type (e.g. KeyRune, KeyTab, KeyEsc, KeyArrowUp and so on)
	Key driver.KeyType
	// Ch is the character pressed when Key == KeyRune.
	Ch rune
	// Ctrl reports whether the Ctrl modifier is required.
	Ctrl bool
	// Shift reports whether the Shift modifier is required.
	Shift bool
	// Handler is the callback run when this shortcut matches.
	Handler func()
	// Label is the description of this shortcut shown in the command palette.
	Label string
	// Category is the category the shortcut belongs to (e.g. "Navigation", "View").
	Category string
	// Scope is the focus scope the shortcut applies in (e.g. "settings_modal"). Empty means global.
	Scope string
	// When decides whether the shortcut is active right now. Nil means always.
	When func() bool
}

// KeybindingManager manages declaratively defined keyboard shortcuts
// in one place.
type KeybindingManager struct {
	bindings []Keybinding
}

// NewKeybindingManager returns a new KeybindingManager.
func NewKeybindingManager() *KeybindingManager {
	return &KeybindingManager{
		bindings: make([]Keybinding, 0, 32),
	}
}

// Register adds a new shortcut.
func (km *KeybindingManager) Register(kb Keybinding) {
	km.bindings = append(km.bindings, kb)
}

// Handle checks a key event against the active focus scopes (activeScopes) in order.
// Scopes are searched from the innermost (highest priority) outwards. The global scope is checked last.
func (km *KeybindingManager) Handle(ev driver.KeyEvent, activeScopes ...string) bool {
	// Build the order to check scopes in: innermost to outermost, then global ("")
	scopesToCheck := make([]string, 0, len(activeScopes)+1)
	for i := len(activeScopes) - 1; i >= 0; i-- {
		scopesToCheck = append(scopesToCheck, activeScopes[i])
	}
	scopesToCheck = append(scopesToCheck, "") // Global fallback

	for _, targetScope := range scopesToCheck {
		for _, kb := range km.bindings {
			kbScope := kb.Scope
			if kbScope == "global" {
				kbScope = ""
			}
			normTarget := targetScope
			if normTarget == "global" {
				normTarget = ""
			}

			if kbScope != normTarget {
				continue
			}
			if kb.When != nil && !kb.When() {
				continue
			}
			if kb.Key != ev.Type {
				continue
			}
			if kb.Key == driver.KeyRune && kb.Ch != ev.Ch {
				continue
			}
			if kb.Ctrl != ev.Ctrl {
				continue
			}
			if kb.Key != driver.KeyRune && kb.Shift != ev.Shift {
				continue
			}
			if kb.Handler != nil {
				kb.Handler()
			}
			return true
		}
	}
	return false
}

// AllBindings returns every registered shortcut.
// It is used to register commands in the command palette automatically.
func (km *KeybindingManager) AllBindings() []Keybinding {
	return km.bindings
}

// ToCommandItems turns every registered shortcut into a list of CommandItems.
// The list can be handed straight to the command palette.
func (km *KeybindingManager) ToCommandItems() []CommandItem {
	items := make([]CommandItem, 0, len(km.bindings))
	for _, kb := range km.bindings {
		if kb.Label == "" {
			continue
		}
		detail := formatKeybinding(kb)
		items = append(items, CommandItem{
			Label:    kb.Label,
			Detail:   detail,
			Category: kb.Category,
			Handler:  kb.Handler,
		})
	}
	return items
}

// formatKeybinding produces a readable text form of the shortcut.
func formatKeybinding(kb Keybinding) string {
	s := ""
	if kb.Ctrl {
		s += "Ctrl+"
	}
	if kb.Shift {
		s += "Shift+"
	}

	switch kb.Key {
	case driver.KeyRune:
		s += string(unicode.ToUpper(kb.Ch))
	case driver.KeyTab:
		s += "Tab"
	case driver.KeyEsc:
		s += "Esc"
	case driver.KeyEnter:
		s += "Enter"
	case driver.KeySpace:
		s += "Space"
	case driver.KeyBackspace:
		s += "Backspace"
	case driver.KeyArrowUp:
		s += "↑"
	case driver.KeyArrowDown:
		s += "↓"
	case driver.KeyArrowLeft:
		s += "←"
	case driver.KeyArrowRight:
		s += "→"
	default:
		s += "?"
	}

	return s
}
