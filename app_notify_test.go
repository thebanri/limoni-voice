package main

import (
	"errors"
	"sync"
	"testing"
)

type sentNotes struct {
	mu    sync.Mutex
	notes []string
}

func (s *sentNotes) send(title, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notes = append(s.notes, title+": "+body)
	return nil
}

func (s *sentNotes) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.notes)
}

func TestDesktopNotifier(t *testing.T) {
	var sent sentNotes
	d := newDesktopNotifier()
	d.send = sent.send

	if d.Notify("Bob", "hi") {
		t.Fatal("notified while the terminal never reported losing focus")
	}
	d.SetFocused(false)
	if !d.Notify("Bob", "hi") {
		t.Fatal("no notification while in the background")
	}
	if d.Notify("Bob", "again") {
		t.Fatal("burst not held back")
	}
	if !d.NotifyNow("Limoni Voice", "Bob wants to send you a file") {
		t.Fatal("file offer held back by the burst limit")
	}
	d.SetFocused(true)
	if d.NotifyNow("Limoni Voice", "x") {
		t.Fatal("notified while focused")
	}
	d.SetFocused(false)
	d.enabled.Store(false)
	if d.NotifyNow("Limoni Voice", "x") {
		t.Fatal("notified while disabled")
	}
	d.pending.Wait()
	if n := sent.count(); n != 2 {
		t.Fatalf("sent %d notifications, want 2: %v", n, sent.notes)
	}
}

func TestDesktopNotifierFailureIsLoggedOnce(t *testing.T) {
	d := newDesktopNotifier()
	calls := 0
	var mu sync.Mutex
	d.send = func(string, string) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return errors.New("no notification daemon")
	}
	d.SetFocused(false)
	d.NotifyNow("a", "b")
	d.NotifyNow("a", "c")
	d.pending.Wait()
	if !d.warned || calls != 2 {
		t.Fatalf("warned=%v calls=%d", d.warned, calls)
	}
}
