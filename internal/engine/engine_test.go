package engine

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func TestAudioEngine(t *testing.T) {
	engine := NewAudioEngine()
	if engine.Muted {
		t.Fatalf("Expected audio engine to start unmuted")
	}

	engine.ToggleMute()
	if !engine.Muted {
		t.Fatalf("Expected audio engine to be muted")
	}

	engine.ToggleMute()
	if engine.Muted {
		t.Fatalf("Expected audio engine to be unmuted")
	}

	engine.AdjustGain(0.5)
	if engine.Gain != 1.5 {
		t.Fatalf("Expected gain 1.5, got %f", engine.Gain)
	}

	// Test RMS and PCM gain mixing
	testPCM := make([]byte, AudioChunkSize)
	for i := 0; i < len(testPCM); i += 2 {
		testPCM[i] = 0x00
		testPCM[i+1] = 0x20
	}
	rms := calculateRMS(testPCM)
	if rms <= 0 {
		t.Fatalf("Expected positive RMS for non-empty PCM, got %f", rms)
	}

	amplified := applyGain(testPCM, 2.0)
	if calculateRMS(amplified) <= rms {
		t.Fatalf("Expected amplified RMS to be greater than original")
	}

	// Test peer audio mixing
	engine.PlayPeerPCM("peer_1", testPCM, rms, true)
	wave := engine.GetPeerWave("peer_1")
	if len(wave) != 40 || wave[len(wave)-1] != rms {
		t.Fatalf("Expected peer wave to record latest RMS")
	}
}

func TestNoiseSuppressionAndTestMode(t *testing.T) {
	audio := NewAudioEngine()

	if audio.SuppressionMode != 1 {
		t.Fatalf("Expected default suppression mode 1 (ON), got %d", audio.SuppressionMode)
	}

	audio.CycleSuppressionMode()
	if audio.SuppressionMode != 2 {
		t.Fatalf("Expected suppression mode 2 (HIGH), got %d", audio.SuppressionMode)
	}

	audio.SetSuppressionMode(1)
	if audio.SuppressionMode != 1 {
		t.Fatalf("Expected suppression mode 1 (ON), got %d", audio.SuppressionMode)
	}

	// Generate synthetic vocal frame (400Hz tone at typical speaking volume)
	speechPCM := upsample16k(func(i int) int16 {
		val := int16(3000.0 * math.Sin(2.0*math.Pi*400.0*float64(i)/16000.0))
		return val
	})

	speaking, finalRMS, filtered := audio.processNoiseCancellation(speechPCM, audio.SuppressionMode)
	if !speaking {
		t.Fatalf("Expected speaking=true for vocal frame in ON mode, got false")
	}
	if finalRMS <= 0.01 {
		t.Fatalf("Expected audible finalRMS > 0.01 for vocal frame, got %f", finalRMS)
	}
	if len(filtered) != len(speechPCM) {
		t.Fatalf("Expected filtered PCM size %d, got %d", len(speechPCM), len(filtered))
	}

	audio.Muted = true
	audio.Deafened = false

	audio.EnterTestMode()
	if !audio.InTestMode || audio.Muted || !audio.Deafened || !audio.Loopback {
		t.Fatalf("EnterTestMode failed: inTest=%v muted=%v deaf=%v loop=%v",
			audio.InTestMode, audio.Muted, audio.Deafened, audio.Loopback)
	}

	audio.LeaveTestMode()
	if audio.InTestMode || !audio.Muted || audio.Deafened || audio.Loopback {
		t.Fatalf("LeaveTestMode failed: inTest=%v muted=%v deaf=%v loop=%v",
			audio.InTestMode, audio.Muted, audio.Deafened, audio.Loopback)
	}
}

