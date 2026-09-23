package main

import (
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/p2p"
)

// Requests the host leaves unanswered are handed back once their window has passed.
func TestKnockQueueExpiry(t *testing.T) {
	var q knockQueue
	now := time.Now()
	q.add("a", "Ann", now.Add(-p2p.KnockWindow-time.Second))
	q.add("b", "Bob", now)
	if q.add("b", "Bob", now) {
		t.Fatal("duplicate knock queued")
	}
	active, expired := q.front(now)
	if active == nil || active.id != "b" || len(expired) != 1 || expired[0].id != "a" {
		t.Fatalf("active=%v expired=%v", active, expired)
	}
	if r, ok := q.pop(); !ok || r.id != "b" {
		t.Fatal("pop did not return the waiting request")
	}
	if _, ok := q.pop(); ok {
		t.Fatal("queue not empty")
	}
}
