package e2ee

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

// KeySize is the group key length (AES-256).
const KeySize = 32

// NonceSize is the AEAD nonce length prefixed to every sealed packet.
const NonceSize = 12

// Overhead is the number of bytes Seal adds to a plaintext.
const Overhead = NonceSize + 16

// GroupKey is the symmetric room key shared by all admitted members.
type GroupKey [KeySize]byte

// NewGroupKey returns a fresh random group key.
func NewGroupKey() GroupKey {
	var k GroupKey
	if _, err := rand.Read(k[:]); err != nil {
		panic(err)
	}
	return k
}

var (
	ErrNoKey      = errors.New("e2ee: no group key installed")
	ErrShort      = errors.New("e2ee: sealed packet too short")
	ErrAuthFailed = errors.New("e2ee: authentication failed")
)

type epochKey struct {
	epoch uint32
	key   GroupKey
	aead  cipher.AEAD
}

func newEpochKey(epoch uint32, key GroupKey) (*epochKey, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &epochKey{epoch: epoch, key: key, aead: aead}, nil
}

// PreviousKeyGrace is how long packets sealed with a superseded key are still accepted.
const PreviousKeyGrace = 15 * time.Second

// Keyring holds the active group key plus the previous (grace period) and a staged next key.
// Seal always uses the active key; Open accepts active, staged and previous keys.
type Keyring struct {
	mu        sync.RWMutex
	cur       *epochKey
	next      *epochKey
	prev      *epochKey
	prevUntil time.Time
}

// NewKeyring returns a keyring with key installed as the active epoch.
func NewKeyring(epoch uint32, key GroupKey) (*Keyring, error) {
	ek, err := newEpochKey(epoch, key)
	if err != nil {
		return nil, err
	}
	return &Keyring{cur: ek}, nil
}

// Epoch returns the active epoch and key.
func (k *Keyring) Current() (uint32, GroupKey) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.cur == nil {
		return 0, GroupKey{}
	}
	return k.cur.epoch, k.cur.key
}

// Stage installs a key for a future epoch: it is accepted for decryption immediately and
// becomes active on Promote. Staging an epoch that is not newer than the active one is ignored.
func (k *Keyring) Stage(epoch uint32, key GroupKey) error {
	ek, err := newEpochKey(epoch, key)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cur != nil && epoch <= k.cur.epoch {
		return nil
	}
	if k.next != nil && k.next.epoch > epoch {
		return nil
	}
	k.next = ek
	return nil
}

// StagedEpoch returns the staged epoch, if any.
func (k *Keyring) StagedEpoch() (uint32, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.next == nil {
		return 0, false
	}
	return k.next.epoch, true
}

// Promote makes the staged key active, keeping the old one for PreviousKeyGrace.
func (k *Keyring) Promote() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.next == nil {
		return false
	}
	k.prev = k.cur
	k.prevUntil = time.Now().Add(PreviousKeyGrace)
	k.cur = k.next
	k.next = nil
	return true
}

// Seal encrypts plaintext with the active key: [nonce][ciphertext+tag].
func (k *Keyring) Seal(plaintext []byte) ([]byte, error) {
	k.mu.RLock()
	cur := k.cur
	k.mu.RUnlock()
	if cur == nil {
		return nil, ErrNoKey
	}
	return sealWith(cur.aead, plaintext)
}

// Open authenticates and decrypts a sealed packet.
func (k *Keyring) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < Overhead {
		return nil, ErrShort
	}
	k.mu.RLock()
	candidates := [3]*epochKey{k.cur, k.next, nil}
	if k.prev != nil && time.Now().Before(k.prevUntil) {
		candidates[2] = k.prev
	}
	k.mu.RUnlock()
	for _, ek := range candidates {
		if ek == nil {
			continue
		}
		if pt, err := ek.aead.Open(nil, sealed[:NonceSize], sealed[NonceSize:], nil); err == nil {
			return pt, nil
		}
	}
	return nil, ErrAuthFailed
}

func sealWith(aead cipher.AEAD, plaintext []byte) ([]byte, error) {
	out := make([]byte, NonceSize, NonceSize+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return aead.Seal(out, out[:NonceSize], plaintext, nil), nil
}

func openWith(aead cipher.AEAD, sealed []byte) ([]byte, error) {
	if len(sealed) < NonceSize+aead.Overhead() {
		return nil, ErrShort
	}
	pt, err := aead.Open(nil, sealed[:NonceSize], sealed[NonceSize:], nil)
	if err != nil {
		return nil, ErrAuthFailed
	}
	return pt, nil
}
