package main

import "github.com/thebanri/limoni-voice/internal/i18n"

// T translates a fixed user interface message; see internal/i18n.
func T(msg string) string { return i18n.T(msg) }

// Tf translates a format and fills it in.
func Tf(format string, args ...any) string { return i18n.Tf(format, args...) }

// tr translates a message that was already formatted in English, such as a network log
// line shown in the room.
func tr(msg string) string { return i18n.Translate(msg) }
