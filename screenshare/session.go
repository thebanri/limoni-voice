package screenshare

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ErrSessionStopped is returned when a stopped session is asked to restart.
var ErrSessionStopped = errors.New("screenshare: session stopped")

// Session manages the lifecycle of a broadcaster or receiver subprocess. A broadcast session can
// restart its encoder (Restart) without ending: Done only closes when the session is over.
type Session struct {
	mu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	errCh  chan error
	doneCh chan struct{}

	stopped    bool
	restarting bool
	finished   bool

	isBroad     bool
	targetURL   string
	opt         BroadcastOptions
	pwSrc       *pipewireSource
	custom      CommandBuilder
	cleanupFunc func()
	gen         *generation

	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// generation is one run of the encoder / player process (and its helper).
type generation struct {
	cmd      *exec.Cmd
	extra    *exec.Cmd  // macOS ScreenCaptureKit helper feeding cmd
	extraLog *logBuffer // the helper's stderr
	cancel   context.CancelFunc
	exited   chan struct{}
	stderr   *logBuffer
}

// logBuffer keeps the tail of a process's stderr (safe for concurrent use).
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() > 64*1024 {
		l.buf.Reset()
	}
	l.buf.WriteString(line + "\n")
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func newSession(ctx context.Context, broadcast bool, targetURL string) *Session {
	sctx, cancel := context.WithCancel(ctx)
	return &Session{
		ctx:       sctx,
		cancel:    cancel,
		errCh:     make(chan error, 1),
		doneCh:    make(chan struct{}),
		isBroad:   broadcast,
		targetURL: targetURL,
	}
}

func (s *Session) Stdin() io.WriteCloser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stdin
}

func (s *Session) Stdout() io.ReadCloser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stdout
}

func (s *Session) BinPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != nil && s.gen.cmd != nil {
		return s.gen.cmd.Path
	}
	return ""
}

// Options returns the options of the running encoder generation.
func (s *Session) Options() BroadcastOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opt
}

// captureStderr streams a process's stderr into the log and a bounded buffer.
func captureStderr(cmd *exec.Cmd, prefix string) *logBuffer {
	buf := &logBuffer{}
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return buf
	}
	go func() {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			text := scanner.Text()
			buf.add(text)
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				logMsg("[%s] %s", prefix, trimmed)
			}
		}
	}()
	return buf
}

// attach makes g the current generation and supervises it.
func (s *Session) attach(g *generation) {
	s.mu.Lock()
	s.gen = g
	s.mu.Unlock()
	go s.wait(g)
}

func (s *Session) wait(g *generation) {
	err := g.cmd.Wait()
	if g.extra != nil && g.extra.Process != nil {
		go killProcessGroup(g.extra)
	}
	close(g.exited)

	s.mu.Lock()
	superseded := s.gen != g || s.restarting
	s.mu.Unlock()
	if superseded {
		return
	}
	if err != nil && g.stderr != nil {
		var errLines []string
		lines := strings.Split(strings.TrimSpace(g.stderr.String()), "\n")
		for i := len(lines) - 1; i >= 0 && len(errLines) < 5; i-- {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed != "" && !strings.HasPrefix(trimmed, "frame=") && !strings.HasPrefix(trimmed, "size=") {
				errLines = append([]string{trimmed}, errLines...)
			}
		}
		if len(errLines) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.Join(errLines, " | "))
		}
	}
	if g.extraLog != nil && strings.Contains(g.extraLog.String(), "PERMISSION|screen") {
		setMacScreenPermissionMissing(true)
		if err != nil {
			err = fmt.Errorf("%s (%w)", MacScreenPermissionHint, err)
		}
	}
	s.finish(err)
}

