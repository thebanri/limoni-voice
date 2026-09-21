package e2ee

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Member identity keys and per-member key grants.
//
// Every member creates an X25519 identity for each room session. A joiner's public key
// travels inside the CPace-sealed auth message; the host answers with its own key and the
// keys of the other members inside the sealed result, and every later rekey carries the
// current list. Member keys are therefore vouched for by a host the member already
// authenticated with the room secret, never by the relay.
//
// Rekeys are sealed separately for each member with a key derived from X25519(host, member).
// A member that left still holds old group keys but cannot read the new one, and no member
// can forge a rekey for another: only the host and the recipient know their pairwise key.
// Because every member knows every other member's key, a newly elected host can rekey the
// room the same way after the original host leaves.

// PublicKeySize is the length of a member identity public key (X25519).
const PublicKeySize = 32

// PublicKey is a member's identity public key.
type PublicKey [PublicKeySize]byte

// MemberKey pairs a member ID with its identity public key.
type MemberKey struct {
	ID  string
	Key PublicKey
}

// KeyGrant is a group key handed to one member, with the room's member keys at that time.
type KeyGrant struct {
	Epoch   uint32
	Key     GroupKey
	Members []MemberKey
}

// maxGrantMembers bounds the member list of a grant (rooms hold at most a handful of members).
const maxGrantMembers = 32

// ErrOutdatedPeer means the other side runs a version without member identity keys.
var ErrOutdatedPeer = errors.New("e2ee: peer runs an outdated protocol version")

const rekeyLabel = "limoni-voice rekey v1"

// Identity is a member's X25519 identity for one room session.
type Identity struct {
	priv *ecdh.PrivateKey
}

// NewIdentity returns a fresh random identity.
func NewIdentity() *Identity {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return &Identity{priv: priv}
}

// Public returns the identity public key.
func (id *Identity) Public() PublicKey {
	var p PublicKey
	copy(p[:], id.priv.PublicKey().Bytes())
	return p
}

// pairAEAD derives the host→member grant key. Both sides pass the same room, host and member
// IDs and public keys, so the key is bound to one direction of one pair in one room.
func (id *Identity) pairAEAD(roomID, hostID, memberID string, hostPub, memberPub, peer PublicKey) (cipher.AEAD, error) {
	pub, err := ecdh.X25519().NewPublicKey(peer[:])
	if err != nil {
		return nil, err
	}
	secret, err := id.priv.ECDH(pub)
	if err != nil {
		return nil, err
	}
	var info []byte
	for _, part := range [][]byte{[]byte(rekeyLabel), []byte(roomID), []byte(hostID), []byte(memberID), hostPub[:], memberPub[:]} {
		info = binary.BigEndian.AppendUint32(info, uint32(len(part)))
		info = append(info, part...)
	}
	key, err := hkdf.Key(sha256.New, secret, nil, string(info), KeySize)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SealGrant seals a grant from this identity (the host) to one member.
func (id *Identity) SealGrant(roomID, hostID, memberID string, member PublicKey, g KeyGrant) ([]byte, error) {
	aead, err := id.pairAEAD(roomID, hostID, memberID, id.Public(), member, member)
	if err != nil {
		return nil, err
	}
	body, err := g.marshal()
	if err != nil {
		return nil, err
	}
	return sealWith(aead, body)
}

// OpenGrant opens a grant the host sealed for this identity (the member).
func (id *Identity) OpenGrant(roomID, hostID, memberID string, host PublicKey, sealed []byte) (KeyGrant, error) {
	aead, err := id.pairAEAD(roomID, hostID, memberID, host, id.Public(), host)
	if err != nil {
		return KeyGrant{}, err
	}
	body, err := openWith(aead, sealed)
	if err != nil {
		return KeyGrant{}, err
	}
	return unmarshalGrant(body)
}

// Grant layout: epoch (4) || group key (32) || member count (1) || count × (LV id || key (32)).
// Grants from versions before member keys end after the group key.
func (g KeyGrant) marshal() ([]byte, error) {
	if len(g.Members) > maxGrantMembers {
		return nil, errors.New("e2ee: too many members in grant")
	}
	b := make([]byte, 4, 4+KeySize+1+len(g.Members)*(1+16+PublicKeySize))
	binary.BigEndian.PutUint32(b, g.Epoch)
	b = append(b, g.Key[:]...)
	b = append(b, byte(len(g.Members)))
	for _, m := range g.Members {
		if m.ID == "" || len(m.ID) > 255 {
			return nil, errors.New("e2ee: invalid member ID in grant")
		}
		b = appendLV(b, []byte(m.ID))
		b = append(b, m.Key[:]...)
	}
	return b, nil
}

func unmarshalGrant(b []byte) (KeyGrant, error) {
	var g KeyGrant
	if len(b) == 4+KeySize {
		return g, ErrOutdatedPeer
	}
	if len(b) < 4+KeySize+1 {
		return g, ErrMalformed
	}
	g.Epoch = binary.BigEndian.Uint32(b[:4])
	copy(g.Key[:], b[4:4+KeySize])
	count := int(b[4+KeySize])
	rest := b[4+KeySize+1:]
	if count > maxGrantMembers {
		return g, ErrMalformed
	}
	g.Members = make([]MemberKey, 0, count)
	for range count {
		idB, r, ok := readLV(rest)
		if !ok || len(idB) == 0 || len(r) < PublicKeySize {
			return KeyGrant{}, ErrMalformed
		}
		var m MemberKey
		m.ID = string(idB)
		copy(m.Key[:], r[:PublicKeySize])
		g.Members = append(g.Members, m)
		rest = r[PublicKeySize:]
	}
	if len(rest) != 0 {
		return KeyGrant{}, ErrMalformed
	}
	return g, nil
}
