package engine

// Lock, Unlock, RLock and RUnlock guard the engine's exported settings (Muted, Gain, the
// device lists, ...) for callers that read or change several of them together.
func (a *AudioEngine) Lock()    { a.mu.Lock() }
func (a *AudioEngine) Unlock()  { a.mu.Unlock() }
func (a *AudioEngine) RLock()   { a.mu.RLock() }
func (a *AudioEngine) RUnlock() { a.mu.RUnlock() }

// MuteState returns whether the microphone is muted and the output deafened.
func (a *AudioEngine) MuteState() (muted, deafened bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.Muted, a.Deafened
}

// RenderFrame mixes the next output frame into out, as the playback device would. It lets
// callers without a sound card, such as tests, read what would be heard.
func (a *AudioEngine) RenderFrame(out []int16) { a.renderFrame(out) }
