package main

import (
	"fmt"

	"github.com/thebanri/limoni-voice/internal/ptt"
)

// applySettings restores persisted preferences into the audio engine and lobby.
func (a *App) applySettings(cfg AppConfig) {
	if cfg.Nickname != "" {
		a.lobby.NickState.SetValue(cfg.Nickname)
	}
	s := cfg.Audio
	if s == nil {
		return
	}
	audio := a.audio
	audio.SetSuppressionMode(s.SuppressionMode)
	audio.mu.Lock()
	audio.EchoCancellation = s.EchoCancellation
	if s.PushToTalk {
		audio.InputMode = InputModePushToTalk
	}
	audio.GlobalPTT = s.GlobalPTT
	if s.Gain > 0 {
		audio.Gain = min(s.Gain, 3.0)
	}
	if s.OutputVolume > 0 {
		audio.OutputVolume = min(s.OutputVolume, 2.0)
	}
	audio.mu.Unlock()
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
}

// saveAudioSettings persists the current audio preferences.
func (a *App) saveAudioSettings() {
	audio := a.audio
	audio.mu.RLock()
	s := &AudioSettings{
		SuppressionMode:  audio.SuppressionMode,
		EchoCancellation: audio.EchoCancellation,
		PushToTalk:       audio.InputMode == InputModePushToTalk,
		PTTKey:           audio.PTTKeyName,
		GlobalPTT:        audio.GlobalPTT,
		Gain:             audio.Gain,
		OutputVolume:     audio.OutputVolume,
		VADSensitivity:   audio.VADSensitivity,
	}
	audio.mu.RUnlock()
	_ = UpdateAppConfig(func(c *AppConfig) { c.Audio = s })
}

func (a *App) toggleGlobalPTT() {
	a.audio.mu.Lock()
	a.audio.GlobalPTT = !a.audio.GlobalPTT
	enabled := a.audio.GlobalPTT
	if enabled && a.audio.InputMode != InputModePushToTalk {
		a.audio.InputMode = InputModePushToTalk
	}
	a.audio.mu.Unlock()
	a.syncGlobalPTT()
	a.saveAudioSettings()
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
	audio.mu.RLock()
	want := audio.GlobalPTT && audio.InputMode == InputModePushToTalk
	key := audio.PTTKeyName
	audio.mu.RUnlock()

	a.pttMu.Lock()
	defer a.pttMu.Unlock()
	if a.pttWatcher != nil && (!want || key != a.pttKey) {
		_ = a.pttWatcher.Close()
		a.pttWatcher = nil
	}
	if !want {
		audio.mu.Lock()
		if !audio.GlobalPTT {
			audio.GlobalPTTStatus = ""
		}
		audio.mu.Unlock()
		return
	}
	if a.pttWatcher != nil {
		return
	}
	a.pttKey = key
	audio.mu.Lock()
	audio.GlobalPTTStatus = "starting..."
	audio.mu.Unlock()
	go func() {
		w, err := ptt.Start(key, audio.SetPTT)
		a.pttMu.Lock()
		defer a.pttMu.Unlock()
		audio.mu.Lock()
		defer audio.mu.Unlock()
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
