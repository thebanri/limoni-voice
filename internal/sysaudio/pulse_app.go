//go:build !windows && !darwin

package sysaudio

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
)

// appRescan is how often new streams of the application are picked up: a browser opens its
// playback stream only when a page starts playing.
const appRescan = 500 * time.Millisecond

// pulseAppStream records each playback stream (sink input) of one application directly, the
// way pavucontrol meters a single stream, and mixes them. Other programs and the rest of the
// output are never captured.
type pulseAppStream struct {
	client *pulse.Client
	app    App
	names  map[string]bool // executable names of the application, lower case
	mix    *mixer
	frames atomic.Uint64
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once

	mu   sync.Mutex
	recs map[uint32]*pulse.RecordStream // by sink input index
}

func (s *pulseAppStream) Frames() uint64 { return s.frames.Load() }

func (s *pulseAppStream) Backend() string {
	s.mu.Lock()
	n := len(s.recs)
	s.mu.Unlock()
	return fmt.Sprintf("pulse app streams of %s (pid %d, %d playing)", s.app.Name, s.app.PID, n)
}

func (s *pulseAppStream) Close() error {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
		s.mu.Lock()
		for id, rec := range s.recs {
			rec.Stop()
			rec.Close()
			delete(s.recs, id)
		}
		s.mu.Unlock()
		s.client.Close()
	})
	return nil
}

func openApp(app App, onFrame FrameFunc) (Stream, error) {
	c, err := pulse.NewClient(pulse.ClientApplicationName("Limoni Voice"), pulse.ClientTimeout(2*time.Second))
	if err != nil {
		return nil, fmt.Errorf("sysaudio: connect to PulseAudio/PipeWire: %w", err)
	}
	s := &pulseAppStream{
		client: c,
		app:    app,
		names:  appNames(app),
		mix:    newMixer(onFrame),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		recs:   map[uint32]*pulse.RecordStream{},
	}
	if err := s.scan(); err != nil {
		c.Close()
		return nil, err
	}
	go s.run()
	return s, nil
}

func (s *pulseAppStream) run() {
	defer close(s.done)
	t := time.NewTicker(appRescan)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			_ = s.scan()
		}
	}
}

// scan starts recording streams of the application that appeared and drops those that ended.
func (s *pulseAppStream) scan() error {
	var inputs proto.GetSinkInputInfoListReply
	if err := s.client.RawRequest(&proto.GetSinkInputInfoList{}, &inputs); err != nil {
		return fmt.Errorf("sysaudio: list playback streams: %w", err)
	}
	var sinks proto.GetSinkInfoListReply
	if err := s.client.RawRequest(&proto.GetSinkInfoList{}, &sinks); err != nil {
		return fmt.Errorf("sysaudio: list outputs: %w", err)
	}
	monitor := map[uint32]uint32{}
	for _, sk := range sinks {
		monitor[sk.SinkIndex] = sk.MonitorSourceIndex
	}
	// Native PipeWire clients (Spotify, …) put the process on the client, not the stream.
	var clients proto.GetClientInfoListReply
	_ = s.client.RawRequest(&proto.GetClientInfoList{}, &clients)
	clientProps := map[uint32]proto.PropList{}
	for _, cl := range clients {
		clientProps[cl.ClientIndex] = cl.Properties
	}

	live := map[uint32]bool{}
	for _, in := range inputs {
		if !s.matches(in.Properties) && !s.matches(clientProps[in.ClientIndex]) {
			continue
		}
		live[in.SinkInputIndex] = true
		s.mu.Lock()
		_, have := s.recs[in.SinkInputIndex]
		s.mu.Unlock()
		// PipeWire does not answer a record request on a paused stream until it plays;
		// the next scan attaches once it does.
		if have || in.Corked {
			continue
		}
		src, ok := monitor[in.SinkIndex]
		if !ok {
			continue
		}
		if rec, err := s.record(in.SinkInputIndex, src); err == nil {
			s.mu.Lock()
			s.recs[in.SinkInputIndex] = rec
			s.mu.Unlock()
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rec := range s.recs {
		if !live[id] || rec.Closed() {
			rec.Stop()
			rec.Close()
			delete(s.recs, id)
			s.mix.remove(id)
		}
	}
	return nil
}

func (s *pulseAppStream) record(input, monitorSource uint32) (*pulse.RecordStream, error) {
	s.mix.add(input)
	rec, err := s.client.NewRecord(pulse.Int16Writer(func(p []int16) (int, error) {
		s.frames.Add(uint64(len(p)))
		s.mix.push(input, p, time.Now())
		return len(p), nil
	}),
		pulse.RecordMono,
		pulse.RecordSampleRate(SampleRate),
		pulse.RecordLatency(0.02),
		pulse.RecordRawOption(func(r *proto.CreateRecordStream) {
			r.SourceIndex = monitorSource
			r.DirectOnInputIndex = input
		}),
		pulse.RecordMediaName("Limoni Voice screen share audio"),
	)
	if err != nil {
		s.mix.remove(input)
		return nil, err
	}
	rec.Start()
	return rec, nil
}

// matches reports whether a playback stream belongs to the application.
func (s *pulseAppStream) matches(props proto.PropList) bool {
	prop := func(k string) string {
		if v, ok := props[k]; ok {
			return v.String()
		}
		return ""
	}
	if pid, err := strconv.Atoi(prop("application.process.id")); err == nil && pid > 1 {
		if pid == os.Getpid() {
			return false
		}
		if s.app.PID > 1 && descendsFrom(pid, s.app.PID) {
			return true
		}
	}
	for _, k := range []string{"application.process.binary", "application.name"} {
		if v := strings.ToLower(prop(k)); v != "" && s.names[v] {
			return true
		}
	}
	return false
}

// appNames lists the executable names the application's processes run under.
func appNames(app App) map[string]bool {
	names := map[string]bool{}
	if app.Name != "" {
		names[strings.ToLower(app.Name)] = true
	}
	if app.PID > 1 {
		if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", app.PID)); err == nil {
			names[strings.ToLower(strings.TrimSpace(string(comm)))] = true
		}
		if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", app.PID)); err == nil {
			names[strings.ToLower(filepath.Base(exe))] = true
		}
	}
	delete(names, "")
	return names
}

// descendsFrom reports whether pid is ancestor or one of its descendants.
func descendsFrom(pid, ancestor int) bool {
	for range 64 {
		if pid == ancestor {
			return true
		}
		if pid <= 1 {
			return false
		}
		pid = parentPID(pid)
	}
	return false
}

func parentPID(pid int) int {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// pid (comm) state ppid ... — comm may contain spaces and parentheses.
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}
