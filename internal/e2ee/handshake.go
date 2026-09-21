package e2ee

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	"filippo.io/cpace"
)

// Join handshake (CPace over ristretto255), run between a joiner and the room host:
//
//	J -> H  hello      = msgA (48)
//	H -> J  challenge  = msgB (32) || confirmH (32)
//	J -> H  auth       = confirmJ (32) || seal_wrap(pin, nickname, joiner identity key)
//	H -> J  result     = status (1) [|| seal_wrap(key grant: epoch, group key, member keys)]
//
// The room secret (the words of the room code) authenticates both sides; an attacker,
// including a malicious relay, gets exactly one online guess per handshake and can never
// test guesses offline. The host only reveals the group key after confirmJ verifies.
// The identity keys exchanged here authenticate later rekeys (see members.go).
const (
	helloSize     = 48
	challengeSize = 64
	confirmSize   = 32
)

// Result status codes.
const (
	StatusOK          byte = 1
	StatusPinRequired byte = 2 // missing or wrong PIN
	StatusLocked      byte = 3 // host locked the room
	StatusFull        byte = 4
	StatusBusy        byte = 5 // host is rate limiting handshakes
	StatusOutdated    byte = 6 // joiner sent no identity key (older version)
)

var (
	ErrWrongCode = errors.New("e2ee: room code mismatch")
	ErrMalformed = errors.New("e2ee: malformed handshake message")
)

const handshakeLabel = "limoni-voice join v2"

func contextInfo(roomID, joinerID string) *cpace.ContextInfo {
	return cpace.NewContextInfo(joinerID, "", []byte(handshakeLabel+"|"+roomID))
}

type handshakeKeys struct {
	confirmHost []byte
	confirmJoin []byte
	wrap        cipher.AEAD
}

func deriveHandshakeKeys(k []byte) (*handshakeKeys, error) {
	derive := func(label string) []byte {
		m := hmac.New(sha256.New, k)
		m.Write([]byte(label))
		return m.Sum(nil)
	}
	block, err := aes.NewCipher(derive("wrap"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &handshakeKeys{confirmHost: derive("confirm host"), confirmJoin: derive("confirm join"), wrap: aead}, nil
}

func transcriptHash(msgA, msgB []byte, roomID, joinerID, hostID string) []byte {
	h := sha256.New()
	for _, part := range [][]byte{msgA, msgB, []byte(roomID), []byte(joinerID), []byte(hostID)} {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(part)))
		h.Write(l[:])
		h.Write(part)
	}
	return h.Sum(nil)
}

