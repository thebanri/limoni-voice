// Package nat provides the NAT traversal building blocks used by Limoni Voice: a minimal
// STUN (RFC 5389) binding client codec, NAT mapping classification and hole punching
// candidate helpers.
package nat

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sort"
	"sync"
	"time"
)

const (
	stunMagicCookie     = 0x2112A442
	stunBindingRequest  = 0x0001
	stunBindingSuccess  = 0x0101
	attrMappedAddress   = 0x0001
	attrXorMappedAddr   = 0x0020
	stunHeaderSize      = 20
	stunTransactionSize = 12
)

// DefaultSTUNServers are queried to discover the public mapping. They are spread over
// different operators so that at least two distinct server IPs answer (needed to classify).
var DefaultSTUNServers = []string{
	"stun.l.google.com:19302",
	"stun1.l.google.com:19302",
	"stun.cloudflare.com:3478",
	"stun.nextcloud.com:443",
}

// TransactionID identifies a STUN request.
type TransactionID [stunTransactionSize]byte

// NewBindingRequest returns a STUN binding request and its transaction ID.
func NewBindingRequest() ([]byte, TransactionID) {
	var tx TransactionID
	if _, err := rand.Read(tx[:]); err != nil {
		panic(err)
	}
	req := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(req[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(req[2:4], 0)
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	copy(req[8:20], tx[:])
	return req, tx
}

// IsBindingResponse reports whether data looks like a STUN binding success response.
func IsBindingResponse(data []byte) bool {
	return len(data) >= stunHeaderSize &&
		binary.BigEndian.Uint16(data[0:2]) == stunBindingSuccess &&
		binary.BigEndian.Uint32(data[4:8]) == stunMagicCookie
}

var ErrNoMappedAddress = errors.New("nat: STUN response without mapped address")

// ParseBindingResponse extracts the transaction ID and the (XOR-)mapped address.
func ParseBindingResponse(data []byte) (TransactionID, *net.UDPAddr, error) {
	var tx TransactionID
	if !IsBindingResponse(data) {
		return tx, nil, errors.New("nat: not a STUN binding response")
	}
	copy(tx[:], data[8:20])
	msgLen := int(binary.BigEndian.Uint16(data[2:4]))
	end := stunHeaderSize + msgLen
	if end > len(data) {
		end = len(data)
	}
	var mapped *net.UDPAddr
	for off := stunHeaderSize; off+4 <= end; {
		attrType := binary.BigEndian.Uint16(data[off : off+2])
		attrLen := int(binary.BigEndian.Uint16(data[off+2 : off+4]))
		off += 4
		if off+attrLen > end {
			break
		}
		val := data[off : off+attrLen]
		if (attrType == attrXorMappedAddr || attrType == attrMappedAddress) && attrLen >= 8 {
			family := val[1]
			port := binary.BigEndian.Uint16(val[2:4])
			var ip net.IP
			switch {
			case family == 0x01 && attrLen >= 8:
				ip = make(net.IP, 4)
				copy(ip, val[4:8])
			case family == 0x02 && attrLen >= 20:
				ip = make(net.IP, 16)
				copy(ip, val[4:20])
			}
			if ip != nil {
				if attrType == attrXorMappedAddr {
					port ^= uint16(stunMagicCookie >> 16)
					var xorKey [16]byte
					binary.BigEndian.PutUint32(xorKey[0:4], stunMagicCookie)
					copy(xorKey[4:], tx[:])
					for i := range ip {
						ip[i] ^= xorKey[i]
					}
				}
				addr := &net.UDPAddr{IP: ip, Port: int(port)}
				// Prefer XOR-MAPPED-ADDRESS (immune to NAT ALG rewriting).
				if attrType == attrXorMappedAddr || mapped == nil {
					mapped = addr
				}
			}
		}
		off += (attrLen + 3) &^ 3
	}
	if mapped == nil {
		return tx, nil, ErrNoMappedAddress
	}
	return tx, mapped, nil
}

// Type describes NAT mapping behaviour (RFC 4787).
type Type string

const (
	TypeUnknown Type = ""
	// TypeEIM is endpoint-independent mapping ("cone"): one public port for all destinations.
	TypeEIM Type = "eim"
	// TypeEDM is endpoint-dependent mapping ("symmetric"): a new public port per destination.
	TypeEDM Type = "edm"
)

// Describe returns a human readable label.
func (t Type) Describe() string {
	switch t {
	case TypeEIM:
		return "Cone (EIM)"
	case TypeEDM:
		return "Symmetric (EDM)"
	default:
		return "Unknown"
	}
}

// Observation is one STUN answer.
type Observation struct {
	Server string
	Mapped *net.UDPAddr
	At     time.Time
}

// Classifier tracks STUN requests on one local socket and classifies the NAT.
type Classifier struct {
	mu        sync.Mutex
	pending   map[TransactionID]string
	round     map[string]*net.UDPAddr // server IP -> mapped address, current round
	lastRound []Observation
	natType   Type
	public    *net.UDPAddr
}

// NewClassifier returns an empty classifier.
func NewClassifier() *Classifier {
	return &Classifier{pending: make(map[TransactionID]string), round: make(map[string]*net.UDPAddr)}
}

// StartRound forgets previous in-flight requests and begins a new measurement round.
func (c *Classifier) StartRound() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = make(map[TransactionID]string)
	c.round = make(map[string]*net.UDPAddr)
}

// Track registers an outgoing request to server (resolved IP:port string).
func (c *Classifier) Track(tx TransactionID, server string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[tx] = server
}

// Observe records a response. It returns changed=true when the public endpoint or NAT type changed.
func (c *Classifier) Observe(tx TransactionID, mapped *net.UDPAddr) (changed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	server, ok := c.pending[tx]
	if !ok || mapped == nil {
		return false
	}
	delete(c.pending, tx)
	host, _, _ := net.SplitHostPort(server)
	c.round[host] = mapped

	obs := make([]Observation, 0, len(c.round))
	for srv, m := range c.round {
		obs = append(obs, Observation{Server: srv, Mapped: m, At: time.Now()})
	}
	sort.Slice(obs, func(i, j int) bool { return obs[i].Server < obs[j].Server })
	c.lastRound = obs

	newType := c.natType
	newPublic := c.public
	if len(c.round) >= 2 {
		same := true
		var first *net.UDPAddr
		for _, m := range c.round {
			if first == nil {
				first = m
				continue
			}
			if m.Port != first.Port || !m.IP.Equal(first.IP) {
				same = false
			}
		}
		if same {
			newType = TypeEIM
		} else {
			newType = TypeEDM
		}
	}
	// With endpoint-independent mapping (or before classification) the mapping is
	// meaningful for every peer; with EDM the first mapping still carries the public IP.
	if newPublic == nil || newType != TypeEDM {
		newPublic = mapped
	} else if !newPublic.IP.Equal(mapped.IP) {
		newPublic = mapped
	}
	changed = newType != c.natType || newPublic == nil || c.public == nil ||
		!newPublic.IP.Equal(c.public.IP) || (newType != TypeEDM && newPublic.Port != c.public.Port)
	c.natType, c.public = newType, newPublic
	return changed
}

// Result returns the NAT type and public endpoint (nil if unknown).
func (c *Classifier) Result() (Type, *net.UDPAddr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.natType, c.public
}

// Observations returns the answers of the latest round, sorted by server.
func (c *Classifier) Observations() []Observation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Observation(nil), c.lastRound...)
}

// Reset clears all state (e.g. after the local socket was rebound).
func (c *Classifier) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = make(map[TransactionID]string)
	c.round = make(map[string]*net.UDPAddr)
	c.lastRound = nil
	c.natType = TypeUnknown
	c.public = nil
}
