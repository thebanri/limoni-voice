package relay

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thebanri/limoni-voice/internal/protocol"
)

type testClient struct {
	t    *testing.T
	conn *websocket.Conn
	text chan protocol.Signal
	bin  chan []byte
}

func newTestServer(t *testing.T, cfg Config) (*Server, string) {
	t.Helper()
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	if cfg.GracePeriod == 0 {
		cfg.GracePeriod = 300 * time.Millisecond
	}
	s := New(cfg)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return s, "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
}

func dial(t *testing.T, url string) *testClient {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	tc := &testClient{t: t, conn: conn, text: make(chan protocol.Signal, 64), bin: make(chan []byte, 64)}
	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				close(tc.text)
				return
			}
			if mt == websocket.TextMessage {
				var sig protocol.Signal
				_ = json.Unmarshal(data, &sig)
				tc.text <- sig
			} else {
				tc.bin <- data
			}
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return tc
}

func (tc *testClient) send(sig protocol.Signal) {
	tc.t.Helper()
	data, _ := json.Marshal(sig)
	if err := tc.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		tc.t.Fatalf("send: %v", err)
	}
}

func (tc *testClient) sendFrame(class byte, payload []byte) {
	tc.t.Helper()
	if err := tc.conn.WriteMessage(websocket.BinaryMessage, append([]byte{class}, payload...)); err != nil {
		tc.t.Fatalf("send frame: %v", err)
	}
}

func (tc *testClient) expect(sigType string) protocol.Signal {
	tc.t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case sig, ok := <-tc.text:
			if !ok {
				tc.t.Fatalf("connection closed while waiting for %s", sigType)
			}
			if sig.Type == sigType {
				return sig
			}
		case <-deadline:
			tc.t.Fatalf("timed out waiting for %s", sigType)
		}
	}
}

func (tc *testClient) expectFrame() []byte {
	tc.t.Helper()
	select {
	case f := <-tc.bin:
		return f
	case <-time.After(2 * time.Second):
		tc.t.Fatal("timed out waiting for binary frame")
	}
	return nil
}

func (tc *testClient) expectNoFrame(d time.Duration) {
	tc.t.Helper()
	select {
	case f := <-tc.bin:
		tc.t.Fatalf("unexpected frame %x", f)
	case <-time.After(d):
	}
}

func host(t *testing.T, url, room, id string) (*testClient, protocol.Signal) {
	h := dial(t, url)
	h.send(protocol.Signal{Type: protocol.SigHostRoom, Proto: protocol.SignalVersion, RoomCode: room, SenderID: id, Nickname: "Alice"})
	return h, h.expect(protocol.SigRoomCreated)
}

// admitJoiner runs the full relay-side admission flow and returns the joiner and its welcome.
func admitJoiner(t *testing.T, url string, h *testClient, room, hostID, id string) (*testClient, protocol.Signal) {
	t.Helper()
	j := dial(t, url)
	j.send(protocol.Signal{Type: protocol.SigJoinRoom, Proto: protocol.SignalVersion, RoomCode: room, SenderID: id, Nickname: "Bob",
		Endpoint: protocol.Endpoint{LocalIP: "192.168.1.20", LocalPort: 50001, PublicPort: 40000, NAT: "eim"}})
	pending := j.expect(protocol.SigJoinPending)
	if pending.SenderID != hostID {
		t.Fatalf("pending must name the host, got %+v", pending)
	}
	req := h.expect(protocol.SigJoinRequest)
	if req.SenderID != id {
		t.Fatalf("join_request for wrong joiner: %+v", req)
	}

	j.send(protocol.Signal{Type: protocol.SigPake, Target: hostID, Data: []byte("hello")})
	if got := h.expect(protocol.SigPake); got.SenderID != id || string(got.Data) != "hello" {
		t.Fatalf("host got wrong pake: %+v", got)
	}
	h.send(protocol.Signal{Type: protocol.SigPake, Target: id, Data: []byte("challenge")})
	if got := j.expect(protocol.SigPake); got.SenderID != hostID || string(got.Data) != "challenge" {
		t.Fatalf("joiner got wrong pake: %+v", got)
	}

	h.send(protocol.Signal{Type: protocol.SigAdmit, Target: id})
	welcome := j.expect(protocol.SigWelcome)
	joined := h.expect(protocol.SigPeerJoined)
	if joined.SenderID != id || joined.LocalPort != 50001 || joined.NAT != "eim" {
		t.Fatalf("peer_joined missing endpoint: %+v", joined)
	}
	return j, welcome
}