func mac(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// JoinClient is the joiner side of the handshake.
type JoinClient struct {
	roomID, secret, joinerID string
	identity                 PublicKey
	state                    *cpace.State
	hello                    []byte
	keys                     *handshakeKeys
}

// NewJoinClient starts a handshake and returns the hello message to send to the host.
// identity is the joiner's identity public key, delivered to the host inside the sealed auth.
func NewJoinClient(roomID, secret, joinerID string, identity PublicKey) (*JoinClient, []byte, error) {
	msgA, st, err := cpace.Start(secret, contextInfo(roomID, joinerID))
	if err != nil {
		return nil, nil, err
	}
	return &JoinClient{roomID: roomID, secret: secret, joinerID: joinerID, identity: identity, state: st, hello: msgA}, msgA, nil
}

// Hello returns the handshake hello (safe to retransmit).
func (c *JoinClient) Hello() []byte { return c.hello }

// HandleChallenge verifies the host knows the room secret and returns the auth message.
func (c *JoinClient) HandleChallenge(challenge []byte, hostID, pin, nickname string) ([]byte, error) {
	if len(challenge) != challengeSize {
		return nil, ErrMalformed
	}
	if c.state == nil {
		return nil, errors.New("e2ee: challenge already processed")
	}
	msgB := challenge[:32]
	key, err := c.state.Finish(msgB)
	if err != nil {
		return nil, ErrWrongCode
	}
	keys, err := deriveHandshakeKeys(key)
	if err != nil {
		return nil, err
	}
	transcript := transcriptHash(c.hello, msgB, c.roomID, c.joinerID, hostID)
	if !hmac.Equal(mac(keys.confirmHost, transcript), challenge[32:]) {
		return nil, ErrWrongCode
	}
	c.state = nil
	c.keys = keys

	body := appendLV(nil, []byte(pin))
	body = appendLV(body, []byte(nickname))
	body = append(body, c.identity[:]...)
	sealed, err := sealWith(keys.wrap, body)
	if err != nil {
		return nil, err
	}
	out := append(mac(keys.confirmJoin, transcript), sealed...)
	return out, nil
}

// HandleResult parses the host's verdict; on StatusOK it returns the group key grant.
// ErrOutdatedPeer means the host sent no member keys (older version).
func (c *JoinClient) HandleResult(result []byte) (status byte, grant KeyGrant, err error) {
	if c.keys == nil {
		return 0, grant, errors.New("e2ee: result before challenge")
	}
	if len(result) < 1 {
		return 0, grant, ErrMalformed
	}
	status = result[0]
	if status != StatusOK {
		return status, grant, nil
	}
	pt, err := openWith(c.keys.wrap, result[1:])
	if err != nil {
		return 0, grant, err
	}
	grant, err = unmarshalGrant(pt)
	if err != nil {
		return 0, KeyGrant{}, err
	}
	return status, grant, nil
}

// JoinServer is the host side of one handshake.
type JoinServer struct {
	JoinerID   string
	Created    time.Time
	transcript []byte
	keys       *handshakeKeys
	challenge  []byte
	verified   bool
}

// AcceptJoin processes a joiner hello and returns the challenge to send back.
func AcceptJoin(roomID, secret, hostID, joinerID string, hello []byte) (*JoinServer, []byte, error) {
	if len(hello) != helloSize {
		return nil, nil, ErrMalformed
	}
	msgB, key, err := cpace.Exchange(secret, contextInfo(roomID, joinerID), hello)
	if err != nil {
		return nil, nil, ErrMalformed
	}
	keys, err := deriveHandshakeKeys(key)
	if err != nil {
		return nil, nil, err
	}
	transcript := transcriptHash(hello, msgB, roomID, joinerID, hostID)
	challenge := append(append([]byte{}, msgB...), mac(keys.confirmHost, transcript)...)
	return &JoinServer{JoinerID: joinerID, Created: time.Now(), transcript: transcript, keys: keys, challenge: challenge}, challenge, nil
}

// Challenge returns the challenge message (for retransmission).
func (s *JoinServer) Challenge() []byte { return s.challenge }

// Verified reports whether VerifyAuth succeeded.
func (s *JoinServer) Verified() bool { return s.verified }

// JoinAuth is what a verified joiner asked for.
type JoinAuth struct {
	PIN      string
	Nickname string
	Identity PublicKey
	HasKey   bool // false for joiners running a version without identity keys
}

// VerifyAuth checks the joiner's key confirmation and returns the requested PIN, nickname
// and identity key. ErrWrongCode means the joiner used a wrong room code.
func (s *JoinServer) VerifyAuth(auth []byte) (JoinAuth, error) {
	if len(auth) < confirmSize {
		return JoinAuth{}, ErrMalformed
	}
	if !hmac.Equal(mac(s.keys.confirmJoin, s.transcript), auth[:confirmSize]) {
		return JoinAuth{}, ErrWrongCode
	}
	pt, err := openWith(s.keys.wrap, auth[confirmSize:])
	if err != nil {
		return JoinAuth{}, ErrWrongCode
	}
	pinB, rest, ok := readLV(pt)
	if !ok {
		return JoinAuth{}, ErrMalformed
	}
	nickB, rest, ok := readLV(rest)
	if !ok {
		return JoinAuth{}, ErrMalformed
	}
	ja := JoinAuth{PIN: string(pinB), Nickname: string(nickB)}
	switch len(rest) {
	case 0:
	case PublicKeySize:
		copy(ja.Identity[:], rest)
		ja.HasKey = true
	default:
		return JoinAuth{}, ErrMalformed
	}
	s.verified = true
	return ja, nil
}

// Result builds the verdict message. The grant is only included for StatusOK after VerifyAuth.
func (s *JoinServer) Result(status byte, grant KeyGrant) ([]byte, error) {
	if status != StatusOK {
		return []byte{status}, nil
	}
	if !s.verified {
		return nil, errors.New("e2ee: refusing to release key before verification")
	}
	body, err := grant.marshal()
	if err != nil {
		return nil, err
	}
	sealed, err := sealWith(s.keys.wrap, body)
	if err != nil {
		return nil, err
	}
	return append([]byte{StatusOK}, sealed...), nil
}

func appendLV(dst, v []byte) []byte {
	if len(v) > 255 {
		v = v[:255]
	}
	dst = append(dst, byte(len(v)))
	return append(dst, v...)
}

func readLV(b []byte) (v, rest []byte, ok bool) {
	if len(b) < 1 || int(b[0]) > len(b)-1 {
		return nil, nil, false
	}
	return b[1 : 1+int(b[0])], b[1+int(b[0]):], true
}
