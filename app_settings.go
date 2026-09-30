package main

import (
	"fmt"
	"reflect"

	"github.com/thebanri/limoni-voice/internal/audioio"
	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/i18n"
	"github.com/thebanri/limoni-voice/internal/ptt"
	"github.com/thebanri/limoni-voice/screenshare"
)

// applySettings restores persisted preferences into the audio engine and lobby.
func (a *App) applySettings(cfg AppConfig) {
	if cfg.Nickname != "" {
		a.lobby.NickState.SetValue(cfg.Nickname)
	}
	if cfg.Notifications != nil {
		a.notifier.enabled.Store(*cfg.Notifications)
	}
	if sc := cfg.Screen; sc != nil {
		a.screenPreset = sc.Preset
		if a.screenPreset < 0 || a.screenPreset >= len(screenshare.Presets) {
			a.screenPreset = screenshare.DefaultPreset
		}
		a.shareSystemAudio = sc.SystemAudio
		a.node.ScreenPreset = a.screenPreset
		a.node.ShareSystemAudio = a.shareSystemAudio
	}
	if cfg.Theme != "" {
		SetThemeByID(cfg.Theme)
	}
	if cfg.CompactHUD != nil {
		SetCompactHUD(*cfg.CompactHUD)
	}
	if cfg.ChatHeight > 0 {
		a.room.mu.Lock()
		a.room.ChatHeight = cfg.ChatHeight
		a.room.mu.Unlock()
	}
	// Only changes made from here on are written back: settings left at their defaults stay
	// out of the file and follow the defaults of later versions.
	defer func() {
		snap := a.currentSettings()
		a.savedSettings = &snap
	}()

	s := cfg.Audio
	if s == nil {
		return
	}
	audio := a.audio
	audio.SetSuppressionMode(s.SuppressionMode)
	audio.Lock()
	audio.EchoCancellation = s.EchoCancellation
	if s.PushToTalk {
		audio.InputMode = engine.InputModePushToTalk
	}
	audio.GlobalPTT = s.GlobalPTT
	if s.VoiceSmoothing != nil {
		audio.VoiceSmoothing = *s.VoiceSmoothing
	}
	if s.Gain > 0 {
		audio.Gain = min(s.Gain, 3.0)
	}
	if s.OutputVolume > 0 {
		audio.OutputVolume = min(s.OutputVolume, 2.0)
	}
	audio.Unlock()
	if s.VADSensitivity > 0 {
		audio.SetVADSensitivity(s.VADSensitivity)
	}
	if key, ok := ptt.NormalizeKey(s.PTTKey); ok {
		r := rune(0)
		switch {
		case key == "Space":
			r = ' '
		case len(key) == 1:
			r = []rune(key)[0] | 0x20
		}
		audio.SetPTTKey(r, key)
	}
	audio.RLock()
	in := findDevice(audio.InputDevices, s.InputDevice, s.InputDeviceName)
	out := findDevice(audio.OutputDevices, s.OutputDevice, s.OutputDeviceName)
	audio.RUnlock()
	if in >= 0 {
		audio.SetInputDevice(in)
	}
	if out >= 0 {
		audio.SetOutputDevice(out)
	}
}

// findDevice returns the index of the saved device in devices, or -1. The ID alone is not
// enough: where it is a position in the list, it names another device after a change.
func findDevice(devices []engine.AudioDevice, id, name string) int {
	if name == "" {
		return -1
	}
	match := -1
	for i, d := range devices {
		if d.Name != name {
			continue
		}
		if d.ID == id {
			return i
		}
		if match < 0 {
			match = i
		}
	}
	return match
}

// settingsSnapshot is what persistSettings keeps on disk: the audio preferences and the layout.
type settingsSnapshot struct {
	Audio      AudioSettings
	Theme      string
	CompactHUD bool
	ChatHeight int
}

func (a *App) currentSettings() settingsSnapshot {
	audio := a.audio
	audio.RLock()
	smoothing := audio.VoiceSmoothing
	s := AudioSettings{
		SuppressionMode:  audio.SuppressionMode,
		EchoCancellation: audio.EchoCancellation,
		PushToTalk:       audio.InputMode == engine.InputModePushToTalk,
		PTTKey:           audio.PTTKeyName,
		GlobalPTT:        audio.GlobalPTT,
		Gain:             audio.Gain,
		OutputVolume:     audio.OutputVolume,
		VADSensitivity:   audio.VADSensitivity,
		VoiceSmoothing:   &smoothing,
	}
	if i := audio.SelectedInputIdx; i >= 0 && i < len(audio.InputDevices) {
		s.InputDevice, s.InputDeviceName = audio.InputDevices[i].ID, audio.InputDevices[i].Name
	}
	if i := audio.SelectedOutputIdx; i >= 0 && i < len(audio.OutputDevices) {
		s.OutputDevice, s.OutputDeviceName = audio.OutputDevices[i].ID, audio.OutputDevices[i].Name
	}
	audio.RUnlock()
	a.room.mu.Lock()
	chatHeight := a.room.ChatHeight
	a.room.mu.Unlock()
	return settingsSnapshot{Audio: s, Theme: CurrentTheme().ID, CompactHUD: GetCompactHUD(), ChatHeight: chatHeight}
}