func TestSpeechPassesThroughAllModes(t *testing.T) {
	audio := NewAudioEngine()

	// 500 Hz tone representing human voice vowel / formant
	speechPCM := upsample16k(func(i int) int16 {
		val := int16(4000.0 * math.Sin(2.0*math.Pi*500.0*float64(i)/16000.0))
		return val
	})

	// Test Mode 0 (OFF)
	rawRMS := calculateRMS(speechPCM)
	if rawRMS < 0.05 {
		t.Fatalf("Expected speech rawRMS >= 0.05, got %f", rawRMS)
	}

	// Test Mode 1 (ON)
	speaking1, rms1, out1 := audio.processNoiseCancellation(speechPCM, 1)
	if !speaking1 {
		t.Fatalf("Expected speaking=true in Mode 1 (ON)")
	}
	if rms1 < 0.01 || calculateRMS(out1) < 0.01 {
		t.Fatalf("Expected non-zero audible output in Mode 1 (ON), got rms=%f", rms1)
	}

	// Test Mode 2 (HIGH)
	speaking2, rms2, out2 := audio.processNoiseCancellation(speechPCM, 2)
	if !speaking2 {
		t.Fatalf("Expected speaking=true in Mode 2 (HIGH)")
	}
	if rms2 < 0.01 || calculateRMS(out2) < 0.01 {
		t.Fatalf("Expected non-zero audible output in Mode 2 (HIGH), got rms=%f", rms2)
	}
}

func TestVADSensitivityMapping(t *testing.T) {
	audio := NewAudioEngine()

	// Default sensitivity
	if audio.GetVADSensitivity() != 65 {
		t.Fatalf("Expected default sensitivity 65, got %d", audio.GetVADSensitivity())
	}

	// Maximum sensitivity (100% -> very low threshold ~0.001)
	audio.SetVADSensitivity(100)
	if audio.GetVADSensitivity() != 100 {
		t.Fatalf("Expected sensitivity 100, got %d", audio.GetVADSensitivity())
	}
	if audio.VADThreshold > 0.0015 {
		t.Fatalf("Expected low threshold for max sensitivity, got %f", audio.VADThreshold)
	}

	// Minimum sensitivity (1% -> high threshold ~0.050)
	audio.SetVADSensitivity(1)
	if audio.GetVADSensitivity() != 1 {
		t.Fatalf("Expected sensitivity 1, got %d", audio.GetVADSensitivity())
	}
	if audio.VADThreshold < 0.045 {
		t.Fatalf("Expected high threshold for min sensitivity, got %f", audio.VADThreshold)
	}

	// Reset to 65%
	audio.SetVADSensitivity(65)
	if audio.GetVADSensitivity() != 65 {
		t.Fatalf("Expected sensitivity 65, got %d", audio.GetVADSensitivity())
	}
}

func TestNoiseSuppressionFiltersFanNoise(t *testing.T) {
	audio := NewAudioEngine()

	// Generate synthetic PC fan / AC hum (120Hz + 240Hz low drone at moderate volume)
	fanPCM := upsample16k(func(i int) int16 {
		s1 := 1200.0 * math.Sin(2.0*math.Pi*120.0*float64(i)/16000.0)
		s2 := 800.0 * math.Sin(2.0*math.Pi*240.0*float64(i)/16000.0)
		val := int16(s1 + s2)
		return val
	})

	// Run fan noise in Mode 1:
	audio.SetSuppressionMode(1)
	audio.IsSpeaking = false

	// Let the adaptive noise tracker adapt, then allow the speech hangover (18 frames) and the
	// ~120ms gate release to finish (the 30-frame version sat exactly on the 0.01 boundary).
	for f := 0; f < 40; f++ {
		audio.processNoiseCancellation(fanPCM, 1)
	}

	speaking, finalRMS, out := audio.processNoiseCancellation(fanPCM, 1)
	if speaking {
		t.Fatalf("Expected fan noise alone to NOT trigger speech VAD, but got speaking=true")
	}
	if finalRMS > 0.01 {
		t.Fatalf("Expected fan noise to be gated to silence, got finalRMS=%f", finalRMS)
	}
	if calculateRMS(out) > 0.01 {
		t.Fatalf("Expected gated output PCM RMS <= 0.01, got %f", calculateRMS(out))
	}
}

