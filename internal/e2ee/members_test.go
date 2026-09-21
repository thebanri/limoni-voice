package e2ee

import "testing"

const testRoom = "7492"

func testGrant(epoch uint32, members ...MemberKey) KeyGrant {
	return KeyGrant{Epoch: epoch, Key: NewGroupKey(), Members: members}
}

func TestGrantRoundTrip(t *testing.T) {
	host, bob := NewIdentity(), NewIdentity()
	sent := testGrant(7, MemberKey{ID: "host", Key: host.Public()}, MemberKey{ID: "bob", Key: bob.Public()})
	sealed, err := host.SealGrant(testRoom, "host", "bob", bob.Public(), sent)
	if err != nil {
		t.Fatal(err)
	}
	got, err := bob.OpenGrant(testRoom, "host", "bob", host.Public(), sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Epoch != 7 || got.Key != sent.Key || len(got.Members) != 2 || got.Members[1] != sent.Members[1] {
		t.Fatalf("grant mismatch: %+v", got)
	}
}

// A member that left keeps its identity and every old group key, and may see all traffic
// (relay operator, same LAN). It still cannot open a grant sealed for anyone else.
func TestDepartedMemberCannotOpenGrant(t *testing.T) {
	host, bob, eve := NewIdentity(), NewIdentity(), NewIdentity()
	sealed, err := host.SealGrant(testRoom, "host", "bob", bob.Public(), testGrant(2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eve.OpenGrant(testRoom, "host", "bob", host.Public(), sealed); err == nil {
		t.Fatal("departed member opened a grant sealed for bob")
	}
	if _, err := eve.OpenGrant(testRoom, "host", "eve", host.Public(), sealed); err == nil {
		t.Fatal("departed member opened a grant by claiming to be its recipient")
	}
}

// A member knows the group key and every member's public key, but not the host's private
// key, so it cannot produce a grant another member accepts as coming from the host.
func TestMemberCannotForgeHostGrant(t *testing.T) {
	host, bob, mallory := NewIdentity(), NewIdentity(), NewIdentity()
	forged, err := mallory.SealGrant(testRoom, "host", "bob", bob.Public(), testGrant(1<<30))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.OpenGrant(testRoom, "host", "bob", host.Public(), forged); err == nil {
		t.Fatal("member forged a host grant")
	}
}

func TestGrantBoundToRoomAndDirection(t *testing.T) {
	host, bob := NewIdentity(), NewIdentity()
	sealed, err := host.SealGrant(testRoom, "host", "bob", bob.Public(), testGrant(2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.OpenGrant("9999", "host", "bob", host.Public(), sealed); err == nil {
		t.Fatal("grant accepted in another room")
	}
	// Same pair with roles swapped derives a different key.
	if _, err := host.OpenGrant(testRoom, "bob", "host", bob.Public(), sealed); err == nil {
		t.Fatal("grant accepted with host and member roles swapped")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := bob.OpenGrant(testRoom, "host", "bob", host.Public(), sealed); err == nil {
		t.Fatal("tampered grant accepted")
	}
}

func TestGrantMalformed(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		make([]byte, 4+KeySize),              // pre-identity grant (ErrOutdatedPeer)
		append(make([]byte, 4+KeySize), 1),   // claims a member, has none
		append(make([]byte, 4+KeySize), 200), // too many members
		append(make([]byte, 4+KeySize+1), 9), // trailing bytes
	} {
		if _, err := unmarshalGrant(b); err == nil {
			t.Fatalf("malformed grant accepted: %x", b)
		}
	}
}
