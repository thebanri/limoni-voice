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

	// Fallback string scan if D-Bus unmarshaled into nested structures
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
			return 0, nil, errors.New("portal request timed out waiting for user response")
		}
	}
}

// RequestPortalScreenCast creates an XDG Desktop Portal session and triggers the native GNOME/KDE picker dialog.
// Returns the PipeWire Node ID, an *os.File for the PipeWire Remote FD (to pass in ExtraFiles as FD 3), and a cleanup function to close the D-Bus session.
func RequestPortalScreenCast(ctx context.Context) (uint32, *os.File, func(), error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}

	sigChan := make(chan *dbus.Signal, 50)
	conn.Signal(sigChan)

	// Subscribe to all portal Request Response signals up front to avoid race conditions
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

	// Extract exact session handle from CreateSession response
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
	selectToken := fmt.Sprintf("req_s_%d", rand.Intn(100000))
	selectOpts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(selectToken),
		"types":        dbus.MakeVariant(uint32(3)), // 1=Monitor, 2=Window, 3=Both
		"multiple":     dbus.MakeVariant(false),
		"cursor_mode":  dbus.MakeVariant(uint32(2)), // 1=Hidden, 2=Embedded, 4=Metadata
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

	// 3. Start (Pops up GNOME/KDE portal dialog)
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

	logMsg("[PORTAL] GNOME/KDE ekran secim penceresi acildi, lutfen ekrani veya pencereyi secin...")
	// Allow up to 120 seconds for the user to make a selection in the GUI dialog
	respCode, results, err := waitForPortalResponse(ctx, reqStartPath, sigChan, 120*time.Second)
	if err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("Start response failed: %w", err)
	}
	if respCode != 0 {
		cleanup()
		return 0, nil, nil, errors.New("ekran paylasimi kullanici tarafindan iptal edildi")
	}

	nodeID := parsePipewireNodeID(results["streams"].Value())
	if nodeID == 0 {
		cleanup()
		return 0, nil, nil, errors.New("portal did not return a valid PipeWire stream Node ID")
	}

	logMsg("[PORTAL] Ekran secildi! PipeWire Node ID: %d", nodeID)

	// 4. OpenPipeWireRemote
	var fd dbus.UnixFD
	callRemote := portal.Call("org.freedesktop.portal.ScreenCast.OpenPipeWireRemote", 0, sessionHandle, map[string]dbus.Variant{})
	if callRemote.Err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("OpenPipeWireRemote failed: %w", callRemote.Err)
	}
	if err := callRemote.Store(&fd); err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("failed to store UnixFD: %w", err)
	}

	pwFile := os.NewFile(uintptr(fd), "pipewire")

	fullCleanup := func() {
		if pwFile != nil {
			_ = pwFile.Close()
		}
		cleanup()
	}

	return nodeID, pwFile, fullCleanup, nil
}

// buildGstreamerPipewireCommand builds a GStreamer command line using PipeWire source to stream MPEG-TS UDP or stdout
func buildGstreamerPipewireCommand(nodeID uint32, targetURL string, fps int) (string, []string, error) {
	gstBin, err := FindExecutable("gst-launch-1.0")
	if err != nil {
		return "", nil, fmt.Errorf("gst-launch-1.0 bulunamadi: %w", err)
	}

	if fps <= 0 {
		fps = 60
	}

	usePipe := (targetURL == "-")

	// If streaming over UDP, extract host and port
	host := "127.0.0.1"
	port := "50100"
	if !usePipe {
		cleanURL := strings.TrimPrefix(targetURL, "udp://")
		if qIdx := strings.Index(cleanURL, "?"); qIdx != -1 {
			cleanURL = cleanURL[:qIdx]
		}
		parts := strings.Split(cleanURL, ":")
		if len(parts) == 2 {
			host = parts[0]
			port = parts[1]
		}
	}

	args := []string{
		"-q",
		"pipewiresrc",
		"fd=3",
		fmt.Sprintf("path=%d", nodeID),
		"autoconnect=false",
		"do-timestamp=true",
		"keepalive-time=1000",
		"!", "videoconvert",
		"!", "video/x-raw,format=I420",
		"!", "x264enc",
		"speed-preset=ultrafast",
		"tune=zerolatency",
		"bitrate=6000",
		"key-int-max=15",
		"bframes=0",
		"byte-stream=true",
		"!", "video/x-h264,profile=baseline,stream-format=byte-stream",
		"!", "mpegtsmux",
	}

	if usePipe {
		args = append(args, "!", "fdsink", "fd=1", "sync=false")
	} else {
		args = append(args,
			"!", "udpsink",
			fmt.Sprintf("host=%s", host),
			fmt.Sprintf("port=%s", port),
			"sync=false",
		)
	}

	return gstBin, args, nil
}
