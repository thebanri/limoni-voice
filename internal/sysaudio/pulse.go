//go:build !windows && !darwin

package sysaudio

import (
	"fmt"
	"sync"
	"time"

	"github.com/jfreymuth/pulse"
)

type pulseStream struct {
	client *pulse.Client
	rec    *pulse.RecordStream
	once   sync.Once
	sink   string
}

func (s *pulseStream) Backend() string { return "pulse monitor (" + s.sink + ")" }

func (s *pulseStream) Close() error {
	s.once.Do(func() {
		s.rec.Stop()
		s.rec.Close()
		s.client.Close()
	})
	return nil
}

func open(onFrame FrameFunc) (Stream, error) {
	c, err := pulse.NewClient(pulse.ClientApplicationName("Limoni Voice"), pulse.ClientTimeout(2*time.Second))
	if err != nil {
		return nil, fmt.Errorf("sysaudio: connect to PulseAudio/PipeWire: %w", err)
	}
	sink, err := c.DefaultSink()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("sysaudio: default output: %w", err)
	}
	fr := newFramer(onFrame)
	rec, err := c.NewRecord(pulse.Int16Writer(func(p []int16) (int, error) {
		fr.push(p)
		return len(p), nil
	}),
		pulse.RecordMono,
		pulse.RecordSampleRate(SampleRate),
		pulse.RecordLatency(0.02),
		pulse.RecordMonitor(sink),
		pulse.RecordMediaName("Limoni Voice screen share audio"),
	)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("sysaudio: record monitor of %s: %w", sink.ID(), err)
	}
	rec.Start()
	return &pulseStream{client: c, rec: rec, sink: sink.Name()}, nil
}
