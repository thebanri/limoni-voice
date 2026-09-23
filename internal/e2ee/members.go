package e2ee

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
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
	Vouch   []MemberTag // join results only: the host's vouch for the joiner, per member
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

// Grant layout: epoch (4) || group key (32) || member count (1) || count × (LV id || key (32))
// || vouch tag count (1) || count × (LV id || tag (32)).
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
	return appendTags(b, g.Vouch)
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
	tags, rest, err := readTags(rest)
	if err != nil || len(rest) != 0 {
		return KeyGrant{}, ErrMalformed
	}
	g.Vouch = tags
	return g, nil
}

// Pairwise MACs.
//
// Two members derive a symmetric MAC key from X25519 of their identities. It authenticates
// statements one member makes to another about itself or a third member, which the group
// key cannot do: every member holds the group key, so it only proves room membership.

const (
	vouchLabel = "limoni-voice vouch v1"
	leaveLabel = "limoni-voice leave v1"
	kickLabel  = "limoni-voice kick v1"
)

// TagSize is the length of a pairwise MAC tag.
const TagSize = sha256.Size

func (id *Identity) pairMACKey(roomID, selfID, peerID string, peer PublicKey, label string) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(peer[:])
	if err != nil {
		return nil, err
	}
	secret, err := id.priv.ECDH(pub)
	if err != nil {
		return nil, err
	}
	// Order both sides identically so they derive the same key.
	lowID, highID, lowPub, highPub := selfID, peerID, id.Public(), peer
	if peerID < selfID {
		lowID, highID, lowPub, highPub = peerID, selfID, peer, id.Public()
	}
	var info []byte
	for _, part := range [][]byte{[]byte(label), []byte(roomID), []byte(lowID), []byte(highID), lowPub[:], highPub[:]} {
		info = binary.BigEndian.AppendUint32(info, uint32(len(part)))
		info = append(info, part...)
	}
	return hkdf.Key(sha256.New, secret, nil, string(info), sha256.Size)
}

func (id *Identity) pairMAC(roomID, selfID, peerID string, peer PublicKey, label string, msg []byte) ([]byte, error) {
	key, err := id.pairMACKey(roomID, selfID, peerID, peer, label)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil), nil
}

// MemberTag is a pairwise MAC addressed to one member.
type MemberTag struct {
	MemberID string
	Tag      [TagSize]byte
}

func appendTags(b []byte, tags []MemberTag) ([]byte, error) {
	if len(tags) > maxGrantMembers {
		return nil, errors.New("e2ee: too many tags")
	}
	b = append(b, byte(len(tags)))
	for _, t := range tags {
		if t.MemberID == "" || len(t.MemberID) > 255 {
			return nil, errors.New("e2ee: invalid member ID in tag")
		}
		b = appendLV(b, []byte(t.MemberID))
		b = append(b, t.Tag[:]...)
	}
	return b, nil
}

func readTags(b []byte) ([]MemberTag, []byte, error) {
	if len(b) < 1 || int(b[0]) > maxGrantMembers {
		return nil, nil, ErrMalformed
	}
	count := int(b[0])
	rest := b[1:]
	tags := make([]MemberTag, 0, count)
	for range count {
		idB, r, ok := readLV(rest)
		if !ok || len(idB) == 0 || len(r) < TagSize {
			return nil, nil, ErrMalformed
		}
		var t MemberTag
		t.MemberID = string(idB)
		copy(t.Tag[:], r[:TagSize])
		tags = append(tags, t)
		rest = r[TagSize:]
	}
	return tags, rest, nil
}

func findTag(tags []MemberTag, memberID string) ([]byte, bool) {
	for _, t := range tags {
		if t.MemberID == memberID {
			return t.Tag[:], true
		}
	}
	return nil, false
}

// Vouch lets a newly admitted member prove to each existing member that the host that
// admitted it vouched for its identity key. The joiner presents it itself, so existing
// members learn its key even if the host leaves before its next rekey reaches them.
type Vouch struct {
	HostID string
	Key    PublicKey // the joiner's identity key
	Tags   []MemberTag
}

func vouchMessage(subjectID string, subject PublicKey) []byte {
	return append(appendLV(nil, []byte(subjectID)), subject[:]...)
}

// VouchTags returns the host's tags vouching subject's key to each of members.
func (id *Identity) VouchTags(roomID, hostID, subjectID string, subject PublicKey, members []MemberKey) ([]MemberTag, error) {
	msg := vouchMessage(subjectID, subject)
	tags := make([]MemberTag, 0, len(members))
	for _, m := range members {
		if m.ID == hostID || m.ID == subjectID {
			continue
		}
		mac, err := id.pairMAC(roomID, hostID, m.ID, m.Key, vouchLabel, msg)
		if err != nil {
			return nil, err
		}
		t := MemberTag{MemberID: m.ID}
		copy(t.Tag[:], mac)
		tags = append(tags, t)
	}
	return tags, nil
}

