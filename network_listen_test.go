package main

import (
	"errors"
	"net"
	"testing"
)

// The receive loop must survive a transient socket error: nothing restarts it, so a loop that
// returns leaves the node deaf until the app is restarted.
func TestReadErrorsEndTheLoopOnlyWhenTheSocketIsClosed(t *testing.T) {
	n := testRoomNode("reader", "4242")
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000}
	failures := 0

	if !n.readErrorRecoverable(&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, addr, &failures) {
		t.Fatal("a transient read error ended the listen loop")
	}
	if failures != 1 {
		t.Fatalf("failure count %d, want 1", failures)
	}
	if n.readErrorRecoverable(net.ErrClosed, addr, &failures) {
		t.Fatal("a closed socket did not end the listen loop")
	}
}
