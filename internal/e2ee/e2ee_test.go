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
	return handshakeAs(t, hostSecret, joinSecret, pin, NewIdentity().Public())
}

func handshakeAs(t *testing.T, hostSecret, joinSecret, pin string, joinerKey PublicKey) (*JoinClient, *JoinServer, []byte, error) {
	t.Helper()
	const roomID, hostID, joinerID = "7492", "host_1", "joiner_1"
	client, hello, err := NewJoinClient(roomID, joinSecret, joinerID, joinerKey)
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
	joiner := NewIdentity()
	client, server, auth, err := handshakeAs(t, "7492-amber-falcon-river", "7492-amber-falcon-river", "1234", joiner.Public())
	if err != nil {
		t.Fatalf("challenge rejected: %v", err)
	}
	ja, err := server.VerifyAuth(auth)
	if err != nil || ja.PIN != "1234" || ja.Nickname != "Bob" || !ja.HasKey || ja.Identity != joiner.Public() {
		t.Fatalf("auth failed: %v %+v", err, ja)
	}
	host := NewIdentity()
	sent := KeyGrant{Epoch: 3, Key: NewGroupKey(), Members: []MemberKey{{ID: "host_1", Key: host.Public()}, {ID: "carol", Key: NewIdentity().Public()}}}
	result, err := server.Result(StatusOK, sent)
	if err != nil {
		t.Fatal(err)
	}
	status, got, err := client.HandleResult(result)
	if err != nil || status != StatusOK || got.Epoch != 3 || got.Key != sent.Key {
		t.Fatalf("result mismatch: %v status=%d grant=%+v", err, status, got)
	}
	if len(got.Members) != 2 || got.Members[0] != sent.Members[0] || got.Members[1] != sent.Members[1] {
		t.Fatalf("member keys not delivered: %+v", got.Members)
	}
}

// An older joiner sends no identity key and an older host sends no member keys: both are
// detected instead of silently producing a member that can never receive a rekey.
func TestHandshakeOutdatedPeers(t *testing.T) {
	_, server, auth, err := handshake(t, "7492-amber-falcon-river", "7492-amber-falcon-river", "")
	if err != nil {
		t.Fatal(err)
	}
	// Strip the identity key from the sealed auth by re-sealing the v2 body.
	pt, err := openWith(server.keys.wrap, auth[confirmSize:])
	if err != nil {
		t.Fatal(err)
	}
	oldBody := pt[:len(pt)-PublicKeySize]
	oldSealed, err := sealWith(server.keys.wrap, oldBody)
	if err != nil {
		t.Fatal(err)
	}
	ja, err := server.VerifyAuth(append(append([]byte{}, auth[:confirmSize]...), oldSealed...))
	if err != nil || ja.HasKey {
		t.Fatalf("old joiner auth: err=%v hasKey=%v", err, ja.HasKey)
	}

	client, server2, auth2, err := handshake(t, "7492-amber-falcon-river", "7492-amber-falcon-river", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server2.VerifyAuth(auth2); err != nil {
		t.Fatal(err)
	}
	gk := NewGroupKey()
	oldResult, err := sealWith(server2.keys.wrap, append([]byte{0, 0, 0, 1}, gk[:]...))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.HandleResult(append([]byte{StatusOK}, oldResult...)); err != ErrOutdatedPeer {
		t.Fatalf("old host result: want ErrOutdatedPeer, got %v", err)
	}
}

func TestHandshakeWrongCode(t *testing.T) {
	_, server, _, err := handshake(t, "7492-amber-falcon-river", "7492-amber-falcon-rain", "")
	if err != ErrWrongCode {
		t.Fatalf("joiner must detect wrong code, got %v", err)
	}
	if _, err := server.Result(StatusOK, KeyGrant{Epoch: 1, Key: NewGroupKey()}); err == nil {
		t.Fatal("host released key without verification")
	}
	// A joiner that ignores the failed confirmation still cannot pass host verification.
	if _, err := server.VerifyAuth(bytes.Repeat([]byte{7}, 80)); err != ErrWrongCode {
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
