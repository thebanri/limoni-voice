package main

import (
	"time"

	"github.com/thebanri/limoni-voice/internal/dsp"
	"github.com/thebanri/limoni-voice/internal/voice"
)

// Screen share audio: playback of the watched sharer's system audio, and removal of our own
// playback from captured system audio before we share it.

// screenAudioDelayFrames delays shared system audio to line up with the video player, which
// adds its own decode / display latency.
const screenAudioDelayFrames = 6 // 120 ms

type screenAudio struct {
	peerID string
	jitter *voice.JitterBuffer
	delay  [][]int16
	pos    int
}

// SetScreenAudioSource selects whose system audio is played ("" stops it).
func (a *AudioEngine) SetScreenAudioSource(peerID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.screen = nil
	if peerID == "" {
		return
	}
	dec, err := voice.NewDecoder(AudioSampleRate, AudioFrameSamples)
	if err != nil {
		return
	}
	sa := &screenAudio{peerID: peerID, jitter: voice.NewJitterBuffer(dec, 20*time.Millisecond)}
	for range screenAudioDelayFrames {
		sa.delay = append(sa.delay, make([]int16, AudioFrameSamples))
	}
	a.screen = sa
}

// PlayScreenAudio queues an Opus frame of the watched sharer's system audio.
func (a *AudioEngine) PlayScreenAudio(peerID string, seq uint32, timestampMs int64, frame []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.screen == nil || a.screen.peerID != peerID || len(frame) == 0 {
		return
	}
	a.screen.jitter.Push(seq, timestampMs, frame, true, time.Now())
}

// mixScreenAudioLocked adds the delayed screen audio frame to accum. Caller holds a.mu.
func (a *AudioEngine) mixScreenAudioLocked(accum []float64, audible bool) bool {
	sa := a.screen
	if sa == nil {
		return false
	}
	kind := sa.jitter.Pull(a.mixScratch)
	slot := sa.delay[sa.pos]
	played := false
	if audible {
		vol := a.ScreenAudioVolume
		for i, s := range slot {
			if s != 0 {
				played = true
			}
			accum[i] += float64(s) * vol
		}
	}
	if kind == voice.FrameNone {
		clear(slot)
	} else {
		copy(slot, a.mixScratch)
	}
	sa.pos = (sa.pos + 1) % len(sa.delay)
	return played
}

// EnableLoopbackExclusion starts (or stops) tracking our own playback so CancelOwnPlayback can
// remove it from captured system audio.
func (a *AudioEngine) EnableLoopbackExclusion(on bool) {
	a.sysMu.Lock()
	defer a.sysMu.Unlock()
	switch {
	case on && a.sysAEC == nil:
		a.sysAEC = dsp.NewLoopbackCanceller()
		a.sysOut = make([]int16, AudioFrameSamples)
	case !on:
		a.sysAEC = nil
	}
}

// CancelOwnPlayback removes what Limoni Voice itself played (other participants' voices, sound
// effects) from a captured 20 ms system audio frame. The returned slice is reused.
func (a *AudioEngine) CancelOwnPlayback(frame []int16) []int16 {
	a.sysMu.Lock()
	defer a.sysMu.Unlock()
	if a.sysAEC == nil || len(frame) != AudioFrameSamples {
		return frame
	}
	a.sysAEC.Capture(frame, a.sysOut)
	return a.sysOut
}

// feedLoopbackReference gives the exclusion canceller the frame we are about to play.
func (a *AudioEngine) feedLoopbackReference(out []int16) {
	a.sysMu.Lock()
	defer a.sysMu.Unlock()
	if a.sysAEC == nil {
		return
	}
	a.sysAEC.Playback(out)
}