// persistSettings writes the settings when they differ from what was saved last. The render
// loop calls it twice a second, so a change is kept whichever control made it, also when the
// app is closed with the settings dialog still open.
func (a *App) persistSettings() {
	snap := a.currentSettings()
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	if a.savedSettings != nil && reflect.DeepEqual(*a.savedSettings, snap) {
		return
	}
	a.savedSettings = &snap
	hud := snap.CompactHUD
	if err := UpdateAppConfig(func(c *AppConfig) {
		c.Audio = &snap.Audio
		c.Theme = snap.Theme
		c.CompactHUD = &hud
		c.ChatHeight = snap.ChatHeight
	}); err != nil {
		AddDebugLog("[CONFIG] Could not save settings: " + err.Error())
	}
}

func (a *App) toggleGlobalPTT() {
	a.audio.Lock()
	a.audio.GlobalPTT = !a.audio.GlobalPTT
	enabled := a.audio.GlobalPTT
	if enabled && a.audio.InputMode != engine.InputModePushToTalk {
		a.audio.InputMode = engine.InputModePushToTalk
	}
	a.audio.Unlock()
	a.syncGlobalPTT()
	a.persistSettings()
	if enabled {
		if ptt.IsTypingKey(a.audio.GetPTTKeyName()) {
			a.toast(fmt.Sprintf("Global PTT on %s also fires while typing in other apps — press K and pick e.g. F9", a.audio.GetPTTKeyName()))
		} else {
			a.toast("Global push-to-talk enabled")
		}
	} else {
		a.toast("Global push-to-talk disabled (terminal key only)")
	}
}

// syncGlobalPTT starts, restarts or stops the system-wide PTT watcher to match settings.
func (a *App) syncGlobalPTT() {
	audio := a.audio
	audio.RLock()
	want := audio.GlobalPTT && audio.InputMode == engine.InputModePushToTalk
	key := audio.PTTKeyName
	audio.RUnlock()

	a.pttMu.Lock()
	defer a.pttMu.Unlock()
	if a.pttWatcher != nil && (!want || key != a.pttKey) {
		_ = a.pttWatcher.Close()
		a.pttWatcher = nil
	}
	if !want {
		audio.Lock()
		if !audio.GlobalPTT {
			audio.GlobalPTTStatus = ""
		}
		audio.Unlock()
		return
	}
	if a.pttWatcher != nil {
		return
	}
	a.pttKey = key
	audio.Lock()
	audio.GlobalPTTStatus = "starting..."
	audio.Unlock()
	go func() {
		w, err := ptt.Start(key, audio.SetPTT)
		a.pttMu.Lock()
		defer a.pttMu.Unlock()
		audio.Lock()
		defer audio.Unlock()
		if err != nil {
			audio.GlobalPTTStatus = "unavailable: " + err.Error()
			AddDebugLog("[PTT] " + audio.GlobalPTTStatus)
			return
		}
		if a.pttKey != key || !audio.GlobalPTT {
			_ = w.Close()
			return
		}
		a.pttWatcher = w
		audio.GlobalPTTStatus = w.Backend()
		AddDebugLog("[PTT] Global push-to-talk via " + w.Backend() + " on " + key)
	}()
}

func (a *App) stopGlobalPTT() {
	a.pttMu.Lock()
	defer a.pttMu.Unlock()
	if a.pttWatcher != nil {
		_ = a.pttWatcher.Close()
		a.pttWatcher = nil
	}
}

// setLanguage switches the user interface language and remembers it.
func (a *App) setLanguage(l i18n.Lang) {
	i18n.Set(l)
	_ = UpdateAppConfig(func(c *AppConfig) { c.Language = string(l) })
	a.term.ForceFullRedraw()
}

// cycleLanguage moves to the next interface language (lobby [L], settings [I]).
func (a *App) cycleLanguage() {
	a.setLanguage(i18n.Next(i18n.Current()))
	a.toast(Tf("Language: %s", i18n.Current().Name()))
}

// warnIfMicBlocked says so when the system blocks the microphone: capture then delivers
// silence and nobody would hear the user, with nothing else pointing at the cause.
func (a *App) warnIfMicBlocked() {
	switch audioio.MicrophonePermission() {
	case audioio.MicPermissionDenied, audioio.MicPermissionRestricted:
		AddDebugLog("[AUDIO] " + audioio.MicPermissionHint)
		a.room.AddLog("[WARN] " + audioio.MicPermissionHint)
		a.lobby.SetToast(audioio.MicPermissionHint)
		a.lobby.ToastTimer = 450 // ~15 s: this one needs reading
	}
}
