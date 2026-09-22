// Package notify shows desktop notifications: the freedesktop notification service over
// D-Bus on Linux, Notification Center through osascript on macOS and a toast through
// PowerShell on Windows.
//
// Titles and bodies often carry text from other people (nicknames, chat), so they are never
// spliced into a script: osascript receives them as arguments and PowerShell through
// environment variables, XML-escaped before they reach the toast template.
package notify

import (
	"strings"
	"unicode"
)

// AppName is shown as the notification's source.
const AppName = "Limoni Voice"

// maxBody bounds the body so a long chat message stays a notification.
const maxBody = 180

// Send shows a notification. It returns once the platform accepted it (or failed).
func Send(title, body string) error {
	return send(clean(title, 80), clean(body, maxBody))
}

// clean drops control characters (which could restyle a terminal-backed notifier or break
// a toast) and shortens s to max runes.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}