func TestHandClapSuppression(t *testing.T) {
	audio := NewAudioEngine()
	audio.SetSuppressionMode(2) // Mode 2: HIGH (Sonar AI mode)

	// Generate synthetic hand clap (sharp impulsive peak at sample 40, rapidly decaying, non-harmonic)
	clapPCM := upsample16k(func(i int) int16 {
		var sample float64
		if i >= 40 && i < 120 {
			tVal := float64(i - 40)
			sample = 16000.0 * math.Exp(-tVal/8.0) * math.Sin(2.0*math.Pi*1800.0*tVal/16000.0)
		}
		val := int16(sample)
		return val
	})

	speaking, _, _ := audio.processNoiseCancellation(clapPCM, 2)
	if speaking {
		t.Fatalf("Expected hand clap to be rejected by Sonar noise suppressor, but got speaking=true")
	}
}

func TestMechanicalKeyboardTypingSuppression(t *testing.T) {
	audio := NewAudioEngine()
	audio.SetSuppressionMode(2)

	// Generate synthetic mechanical keyboard switch click (high frequency click burst at 3500Hz)
	keyPCM := upsample16k(func(i int) int16 {
		var sample float64
		if i >= 30 && i < 90 {
			tVal := float64(i - 30)
			sample = 10000.0 * math.Exp(-tVal/6.0) * math.Sin(2.0*math.Pi*3600.0*tVal/16000.0)
		}
		val := int16(sample)
		return val
	})

	speaking, _, _ := audio.processNoiseCancellation(keyPCM, 2)
	if speaking {
		t.Fatalf("Expected mechanical keyboard click to be suppressed, but got speaking=true")
	}
}

func TestCoughAndThroatClearingSuppression(t *testing.T) {
	audio := NewAudioEngine()
	audio.SetSuppressionMode(2)

	// Generate synthetic cough / non-harmonic turbulent burst (pseudo-random broadband noise burst)
	coughPCM := upsample16k(func(i int) int16 {
		// Non-harmonic multi-frequency turbulent burst
		s := 4000.0*math.Sin(float64(i*i)*0.13) + 3000.0*math.Cos(float64(i*i*i)*0.07)
		val := int16(s)
		return val
	})

	speaking, _, _ := audio.processNoiseCancellation(coughPCM, 2)
	if speaking {
		t.Fatalf("Expected cough turbulence to be suppressed, but got speaking=true")
	}
}

func TestPitchHarmonicSpeechPassthrough(t *testing.T) {
	audio := NewAudioEngine()

	// Generate synthetic human speech with rich fundamental pitch + vocal harmonics (160 Hz + 320 Hz + 480 Hz)
	speechPCM := upsample16k(func(i int) int16 {
		f0 := 160.0
		tVal := float64(i) / 16000.0
		s1 := 3500.0 * math.Sin(2.0*math.Pi*f0*tVal)
		s2 := 2500.0 * math.Sin(2.0*math.Pi*2.0*f0*tVal)
		s3 := 1500.0 * math.Sin(2.0*math.Pi*3.0*f0*tVal)
		val := int16(s1 + s2 + s3)
		return val
	})

	// Test Mode 1 (Standard) and Mode 2 (High Sonar)
	speaking1, rms1, out1 := audio.processNoiseCancellation(speechPCM, 1)
	if !speaking1 || rms1 < 0.02 || calculateRMS(out1) < 0.02 {
		t.Fatalf("Expected rich voiced speech to pass cleanly in Mode 1, speaking=%v, rms=%f", speaking1, rms1)
	}

	speaking2, rms2, out2 := audio.processNoiseCancellation(speechPCM, 2)
	if !speaking2 || rms2 < 0.02 || calculateRMS(out2) < 0.02 {
		t.Fatalf("Expected rich voiced speech to pass cleanly in Mode 2, speaking=%v, rms=%f", speaking2, rms2)
	}
}

