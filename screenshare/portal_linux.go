//go:build linux
// +build linux

package screenshare

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// parsePipewireNodeID parses the PipeWire node ID from D-Bus streams variant
func parsePipewireNodeID(raw interface{}) uint32 {
	if raw == nil {
		return 0
	}
	if id, ok := raw.(uint32); ok && id > 0 {
		return id
	}

	switch list := raw.(type) {
	case [][2]interface{}:
		if len(list) > 0 {
			if id, ok := list[0][0].(uint32); ok && id > 0 {
				return id
			}
		}
	case []interface{}:
		for _, item := range list {
			switch s := item.(type) {
			case [2]interface{}:
				if id, ok := s[0].(uint32); ok && id > 0 {
					return id
				}
			case []interface{}:
				if len(s) > 0 {
					if id, ok := s[0].(uint32); ok && id > 0 {
						return id
					}
				}
			case map[string]interface{}:
				if id, ok := s["id"].(uint32); ok && id > 0 {
					return id
				}
			}
		}
	}

	str := fmt.Sprintf("%v", raw)
	var id uint32
	if n, _ := fmt.Sscanf(str, "[{%d", &id); n == 1 && id > 0 {
		return id
	}
	if n, _ := fmt.Sscanf(str, "[[%d", &id); n == 1 && id > 0 {
		return id
	}
	return 0
}

func waitForPortalResponse(ctx context.Context, reqPath dbus.ObjectPath, sigChan <-chan *dbus.Signal, timeout time.Duration) (uint32, map[string]dbus.Variant, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case sig, ok := <-sigChan:
			if !ok {
				return 0, nil, errors.New("dbus signal channel closed")
			}
			if sig.Path == reqPath && sig.Name == "org.freedesktop.portal.Request.Response" {
				if len(sig.Body) >= 2 {
					respCode, ok := sig.Body[0].(uint32)
					if !ok {
						if codeInt, ok := sig.Body[0].(int32); ok {
							respCode = uint32(codeInt)
						}
					}
					results, _ := sig.Body[1].(map[string]dbus.Variant)
					return respCode, results, nil
				}
				return 0, nil, errors.New("invalid portal response signal body")
			}
		case <-timer.C:
			return 0, nil, errors.New("portal request timed out")
		}
	}
}

