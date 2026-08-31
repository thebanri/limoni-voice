//go:build linux
// +build linux

package screenshare

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// RequestMutterScreenCast creates a direct, popup-less screencast session with GNOME Mutter compositor.
// Returns the PipeWire Node ID and a cleanup function to stop the screencast session.
func RequestMutterScreenCast(ctx context.Context, connector string) (uint32, func(), error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return 0, nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}

	rule := "type='signal',interface='org.gnome.Mutter.ScreenCast.Stream',member='PipeWireStreamAdded'"
	callMatch := conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, rule)
	if callMatch.Err != nil {
		conn.Close()
		return 0, nil, fmt.Errorf("failed to add dbus match rule: %w", callMatch.Err)
	}

	sigChan := make(chan *dbus.Signal, 20)
	conn.Signal(sigChan)

	mutter := conn.Object("org.gnome.Mutter.ScreenCast", "/org/gnome/Mutter/ScreenCast")

	// 1. CreateSession (zero popups)
	var sessionPath dbus.ObjectPath
	callCreate := mutter.Call("org.gnome.Mutter.ScreenCast.CreateSession", 0, map[string]dbus.Variant{})
	if callCreate.Err != nil {
		conn.Close()
		return 0, nil, fmt.Errorf("Mutter CreateSession failed: %w", callCreate.Err)
	}
	if err := callCreate.Store(&sessionPath); err != nil {
		conn.Close()
		return 0, nil, fmt.Errorf("failed to decode session path: %w", err)
	}

	sessObj := conn.Object("org.gnome.Mutter.ScreenCast", sessionPath)

	cleanup := func() {
		_ = sessObj.Call("org.gnome.Mutter.ScreenCast.Session.Stop", 0)
		_ = conn.Close()
	}

	// 2. RecordMonitor (connector can be "" for primary monitor, or specific display like "eDP-1")
	var streamPath dbus.ObjectPath
	streamProps := map[string]dbus.Variant{
		"cursor-mode": dbus.MakeVariant(uint32(2)), // 2 = Embedded cursor
	}
	callRecord := sessObj.Call("org.gnome.Mutter.ScreenCast.Session.RecordMonitor", 0, connector, streamProps)
	if callRecord.Err != nil {
		cleanup()
		return 0, nil, fmt.Errorf("Mutter RecordMonitor failed: %w", callRecord.Err)
	}
	if err := callRecord.Store(&streamPath); err != nil {
		cleanup()
		return 0, nil, fmt.Errorf("failed to decode stream path: %w", err)
	}

	// 3. Start
	callStart := sessObj.Call("org.gnome.Mutter.ScreenCast.Session.Start", 0)
	if callStart.Err != nil {
		cleanup()
		return 0, nil, fmt.Errorf("Mutter Start failed: %w", callStart.Err)
	}

	// 4. Wait for PipeWireStreamAdded signal
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			cleanup()
			return 0, nil, ctx.Err()
		case sig, ok := <-sigChan:
			if !ok {
				cleanup()
				return 0, nil, fmt.Errorf("dbus signal channel closed")
			}
			if sig.Name == "org.gnome.Mutter.ScreenCast.Stream.PipeWireStreamAdded" {
				if len(sig.Body) > 0 {
					if id, ok := sig.Body[0].(uint32); ok && id > 0 {
						logMsg("[MUTTER] Direct PipeWire stream connected! Node ID: %d", id)
						return id, cleanup, nil
					}
				}
			}
		case <-timer.C:
			cleanup()
			return 0, nil, fmt.Errorf("timeout waiting for Mutter PipeWire stream")
		}
	}
}

func isMutterAvailable() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()

	var owner string
	err = conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, "org.gnome.Mutter.ScreenCast").Store(&owner)
	return err == nil && strings.TrimSpace(owner) != ""
}

// buildGstreamerPipewireCommand builds a GStreamer command line using PipeWire source to stream MPEG-TS UDP or stdout
func buildGstreamerPipewireCommand(nodeID uint32, targetURL string, fps int) (string, []string, error) {
	gstBin, err := FindExecutable("gst-launch-1.0")
	if err != nil {
		return "", nil, fmt.Errorf("gst-launch-1.0 not found: %w", err)
	}

	if fps <= 0 {
		fps = 60
	}

	usePipe := (targetURL == "-")
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
		fmt.Sprintf("path=%d", nodeID),
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
