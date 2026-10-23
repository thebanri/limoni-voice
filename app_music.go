package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/thebanri/limoni-voice/internal/p2p"
)

const (
	maxSounds         = 6        // how many /music sounds a user keeps
	maxSoundFileBytes = 20 << 20 // the whole file is read each time it plays
)

// musicCommand handles /music: alone it lists the user's sounds, /music <name> <file> adds
// one, played to the room with /<name>, and /music del <name> removes it.
func (a *App) musicCommand(text string) {
	f := strings.Fields(text)
	sounds := LoadAppConfig().Sounds
	switch {
	case len(f) == 1:
		if len(sounds) == 0 {
			a.room.SetToast(Tf("No sounds yet. Add one: /music <name> <file> (up to %d)", maxSounds))
			return
		}
		names := make([]string, 0, len(sounds))
		for name := range sounds {
			names = append(names, "/"+name)
		}
		slices.Sort(names)
		a.room.SetToast(Tf("Sounds: %s (remove one: /music del <name>)", strings.Join(names, ", ")))
	case len(f) == 3 && strings.EqualFold(f[1], "del"):
		name := soundName(f[2])
		if _, ok := sounds[name]; !ok {
			a.room.SetToast(Tf("No sound named /%s", name))
			return
		}
		_ = UpdateAppConfig(func(c *AppConfig) { delete(c.Sounds, name) })
		a.room.SetToast(Tf("Sound /%s removed", name))
	case len(f) >= 3:
		name := soundName(f[1])
		if utf8.RuneCountInString(name) > 20 || strings.ContainsRune(name, '/') {
			a.room.SetToast(T("A sound's name is one word of up to 20 letters"))
			return
		}
		if _, ok := sounds[name]; !ok && len(sounds) >= maxSounds {
			a.room.SetToast(Tf("You have %d sounds already; remove one first: /music del <name>", maxSounds))
			return
		}
		path := cleanFilePath(strings.TrimPrefix(strings.TrimSpace(text[len(f[0]):]), f[1]))
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			a.room.SetToast(Tf("File not found: %s", path))
			return
		}
		if fi.Size() > maxSoundFileBytes {
			a.room.SetToast(Tf("%s is too large for a sound (up to %d MB)", filepath.Base(path), maxSoundFileBytes>>20))
			return
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		_ = UpdateAppConfig(func(c *AppConfig) {
			if c.Sounds == nil {
				c.Sounds = map[string]string{}
			}
			c.Sounds[name] = path
		})
		a.room.SetToast(Tf("Sound /%s added; type /%s to play it to the room", name, name))
	default:
		a.room.SetToast(Tf("Usage: /music <name> <file>, /music del <name> (up to %d sounds)", maxSounds))
	}
}

func soundName(s string) string { return strings.ToLower(strings.TrimPrefix(s, "/")) }

// playSound plays the user's /music sound text names, if it names one, here and to the room.
func (a *App) playSound(text string) bool {
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \t") {
		return false
	}
	name := soundName(text)
	path, ok := LoadAppConfig().Sounds[name]
	if !ok {
		return false
	}
	go func() {
		data, err := os.ReadFile(path)
		var pcm []byte
		if err == nil {
			pcm, err = decodeSound(data, p2p.SoundboardMaxSeconds)
		}
		if err == nil {
			a.audio.PlaySoundboard(pcm)
			err = a.node.SendSoundboard(name, pcm)
		}
		if err != nil {
			a.room.SetToast(Tf("Could not play /%s: %v", name, err))
		}
	}()
	return true
}