func TestAdmissionFlowAndForwarding(t *testing.T) {
	_, url := newTestServer(t, Config{})
	h, created := host(t, url, "7492", "host_1")
	if created.HostToken == "" || created.MemberToken == "" || created.Proto != protocol.SignalVersion {
		t.Fatalf("room_created incomplete: %+v", created)
	}
	j, welcome := admitJoiner(t, url, h, "7492", "host_1", "joiner_1")
	if welcome.SenderID != "host_1" || welcome.Nickname != "Alice" || welcome.MemberToken == "" || len(welcome.Peers) != 1 {
		t.Fatalf("welcome incomplete: %+v", welcome)
	}

	h.sendFrame(protocol.FrameRealtime, []byte("audio-from-host"))
	if f := j.expectFrame(); f[0] != protocol.FrameRealtime || string(f[1:]) != "audio-from-host" {
		t.Fatalf("joiner got %q", f)
	}
	j.sendFrame(protocol.FrameReliable, []byte("file-chunk"))
	if f := h.expectFrame(); f[0] != protocol.FrameReliable || string(f[1:]) != "file-chunk" {
		t.Fatalf("host got %q", f)
	}
}

func TestPendingJoinerIsIsolated(t *testing.T) {
	_, url := newTestServer(t, Config{})
	h, _ := host(t, url, "1111", "host_1")
	intruder := dial(t, url)
	intruder.send(protocol.Signal{Type: protocol.SigJoinRoom, Proto: protocol.SignalVersion, RoomCode: "1111", SenderID: "intruder", Nickname: "Mallory"})
	intruder.expect(protocol.SigJoinPending)
	h.expect(protocol.SigJoinRequest)

	intruder.sendFrame(protocol.FrameRealtime, []byte("spam"))
	h.expectNoFrame(150 * time.Millisecond)
	h.sendFrame(protocol.FrameRealtime, []byte("secret-audio"))
	intruder.expectNoFrame(150 * time.Millisecond)

	// Admit is host-only.
	intruder.send(protocol.Signal{Type: protocol.SigAdmit, Target: "intruder"})
	select {
	case sig := <-intruder.text:
		if sig.Type == protocol.SigWelcome {
			t.Fatal("pending joiner admitted itself")
		}
	case <-time.After(150 * time.Millisecond):
	}

	h.send(protocol.Signal{Type: protocol.SigReject, Target: "intruder", Message: "wrong room code"})
	if rej := intruder.expect(protocol.SigRejected); rej.Message != "wrong room code" {
		t.Fatalf("unexpected reject: %+v", rej)
	}
}

func TestMemberReconnectRequiresToken(t *testing.T) {
	_, url := newTestServer(t, Config{GracePeriod: 2 * time.Second})
	h, _ := host(t, url, "2222", "host_1")
	j, welcome := admitJoiner(t, url, h, "2222", "host_1", "joiner_1")
	j.conn.Close()

	thief := dial(t, url)
	thief.send(protocol.Signal{Type: protocol.SigJoinRoom, Proto: protocol.SignalVersion, RoomCode: "2222", SenderID: "joiner_1"})
	if e := thief.expect(protocol.SigError); !strings.Contains(e.Message, "Unauthorized") {
		t.Fatalf("identity takeover not blocked: %+v", e)
	}

	back := dial(t, url)
	back.send(protocol.Signal{Type: protocol.SigJoinRoom, Proto: protocol.SignalVersion, RoomCode: "2222", SenderID: "joiner_1", MemberToken: welcome.MemberToken})
	back.expect(protocol.SigWelcome)
	h.sendFrame(protocol.FrameRealtime, []byte("after-reconnect"))
	if f := back.expectFrame(); string(f[1:]) != "after-reconnect" {
		t.Fatalf("reconnected member got %q", f)
	}
}

