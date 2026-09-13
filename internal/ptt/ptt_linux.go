//go:build linux

package ptt

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

func startPlatform(key string, onChange ChangeFunc) (Watcher, error) {
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	var errs []error
	try := []func(string, ChangeFunc) (Watcher, error){startX11, startPortal}
	if wayland {
		// XQueryKeymap only sees keys while an X client is focused under XWayland.
		try = []func(string, ChangeFunc) (Watcher, error){startPortal, startX11}
	}
	for _, start := range try {
		w, err := start(key, onChange)
		if err == nil {
			return w, nil
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("%w: %v", ErrUnsupported, errors.Join(errs...))
}

// --- X11 ---

var x11Keysyms = map[string]xproto.Keysym{
	"Space": 0x0020, "Tab": 0xff09, "Enter": 0xff0d, "CapsLock": 0xffe5,
	"RightCtrl": 0xffe4, "RightAlt": 0xffea,
	"F1": 0xffbe, "F2": 0xffbf, "F3": 0xffc0, "F4": 0xffc1, "F5": 0xffc2, "F6": 0xffc3,
	"F7": 0xffc4, "F8": 0xffc5, "F9": 0xffc6, "F10": 0xffc7, "F11": 0xffc8,
}

func startX11(key string, onChange ChangeFunc) (Watcher, error) {
	if os.Getenv("DISPLAY") == "" {
		return nil, errors.New("x11: DISPLAY not set")
	}
	sym, ok := x11Keysyms[key]
	if !ok && len(key) == 1 {
		sym = xproto.Keysym(strings.ToLower(key)[0])
		ok = true
	}
	if !ok {
		return nil, errors.New("x11: key not supported")
	}
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("x11: %w", err)
	}
	setup := xproto.Setup(conn)
	count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
	mapping, err := xproto.GetKeyboardMapping(conn, setup.MinKeycode, count).Reply()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("x11: %w", err)
	}
	per := int(mapping.KeysymsPerKeycode)
	var keycodes []xproto.Keycode
	for i := 0; i < int(count); i++ {
		for j := 0; j < per; j++ {
			if mapping.Keysyms[i*per+j] == sym {
				keycodes = append(keycodes, xproto.Keycode(int(setup.MinKeycode)+i))
				break
			}
		}
	}
	if len(keycodes) == 0 {
		conn.Close()
		return nil, errors.New("x11: key not present in keyboard layout")
	}
	pressed := func() bool {
		reply, err := xproto.QueryKeymap(conn).Reply()
		if err != nil {
			return false
		}
		for _, kc := range keycodes {
			if reply.Keys[kc/8]&(1<<(kc%8)) != 0 {
				return true
			}
		}
		return false
	}
	return startPoller("X11", pressed, onChange, conn.Close), nil
}

// --- XDG GlobalShortcuts portal (Wayland) ---

var portalTriggers = map[string]string{
	"Space": "space", "Tab": "Tab", "Enter": "Return", "CapsLock": "Caps_Lock",
	"RightCtrl": "Control_R", "RightAlt": "Alt_R",
}

type portalShortcut struct {
	ID      string
	Options map[string]dbus.Variant
}

type portalWatcher struct {
	conn *dbus.Conn
	once sync.Once
}

func (p *portalWatcher) Backend() string { return "XDG GlobalShortcuts portal" }

func (p *portalWatcher) Close() error {
	p.once.Do(func() { p.conn.Close() })
	return nil
}

func randomToken() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "limoni_" + hex.EncodeToString(b)
}

func startPortal(key string, onChange ChangeFunc) (Watcher, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("portal: %w", err)
	}
	portal := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	if _, err := portal.GetProperty("org.freedesktop.portal.GlobalShortcuts.version"); err != nil {
		conn.Close()
		return nil, errors.New("portal: GlobalShortcuts not provided by this desktop")
	}
	names := conn.Names()
	if len(names) == 0 {
		conn.Close()
		return nil, errors.New("portal: no unique bus name")
	}
	sender := strings.ReplaceAll(strings.TrimPrefix(names[0], ":"), ".", "_")

	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	if err := conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.portal.Request"), dbus.WithMatchMember("Response")); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.portal.GlobalShortcuts")); err != nil {
		conn.Close()
		return nil, err
	}

	request := func(method string, args ...any) (map[string]dbus.Variant, error) {
		token := randomToken()
		path := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
		options := map[string]dbus.Variant{"handle_token": dbus.MakeVariant(token)}
		if method == "org.freedesktop.portal.GlobalShortcuts.CreateSession" {
			options["session_handle_token"] = dbus.MakeVariant(randomToken())
			args = []any{options}
		} else {
			args = append(args, options)
		}
		if call := portal.Call(method, 0, args...); call.Err != nil {
			return nil, call.Err
		}
		timeout := time.After(2 * time.Minute) // BindShortcuts waits for the user to confirm
		for {
			select {
			case sig := <-signals:
				if sig == nil || sig.Path != path || sig.Name != "org.freedesktop.portal.Request.Response" || len(sig.Body) < 2 {
					continue
				}
				if code, _ := sig.Body[0].(uint32); code != 0 {
					return nil, fmt.Errorf("portal: %s was cancelled (%d)", method, code)
				}
				results, _ := sig.Body[1].(map[string]dbus.Variant)
				return results, nil
			case <-timeout:
				return nil, fmt.Errorf("portal: %s timed out", method)
			}
		}
	}

	results, err := request("org.freedesktop.portal.GlobalShortcuts.CreateSession")
	if err != nil {
		conn.Close()
		return nil, err
	}
	var session dbus.ObjectPath
	switch v := results["session_handle"].Value().(type) {
	case string:
		session = dbus.ObjectPath(v)
	case dbus.ObjectPath:
		session = v
	}
	if !session.IsValid() {
		conn.Close()
		return nil, errors.New("portal: invalid session handle")
	}

	trigger := portalTriggers[key]
	if trigger == "" {
		trigger = strings.ToLower(key)
		if strings.HasPrefix(key, "F") && len(key) > 1 {
			trigger = key
		}
	}
	shortcuts := []portalShortcut{{
		ID: "push-to-talk",
		Options: map[string]dbus.Variant{
			"description":       dbus.MakeVariant("Limoni Voice push-to-talk"),
			"preferred_trigger": dbus.MakeVariant(trigger),
		},
	}}
	if _, err := request("org.freedesktop.portal.GlobalShortcuts.BindShortcuts", session, shortcuts, ""); err != nil {
		conn.Close()
		return nil, err
	}

	w := &portalWatcher{conn: conn}
	go func() {
		for sig := range signals {
			if sig == nil || len(sig.Body) < 2 {
				continue
			}
			if id, _ := sig.Body[1].(string); id != "push-to-talk" {
				continue
			}
			switch sig.Name {
			case "org.freedesktop.portal.GlobalShortcuts.Activated":
				onChange(true)
			case "org.freedesktop.portal.GlobalShortcuts.Deactivated":
				onChange(false)
			}
		}
	}()
	return w, nil
}