// finish ends the session once: releases the capture source and closes Done.
func (s *Session) finish(err error) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	s.stopped = true
	cleanup := s.cleanupFunc
	s.cleanupFunc = nil
	src := s.pwSrc
	s.mu.Unlock()

	if cleanup != nil {
		cleanup()
	}
	if src != nil && src.cleanup != nil {
		src.cleanup()
		src.cleanup = nil
	}
	if err != nil && s.ctx.Err() == nil {
		logMsg("[SESSION] Process exited with error: %v", err)
		s.errCh <- err
	} else {
		logMsg("[SESSION] Process terminated cleanly.")
	}
	close(s.doneCh)
}

// Restart replaces the running encoder with one using opt (bitrate / resolution changes). The
// compositor capture session (portal, Mutter) is kept, so no picker appears again.
func (s *Session) Restart(opt BroadcastOptions) error {
	s.mu.Lock()
	if s.stopped || !s.isBroad || s.gen == nil {
		s.mu.Unlock()
		return ErrSessionStopped
	}
	s.restarting = true
	old := s.gen
	if opt.OnAudio == nil {
		opt.OnAudio = s.opt.OnAudio
	}
	s.mu.Unlock()

	old.cancel()
	if old.cmd.Process != nil {
		killProcessGroup(old.cmd)
	}
	if old.extra != nil && old.extra.Process != nil {
		killProcessGroup(old.extra)
	}
	select {
	case <-old.exited:
	case <-time.After(3 * time.Second):
	}

	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		s.mu.Lock()
		s.restarting = false
		s.mu.Unlock()
		s.finish(nil)
		return ErrSessionStopped
	}

	err := s.launchBroadcast(opt)
	s.mu.Lock()
	s.restarting = false
	s.mu.Unlock()
	if err != nil {
		s.finish(fmt.Errorf("encoder restart failed: %w", err))
		return err
	}
	logMsg("[BROADCAST] Encoder restarted (%s @ %d FPS, %s)", opt.Resolution, opt.FPS, opt.bitrateString("default"))
	return nil
}

// Stop terminates the subprocess
func (s *Session) Stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	cleanup := s.cleanupFunc
	s.cleanupFunc = nil
	g := s.gen
	s.mu.Unlock()

	if cleanup != nil {
		cleanup()
	}
	s.cancel()

	// Terminate process group instantly in background
	if g != nil {
		if g.cmd != nil && g.cmd.Process != nil {
			go killProcessGroup(g.cmd)
		}
		if g.extra != nil && g.extra.Process != nil {
			go killProcessGroup(g.extra)
		}
	}
	return nil
}

// Done returns a channel that closes when the session terminates
func (s *Session) Done() <-chan struct{} {
	return s.doneCh
}

// Err returns any error encountered during execution
func (s *Session) Err() <-chan error {
	return s.errCh
}

// IsBroadcasting returns true if this session is sending video
func (s *Session) IsBroadcasting() bool {
	return s.isBroad
}

// TargetURL returns the UDP stream URL
func (s *Session) TargetURL() string {
	return s.targetURL
}

// StartProcess runs an arbitrary command as a Session: a custom capture pipeline writing
// MPEG-TS to a UDP target, or a custom player reading the stream from Stdin (withStdin).
func StartProcess(ctx context.Context, bin string, args, env []string, withStdin bool) (*Session, error) {
	s := newSession(ctx, !withStdin, "")
	genCtx, genCancel := context.WithCancel(s.ctx)
	g := &generation{cancel: genCancel, exited: make(chan struct{})}
	g.cmd = exec.CommandContext(genCtx, bin, args...)
	if len(env) > 0 {
		g.cmd.Env = append(g.cmd.Environ(), env...)
	}
	setupProcessGroup(g.cmd)
	g.stderr = captureStderr(g.cmd, "PROCESS")
	if withStdin {
		stdin, err := g.cmd.StdinPipe()
		if err != nil {
			genCancel()
			s.cancel()
			return nil, err
		}
		s.stdin = stdin
	}
	if err := g.cmd.Start(); err != nil {
		genCancel()
		s.cancel()
		return nil, err
	}
	s.attach(g)
	return s, nil
}