func TestQuietSpeechAndDeepVoicePassthrough(t *testing.T) {
	audio := NewAudioEngine()

	// 1. Test quiet human speech (RMS ~ 0.005)
	quietPCM := upsample16k(func(i int) int16 {
		f0 := 140.0
		tVal := float64(i) / 16000.0
		s := 220.0*math.Sin(2.0*math.Pi*f0*tVal) + 150.0*math.Sin(2.0*math.Pi*2.0*f0*tVal)
		return int16(s)
	})

	speaking, rms, _ := audio.processNoiseCancellation(quietPCM, 1)
	if !speaking || rms < 0.003 {
		t.Fatalf("Expected quiet speech to trigger VAD in Mode 1, speaking=%v, rms=%f", speaking, rms)
	}

	// 2. Test deep male voice (95 Hz low pitch fundamental with high low-frequency energy)
	deepPCM := upsample16k(func(i int) int16 {
		f0 := 95.0
		tVal := float64(i) / 16000.0
		s := 800.0*math.Sin(2.0*math.Pi*f0*tVal) + 500.0*math.Sin(2.0*math.Pi*2.0*f0*tVal) + 300.0*math.Sin(2.0*math.Pi*3.0*f0*tVal)
		return int16(s)
	})

	speakingDeep, rmsDeep, _ := audio.processNoiseCancellation(deepPCM, 1)
	if !speakingDeep || rmsDeep < 0.01 {
		t.Fatalf("Expected deep voice to trigger VAD in Mode 1, speaking=%v, rms=%f", speakingDeep, rmsDeep)
	}
}

func TestFricativeConsonantOnsetPassthrough(t *testing.T) {
	audio := NewAudioEngine()

	// Generate synthetic unvoiced fricative 'S' consonant sound (e.g. "Selam" onset)
	// Turbulent noise concentrated in 4500Hz - 7500Hz with zero pitch harmonicity (natural fricative 'S' sound)
	// High-pass filtered turbulent noise (unvoiced fricative 'S' with low harmonicity)
	var state uint32 = 987654321
	var hpPrevIn, hpPrevOut float64
	fricativePCM := upsample16k(func(i int) int16 {
		state = state*1664525 + 1013904223
		rawNoise := (float64(int32(state)%2000) / 2000.0) * 1200.0 // +/- 1200 amplitude
		// 4500Hz high-pass filter at 16000Hz (alpha ~ 0.36)
		hpOut := 0.36 * (hpPrevOut + rawNoise - hpPrevIn)
		hpPrevIn = rawNoise
		hpPrevOut = hpOut
		val := int16(hpOut)
		return val
	})

	speaking1, rms1, _ := audio.processNoiseCancellation(fricativePCM, 1)
	t.Logf("Mode 1: speaking=%v, rms=%f, threshold=%f", speaking1, rms1, audio.VADThreshold)
	if !speaking1 {
		t.Fatalf("Expected fricative consonant 'S' to pass in Mode 1, but got speaking=false, rms=%f", rms1)
	}

	audio2 := NewAudioEngine()
	speaking2, rms2, _ := audio2.processNoiseCancellation(fricativePCM, 2)
	t.Logf("Mode 2: speaking=%v, rms=%f", speaking2, rms2)
	if !speaking2 {
		t.Fatalf("Expected fricative consonant 'S' to pass in Mode 2, but got speaking=false, rms=%f", rms2)
	}
}

func TestPushToTalkMode(t *testing.T) {
	engine := NewAudioEngine()
	if engine.InputMode != InputModeVoiceActivity {
		t.Fatalf("Expected default input mode to be VoiceActivity")
	}
	if !engine.IsTransmitting() {
		t.Fatalf("Expected VoiceActivity mode to be transmitting when unmuted")
	}

	engine.CycleInputMode()
	if engine.InputMode != InputModePushToTalk {
		t.Fatalf("Expected InputMode to be PushToTalk")
	}
	if engine.IsTransmitting() {
		t.Fatalf("Expected PushToTalk to NOT be transmitting when PTT is idle")
	}

	engine.PulsePTT(200 * time.Millisecond)
	if !engine.IsTransmitting() {
		t.Fatalf("Expected PushToTalk to be transmitting after PulsePTT")
	}

	engine.SetPTT(true)
	if !engine.IsTransmitting() {
		t.Fatalf("Expected PushToTalk to be transmitting when PTT is active")
	}

	engine.SetPTT(false)
	// Due to hangover delay, should still be transmitting briefly
	if !engine.IsTransmitting() {
		t.Fatalf("Expected PushToTalk to still be transmitting during release hangover")
	}
}

