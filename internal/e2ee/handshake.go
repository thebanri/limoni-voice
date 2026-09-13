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
//	J -> H  auth       = confirmJ (32) || seal_wrap(pin, nickname)
//	H -> J  result     = status (1) [|| seal_wrap(epoch, group key)]
//
// The room secret (the words of the room code) authenticates both sides; an attacker,
// including a malicious relay, gets exactly one online guess per handshake and can never
// test guesses offline. The host only reveals the group key after confirmJ verifies.
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
	state                    *cpace.State
	hello                    []byte
	keys                     *handshakeKeys
}

// NewJoinClient starts a handshake and returns the hello message to send to the host.
func NewJoinClient(roomID, secret, joinerID string) (*JoinClient, []byte, error) {
	msgA, st, err := cpace.Start(secret, contextInfo(roomID, joinerID))
	if err != nil {
		return nil, nil, err
	}
	return &JoinClient{roomID: roomID, secret: secret, joinerID: joinerID, state: st, hello: msgA}, msgA, nil
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
	sealed, err := sealWith(keys.wrap, body)
	if err != nil {
		return nil, err
	}
	out := append(mac(keys.confirmJoin, transcript), sealed...)
	return out, nil
}

// HandleResult parses the host's verdict; on StatusOK it returns the room group key.
func (c *JoinClient) HandleResult(result []byte) (status byte, epoch uint32, key GroupKey, err error) {
	if c.keys == nil {
		return 0, 0, key, errors.New("e2ee: result before challenge")
	}
	if len(result) < 1 {
		return 0, 0, key, ErrMalformed
	}
	status = result[0]
	if status != StatusOK {
		return status, 0, key, nil
	}
	pt, err := openWith(c.keys.wrap, result[1:])
	if err != nil {
		return 0, 0, key, err
	}
	if len(pt) != 4+KeySize {
		return 0, 0, key, ErrMalformed
	}
	epoch = binary.BigEndian.Uint32(pt[:4])
	copy(key[:], pt[4:])
	return status, epoch, key, nil
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

// VerifyAuth checks the joiner's key confirmation and returns the requested PIN and nickname.
// ErrWrongCode means the joiner used a wrong room code.
func (s *JoinServer) VerifyAuth(auth []byte) (pin, nickname string, err error) {
	if len(auth) < confirmSize {
		return "", "", ErrMalformed
	}
	if !hmac.Equal(mac(s.keys.confirmJoin, s.transcript), auth[:confirmSize]) {
		return "", "", ErrWrongCode
	}
	pt, err := openWith(s.keys.wrap, auth[confirmSize:])
	if err != nil {
		return "", "", ErrWrongCode
	}
	pinB, rest, ok := readLV(pt)
	if !ok {
		return "", "", ErrMalformed
	}
	nickB, _, ok := readLV(rest)
	if !ok {
		return "", "", ErrMalformed
	}
	s.verified = true
	return string(pinB), string(nickB), nil
}

// Result builds the verdict message. The group key is only included for StatusOK after VerifyAuth.
func (s *JoinServer) Result(status byte, epoch uint32, key GroupKey) ([]byte, error) {
	if status != StatusOK {
		return []byte{status}, nil
	}
	if !s.verified {
		return nil, errors.New("e2ee: refusing to release key before verification")
	}
	body := make([]byte, 4, 4+KeySize)
	binary.BigEndian.PutUint32(body, epoch)
	body = append(body, key[:]...)
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
