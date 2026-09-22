//go:build linux

package notify

import "github.com/godbus/dbus/v5"

func send(title, body string) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	// Notify(app_name, replaces_id, app_icon, summary, body, actions, hints, expire_timeout).
	// The body is plain text: servers that render markup would interpret "<b>" from a chat
	// message, so it is escaped.
	return obj.Call("org.freedesktop.Notifications.Notify", 0,
		AppName, uint32(0), "audio-input-microphone", title, escapeMarkup(body),
		[]string{}, map[string]dbus.Variant{"category": dbus.MakeVariant("im.received")}, int32(6000),
	).Err
}