func TestPeerJitterBuffer(t *testing.T) {
	jb := newPeerJitterBuffer(2) // 2-chunk prebuffer
	chunk1 := make([]byte, AudioChunkSize)
	chunk2 := make([]byte, AudioChunkSize)
	chunk1[0] = 0x11
	chunk2[0] = 0x22

	// Push 1st chunk: should not be playable yet (cushion building)
	jb.Push(chunk1)
	if _, ok := jb.Pop(); ok {
		t.Fatalf("Expected Pop to return false before prebuffer is full")
	}

	// Push 2nd chunk: now prebuffer target reached -> playable!
	jb.Push(chunk2)
	c, ok := jb.Pop()
	if !ok || c[0] != 0x11 {
		t.Fatalf("Expected Pop to return chunk1 (0x11)")
	}

	c, ok = jb.Pop()
	if !ok || c[0] != 0x22 {
		t.Fatalf("Expected Pop to return chunk2 (0x22)")
	}

	// Now starved: Pop should return false
	if _, ok = jb.Pop(); ok {
		t.Fatalf("Expected Pop to return false on starved buffer")
	}
}

func TestGainRange300(t *testing.T) {
	engine := NewAudioEngine()
	engine.Gain = 1.0

	// Boost beyond 200% up to 300%
	engine.AdjustGain(1.5) // 1.0 + 1.5 = 2.5 (250%)
	if engine.Gain != 2.5 {
		t.Fatalf("Expected gain 2.5, got %f", engine.Gain)
	}

	engine.AdjustGain(1.0) // 2.5 + 1.0 = 3.5 -> clamped to 3.0 (300%)
	if engine.Gain != 3.0 {
		t.Fatalf("Expected gain 3.0, got %f", engine.Gain)
	}

	testPCM := make([]byte, AudioChunkSize)
	for i := 0; i < len(testPCM)/2; i++ {
		binary.LittleEndian.PutUint16(testPCM[i*2:i*2+2], 10000)
	}
	boosted := applyGain(testPCM, 3.0)
	val := int16(binary.LittleEndian.Uint16(boosted[0:2]))
	if val <= 10000 {
		t.Fatalf("Expected soft-boosted sample between 10000 and 32767, got %d", val)
	}
}

func TestAudioDeviceEnumerationAndSelection(t *testing.T) {
	inDevs := EnumerateInputDevices()
	if len(inDevs) == 0 {
		t.Fatalf("Expected at least 1 input device (default), got 0")
	}

	outDevs := EnumerateOutputDevices()
	if len(outDevs) == 0 {
		t.Fatalf("Expected at least 1 output device (default), got 0")
	}

	engine := NewAudioEngine()
	// Add mock devices for comprehensive cycling test
	engine.InputDevices = []AudioDevice{
		{ID: "default", Name: "Default Mic", IsDefault: true, IsInput: true},
		{ID: "mic_2", Name: "USB Headset Mic", IsInput: true},
		{ID: "mic_3", Name: "Webcam Mic", IsInput: true},
	}
	engine.OutputDevices = []AudioDevice{
		{ID: "default", Name: "Default Speakers", IsDefault: true, IsInput: false},
		{ID: "out_2", Name: "Headphones", IsInput: false},
	}

	// Test Mic Cycling
	if engine.GetSelectedInputName() != "Default Mic" {
		t.Fatalf("Expected 'Default Mic', got %q", engine.GetSelectedInputName())
	}
	engine.CycleInputDevice(1)
	if engine.SelectedInputIdx != 1 || engine.GetSelectedInputName() != "USB Headset Mic" {
		t.Fatalf("Expected 'USB Headset Mic' at index 1, got %q", engine.GetSelectedInputName())
	}
	engine.CycleInputDevice(-1)
	if engine.SelectedInputIdx != 0 {
		t.Fatalf("Expected index 0 after reverse cycle, got %d", engine.SelectedInputIdx)
	}

	// Test Output Device Cycling
	if engine.GetSelectedOutputName() != "Default Speakers" {
		t.Fatalf("Expected 'Default Speakers', got %q", engine.GetSelectedOutputName())
	}
	engine.CycleOutputDevice(1)
	if engine.SelectedOutputIdx != 1 || engine.GetSelectedOutputName() != "Headphones" {
		t.Fatalf("Expected 'Headphones' at index 1, got %q", engine.GetSelectedOutputName())
	}
}