// CheckVouch reports whether v proves, to this member (selfID), that the host whose
// identity key is host vouched for subjectID's key v.Key.
func (id *Identity) CheckVouch(roomID, selfID, subjectID string, host PublicKey, v Vouch) bool {
	tag, ok := findTag(v.Tags, selfID)
	if !ok {
		return false
	}
	mac, err := id.pairMAC(roomID, selfID, v.HostID, host, vouchLabel, vouchMessage(subjectID, v.Key))
	return err == nil && hmac.Equal(mac, tag)
}

// MarshalVouch encodes v: LV host ID || key (32) || tags.
func MarshalVouch(v Vouch) ([]byte, error) {
	if v.HostID == "" || len(v.HostID) > 255 {
		return nil, errors.New("e2ee: invalid vouch host")
	}
	b := appendLV(nil, []byte(v.HostID))
	b = append(b, v.Key[:]...)
	return appendTags(b, v.Tags)
}

// UnmarshalVouch decodes a vouch produced by MarshalVouch.
func UnmarshalVouch(b []byte) (Vouch, error) {
	var v Vouch
	hostB, rest, ok := readLV(b)
	if !ok || len(hostB) == 0 || len(rest) < PublicKeySize {
		return v, ErrMalformed
	}
	v.HostID = string(hostB)
	copy(v.Key[:], rest[:PublicKeySize])
	tags, rest, err := readTags(rest[PublicKeySize:])
	if err != nil || len(rest) != 0 {
		return Vouch{}, ErrMalformed
	}
	v.Tags = tags
	return v, nil
}

func leaveMessage(senderID string, timestamp int64) []byte {
	return binary.BigEndian.AppendUint64(appendLV(nil, []byte(senderID)), uint64(timestamp))
}

// LeaveProof authenticates a leave announcement to each of peers: only the leaving member
// and the recipient can compute the recipient's tag, so no member can make another one
// appear to leave.
func (id *Identity) LeaveProof(roomID, selfID string, timestamp int64, peers []MemberKey) ([]byte, error) {
	return id.proof(roomID, selfID, leaveLabel, leaveMessage(selfID, timestamp), peers)
}

// proof tags msg for each of peers with the pairwise key between selfID and that peer.
func (id *Identity) proof(roomID, selfID, label string, msg []byte, peers []MemberKey) ([]byte, error) {
	tags := make([]MemberTag, 0, len(peers))
	for _, p := range peers {
		mac, err := id.pairMAC(roomID, selfID, p.ID, p.Key, label, msg)
		if err != nil {
			return nil, err
		}
		t := MemberTag{MemberID: p.ID}
		copy(t.Tag[:], mac)
		tags = append(tags, t)
	}
	return appendTags(nil, tags)
}

// checkProof reports whether proof carries, for selfID, senderID's tag over msg.
func (id *Identity) checkProof(roomID, selfID, senderID string, sender PublicKey, label string, msg, proof []byte) bool {
	tags, rest, err := readTags(proof)
	if err != nil || len(rest) != 0 {
		return false
	}
	tag, ok := findTag(tags, selfID)
	if !ok {
		return false
	}
	mac, err := id.pairMAC(roomID, selfID, senderID, sender, label, msg)
	return err == nil && hmac.Equal(mac, tag)
}

// CheckLeave reports whether proof shows that senderID (identity key sender) announced
// its leave at timestamp to this member (selfID).
func (id *Identity) CheckLeave(roomID, selfID, senderID string, sender PublicKey, timestamp int64, proof []byte) bool {
	return id.checkProof(roomID, selfID, senderID, sender, leaveLabel, leaveMessage(senderID, timestamp), proof)
}

func kickMessage(hostID, targetID string, ban bool, timestamp int64) []byte {
	b := appendLV(appendLV(nil, []byte(hostID)), []byte(targetID))
	flag := byte(0)
	if ban {
		flag = 1
	}
	return binary.BigEndian.AppendUint64(append(b, flag), uint64(timestamp))
}

// KickProof authenticates the host's removal of targetID to each of members (the target
// included, so it knows the notice is real). Only the host and the recipient can compute
// the recipient's tag, so a member holding the group key cannot remove anyone.
func (id *Identity) KickProof(roomID, hostID, targetID string, ban bool, timestamp int64, members []MemberKey) ([]byte, error) {
	return id.proof(roomID, hostID, kickLabel, kickMessage(hostID, targetID, ban, timestamp), members)
}

// CheckKick reports whether proof shows, to this member (selfID), that the host whose
// identity key is host removed targetID (and banned it when ban) at timestamp.
func (id *Identity) CheckKick(roomID, selfID, hostID, targetID string, host PublicKey, ban bool, timestamp int64, proof []byte) bool {
	return id.checkProof(roomID, selfID, hostID, host, kickLabel, kickMessage(hostID, targetID, ban, timestamp), proof)
}
