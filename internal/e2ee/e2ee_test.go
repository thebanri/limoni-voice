package e2ee

import (
	"bytes"
	"strings"
	"testing"
)

func TestWordListUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, w := range Words {
		if w == "" || strings.ContainsAny(w, "- ") {
			t.Fatalf("invalid word %q", w)
		}
		if seen[w] {
			t.Fatalf("duplicate word %q", w)
		}
		seen[w] = true
	}
}

func TestRoomCodeSplit(t *testing.T) {
	code := GenerateRoomCode()
	if !IsStrongCode(code) {
		t.Fatalf("generated code not strong: %s", code)
	}
	id, secret := SplitRoomCode("  " + strings.ToUpper(code) + " ")
	if len(id) != 4 || !strings.HasPrefix(code, id+"-") || secret != code {
		t.Fatalf("split mismatch: id=%q secret=%q code=%q", id, secret, code)
	}
	cid, csecret := SplitRoomCode("Test-Room")
	if !strings.HasPrefix(cid, "x") || strings.Contains(cid, "test") || csecret != "test-room" {
		t.Fatalf("custom code split leaked secret: %q %q", cid, csecret)
	}
}

func handshake(t *testing.T, hostSecret, joinSecret, pin string) (*JoinClient, *JoinServer, []byte, error) {
	t.Helper()
	const roomID, hostID, joinerID = "7492", "host_1", "joiner_1"
	client, hello, err := NewJoinClient(roomID, joinSecret, joinerID)
	if err != nil {
		t.Fatal(err)
	}
	server, challenge, err := AcceptJoin(roomID, hostSecret, hostID, joinerID, hello)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := client.HandleChallenge(challenge, hostID, pin, "Bob")
	return client, server, auth, err
}

func TestHandshakeSuccess(t *testing.T) {
	client, server, auth, err := handshake(t, "7492-amber-falcon-river", "7492-amber-falcon-river", "1234")
	if err != nil {
		t.Fatalf("challenge rejected: %v", err)
	}
	pin, nick, err := server.VerifyAuth(auth)
	if err != nil || pin != "1234" || nick != "Bob" {
		t.Fatalf("auth failed: %v %q %q", err, pin, nick)
	}
	gk := NewGroupKey()
	result, err := server.Result(StatusOK, 3, gk)
	if err != nil {
		t.Fatal(err)
	}
	status, epoch, key, err := client.HandleResult(result)
	if err != nil || status != StatusOK || epoch != 3 || key != gk {
		t.Fatalf("result mismatch: %v status=%d epoch=%d", err, status, epoch)
	}
}

func TestHandshakeWrongCode(t *testing.T) {
	_, server, _, err := handshake(t, "7492-amber-falcon-river", "7492-amber-falcon-rain", "")
	if err != ErrWrongCode {
		t.Fatalf("joiner must detect wrong code, got %v", err)
	}
	if _, err := server.Result(StatusOK, 1, NewGroupKey()); err == nil {
		t.Fatal("host released key without verification")
	}
	// A joiner that ignores the failed confirmation still cannot pass host verification.
	if _, _, err := server.VerifyAuth(bytes.Repeat([]byte{7}, 80)); err != ErrWrongCode {
		t.Fatalf("forged auth accepted: %v", err)
	}
}

func TestKeyringEpochs(t *testing.T) {
	k1 := NewGroupKey()
	ring, err := NewKeyring(1, k1)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := ring.Seal([]byte("epoch1"))
	k2 := NewGroupKey()
	if err := ring.Stage(2, k2); err != nil {
		t.Fatal(err)
	}
	peer, _ := NewKeyring(2, k2)
	staged, _ := peer.Seal([]byte("epoch2"))
	if pt, err := ring.Open(staged); err != nil || string(pt) != "epoch2" {
		t.Fatalf("staged key not accepted: %v", err)
	}
	ring.Promote()
	if e, _ := ring.Current(); e != 2 {
		t.Fatalf("expected epoch 2, got %d", e)
	}
	if pt, err := ring.Open(old); err != nil || string(pt) != "epoch1" {
		t.Fatalf("previous key not accepted during grace: %v", err)
	}
	other, _ := NewKeyring(1, NewGroupKey())
	foreign, _ := other.Seal([]byte("x"))
	if _, err := ring.Open(foreign); err == nil {
		t.Fatal("foreign key accepted")
	}
	a, _ := ring.Seal([]byte("same"))
	b, _ := ring.Seal([]byte("same"))
	if bytes.Equal(a[:NonceSize], b[:NonceSize]) {
		t.Fatal("nonce reuse")
	}
}