func TestOutputVolumeAndAGC(t *testing.T) {
	engine := NewAudioEngine()

	// Initial output volume should be 1.0 (100%)
	if engine.OutputVolume != 1.0 {
		t.Fatalf("Expected initial output volume 1.0, got %f", engine.OutputVolume)
	}

	// Boost output volume to 1.5 (150%)
	engine.AdjustOutputVolume(0.5)
	if engine.OutputVolume != 1.5 {
		t.Fatalf("Expected output volume 1.5, got %f", engine.OutputVolume)
	}

	// Test mixPCM with output volume multiplier
	chunk := make([]byte, AudioChunkSize)
	for i := 0; i < len(chunk)/2; i++ {
		binary.LittleEndian.PutUint16(chunk[i*2:i*2+2], 1000)
	}
	mixed := mixPCM([][]byte{chunk}, len(chunk)/2, 2.0)
	val := int16(binary.LittleEndian.Uint16(mixed[0:2]))
	if val < 1900 || val > 2100 {
		t.Fatalf("Expected output sample ~2000 after 2.0x volume, got %d", val)
	}

	// Test PlayPeerPCM dynamic AGC boosting for quiet incoming voice
	quietChunk := make([]byte, AudioChunkSize)
	for i := 0; i < len(quietChunk)/2; i++ {
		binary.LittleEndian.PutUint16(quietChunk[i*2:i*2+2], 500)
	}
	quietRMS := calculateRMS(quietChunk)
	engine.PlayPeerPCM("peer_quiet", quietChunk, quietRMS, true)
	jb := engine.peerJitterBuffers["peer_quiet"]
	if jb == nil {
		t.Fatalf("Expected jitter buffer created for peer_quiet")
	}
}

func TestSoundEffectsSynthesis(t *testing.T) {
	// 1. Test Join Sound Synthesis
	joinPCM := generateJoinSoundPCM()
	if len(joinPCM) == 0 || len(joinPCM)%AudioChunkSize != 0 {
		t.Fatalf("Join sound PCM length must be non-zero multiple of AudioChunkSize, got %d", len(joinPCM))
	}

	// 2. Test Leave Sound Synthesis
	leavePCM := generateLeaveSoundPCM()
	if len(leavePCM) == 0 || len(leavePCM)%AudioChunkSize != 0 {
		t.Fatalf("Leave sound PCM length must be non-zero multiple of AudioChunkSize, got %d", len(leavePCM))
	}

	// 3. Test Chat Sound Synthesis
	chatPCM := generateChatSoundPCM()
	if len(chatPCM) == 0 || len(chatPCM)%AudioChunkSize != 0 {
		t.Fatalf("Chat sound PCM length must be non-zero multiple of AudioChunkSize, got %d", len(chatPCM))
	}

	// 4. Test PlaySound queuing and duplicate debouncing in AudioEngine
	engine := NewAudioEngine()
	engine.PlaySound(SoundJoin)
	initialLen := len(engine.sfxQueue)
	if initialLen == 0 {
		t.Fatalf("Expected sfxQueue to have queued chunks after PlaySound(SoundJoin)")
	}

	// Immediate second PlaySound(SoundJoin) must be debounced and ignored
	engine.PlaySound(SoundJoin)
	if len(engine.sfxQueue) != initialLen {
		t.Fatalf("Expected duplicate SoundJoin within 400ms to be debounced, got queue len %d vs initial %d", len(engine.sfxQueue), initialLen)
	}

	engine.PlaySound(SoundLeave)
	engine.PlaySound(SoundChat)
	if len(engine.sfxQueue) < 10 {
		t.Fatalf("Expected multiple sound effect chunks in sfxQueue, got %d", len(engine.sfxQueue))
	}
}

