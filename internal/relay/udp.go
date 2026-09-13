package relay

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"time"

	"github.com/thebanri/limoni-voice/internal/protocol"
)

const (
	udpBindingTTL = 30 * time.Second
	// maxUDPPayload keeps relayed datagrams below a 1500 byte MTU (IPv6 + UDP headers + kind byte).
	maxUDPPayload = 1400
)

// UDPKindBulk marks client datagrams carrying screen share video (WebSocket fallback scheduling).
const UDPKindBulk = protocol.UDPKindBulk

// ServeUDP runs the datagram relay on conn until it is closed.
func (s *Server) ServeUDP(conn *net.UDPConn) error {
	_ = conn.SetReadBuffer(4 * 1024 * 1024)
	_ = conn.SetWriteBuffer(4 * 1024 * 1024)
	s.udpConn.Store(conn)
	defer s.udpConn.CompareAndSwap(conn, nil)

	buf := make([]byte, 65535)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if n < protocol.UDPTokenSize+1 {
			continue
		}
		s.metrics.udpPacketsIn.Add(1)
		s.metrics.udpBytesIn.Add(uint64(n))

		var token [protocol.UDPTokenSize]byte
		copy(token[:], buf[:protocol.UDPTokenSize])
		s.mu.Lock()
		c := s.udpTokens[token]
		s.mu.Unlock()
		if c == nil {
			s.metrics.udpUnknownToken.Add(1)
			continue
		}
		c.udpAddr.Store(cloneUDPAddr(addr))
		c.udpSeen.Store(time.Now().UnixNano())

		kind := buf[protocol.UDPTokenSize]
		payload := buf[protocol.UDPTokenSize+1 : n]
		switch kind {
		case protocol.UDPKindKeepalive:
			ack := []byte{protocol.UDPKindProbeAck}
			if _, err := conn.WriteToUDP(ack, addr); err == nil {
				s.metrics.udpPacketsOut.Add(1)
			}
		case protocol.UDPKindData:
			s.forward(c, protocol.FrameRealtime, payload, true)
		case UDPKindBulk:
			s.forward(c, protocol.FrameBulk, payload, true)
		case protocol.UDPKindTo:
			// [class][len][member][packet]
			if len(payload) > 1 {
				if target, packet, ok := protocol.SplitTarget(payload[1:]); ok {
					s.forwardTo(c, payload[0]&^protocol.FrameTargetFlag, target, packet, true)
				}
			}
		}
	}
}

func cloneUDPAddr(a *net.UDPAddr) *net.UDPAddr {
	out := &net.UDPAddr{Port: a.Port, Zone: a.Zone}
	out.IP = append(net.IP(nil), a.IP...)
	return out
}

func (s *Server) udpEnabled() bool {
	return s.udpConn.Load() != nil || s.cfg.UDPPort > 0 || s.cfg.UDPPublicAddr != ""
}

func (s *Server) assignUDPTokenLocked(c *client) {
	if !s.udpEnabled() {
		return
	}
	s.releaseUDPTokenLocked(c)
	for {
		if _, err := rand.Read(c.udpToken[:]); err != nil {
			panic(err)
		}
		if _, taken := s.udpTokens[c.udpToken]; !taken {
			break
		}
	}
	c.hasUDPToken = true
	s.udpTokens[c.udpToken] = c
}

func (s *Server) releaseUDPTokenLocked(c *client) {
	if c.hasUDPToken {
		if s.udpTokens[c.udpToken] == c {
			delete(s.udpTokens, c.udpToken)
		}
		c.hasUDPToken = false
	}
}

func (s *Server) fillUDPLocked(sig *protocol.Signal, c *client) {
	if !c.hasUDPToken {
		return
	}
	sig.UDPToken = hex.EncodeToString(c.udpToken[:])
	sig.UDPAddr = s.cfg.UDPPublicAddr
	sig.UDPPort = s.cfg.UDPPort
}
