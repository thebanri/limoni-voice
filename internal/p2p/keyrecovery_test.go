package p2p

import (
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/e2ee"
)

// promoteHostKey moves the host to a fresh group key without telling anyone, which is where a
// member ends up when it misses every delivery of a rekey: the host promotes the new key
// regardless, and from then on neither side can read the other.
func promoteHostKey(t *testing.T, host *P2PNode) uint32 {
	t.Helper()
	host.mu.Lock()
	defer host.mu.Unlock()
	epoch, _ := host.keyring.Current()
	if err := host.keyring.Stage(epoch+1, e2ee.NewGroupKey()); err != nil {
		t.Fatal(err)
	}
	host.keyring.Promote()
	return epoch + 1
}

func currentEpoch(n *P2PNode) uint32 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	epoch, _ := n.keyring.Current()
	return epoch
}

// A member that missed a group key rotation used to stay in the room unable to read anything,
// showing everyone as reconnecting until it was restarted. It now asks the host for the key.
func TestMemberRecoversAfterMissingARekey(t *testing.T) {
	relayURL, _ := startTestRelay(t)
	host := newRelayNode(t, "recover_host", "Alice", relayURL)
	joiner := newRelayNode(t, "recover_joiner", "Bob", relayURL)

	var hostChat, joinerChat chatSink
	host.OnChatMessage = hostChat.add
	joiner.OnChatMessage = joinerChat.add

	code := "4242-amber-falcon-river"
	host.HostRoom(code)
	waitFor(t, "host registered on relay", 3*time.Second, func() bool {
		host.mu.RLock()
		defer host.mu.RUnlock()
		return host.hostToken != ""
	})
	if res := joinAndWait(t, joiner, code, 5*time.Second); !res.ok {
		t.Fatalf("join failed: %+v", res)
	}
	waitFor(t, "host sees joiner", 3*time.Second, func() bool { return host.GetPeer("recover_joiner") != nil })

	epoch := promoteHostKey(t, host)
	if currentEpoch(joiner) >= epoch {
		t.Fatal("the joiner was not left behind by the rotation")
	}

	// The host keeps talking, the joiner cannot read it, asks for the key and catches up.
	waitFor(t, "the joiner recovers the group key", 15*time.Second, func() bool {
		host.SendChatMessage("hala buradayim")
		time.Sleep(200 * time.Millisecond)
		return currentEpoch(joiner) == epoch
	})

	// Both directions work again.
	joiner.SendChatMessage("geri dondum")
	host.SendChatMessage("tekrar hos geldin")
	waitFor(t, "chat flows after the recovery", 5*time.Second, func() bool {
		return hostChat.has("geri dondum") && joinerChat.has("tekrar hos geldin")
	})
}

// The host answers a key request only for a member it knows, and not more than once every
// couple of seconds, so the plaintext request cannot be used to make it work.
func TestKeyRequestIsAnsweredOncePerMemberAndOnlyForMembers(t *testing.T) {
	bob := rekeyMember("bob", "host")
	host := rekeyHost("host", bob)
	host.IsConnected = true

	host.mu.Lock()
	_, epoch, ok := host.keyGrantFrameLocked("bob")
	host.mu.Unlock()
	if !ok || epoch == 0 {
		t.Fatal("the host refused to answer a member it knows")
	}

	host.mu.Lock()
	_, _, again := host.keyGrantFrameLocked("bob")
	_, _, stranger := host.keyGrantFrameLocked("mallory")
	host.mu.Unlock()
	if again {
		t.Fatal("the host answered the same member twice in a row")
	}
	if stranger {
		t.Fatal("the host handed a group key to a member it does not know")
	}

	// A member takes up the key from the frame the host built.
	promoteHostKey(t, host)
	host.mu.Lock()
	delete(host.keyAnswers, "bob")
	frame, epoch, _ := host.keyGrantFrameLocked("bob")
	host.mu.Unlock()
	if !bob.handleKeyFrame(frame, nil) {
		t.Fatal("the key grant frame was not recognised")
	}
	if got := currentEpoch(bob); got != epoch {
		t.Fatalf("member is on epoch %d, want the host's %d", got, epoch)
	}

	// A grant sealed for somebody else is refused.
	mallory := rekeyMember("mallory", "host")
	mallory.memberKeys["host"] = host.identity.Public()
	mallory.handleKeyFrame(frame, nil)
	if currentEpoch(mallory) == epoch {
		t.Fatal("a member installed a group key sealed for another member")
	}
}

// A frame from another room is not ours, and a request with no room is ignored.
func TestKeyFrameRequiresOurRoom(t *testing.T) {
	bob := rekeyMember("bob", "host")
	other := testRoomNode("carol", "9999")
	other.identity = e2ee.NewIdentity()

	bob.mu.RLock()
	frame := bob.lanFrameLocked(hsKeyRequest, "bob", nil)
	bob.mu.RUnlock()
	if other.handleKeyFrame(frame, nil) {
		t.Fatal("a frame tagged for another room was accepted")
	}
}