func TestPerUserVolumeAdjustment(t *testing.T) {
	audio := NewAudioEngine()
	peerID := "peer_alice_123"

	// 1. Default volume should be 1.0 (100%)
	if vol := audio.GetPeerVolume(peerID); vol != 1.0 {
		t.Fatalf("Expected default peer volume to be 1.0, got %f", vol)
	}

	// 2. Setting peer volume
	audio.SetPeerVolume(peerID, 1.50)
	if vol := audio.GetPeerVolume(peerID); math.Abs(vol-1.50) > 0.001 {
		t.Fatalf("Expected peer volume 1.50, got %f", vol)
	}

	// 3. Clamping: negative volume clamped to 0.0, >2.0 clamped to 2.0
	audio.SetPeerVolume(peerID, -0.5)
	if vol := audio.GetPeerVolume(peerID); vol != 0.0 {
		t.Fatalf("Expected volume clamped to 0.0, got %f", vol)
	}
	audio.SetPeerVolume(peerID, 2.5)
	if vol := audio.GetPeerVolume(peerID); vol != 2.0 {
		t.Fatalf("Expected volume clamped to 2.0, got %f", vol)
	}

	// 4. AdjustPeerVolume delta
	audio.SetPeerVolume(peerID, 1.0)
	audio.AdjustPeerVolume(peerID, 0.25)
	if vol := audio.GetPeerVolume(peerID); math.Abs(vol-1.25) > 0.001 {
		t.Fatalf("Expected peer volume 1.25 after +0.25 delta, got %f", vol)
	}

	// 5. Test applyGain with per-peer volume
	rawPCM := make([]byte, 320*2)
	for i := 0; i < 320; i++ {
		binary.LittleEndian.PutUint16(rawPCM[i*2:i*2+2], uint16(1000))
	}

	// 0% volume should silence the audio completely
	silenced := applyGain(rawPCM, 0.0)
	for i := 0; i < 320; i++ {
		v := int16(binary.LittleEndian.Uint16(silenced[i*2 : i*2+2]))
		if v != 0 {
			t.Fatalf("Expected sample %d to be 0 at 0%% volume, got %d", i, v)
		}
	}

	// 150% volume should amplify samples
	boosted := applyGain(rawPCM, 1.50)
	vBoosted := int16(binary.LittleEndian.Uint16(boosted[0:2]))
	if vBoosted != 1500 {
		t.Fatalf("Expected amplified sample to be 1500 at 150%% gain, got %d", vBoosted)
	}
}

func TestAudioEngineRemovePeerMemoryCleanup(t *testing.T) {
	engine := NewAudioEngine()
	defer engine.Stop()

	testPCM := make([]byte, AudioChunkSize)
	engine.PlayPeerPCM("peer_99", testPCM, 0.05, true)

	// Check peer jitter buffer and wave exist
	engine.mu.RLock()
	_, jbExists := engine.peerJitterBuffers["peer_99"]
	_, waveExists := engine.PeerWaves["peer_99"]
	engine.mu.RUnlock()

	if !jbExists || !waveExists {
		t.Fatalf("Expected peer jitter buffer and wave to be created")
	}

	// Remove peer
	engine.RemovePeer("peer_99")

	engine.mu.RLock()
	_, jbExistsAfter := engine.peerJitterBuffers["peer_99"]
	_, waveExistsAfter := engine.PeerWaves["peer_99"]
	engine.mu.RUnlock()

	if jbExistsAfter || waveExistsAfter {
		t.Fatalf("Expected peer jitter buffer and wave to be deleted after RemovePeer")
	}

	// Test ClearAllPeers
	engine.PlayPeerPCM("peer_1", testPCM, 0.05, true)
	engine.PlayPeerPCM("peer_2", testPCM, 0.05, true)
	engine.ClearAllPeers()

	engine.mu.RLock()
	count := len(engine.peerJitterBuffers)
	waveCount := len(engine.PeerWaves)
	engine.mu.RUnlock()

	if count != 0 || waveCount != 0 {
		t.Fatalf("Expected 0 peers after ClearAllPeers, got %d buffers and %d waves", count, waveCount)
	}
}

func TestMutedSoundEffectsAreNotQueued(t *testing.T) {
	audioEngine := NewAudioEngine()
	if audioEngine.SFXMuted {
		t.Fatalf("Expected SFXMuted to start false")
	}
	audioEngine.ToggleSFXMute()
	if !audioEngine.SFXMuted {
		t.Fatalf("Expected SFXMuted to be true after toggle")
	}
	audioEngine.PlaySound(SoundChat)
	if len(audioEngine.sfxQueue) != 0 {
		t.Fatalf("Expected sfxQueue to be empty when SFXMuted is true, got %d chunks", len(audioEngine.sfxQueue))
	}
}