func TestHostReclaimAndHijackPrevention(t *testing.T) {
	_, url := newTestServer(t, Config{GracePeriod: 2 * time.Second})
	h, created := host(t, url, "3333", "host_1")
	h.conn.Close()
	time.Sleep(50 * time.Millisecond)

	attacker := dial(t, url)
	attacker.send(protocol.Signal{Type: protocol.SigHostRoom, Proto: protocol.SignalVersion, RoomCode: "3333", SenderID: "host_1"})
	if e := attacker.expect(protocol.SigError); !strings.HasPrefix(e.Message, "ROOM_IN_USE") {
		t.Fatalf("hijack not blocked: %+v", e)
	}

	back := dial(t, url)
	back.send(protocol.Signal{Type: protocol.SigHostRoom, Proto: protocol.SignalVersion, RoomCode: "3333", SenderID: "host_1", HostToken: created.HostToken})
	back.expect(protocol.SigRoomCreated)
}

func TestHostMigration(t *testing.T) {
	_, url := newTestServer(t, Config{})
	h, _ := host(t, url, "4444", "host_1")
	j1, _ := admitJoiner(t, url, h, "4444", "host_1", "joiner_1")
	j2, _ := admitJoiner(t, url, h, "4444", "host_1", "joiner_2")
	j1.expect(protocol.SigPeerJoined)

	h.send(protocol.Signal{Type: protocol.SigLeave})
	if left := j1.expect(protocol.SigPeerLeft); left.SenderID != "host_1" {
		t.Fatalf("unexpected peer_left: %+v", left)
	}
	nh1 := j1.expect(protocol.SigNewHost)
	nh2 := j2.expect(protocol.SigNewHost)
	if nh1.SenderID != "joiner_1" || nh2.SenderID != "joiner_1" {
		t.Fatalf("expected joiner_1 as new host: %+v %+v", nh1, nh2)
	}
	if nh1.HostToken == "" || nh2.HostToken != "" {
		t.Fatal("host token must only be sent to the new host")
	}
}

func TestLockedAndFullRooms(t *testing.T) {
	_, url := newTestServer(t, Config{MaxRoomMembers: 2})
	h, _ := host(t, url, "5555", "host_1")
	h.send(protocol.Signal{Type: protocol.SigLockRoom})
	time.Sleep(50 * time.Millisecond)
	locked := dial(t, url)
	locked.send(protocol.Signal{Type: protocol.SigJoinRoom, Proto: protocol.SignalVersion, RoomCode: "5555", SenderID: "x"})
	locked.expect(protocol.SigRoomLocked)

	// PIN-protected rooms defer the PIN check to the host handshake.
	h.send(protocol.Signal{Type: protocol.SigLockRoom, PinRequired: true})
	time.Sleep(50 * time.Millisecond)
	admitJoiner(t, url, h, "5555", "host_1", "joiner_1")

	full := dial(t, url)
	full.send(protocol.Signal{Type: protocol.SigJoinRoom, Proto: protocol.SignalVersion, RoomCode: "5555", SenderID: "late"})
	full.expect(protocol.SigRoomFull)
}

func TestAuthTokenAndOutdatedClient(t *testing.T) {
	_, url := newTestServer(t, Config{AuthToken: "s3cret"})
	if _, resp, err := websocket.DefaultDialer.Dial(url, nil); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %v", err)
	}
	c := dial(t, url+"?token=s3cret")
	c.send(protocol.Signal{Type: protocol.SigHostRoom, RoomCode: "6666", SenderID: "old"})
	if e := c.expect(protocol.SigError); !strings.HasPrefix(e.Message, "CLIENT_OUTDATED") {
		t.Fatalf("old client not rejected: %+v", e)
	}
}

func TestRoomCreationRateLimit(t *testing.T) {
	_, url := newTestServer(t, Config{})
	c := dial(t, url)
	for i := 0; i < 10; i++ {
		c.send(protocol.Signal{Type: protocol.SigHostRoom, Proto: protocol.SignalVersion, RoomCode: "r" + string(rune('a'+i)), SenderID: "spammer"})
		c.expect(protocol.SigRoomCreated)
	}
	c.send(protocol.Signal{Type: protocol.SigHostRoom, Proto: protocol.SignalVersion, RoomCode: "overflow", SenderID: "spammer"})
	if e := c.expect(protocol.SigError); !strings.Contains(e.Message, "Too many") {
		t.Fatalf("rate limit not enforced: %+v", e)
	}
}

