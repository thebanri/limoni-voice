package main

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/thebanri/limoni-voice/internal/notify"
)

// notifyTitle heads notifications that are not chat messages.
const notifyTitle = "Limoni Voice"

// notifyGap is the minimum time between two desktop notifications, so a burst of chat
// messages raises one notification instead of a stack of them.
const notifyGap = 2 * time.Second

// desktopNotifier raises desktop notifications for room events while the terminal is in
// the background. Focus comes from the terminal's focus reports (DECSET 1004); a terminal
// that never reports focus counts as focused, so it never gets unwanted notifications.
type desktopNotifier struct {
	enabled    atomic.Bool
	background atomic.Bool
	send       func(title, body string) error // notify.Send; replaced in tests

	mu      sync.Mutex
	last    time.Time
	warned  bool
	pending sync.WaitGroup
}

func newDesktopNotifier() *desktopNotifier {
	d := &desktopNotifier{send: notify.Send}
	d.enabled.Store(true)
	return d
}

// SetFocused records a focus report from the terminal.
func (d *desktopNotifier) SetFocused(focused bool) { d.background.Store(!focused) }

// Notify raises a notification if they are enabled, the terminal is in the background and
// the last one was not just shown. It reports whether a notification was sent.
func (d *desktopNotifier) Notify(title, body string) bool { return d.notify(title, body, false) }

// NotifyNow is Notify for events that wait on the user (a file offer): it is not held back
// by a recent notification.
func (d *desktopNotifier) NotifyNow(title, body string) bool { return d.notify(title, body, true) }

func (d *desktopNotifier) notify(title, body string, urgent bool) bool {
	if !d.enabled.Load() || !d.background.Load() {
		return false
	}
	d.mu.Lock()
	now := time.Now()
	if !urgent && now.Sub(d.last) < notifyGap {
		d.mu.Unlock()
		return false
	}
	d.last = now
	d.mu.Unlock()

	d.pending.Add(1)
	go func() {
		defer d.pending.Done()
		if err := d.send(title, body); err != nil {
			d.mu.Lock()
			first := !d.warned
			d.warned = true
			d.mu.Unlock()
			if first {
				AddDebugLog("[NOTIFY] Desktop notifications unavailable: " + err.Error())
			}
		}
	}()
	return true
}

// toggleNotificationsToast toggles notifications and says what changed.
func (a *App) toggleNotificationsToast() {
	if a.toggleNotifications() {
		a.toast("Desktop notifications ON (shown while the terminal is in the background)")
	} else {
		a.toast("Desktop notifications OFF")
	}
}

// toggleNotifications switches desktop notifications and saves the choice.
func (a *App) toggleNotifications() bool {
	on := !a.notifier.enabled.Load()
	a.notifier.enabled.Store(on)
	_ = UpdateAppConfig(func(c *AppConfig) { c.Notifications = &on })
	return on
}
