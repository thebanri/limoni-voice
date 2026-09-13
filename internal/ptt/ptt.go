// Package ptt watches a push-to-talk key system wide, so talking works while another window
// (a game, an editor) has focus. Terminals cannot report key releases, which is why this
// uses the operating system: GetAsyncKeyState on Windows, CGEventSourceKeyState on macOS,
// the XDG GlobalShortcuts portal on Wayland and XQueryKeymap on X11.
package ptt

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// Watcher reports key state changes until closed.
type Watcher interface {
	Close() error
	Backend() string
}

// ChangeFunc is called with pressed=true on key down and false on key up.
type ChangeFunc func(pressed bool)

var ErrUnsupported = errors.New("ptt: global hotkeys are not available on this system")

// Keys lists the key names accepted by Start (terminal-capturable names).
var Keys = []string{
	"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11",
	"Space", "Tab", "Enter", "CapsLock", "RightCtrl", "RightAlt", "Mouse4", "Mouse5",
	"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M",
	"N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z",
	"0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
}

// NormalizeKey maps user / terminal key names to the canonical names in Keys.
func NormalizeKey(name string) (string, bool) {
	switch name {
	case " ":
		return "Space", true
	case "\t":
		return "Tab", true
	case "\n", "\r":
		return "Enter", true
	}
	n := strings.TrimSpace(name)
	switch strings.ToLower(n) {
	case "space":
		return "Space", true
	case "tab":
		return "Tab", true
	case "enter", "return":
		return "Enter", true
	case "capslock", "caps":
		return "CapsLock", true
	case "rightctrl", "rctrl", "control_r":
		return "RightCtrl", true
	case "rightalt", "ralt", "altgr", "alt_r":
		return "RightAlt", true
	case "mouse4", "xbutton1":
		return "Mouse4", true
	case "mouse5", "xbutton2":
		return "Mouse5", true
	}
	for _, k := range Keys {
		if strings.EqualFold(k, n) {
			return k, true
		}
	}
	return "", false
}

// IsTypingKey reports whether a key also produces text (a global watcher on it fires while typing).
func IsTypingKey(name string) bool {
	return len(name) == 1 || name == "Space" || name == "Tab" || name == "Enter"
}

// Start begins watching key. onChange runs on a background goroutine.
func Start(key string, onChange ChangeFunc) (Watcher, error) {
	canonical, ok := NormalizeKey(key)
	if !ok {
		return nil, errors.New("ptt: unsupported key " + key)
	}
	return startPlatform(canonical, onChange)
}

// poller implements Watcher for backends that sample key state.
type poller struct {
	name string
	done chan struct{}
	once sync.Once
}

func (p *poller) Backend() string { return p.name }

func (p *poller) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

// startPoller samples pressed() every 10 ms and reports edges.
func startPoller(name string, pressed func() bool, onChange ChangeFunc, cleanup func()) *poller {
	p := &poller{name: name, done: make(chan struct{})}
	go func() {
		defer func() {
			if cleanup != nil {
				cleanup()
			}
		}()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		last := false
		for {
			select {
			case <-p.done:
				if last {
					onChange(false)
				}
				return
			case <-ticker.C:
				if now := pressed(); now != last {
					last = now
					onChange(now)
				}
			}
		}
	}()
	return p
}
