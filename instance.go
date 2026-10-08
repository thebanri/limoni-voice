package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Invite links and a running app.
//
// The URL handler starts a new process for every invite link it is given. When Limoni Voice
// was already open, that made a second copy on the same machine: two windows, two
// microphones, and the copy hosting a room it was left in announcing that room on the LAN
// ports the other copy listens on. So the open app listens on a socket only its user can
// reach, and a process started for an invite link hands the link to it and exits. The open
// app treats the link like one it was started with: it asks before joining.

// inviteSocketName is the socket's file name in the user's runtime or temporary directory.
const inviteSocketName = "limoni-voice.sock"

// maxForwardedInvite bounds what the app reads from the socket: an invite link is far shorter.
const maxForwardedInvite = 4096

// inviteSocketPath returns where the open app listens for invite links. The directory is the
// user's own (XDG_RUNTIME_DIR, macOS's per-user TMPDIR, Windows' %TEMP%); in a shared /tmp
// the name carries the user ID.
func inviteSocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, inviteSocketName)
	}
	dir := os.TempDir()
	if uid := os.Getuid(); uid >= 0 && dir == "/tmp" {
		return filepath.Join(dir, "limoni-voice-"+strconv.Itoa(uid)+".sock")
	}
	return filepath.Join(dir, inviteSocketName)
}

// forwardInvite hands an invite link to the open app at path. It reports whether that app
// took it; false means none is listening, or it did not answer, and this process goes on
// as the app.
func forwardInvite(path, link string) bool {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, strings.TrimSpace(link)+"\n"); err != nil {
		return false
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	return err == nil && strings.TrimSpace(reply) == "ok"
}

// listenForInvites starts taking invite links from processes started for one, handing each
// to deliver. It returns nil when another copy of the app already listens, or the platform
// has no such sockets: invite links then open a new copy, as they did before.
func listenForInvites(path string, deliver func(link string)) io.Closer {
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		conn.Close()
		return nil // an earlier copy owns the socket
	}
	_ = os.Remove(path) // left behind by a copy that did not close
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(true)
	}
	_ = os.Chmod(path, 0o600)
	go func() {
		for {
			conn, err := ln.Accept()
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			go serveInvite(conn, deliver)
		}
	}()
	return ln
}

func serveInvite(conn net.Conn, deliver func(link string)) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(conn, maxForwardedInvite)).ReadString('\n')
	if err != nil {
		return
	}
	if link := strings.TrimSpace(line); link != "" {
		deliver(link)
	}
	_, _ = io.WriteString(conn, "ok\n")
}