func TestUDPRelayWithWebSocketFallback(t *testing.T) {
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s, url := newTestServer(t, Config{UDPPort: udp.LocalAddr().(*net.UDPAddr).Port})
	go s.ServeUDP(udp)
	t.Cleanup(func() { udp.Close() })

	h, created := host(t, url, "7777", "host_1")
	j, welcome := admitJoiner(t, url, h, "7777", "host_1", "joiner_1")
	if created.UDPToken == "" || welcome.UDPToken == "" || welcome.UDPPort == 0 {
		t.Fatalf("UDP parameters missing: %+v / %+v", created, welcome)
	}

	openUDP := func(tokenHex string) (*net.UDPConn, []byte) {
		token, _ := hex.DecodeString(tokenHex)
		conn, err := net.DialUDP("udp", nil, udp.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		if _, err := conn.Write(append(append([]byte{}, token...), protocol.UDPKindKeepalive)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if err != nil || n != 1 || buf[0] != protocol.UDPKindProbeAck {
			t.Fatalf("keepalive not acknowledged: n=%d err=%v", n, err)
		}
		return conn, token
	}

	hostUDP, hostToken := openUDP(created.UDPToken)
	joinUDP, _ := openUDP(welcome.UDPToken)

	// host -> joiner: both bound, delivered over UDP
	hostUDP.Write(append(append(append([]byte{}, hostToken...), protocol.UDPKindData), []byte("udp-audio")...))
	buf := make([]byte, 1500)
	joinUDP.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := joinUDP.Read(buf)
	if err != nil || buf[0] != protocol.UDPKindData || string(buf[1:n]) != "udp-audio" {
		t.Fatalf("UDP delivery failed: %v %q", err, buf[:n])
	}
	j.expectNoFrame(100 * time.Millisecond)

	// A member without a UDP binding receives UDP-originated traffic over WebSocket.
	w, _ := admitJoiner(t, url, h, "7777", "host_1", "ws_only")
	j.expect(protocol.SigPeerJoined)
	hostUDP.Write(append(append(append([]byte{}, hostToken...), protocol.UDPKindData), []byte("fallback")...))
	if f := w.expectFrame(); f[0] != protocol.FrameRealtime || string(f[1:]) != "fallback" {
		t.Fatalf("WebSocket fallback failed: %q", f)
	}

	// Unknown tokens are ignored without a response (no amplification).
	stranger, _ := net.DialUDP("udp", nil, udp.LocalAddr().(*net.UDPAddr))
	defer stranger.Close()
	stranger.Write(append(bytes.Repeat([]byte{9}, protocol.UDPTokenSize), protocol.UDPKindKeepalive))
	stranger.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, err := stranger.Read(buf); err == nil {
		t.Fatal("relay answered an unknown token")
	}
}

func TestVideoDoesNotStarveAudio(t *testing.T) {
	_, url := newTestServer(t, Config{})
	h, _ := host(t, url, "8888", "host_1")
	j, _ := admitJoiner(t, url, h, "8888", "host_1", "joiner_1")

	chunk := bytes.Repeat([]byte{0xAB}, 1300)
	for i := 0; i < 400; i++ {
		h.sendFrame(protocol.FrameBulk, chunk)
	}
	h.sendFrame(protocol.FrameRealtime, []byte("ping"))

	deadline := time.After(3 * time.Second)
	for {
		select {
		case f := <-j.bin:
			if f[0] == protocol.FrameRealtime {
				return
			}
		case <-deadline:
			t.Fatal("realtime frame never delivered behind video burst")
		}
	}
}

func TestMetricsEndpoint(t *testing.T) {
	s := New(Config{})
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, name := range []string{"limoni_relay_rooms", "limoni_relay_udp_packets_in_total", "limoni_relay_rate_limited_total"} {
		if !strings.Contains(string(body), name) {
			t.Fatalf("metric %s missing", name)
		}
	}
}
