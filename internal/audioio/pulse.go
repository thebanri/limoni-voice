//go:build !windows && !darwin

package audioio

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
)

// pulseBackend speaks the PulseAudio native protocol directly (also served by PipeWire's
// pipewire-pulse), giving low-latency, device-clocked streams without external processes.
type pulseBackend struct {
	once      sync.Once
	available bool
}

func init() { register(&pulseBackend{}, priorityNative) }

func (b *pulseBackend) Name() string { return "pulse" }

func (b *pulseBackend) Available() bool {
	if os.Getenv("LIMONI_AUDIO_BACKEND") == "exec" {
		return false
	}
	b.once.Do(func() {
		c, err := b.client()
		if err == nil {
			c.Close()
			b.available = true
		}
	})
	return b.available
}

func (b *pulseBackend) client() (*pulse.Client, error) {
	return pulse.NewClient(pulse.ClientApplicationName("Limoni Voice"), pulse.ClientApplicationIconName("audio-input-microphone"), pulse.ClientTimeout(2*time.Second))
}

func (b *pulseBackend) InputDevices() ([]Device, error) {
	c, err := b.client()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	sources, err := c.ListSources()
	if err != nil {
		return nil, err
	}
	devs := []Device{{ID: "default", Name: "Default System Microphone", IsDefault: true}}
	for _, s := range sources {
		if len(s.ID()) > 8 && s.ID()[len(s.ID())-8:] == ".monitor" {
			continue
		}
		devs = append(devs, Device{ID: s.ID(), Name: s.Name()})
	}
	return devs, nil
}

func (b *pulseBackend) OutputDevices() ([]Device, error) {
	c, err := b.client()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	sinks, err := c.ListSinks()
	if err != nil {
		return nil, err
	}
	devs := []Device{{ID: "default", Name: "Default System Output / Speakers", IsDefault: true}}
	for _, s := range sinks {
		devs = append(devs, Device{ID: s.ID(), Name: s.Name()})
	}
	return devs, nil
}

type pulseStream struct {
	client *pulse.Client
	rec    *pulse.RecordStream
	play   *pulse.PlaybackStream
	once   sync.Once
}

func (s *pulseStream) Backend() string { return "pulse" }

func (s *pulseStream) Close() error {
	s.once.Do(func() {
		if s.rec != nil {
			s.rec.Stop()
			s.rec.Close()
		}
		if s.play != nil {
			s.play.Stop()
			s.play.Close()
		}
		s.client.Close()
	})
	return nil
}

func (b *pulseBackend) OpenCapture(deviceID string, cb CaptureFunc) (Stream, error) {
	c, err := b.client()
	if err != nil {
		return nil, err
	}
	opts := []pulse.RecordOption{
		pulse.RecordMono,
		pulse.RecordSampleRate(SampleRate),
		pulse.RecordLatency(0.02),
		pulse.RecordMediaName("Limoni Voice microphone"),
		pulse.RecordRawOption(func(r *proto.CreateRecordStream) {
			// No "filter.want=echo-cancel": a system echo-cancel filter usually brings its own AGC,
			// which pumps keyboard and room noise up between words and fights our own AEC.
			r.Properties["media.role"] = proto.PropListString("phone")
		}),
	}
	if !isDefault(deviceID) {
		src, err := c.SourceByID(deviceID)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("pulse source %q: %w", deviceID, err)
		}
		opts = append(opts, pulse.RecordSource(src))
	}
	acc := newFrameAccumulator(cb)
	rec, err := c.NewRecord(pulse.Int16Writer(func(p []int16) (int, error) {
		acc.push(p)
		return len(p), nil
	}), opts...)
	if err != nil {
		c.Close()
		return nil, err
	}
	rec.Start()
	return &pulseStream{client: c, rec: rec}, nil
}

func (b *pulseBackend) OpenPlayback(deviceID string, cb RenderFunc) (Stream, error) {
	c, err := b.client()
	if err != nil {
		return nil, err
	}
	opts := []pulse.PlaybackOption{
		pulse.PlaybackMono,
		pulse.PlaybackSampleRate(SampleRate),
		pulse.PlaybackLatency(0.04),
		pulse.PlaybackMediaName("Limoni Voice"),
		pulse.PlaybackRawOption(func(r *proto.CreatePlaybackStream) {
			r.Properties["media.role"] = proto.PropListString("phone")
		}),
	}
	if !isDefault(deviceID) {
		sink, err := c.SinkByID(deviceID)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("pulse sink %q: %w", deviceID, err)
		}
		opts = append(opts, pulse.PlaybackSink(sink))
	}
	adapter := newRenderAdapter(cb)
	play, err := c.NewPlayback(pulse.Int16Reader(func(out []int16) (int, error) {
		adapter.fill(out)
		return len(out), nil
	}), opts...)
	if err != nil {
		c.Close()
		return nil, err
	}
	play.Start()
	return &pulseStream{client: c, play: play}, nil
}
