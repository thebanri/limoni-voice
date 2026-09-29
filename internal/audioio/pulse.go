//go:build !windows && !darwin

package audioio

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thebanri/limoni-voice/internal/applog"

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
	done   chan struct{}
}

func (s *pulseStream) Backend() string { return "pulse" }

func (s *pulseStream) Close() error {
	s.once.Do(func() {
		if s.done != nil {
			close(s.done)
		}
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

// captureSeq numbers capture streams, so watchSource can find its own among the server's.
var captureSeq atomic.Uint64

func (b *pulseBackend) OpenCapture(deviceID string, cb CaptureFunc) (Stream, error) {
	c, err := b.client()
	if err != nil {
		return nil, err
	}
	tag := fmt.Sprintf("%d-%d", os.Getpid(), captureSeq.Add(1))
	opts := []pulse.RecordOption{
		pulse.RecordMono,
		pulse.RecordSampleRate(SampleRate),
		pulse.RecordLatency(0.02),
		pulse.RecordMediaName("Limoni Voice microphone"),
		pulse.RecordRawOption(func(r *proto.CreateRecordStream) {
			// No "filter.want=echo-cancel": a system echo-cancel filter usually brings its own AGC,
			// which pumps keyboard and room noise up between words and fights our own AEC.
			r.Properties["media.role"] = proto.PropListString("phone")
			r.Properties[captureTagProp] = proto.PropListString(tag)
		}),
	}
	src, err := microphoneSource(c, deviceID)
	if err != nil {
		c.Close()
		return nil, err
	}
	if src != nil {
		opts = append(opts, pulse.RecordSource(src))
	}
	var onMonitor atomic.Bool
	onMonitor.Store(true) // silent until watchSource has seen where the stream is connected
	acc := newFrameAccumulator(cb)
	rec, err := c.NewRecord(pulse.Int16Writer(func(p []int16) (int, error) {
		if onMonitor.Load() {
			clear(p) // moved to a speaker monitor: never send the system's sound as the voice
		}
		acc.push(p)
		return len(p), nil
	}), opts...)
	if err != nil {
		c.Close()
		return nil, err
	}
	rec.Start()
	s := &pulseStream{client: c, rec: rec, done: make(chan struct{})}
	go s.watchSource(tag, &onMonitor)
	return s, nil
}

// sourceInfo looks a source up by index (proto.Undefined and name "" = the default source).
func sourceInfo(c *pulse.Client, index uint32, name string) (*proto.GetSourceInfoReply, error) {
	var info proto.GetSourceInfoReply
	err := c.RawRequest(&proto.GetSourceInfo{SourceIndex: index, SourceName: name}, &info)
	return &info, err
}

// isMonitor reports a sink monitor source: it carries what the computer plays.
func isMonitor(info *proto.GetSourceInfoReply) bool {
	return info.MonitorSourceIndex != proto.Undefined || strings.HasSuffix(info.SourceName, ".monitor")
}

// microphoneSource picks the source to record. nil keeps the server default (and follows it
// when it changes) as long as that default is a microphone. PulseAudio makes a speaker
// monitor the default source when no microphone is present (a headset switched off, say);
// then the first real microphone is used, and without one capture fails with ErrNoMicrophone.
func microphoneSource(c *pulse.Client, deviceID string) (*pulse.Source, error) {
	if !isDefault(deviceID) {
		src, err := c.SourceByID(deviceID)
		if err != nil {
			return nil, fmt.Errorf("pulse source %q: %w", deviceID, err)
		}
		if info, err := sourceInfo(c, src.SourceIndex(), ""); err == nil && isMonitor(info) {
			return nil, fmt.Errorf("pulse source %q: %w", deviceID, ErrNoMicrophone)
		}
		return src, nil
	}
	def, err := sourceInfo(c, proto.Undefined, "")
	if err != nil || !isMonitor(def) {
		return nil, nil
	}
	sources, err := c.ListSources()
	if err != nil {
		return nil, err
	}
	for _, src := range sources {
		if info, err := sourceInfo(c, src.SourceIndex(), ""); err == nil && !isMonitor(info) {
			applog.Printf("[AUDIO] Default input %s is a speaker monitor; recording %s instead", def.SourceName, src.ID())
			return src, nil
		}
	}
	applog.Printf("[AUDIO] No microphone: the only input is the speaker monitor %s", def.SourceName)
	return nil, ErrNoMicrophone
}

// captureTagProp marks our record stream (the library does not expose its source output index).
const captureTagProp = "limoni.capture.id"

// recordSource returns the source output index of the record stream tagged tag and the index
// of the source it is connected to.
func (s *pulseStream) recordSource(tag string) (output, source uint32, ok bool) {
	var outputs proto.GetSourceOutputInfoListReply
	if s.client.RawRequest(&proto.GetSourceOutputInfoList{}, &outputs) != nil {
		return 0, 0, false
	}
	for _, o := range outputs {
		if e, ok := o.Properties[captureTagProp]; ok && e.String() == tag {
			return o.SourceOutpuIndex, o.SourceIndex, true
		}
	}
	return 0, 0, false
}

// realMicrophone returns the source to move a stream to: the default source unless that is a
// monitor, otherwise the first microphone.
func realMicrophone(c *pulse.Client) (*proto.GetSourceInfoReply, bool) {
	if def, err := sourceInfo(c, proto.Undefined, ""); err == nil && !isMonitor(def) {
		return def, true
	}
	var sources proto.GetSourceInfoListReply
	if c.RawRequest(&proto.GetSourceInfoList{}, &sources) != nil {
		return nil, false
	}
	for _, src := range sources {
		if !isMonitor(src) {
			return src, true
		}
	}
	return nil, false
}

// watchSource checks which source the record stream is connected to. The server can connect
// it to a speaker monitor: when its microphone goes away (a headset switched off), or because
// the session manager restores a target remembered for "Limoni Voice" (whose screen share audio
// records a monitor). The stream is then moved back to a microphone; with none available the
// captured audio is replaced by silence. The system's sound is never sent as the voice.
func (s *pulseStream) watchSource(tag string, onMonitor *atomic.Bool) {
	wait := 150 * time.Millisecond // first check: the stream starts muted until then
	checked, warned := false, false
	for {
		select {
		case <-s.done:
			return
		case <-time.After(wait):
		}
		wait = 250 * time.Millisecond
		output, source, ok := s.recordSource(tag)
		if !ok {
			continue
		}
		info, err := sourceInfo(s.client, source, "")
		if err != nil {
			continue
		}
		if !isMonitor(info) {
			if onMonitor.Swap(false) && checked {
				applog.Printf("[AUDIO] Microphone stream is on %s again", info.SourceName)
			}
			checked, warned = true, false
			continue
		}
		checked = true
		onMonitor.Store(true)
		mic, found := realMicrophone(s.client)
		if !found {
			if !warned {
				applog.Printf("[AUDIO] Microphone stream is on the speaker monitor %s and there is no microphone; sending silence", info.SourceName)
				warned = true
			}
			continue
		}
		err = s.client.RawRequest(&proto.MoveSourceOutput{SourceOutputIndex: output, DeviceIndex: mic.SourceIndex}, nil)
		applog.Printf("[AUDIO] Microphone stream was on the speaker monitor %s; moved it to %s (err %v)", info.SourceName, mic.SourceName, err)
		wait = 200 * time.Millisecond // confirm the move soon
	}
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