// RequestPortalScreenCast creates a Portal screencast session.
// sourceType: 1 = Monitor only, 2 = Window only, 3 = Both
func RequestPortalScreenCast(ctx context.Context, sourceType uint32) (uint32, *os.File, func(), error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}

	sigChan := make(chan *dbus.Signal, 50)
	conn.Signal(sigChan)

	rule := "type='signal',interface='org.freedesktop.portal.Request',member='Response'"
	callMatch := conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, rule)
	if callMatch.Err != nil {
		conn.Close()
		return 0, nil, nil, fmt.Errorf("failed to add dbus match rule: %w", callMatch.Err)
	}

	token := fmt.Sprintf("limoni_%d", rand.Intn(1000000))
	portal := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")

	// 1. CreateSession
	createToken := fmt.Sprintf("req_c_%d", rand.Intn(100000))
	createOpts := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(token),
		"handle_token":         dbus.MakeVariant(createToken),
	}

	var reqCreatePath dbus.ObjectPath
	callCreate := portal.Call("org.freedesktop.portal.ScreenCast.CreateSession", 0, createOpts)
	if callCreate.Err != nil {
		conn.Close()
		return 0, nil, nil, fmt.Errorf("CreateSession failed: %w", callCreate.Err)
	}
	if err := callCreate.Store(&reqCreatePath); err != nil {
		conn.Close()
		return 0, nil, nil, fmt.Errorf("failed to decode CreateSession response path: %w", err)
	}

	respCode, createResults, err := waitForPortalResponse(ctx, reqCreatePath, sigChan, 10*time.Second)
	if err != nil {
		conn.Close()
		return 0, nil, nil, fmt.Errorf("CreateSession response failed: %w", err)
	}
	if respCode != 0 {
		conn.Close()
		return 0, nil, nil, fmt.Errorf("CreateSession rejected by portal with code %d", respCode)
	}

	var sessionHandle dbus.ObjectPath
	if sh, ok := createResults["session_handle"]; ok {
		if shStr, ok := sh.Value().(string); ok && shStr != "" {
			sessionHandle = dbus.ObjectPath(shStr)
		} else if shPath, ok := sh.Value().(dbus.ObjectPath); ok && shPath != "" {
			sessionHandle = shPath
		}
	}
	if sessionHandle == "" {
		cleanSender := strings.TrimPrefix(conn.Names()[0], ":")
		cleanSender = strings.ReplaceAll(cleanSender, ".", "_")
		sessionHandle = dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/portal/desktop/session/%s/%s", cleanSender, token))
	}

	cleanup := func() {
		sessObj := conn.Object("org.freedesktop.portal.Desktop", sessionHandle)
		_ = sessObj.Call("org.freedesktop.portal.Session.Close", 0)
		_ = conn.Close()
	}

	// 2. SelectSources
	if sourceType == 0 {
		sourceType = 2 // default to window
	}
	selectToken := fmt.Sprintf("req_s_%d", rand.Intn(100000))
	selectOpts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(selectToken),
		"types":        dbus.MakeVariant(sourceType),
		"multiple":     dbus.MakeVariant(false),
		"cursor_mode":  dbus.MakeVariant(uint32(2)), // 2 = Embedded
	}

	var reqSelectPath dbus.ObjectPath
	callSelect := portal.Call("org.freedesktop.portal.ScreenCast.SelectSources", 0, sessionHandle, selectOpts)
	if callSelect.Err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("SelectSources failed: %w", callSelect.Err)
	}
	if err := callSelect.Store(&reqSelectPath); err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("failed to decode SelectSources response path: %w", err)
	}

	respCode, _, err = waitForPortalResponse(ctx, reqSelectPath, sigChan, 10*time.Second)
	if err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("SelectSources response failed: %w", err)
	}
	if respCode != 0 {
		cleanup()
		return 0, nil, nil, fmt.Errorf("SelectSources rejected with code %d", respCode)
	}

	// 3. Start (Opens window selector)
	startToken := fmt.Sprintf("req_st_%d", rand.Intn(100000))
	startOpts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(startToken),
	}

	var reqStartPath dbus.ObjectPath
	callStart := portal.Call("org.freedesktop.portal.ScreenCast.Start", 0, sessionHandle, "", startOpts)
	if callStart.Err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("Start failed: %w", callStart.Err)
	}
	if err := callStart.Store(&reqStartPath); err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("failed to decode Start response path: %w", err)
	}

	logMsg("[PORTAL] Lutfen paylasmak istediginiz pencereyi secin...")
	respCode, results, err := waitForPortalResponse(ctx, reqStartPath, sigChan, 120*time.Second)
	if err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("Start response failed: %w", err)
	}
	if respCode != 0 {
		cleanup()
		return 0, nil, nil, errors.New("pencere secimi kullanici tarafindan iptal edildi")
	}

	nodeID := parsePipewireNodeID(results["streams"].Value())
	if nodeID == 0 {
		cleanup()
		return 0, nil, nil, errors.New("portal did not return a valid PipeWire stream Node ID")
	}

	logMsg("[PORTAL] Pencere secildi! PipeWire Node ID: %d", nodeID)

	// 4. OpenPipeWireRemote
	var fd dbus.UnixFD
	callRemote := portal.Call("org.freedesktop.portal.ScreenCast.OpenPipeWireRemote", 0, sessionHandle, map[string]dbus.Variant{})
	if callRemote.Err != nil {
		return nodeID, nil, cleanup, nil
	}
	_ = callRemote.Store(&fd)
	var pwFile *os.File
	if fd > 0 {
		pwFile = os.NewFile(uintptr(fd), "pipewire")
	}

	fullCleanup := func() {
		if pwFile != nil {
			_ = pwFile.Close()
		}
		cleanup()
	}

	return nodeID, pwFile, fullCleanup, nil
}
